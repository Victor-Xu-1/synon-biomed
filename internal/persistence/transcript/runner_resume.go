package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// RetiredCorrectionBudgetReason identifies an obsolete execution policy, not
// a user decision or an external prerequisite. Current recovery quarantines
// failed strategies using canonical receipts, so these checkpoints can safely
// re-enter the existing dispatcher. Keep their historical payload immutable.
const RetiredCorrectionBudgetReason = "runner_correction_no_progress_exhausted"

const automaticCheckpointEligibilitySQL = `(COALESCE(json_extract(events.payload_json,'$.auto_resume'),1)=1
	OR json_extract(events.payload_json,'$.reason_code')='` + RetiredCorrectionBudgetReason + `')`

// RunnerInterruption is the newest durable interruption checkpoint for a
// stream whose runner lease is no longer active. It is the explicit-continue
// counterpart to AutoResume candidates: an interruption with AutoResume=false
// must wait for a user or dispatch continue, except for the explicitly retired
// correction-budget policy. This lookup also gates explicit continuation.
type RunnerInterruption struct {
	StreamUID          string
	Attempt            int64
	CheckpointSequence int64
	EventID            int64
	ReasonCode         string
	ResumeDetail       string
	CreatedAt          time.Time
}

// LatestRunnerInterruption returns the most recent runner checkpoint that
// carries an interruption reason code and whose attempt is no longer leased.
func (r *Repository) LatestRunnerInterruption(
	ctx context.Context,
	streamUID, ownerID string,
) (RunnerInterruption, bool, error) {
	db := r.readDatabase()
	if db == nil {
		return RunnerInterruption{}, false, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" {
		return RunnerInterruption{}, false, errors.New("stream uid and owner are required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RunnerInterruption{}, false, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var storedOwner string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM transcript_streams WHERE stream_uid=?`, streamUID).Scan(&storedOwner); err != nil {
		return RunnerInterruption{}, false, schemaError(err)
	}
	if storedOwner != ownerID {
		return RunnerInterruption{}, false, ErrOwnerMismatch
	}
	var interruption RunnerInterruption
	var rawPayload string
	err = tx.QueryRowContext(ctx, `
		SELECT checkpoint.stream_uid,checkpoint.runner_attempt,checkpoint.checkpoint_sequence,
			checkpoint.event_id,events.payload_json,events.created_at
		FROM transcript_runner_checkpoints checkpoint
		JOIN transcript_runner_attempts attempt
			ON attempt.stream_uid=checkpoint.stream_uid AND attempt.attempt=checkpoint.runner_attempt
		JOIN transcript_events events
			ON events.stream_uid=checkpoint.stream_uid AND events.event_id=checkpoint.event_id
		WHERE checkpoint.stream_uid=?
			AND attempt.attempt=(SELECT MAX(latest.attempt) FROM transcript_runner_attempts latest WHERE latest.stream_uid=checkpoint.stream_uid)
			AND (attempt.status IN ('failed','cancelled','reclaimed')
				OR (attempt.status='running' AND attempt.expires_at<=?))
		ORDER BY checkpoint.checkpoint_sequence DESC LIMIT 1`, streamUID, r.now().UTC(),
	).Scan(
		&interruption.StreamUID, &interruption.Attempt, &interruption.CheckpointSequence,
		&interruption.EventID, &rawPayload, &interruption.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return RunnerInterruption{}, false, schemaError(err)
		}
		return RunnerInterruption{}, false, nil
	}
	if err != nil {
		return RunnerInterruption{}, false, schemaError(err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(rawPayload), &payload); err != nil {
		return RunnerInterruption{}, false, schemaError(err)
	}
	interruption.ReasonCode = strings.TrimSpace(stringValueFromAny(payload["reason_code"]))
	if interruption.ReasonCode == "" {
		interruption.ReasonCode = strings.TrimSpace(stringValueFromAny(payload["reasonCode"]))
	}
	if interruption.ReasonCode == "" {
		if err := tx.Commit(); err != nil {
			return RunnerInterruption{}, false, schemaError(err)
		}
		return RunnerInterruption{}, false, nil
	}
	interruption.ResumeDetail = strings.TrimSpace(stringValueFromAny(payload["resume_detail"]))
	if interruption.ResumeDetail == "" {
		interruption.ResumeDetail = strings.TrimSpace(stringValueFromAny(payload["resumeDetail"]))
	}
	if err := tx.Commit(); err != nil {
		return RunnerInterruption{}, false, schemaError(err)
	}
	return interruption, true, nil
}

func (r *Repository) LatestResumableCheckpoint(
	ctx context.Context,
	streamUID, ownerID string,
) (RunnerCheckpoint, bool, error) {
	db := r.readDatabase()
	if db == nil {
		return RunnerCheckpoint{}, false, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" {
		return RunnerCheckpoint{}, false, errors.New("stream uid and owner are required")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return RunnerCheckpoint{}, false, schemaError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var storedOwner string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id FROM transcript_streams WHERE stream_uid=?`, streamUID).Scan(&storedOwner); err != nil {
		return RunnerCheckpoint{}, false, schemaError(err)
	}
	if storedOwner != ownerID {
		return RunnerCheckpoint{}, false, ErrOwnerMismatch
	}
	var checkpoint RunnerCheckpoint
	var phase string
	err = tx.QueryRowContext(ctx, `
		SELECT checkpoint.stream_uid,checkpoint.checkpoint_sequence,checkpoint.runner_attempt,
			checkpoint.event_id,checkpoint.phase,checkpoint.resumable,checkpoint.created_at
		FROM transcript_runner_checkpoints checkpoint
		JOIN transcript_runner_attempts attempt
			ON attempt.stream_uid=checkpoint.stream_uid AND attempt.attempt=checkpoint.runner_attempt
		WHERE checkpoint.stream_uid=? AND checkpoint.resumable=1
			AND attempt.attempt=(SELECT MAX(attempt) FROM transcript_runner_attempts WHERE stream_uid=?)
			AND (attempt.status IN ('failed','cancelled','reclaimed')
				OR (attempt.status='running' AND attempt.expires_at<=?))
		ORDER BY checkpoint.checkpoint_sequence DESC LIMIT 1`, streamUID, streamUID, r.now().UTC(),
	).Scan(
		&checkpoint.StreamUID, &checkpoint.Sequence, &checkpoint.Attempt, &checkpoint.EventID,
		&phase, &checkpoint.Resumable, &checkpoint.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return RunnerCheckpoint{}, false, schemaError(err)
		}
		return RunnerCheckpoint{}, false, nil
	}
	if err != nil {
		return RunnerCheckpoint{}, false, schemaError(err)
	}
	checkpoint.Phase = RunnerPhase(phase)
	if err := tx.Commit(); err != nil {
		return RunnerCheckpoint{}, false, schemaError(err)
	}
	return checkpoint, true, nil
}

// GetResumableCheckpoint loads the exact durable checkpoint named by a
// control event. It never substitutes a newer or older checkpoint.
func (r *Repository) GetResumableCheckpoint(
	ctx context.Context,
	streamUID, ownerID string,
	attempt, sequence int64,
) (RunnerCheckpoint, bool, error) {
	db := r.readDatabase()
	if db == nil {
		return RunnerCheckpoint{}, false, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" || attempt <= 0 || sequence <= 0 {
		return RunnerCheckpoint{}, false, errors.New("stream, owner, attempt, and checkpoint sequence are required")
	}
	var storedOwner string
	if err := db.QueryRowContext(ctx, `SELECT owner_id FROM transcript_streams WHERE stream_uid=?`, streamUID).Scan(&storedOwner); err != nil {
		return RunnerCheckpoint{}, false, schemaError(err)
	}
	if storedOwner != ownerID {
		return RunnerCheckpoint{}, false, ErrOwnerMismatch
	}
	var checkpoint RunnerCheckpoint
	var phase string
	err := db.QueryRowContext(ctx, `
		SELECT checkpoint.stream_uid,checkpoint.checkpoint_sequence,checkpoint.runner_attempt,
			checkpoint.event_id,checkpoint.phase,checkpoint.resumable,checkpoint.created_at
		FROM transcript_runner_checkpoints checkpoint
		JOIN transcript_runner_attempts attempt
			ON attempt.stream_uid=checkpoint.stream_uid AND attempt.attempt=checkpoint.runner_attempt
		WHERE checkpoint.stream_uid=? AND checkpoint.runner_attempt=? AND checkpoint.checkpoint_sequence=?
			AND checkpoint.resumable=1
			AND (attempt.status IN ('failed','cancelled','reclaimed')
				OR (attempt.status='running' AND attempt.expires_at<=?))`,
		streamUID, attempt, sequence, r.now().UTC(),
	).Scan(
		&checkpoint.StreamUID, &checkpoint.Sequence, &checkpoint.Attempt, &checkpoint.EventID,
		&phase, &checkpoint.Resumable, &checkpoint.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerCheckpoint{}, false, nil
	}
	if err != nil {
		return RunnerCheckpoint{}, false, schemaError(err)
	}
	checkpoint.Phase = RunnerPhase(phase)
	return checkpoint, true, nil
}

// AutoResumeCandidate is a frame whose latest runner interruption is marked
// AutoResume or used the retired correction-budget policy. The bounce
// count is diagnostic scheduling metadata only; it never limits continuation.
type AutoResumeCandidate struct {
	StreamUID                string
	OwnerID                  string
	FrameID                  string
	Attempt                  int64
	CheckpointSequence       int64
	ReasonCode               string
	RecoveryContractRevision int64
	InputRevisionBounces     int64
}

// ExpiredRunnerCandidate is a frame stream whose latest runner attempt has
// lost its lease while a resumable checkpoint still exists. It is kept
// separate from AutoResumeCandidate because a stale lease has no prior
// interruption reason; the dispatcher supplies the recovery reason when it
// creates the durable frame-resume dispatch.
type ExpiredRunnerCandidate struct {
	StreamUID          string
	OwnerID            string
	FrameID            string
	Attempt            int64
	CheckpointSequence int64
}

// GetAutoResumeCandidate returns the latest eligible interruption for one
// stream. InputRevisionBounces is diagnostic scheduling metadata; it never
// imposes a logical-task continuation ceiling.
func (r *Repository) GetAutoResumeCandidate(
	ctx context.Context,
	streamUID, ownerID string,
) (AutoResumeCandidate, bool, error) {
	db := r.readDatabase()
	if db == nil {
		return AutoResumeCandidate{}, false, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" {
		return AutoResumeCandidate{}, false, errors.New("stream and owner are required")
	}
	var storedOwner string
	if err := db.QueryRowContext(ctx, `SELECT owner_id FROM transcript_streams WHERE stream_uid=?`, streamUID).Scan(&storedOwner); err != nil {
		return AutoResumeCandidate{}, false, schemaError(err)
	}
	if storedOwner != ownerID {
		return AutoResumeCandidate{}, false, ErrOwnerMismatch
	}
	var candidate AutoResumeCandidate
	var reason sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT checkpoint.stream_uid,
			streams.owner_id,
			streams.frame_id,
			checkpoint.runner_attempt,
			checkpoint.checkpoint_sequence,
			json_extract(events.payload_json,'$.reason_code') AS reason_code,
			COALESCE(json_extract(events.payload_json,'$.recovery_contract_revision'),0) AS recovery_contract_revision,
			(SELECT COUNT(*) FROM transcript_runner_checkpoints c2
				JOIN transcript_runner_attempts a2
					ON a2.stream_uid=c2.stream_uid AND a2.attempt=c2.runner_attempt
				JOIN transcript_events e2
					ON e2.stream_uid=c2.stream_uid AND e2.event_id=c2.event_id
				WHERE c2.stream_uid=checkpoint.stream_uid
					AND a2.claimed_input_revision=checkpoint.claimed_input_revision
					AND c2.checkpoint_sequence<=checkpoint.checkpoint_sequence
					AND c2.resumable=1
					AND json_extract(e2.payload_json,'$.reason_code') IS NOT NULL
					AND COALESCE(json_extract(e2.payload_json,'$.auto_resume'),1)=1
					AND json_extract(e2.payload_json,'$.reason_code') =
						json_extract(events.payload_json,'$.reason_code')
					AND COALESCE(json_extract(e2.payload_json,'$.recovery_contract_revision'),0) =
						COALESCE(json_extract(events.payload_json,'$.recovery_contract_revision'),0)) AS input_revision_bounces
		FROM (
			SELECT c.stream_uid,c.runner_attempt,c.checkpoint_sequence,c.event_id,c.resumable,
				a.claimed_input_revision,
				ROW_NUMBER() OVER (ORDER BY c.checkpoint_sequence DESC) AS rn
			FROM transcript_runner_checkpoints c
			JOIN transcript_runner_attempts a
				ON a.stream_uid=c.stream_uid AND a.attempt=c.runner_attempt
			WHERE c.stream_uid=?
				AND a.attempt=(
					SELECT MAX(latest.attempt) FROM transcript_runner_attempts latest
					WHERE latest.stream_uid=c.stream_uid)
				AND (a.status IN ('failed','cancelled','reclaimed')
					OR (a.status='running' AND a.expires_at<=?))
		) checkpoint
		JOIN transcript_streams streams ON streams.stream_uid=checkpoint.stream_uid
		JOIN frames frame ON frame.id=streams.frame_id
		JOIN transcript_events events
			ON events.stream_uid=checkpoint.stream_uid AND events.event_id=checkpoint.event_id
		WHERE checkpoint.rn=1 AND checkpoint.resumable=1
			AND streams.owner_id=? AND streams.kind='frame_ref'
			AND LOWER(TRIM(frame.status)) NOT IN ('completed','failed','cancelled','canceled','success','replaced','stopped')
			AND json_extract(events.payload_json,'$.reason_code') IS NOT NULL
			AND `+automaticCheckpointEligibilitySQL,
		streamUID, r.now().UTC(), ownerID,
	).Scan(
		&candidate.StreamUID, &candidate.OwnerID, &candidate.FrameID,
		&candidate.Attempt, &candidate.CheckpointSequence, &reason,
		&candidate.RecoveryContractRevision, &candidate.InputRevisionBounces,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return AutoResumeCandidate{}, false, nil
	}
	if err != nil {
		return AutoResumeCandidate{}, false, schemaError(err)
	}
	if !reason.Valid {
		return AutoResumeCandidate{}, false, nil
	}
	candidate.ReasonCode = strings.TrimSpace(reason.String)
	if candidate.ReasonCode == "" {
		return AutoResumeCandidate{}, false, nil
	}
	return candidate, true, nil
}

// ListAutoResumeCandidates returns AutoResume interruptions across every
// frame_ref stream for an owner. The result count is bounded only as a scanner
// page size; no per-task continuation ceiling is applied.
func (r *Repository) ListAutoResumeCandidates(
	ctx context.Context,
	ownerID string,
	limit int,
) ([]AutoResumeCandidate, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" || limit <= 0 {
		return nil, errors.New("owner and positive limit are required")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT checkpoint.stream_uid,
			streams.owner_id,
			streams.frame_id,
			checkpoint.runner_attempt,
			checkpoint.checkpoint_sequence,
			json_extract(events.payload_json,'$.reason_code') AS reason_code,
			COALESCE(json_extract(events.payload_json,'$.recovery_contract_revision'),0) AS recovery_contract_revision,
			(SELECT COUNT(*) FROM transcript_runner_checkpoints c2
				JOIN transcript_runner_attempts a2
					ON a2.stream_uid=c2.stream_uid AND a2.attempt=c2.runner_attempt
				JOIN transcript_events e2
					ON e2.stream_uid=c2.stream_uid AND e2.event_id=c2.event_id
				WHERE c2.stream_uid=checkpoint.stream_uid
					AND a2.claimed_input_revision=checkpoint.claimed_input_revision
					AND c2.checkpoint_sequence<=checkpoint.checkpoint_sequence
					AND c2.resumable=1
					AND json_extract(e2.payload_json,'$.reason_code') IS NOT NULL
					AND COALESCE(json_extract(e2.payload_json,'$.auto_resume'),1)=1
					AND json_extract(e2.payload_json,'$.reason_code') =
						json_extract(events.payload_json,'$.reason_code')
					AND COALESCE(json_extract(e2.payload_json,'$.recovery_contract_revision'),0) =
						COALESCE(json_extract(events.payload_json,'$.recovery_contract_revision'),0)) AS input_revision_bounces
		FROM (
			SELECT c.stream_uid, c.runner_attempt, c.checkpoint_sequence, c.event_id, c.resumable,
				a.claimed_input_revision,
				ROW_NUMBER() OVER (
					PARTITION BY c.stream_uid
					ORDER BY c.checkpoint_sequence DESC
				) AS rn
			FROM transcript_runner_checkpoints c
			JOIN transcript_runner_attempts a
				ON a.stream_uid=c.stream_uid AND a.attempt=c.runner_attempt
			WHERE a.attempt=(
					SELECT MAX(latest.attempt) FROM transcript_runner_attempts latest
					WHERE latest.stream_uid=c.stream_uid)
				AND (a.status IN ('failed','cancelled','reclaimed')
					OR (a.status='running' AND a.expires_at<=?))
		) checkpoint
		JOIN transcript_streams streams ON streams.stream_uid=checkpoint.stream_uid
		JOIN frames frame ON frame.id=streams.frame_id
		JOIN transcript_events events
			ON events.stream_uid=checkpoint.stream_uid
			AND events.event_id=checkpoint.event_id
		WHERE checkpoint.rn=1
			AND checkpoint.resumable=1
			AND streams.owner_id=?
			AND streams.kind='frame_ref'
			AND LOWER(TRIM(frame.status)) NOT IN ('completed','failed','cancelled','canceled','success','replaced','stopped')
			AND json_extract(events.payload_json,'$.reason_code') IS NOT NULL
			AND `+automaticCheckpointEligibilitySQL+`
		ORDER BY checkpoint.checkpoint_sequence DESC
		LIMIT ?`, r.now().UTC(), ownerID, limit)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	result := make([]AutoResumeCandidate, 0, limit)
	for rows.Next() {
		var candidate AutoResumeCandidate
		var reason sql.NullString
		if err := rows.Scan(&candidate.StreamUID, &candidate.OwnerID, &candidate.FrameID,
			&candidate.Attempt, &candidate.CheckpointSequence, &reason,
			&candidate.RecoveryContractRevision, &candidate.InputRevisionBounces); err != nil {
			return nil, schemaError(err)
		}
		if !reason.Valid {
			continue
		}
		candidate.ReasonCode = strings.TrimSpace(reason.String)
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, schemaError(err)
	}
	return result, nil
}

// ListAllAutoResumeCandidates returns AutoResume interruptions across every
// owner. It is the dispatch-loop counterpart to ListAutoResumeCandidates;
// limit bounds only each scanner batch.
func (r *Repository) ListAllAutoResumeCandidates(
	ctx context.Context,
	limit int,
) ([]AutoResumeCandidate, error) {
	return r.ListAllAutoResumeCandidatesPage(ctx, 0, limit)
}

// ListAllAutoResumeCandidatesPage returns one deterministic scanner page.
// Offset is a scan cursor, not a task retry count; the recovery coordinator
// drains every page so older tasks cannot be starved by newer durable
// interruptions that remain eligible while their dispatch is registered.
func (r *Repository) ListAllAutoResumeCandidatesPage(
	ctx context.Context,
	offset, limit int,
) ([]AutoResumeCandidate, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	if offset < 0 || limit <= 0 {
		return nil, errors.New("nonnegative offset and positive limit are required")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT checkpoint.stream_uid,
			streams.owner_id,
			streams.frame_id,
			checkpoint.runner_attempt,
			checkpoint.checkpoint_sequence,
			json_extract(events.payload_json,'$.reason_code') AS reason_code,
			COALESCE(json_extract(events.payload_json,'$.recovery_contract_revision'),0) AS recovery_contract_revision,
			(SELECT COUNT(*) FROM transcript_runner_checkpoints c2
				JOIN transcript_runner_attempts a2
					ON a2.stream_uid=c2.stream_uid AND a2.attempt=c2.runner_attempt
				JOIN transcript_events e2
					ON e2.stream_uid=c2.stream_uid AND e2.event_id=c2.event_id
				WHERE c2.stream_uid=checkpoint.stream_uid
					AND a2.claimed_input_revision=checkpoint.claimed_input_revision
					AND c2.checkpoint_sequence<=checkpoint.checkpoint_sequence
					AND c2.resumable=1
					AND json_extract(e2.payload_json,'$.reason_code') IS NOT NULL
					AND COALESCE(json_extract(e2.payload_json,'$.auto_resume'),1)=1
					AND json_extract(e2.payload_json,'$.reason_code') =
						json_extract(events.payload_json,'$.reason_code')
					AND COALESCE(json_extract(e2.payload_json,'$.recovery_contract_revision'),0) =
						COALESCE(json_extract(events.payload_json,'$.recovery_contract_revision'),0)) AS input_revision_bounces
		FROM (
			SELECT c.stream_uid, c.runner_attempt, c.checkpoint_sequence, c.event_id, c.resumable,
				a.claimed_input_revision,
				ROW_NUMBER() OVER (
					PARTITION BY c.stream_uid
					ORDER BY c.checkpoint_sequence DESC
				) AS rn
			FROM transcript_runner_checkpoints c
			JOIN transcript_runner_attempts a
				ON a.stream_uid=c.stream_uid AND a.attempt=c.runner_attempt
			WHERE a.attempt=(
					SELECT MAX(latest.attempt) FROM transcript_runner_attempts latest
					WHERE latest.stream_uid=c.stream_uid)
				AND (a.status IN ('failed','cancelled','reclaimed')
					OR (a.status='running' AND a.expires_at<=?))
		) checkpoint
		JOIN transcript_streams streams ON streams.stream_uid=checkpoint.stream_uid
		JOIN frames frame ON frame.id=streams.frame_id
		JOIN transcript_events events
			ON events.stream_uid=checkpoint.stream_uid
			AND events.event_id=checkpoint.event_id
			WHERE checkpoint.rn=1
				AND checkpoint.resumable=1
				AND streams.kind='frame_ref'
				AND LOWER(TRIM(frame.status)) NOT IN ('completed','failed','cancelled','canceled','success','replaced','stopped')
				AND json_extract(events.payload_json,'$.reason_code') IS NOT NULL
				AND `+automaticCheckpointEligibilitySQL+`
			ORDER BY checkpoint.checkpoint_sequence DESC, checkpoint.stream_uid
			LIMIT ? OFFSET ?`, r.now().UTC(), limit, offset)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	result := make([]AutoResumeCandidate, 0, limit)
	for rows.Next() {
		var candidate AutoResumeCandidate
		var reason sql.NullString
		if err := rows.Scan(&candidate.StreamUID, &candidate.OwnerID, &candidate.FrameID,
			&candidate.Attempt, &candidate.CheckpointSequence, &reason,
			&candidate.RecoveryContractRevision, &candidate.InputRevisionBounces); err != nil {
			return nil, schemaError(err)
		}
		if !reason.Valid {
			continue
		}
		candidate.ReasonCode = strings.TrimSpace(reason.String)
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, schemaError(err)
	}
	return result, nil
}

// ListAllExpiredRunnerCandidates returns bounded frame streams whose latest
// runner attempt is still marked running even though its durable lease has
// expired. A resumable checkpoint is required so recovery can continue from a
// known execution boundary instead of replaying an unbounded task from the
// beginning.
func (r *Repository) ListAllExpiredRunnerCandidates(
	ctx context.Context,
	limit int,
) ([]ExpiredRunnerCandidate, error) {
	return r.ListAllExpiredRunnerCandidatesPage(ctx, 0, limit)
}

// ListAllExpiredRunnerCandidatesPage returns one deterministic recovery page.
// The caller drains all pages; limit bounds one database read, not the number
// of long-running tasks eligible for lease takeover.
func (r *Repository) ListAllExpiredRunnerCandidatesPage(
	ctx context.Context,
	offset, limit int,
) ([]ExpiredRunnerCandidate, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	if offset < 0 || limit <= 0 || limit > 1000 {
		return nil, errors.New("nonnegative offset and expired-runner page size up to 1000 are required")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT attempt.stream_uid, streams.owner_id, streams.frame_id,
			attempt.attempt, MAX(checkpoint.checkpoint_sequence)
		FROM transcript_runner_attempts attempt
		JOIN transcript_streams streams ON streams.stream_uid=attempt.stream_uid
		JOIN transcript_runner_checkpoints checkpoint
			ON checkpoint.stream_uid=attempt.stream_uid
			AND checkpoint.runner_attempt=attempt.attempt
		JOIN transcript_events events
			ON events.stream_uid=checkpoint.stream_uid AND events.event_id=checkpoint.event_id
			WHERE attempt.status='running'
				AND attempt.expires_at<=?
			AND checkpoint.resumable=1
			AND COALESCE(json_extract(events.payload_json,'$.auto_resume'),1)=1
			AND checkpoint.checkpoint_sequence=(
				SELECT MAX(latest_checkpoint.checkpoint_sequence)
				FROM transcript_runner_checkpoints latest_checkpoint
				WHERE latest_checkpoint.stream_uid=attempt.stream_uid
					AND latest_checkpoint.runner_attempt=attempt.attempt)
			AND streams.kind='frame_ref'
			AND attempt.attempt=(
				SELECT MAX(latest.attempt)
				FROM transcript_runner_attempts latest
				WHERE latest.stream_uid=attempt.stream_uid
			)
		GROUP BY attempt.stream_uid, streams.owner_id, streams.frame_id, attempt.attempt, attempt.expires_at
		ORDER BY attempt.expires_at, MAX(checkpoint.checkpoint_sequence) DESC, attempt.stream_uid
			LIMIT ? OFFSET ?`, r.now().UTC(), limit, offset)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	result := make([]ExpiredRunnerCandidate, 0, limit)
	for rows.Next() {
		var candidate ExpiredRunnerCandidate
		if err := rows.Scan(
			&candidate.StreamUID, &candidate.OwnerID, &candidate.FrameID,
			&candidate.Attempt, &candidate.CheckpointSequence,
		); err != nil {
			return nil, schemaError(err)
		}
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, schemaError(err)
	}
	return result, nil
}

// ListAllExpiredUnrecoverableRunnerCandidates returns Frame attempts
// whose lease expired at a latest non-resumable execution checkpoint. These
// cannot be resumed safely and must be terminalized by the Frame lifecycle;
// intentional user/approval/external waits are excluded.
func (r *Repository) ListAllExpiredUnrecoverableRunnerCandidates(
	ctx context.Context,
	limit int,
) ([]AutoResumeCandidate, error) {
	return r.ListAllExpiredUnrecoverableRunnerCandidatesPage(ctx, 0, limit)
}

// ListAllExpiredUnrecoverableRunnerCandidatesPage returns one deterministic
// terminalization page. Draining every page prevents an old dead execution
// from remaining falsely visible as running behind a busy recovery cohort.
func (r *Repository) ListAllExpiredUnrecoverableRunnerCandidatesPage(
	ctx context.Context,
	offset, limit int,
) ([]AutoResumeCandidate, error) {
	if r == nil || r.db == nil {
		return nil, ErrSchemaUnavailable
	}
	if offset < 0 || limit <= 0 || limit > 1000 {
		return nil, errors.New("nonnegative offset and expired-runner page size up to 1000 are required")
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT attempt.stream_uid, streams.owner_id, streams.frame_id,
			attempt.attempt, checkpoint.checkpoint_sequence,
			COALESCE(json_extract(events.payload_json,'$.reason_code'),'automatic_recovery_exhausted')
		FROM transcript_runner_attempts attempt
		JOIN transcript_streams streams ON streams.stream_uid=attempt.stream_uid
		JOIN transcript_runner_checkpoints checkpoint
			ON checkpoint.stream_uid=attempt.stream_uid
			AND checkpoint.runner_attempt=attempt.attempt
		JOIN transcript_events events
			ON events.stream_uid=checkpoint.stream_uid AND events.event_id=checkpoint.event_id
		WHERE attempt.status='running'
			AND attempt.expires_at<=?
			AND checkpoint.resumable=0
			AND checkpoint.phase NOT IN ('waiting_user','waiting_approval','waiting_external')
			AND checkpoint.checkpoint_sequence=(
				SELECT MAX(latest_checkpoint.checkpoint_sequence)
				FROM transcript_runner_checkpoints latest_checkpoint
				WHERE latest_checkpoint.stream_uid=attempt.stream_uid
					AND latest_checkpoint.runner_attempt=attempt.attempt)
			AND streams.kind='frame_ref'
			AND attempt.attempt=(
				SELECT MAX(latest.attempt)
				FROM transcript_runner_attempts latest
				WHERE latest.stream_uid=attempt.stream_uid)
		ORDER BY attempt.expires_at, checkpoint.checkpoint_sequence DESC, attempt.stream_uid
		LIMIT ? OFFSET ?`, r.now().UTC(), limit, offset)
	if err != nil {
		return nil, schemaError(err)
	}
	defer rows.Close()
	result := make([]AutoResumeCandidate, 0, limit)
	for rows.Next() {
		var candidate AutoResumeCandidate
		if err := rows.Scan(
			&candidate.StreamUID, &candidate.OwnerID, &candidate.FrameID,
			&candidate.Attempt, &candidate.CheckpointSequence, &candidate.ReasonCode,
		); err != nil {
			return nil, schemaError(err)
		}
		candidate.ReasonCode = strings.TrimSpace(candidate.ReasonCode)
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, schemaError(err)
	}
	return result, nil
}
