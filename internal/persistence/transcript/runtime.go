package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"synon-go/internal/kernelcontract"
	"unicode/utf8"
)

const maxRunnerInterruptionResumeDetailBytes = 4096

type cancellationPayload struct {
	Status     string `json:"status"`
	ReasonCode string `json:"reason_code"`
}

func (r *Repository) HeartbeatRunner(
	ctx context.Context,
	input HeartbeatRunnerInput,
) (HeartbeatRunnerResult, error) {
	if err := validateClaimInput(input.Claim); err != nil {
		return HeartbeatRunnerResult{}, err
	}
	if input.TTL <= 0 {
		return HeartbeatRunnerResult{}, errors.New("positive runner ttl is required")
	}
	now := r.now().UTC()
	expiresAt := now.Add(input.TTL)
	var result HeartbeatRunnerResult
	terminalSettled := false
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		_, err := validateClaimConn(ctx, conn, input.Claim, now, true)
		if err != nil {
			stream, authorityErr := validateClaimConn(ctx, conn, input.Claim, now, false)
			if authorityErr != nil {
				return err
			}
			status, terminal, statusErr := frameTerminalStatusConn(ctx, conn, stream)
			if statusErr != nil {
				return statusErr
			}
			if !terminal {
				return err
			}
			if _, _, _, finishErr := (&ImmediateTransaction{repository: r, conn: conn}).FinishLatestFrameRunner(
				ctx, stream.OwnerID, stream.FrameID, status,
				terminalFrameRunnerDetail(status), nil,
			); finishErr != nil {
				return finishErr
			}
			terminalSettled = true
			return nil
		}
		updated, err := conn.ExecContext(ctx, `
			UPDATE transcript_runner_attempts SET expires_at=?
			WHERE stream_uid=? AND attempt=? AND runner_id=? AND status='running' AND expires_at>?`,
			expiresAt, input.Claim.StreamUID, input.Claim.Attempt, input.Claim.RunnerID, now)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			return ErrClaimStale
		}
		result = HeartbeatRunnerResult{Renewed: true, ExpiresAt: expiresAt}
		return nil
	})
	err = schemaError(err)
	if err == nil && terminalSettled {
		return result, ErrClaimStale
	}
	return result, err
}

// ReconcileTerminalFrameRunnerAttempts repairs the only invalid terminal
// authority state that can survive an older direct Frame status write: an
// active Frame stream whose latest runner attempt is still running while the
// Frame is already terminal. The repair writes the canonical runner_finished
// event and receipt in the same SQLite transaction; it never starts new work.
func (r *Repository) ReconcileTerminalFrameRunnerAttempts(ctx context.Context, limit int) (int, error) {
	if limit <= 0 || limit > 1000 {
		return 0, errors.New("terminal frame runner reconciliation limit must be between 1 and 1000")
	}
	settled := 0
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		settled, err = reconcileTerminalFrameRunnerAttemptsConn(
			ctx, &ImmediateTransaction{repository: r, conn: conn}, limit,
		)
		return err
	})
	return settled, schemaError(err)
}

func reconcileTerminalFrameRunnerAttemptsConn(
	ctx context.Context,
	tx *ImmediateTransaction,
	limit int,
) (int, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil || limit <= 0 {
		return 0, errors.New("active transcript transaction and positive reconciliation limit are required")
	}
	type candidate struct {
		ownerID string
		frameID string
		status  string
	}
	rows, err := tx.conn.QueryContext(ctx, `
		SELECT stream.owner_id,stream.frame_id,frame.status
		FROM transcript_frame_authority authority
		JOIN transcript_streams stream ON stream.stream_uid=authority.active_stream_uid
			AND stream.owner_id=authority.owner_id AND stream.session_id=authority.session_id
			AND stream.epoch=authority.active_epoch
		JOIN frames frame ON frame.id=stream.frame_id AND frame.project_id=stream.project_id
		JOIN transcript_runner_attempts attempt ON attempt.stream_uid=stream.stream_uid
		WHERE stream.kind='frame_ref' AND attempt.status='running'
			AND attempt.attempt=(
				SELECT MAX(latest.attempt) FROM transcript_runner_attempts latest
				WHERE latest.stream_uid=stream.stream_uid
			)
			AND LOWER(TRIM(frame.status)) IN ('completed','failed','cancelled','canceled')
		ORDER BY attempt.expires_at,stream.stream_uid LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var value candidate
		if err := rows.Scan(&value.ownerID, &value.frameID, &value.status); err != nil {
			_ = rows.Close()
			return 0, err
		}
		candidates = append(candidates, value)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	settled := 0
	for _, value := range candidates {
		status := normalizeTerminalStatus(value.status)
		_, _, created, err := tx.FinishLatestFrameRunner(
			ctx, value.ownerID, value.frameID, status,
			terminalFrameRunnerDetail(status), nil,
		)
		if err != nil {
			return settled, err
		}
		if created {
			settled++
		}
	}
	return settled, nil
}

func (r *Repository) CancelRunner(ctx context.Context, input CancelRunnerInput) (CancelRunnerResult, error) {
	if err := normalizeCancelRunnerInput(&input); err != nil {
		return CancelRunnerResult{}, err
	}
	var result CancelRunnerResult
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		result, err = cancelRunnerConn(ctx, conn, r.now().UTC(), input)
		return err
	})
	return result, schemaError(err)
}

// InterruptRunner persists a resumable checkpoint and expires the exact
// infrastructure-owned lease without creating a terminal receipt. It is the
// graceful-drain counterpart to CancelRunner: the input revision remains
// pending and the Frame remains nonterminal so another runtime can reclaim it.
func (r *Repository) InterruptRunner(ctx context.Context, input InterruptRunnerInput) (InterruptRunnerResult, error) {
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	input.ResumeDetail = strings.TrimSpace(input.ResumeDetail)
	if err := validateClaimInput(input.Claim); err != nil {
		return InterruptRunnerResult{}, err
	}
	if input.ClientMessageID == "" || !validReasonCode(input.ReasonCode) {
		return InterruptRunnerResult{}, errors.New("client message id and interruption reason code are required")
	}
	if !utf8.ValidString(input.ResumeDetail) || len(input.ResumeDetail) > maxRunnerInterruptionResumeDetailBytes ||
		strings.ContainsRune(input.ResumeDetail, '\x00') {
		return InterruptRunnerResult{}, errors.New("runner interruption resume detail is invalid")
	}
	if input.RecoveryContractRevision < 0 || input.RecoveryContractRevision > 1_000_000 {
		return InterruptRunnerResult{}, errors.New("runner interruption recovery contract revision is invalid")
	}
	payloadValue := map[string]any{
		"status":      "interrupted",
		"reason_code": input.ReasonCode,
		"auto_resume": input.AutoResume,
	}
	if input.ResumeDetail != "" {
		payloadValue["resume_detail"] = input.ResumeDetail
	}
	if input.RecoveryContractRevision > 0 {
		payloadValue["recovery_contract_revision"] = input.RecoveryContractRevision
	}
	payload, err := json.Marshal(payloadValue)
	if err != nil {
		return InterruptRunnerResult{}, err
	}
	var result InterruptRunnerResult
	err = r.withImmediate(ctx, func(conn *sql.Conn) error {
		now := r.now().UTC()
		phase := RunnerPhasePlanning
		var currentPhase string
		if err := conn.QueryRowContext(ctx, `
			SELECT phase FROM transcript_runner_attempts
			WHERE stream_uid=? AND attempt=? AND runner_id=?`,
			input.Claim.StreamUID, input.Claim.Attempt, input.Claim.RunnerID,
		).Scan(&currentPhase); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrClaimStale
			}
			return err
		}
		if candidate := RunnerPhase(strings.TrimSpace(currentPhase)); validCheckpointPhase(candidate) {
			phase = candidate
		}
		checkpoint, event, created, err := appendRunnerCheckpointConn(ctx, conn, AppendRunnerCheckpointInput{
			Claim: input.Claim, ClientMessageID: input.ClientMessageID,
			Phase: phase, Resumable: input.Resumable || input.AutoResume,
			PayloadJSON: payload, Destinations: input.Destinations,
		}, now)
		if err != nil {
			return err
		}
		result = InterruptRunnerResult{Checkpoint: checkpoint, Event: event, Created: created}
		if !created {
			return nil
		}
		updated, err := conn.ExecContext(ctx, `
			UPDATE transcript_runner_attempts SET expires_at=?
			WHERE stream_uid=? AND attempt=? AND runner_id=? AND status='running'`,
			now, input.Claim.StreamUID, input.Claim.Attempt, input.Claim.RunnerID,
		)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			return ErrClaimStale
		}
		return nil
	})
	return result, schemaError(err)
}

func (tx *ImmediateTransaction) CancelLatestFrameRunner(
	ctx context.Context,
	ownerID, frameID, reasonCode string,
	destinations []string,
) (CancelRunnerResult, error) {
	ownerID = strings.TrimSpace(ownerID)
	frameID = strings.TrimSpace(frameID)
	if tx == nil || tx.repository == nil || tx.conn == nil || ownerID == "" || frameID == "" {
		return CancelRunnerResult{}, errors.New("active transcript transaction, owner, and frame id are required")
	}
	var streamUID string
	var inputRevision int64
	err := tx.conn.QueryRowContext(ctx, `
		SELECT stream_uid,input_revision FROM transcript_streams
		WHERE kind='frame_ref' AND owner_id=? AND frame_id=? ORDER BY epoch DESC LIMIT 1`, ownerID, frameID,
	).Scan(&streamUID, &inputRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return CancelRunnerResult{}, ErrSchemaUnavailable
	}
	if err != nil {
		return CancelRunnerResult{}, err
	}
	var attempt int64
	err = tx.conn.QueryRowContext(ctx, `
		SELECT attempt FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt DESC LIMIT 1`, streamUID,
	).Scan(&attempt)
	if errors.Is(err, sql.ErrNoRows) {
		_, digest, tokenErr := tx.repository.newClaimToken()
		if tokenErr != nil {
			return CancelRunnerResult{}, tokenErr
		}
		attempt = 1
		now := tx.repository.now().UTC()
		if _, err := tx.conn.ExecContext(ctx, `
			INSERT INTO transcript_runner_attempts(
				stream_uid,attempt,runner_id,claim_token_sha256,claimed_input_revision,
				resume_source,resume_checkpoint_sequence,status,phase,phase_sequence,
				last_checkpoint_sequence,claimed_at,expires_at
			) VALUES(?,1,?,?,?,'fresh',0,'running','claimed',1,0,?,?)`,
			streamUID, "cancel-before-claim", digest, inputRevision, now, now,
		); err != nil {
			return CancelRunnerResult{}, err
		}
		err = nil
	}
	if err != nil {
		return CancelRunnerResult{}, err
	}
	input := CancelRunnerInput{
		StreamUID:       streamUID,
		OwnerID:         ownerID,
		ExpectedAttempt: attempt,
		ClientMessageID: "runner-cancel:" + streamUID + ":" + strconv.FormatInt(attempt, 10),
		ReasonCode:      reasonCode,
		Destinations:    destinations,
	}
	if err := normalizeCancelRunnerInput(&input); err != nil {
		return CancelRunnerResult{}, err
	}
	return cancelRunnerConn(ctx, tx.conn, tx.repository.now().UTC(), input)
}

// CompleteLatestFrameRunner settles the latest Frame runner from a trusted
// workspace transaction. It is intentionally unavailable on Repository so
// callers cannot bypass the public claim-token contract.
func (tx *ImmediateTransaction) CompleteLatestFrameRunner(
	ctx context.Context,
	ownerID, frameID, detail string,
	destinations []string,
) (Event, FinishReceipt, bool, error) {
	return tx.FinishLatestFrameRunner(ctx, ownerID, frameID, "completed", detail, destinations)
}

// FinishLatestFrameRunner settles an internally owned Frame runner without
// exposing a claim-token bypass on Repository. Callers must already hold the
// workspace transaction that owns the Frame lifecycle mutation.
func (tx *ImmediateTransaction) FinishLatestFrameRunner(
	ctx context.Context,
	ownerID, frameID, status, detail string,
	destinations []string,
) (Event, FinishReceipt, bool, error) {
	ownerID = strings.TrimSpace(ownerID)
	frameID = strings.TrimSpace(frameID)
	status = normalizeTerminalStatus(status)
	detail = strings.TrimSpace(detail)
	if tx == nil || tx.repository == nil || tx.conn == nil || ownerID == "" || frameID == "" ||
		!terminalStatus(status) || detail == "" {
		return Event{}, FinishReceipt{}, false, errors.New("active transcript transaction, owner, frame id, terminal status, and detail are required")
	}
	stream, err := latestFrameStreamConn(ctx, tx.conn, ownerID, frameID)
	if err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	var attempt, claimedRevision int64
	var runnerID, currentStatus string
	err = tx.conn.QueryRowContext(ctx, `
		SELECT attempt,runner_id,claimed_input_revision,status
		FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt DESC LIMIT 1`, stream.UID,
	).Scan(&attempt, &runnerID, &claimedRevision, &currentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, FinishReceipt{}, false, ErrClaimStale
	}
	if err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	payload, err := json.Marshal(map[string]string{"status": status, "detail": detail})
	if err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	destinations = terminalDeliveryDestinations(stream, destinations)
	clientMessageID := "runner-terminal:" + status + ":" + stream.UID + ":" + strconv.FormatInt(attempt, 10)
	if existing, found, err := findEventByClientID(ctx, tx.conn, stream.UID, clientMessageID); err != nil {
		return Event{}, FinishReceipt{}, false, err
	} else if found {
		record := eventRecord{
			clientMessageID: clientMessageID, eventType: "runner_finished", source: EventSourcePayload,
			runnerAttempt: &attempt, payloadJSON: payload, destinations: destinations, createdAt: existing.CreatedAt,
		}
		if !eventMatches(existing, record) {
			return Event{}, FinishReceipt{}, false, ErrEventConflict
		}
		storedDestinations, err := deliveryDestinations(ctx, tx.conn, stream.UID, existing.PublicationSeq)
		if err != nil {
			return Event{}, FinishReceipt{}, false, err
		}
		if !stringSlicesEqual(storedDestinations, normalizedDestinations(destinations)) {
			return Event{}, FinishReceipt{}, false, ErrEventConflict
		}
		receipt, err := loadRunnerReceiptConn(ctx, tx.conn, stream.UID, attempt)
		if err != nil || receipt.EventID != existing.EventID || receipt.Status != status {
			if err != nil {
				return Event{}, FinishReceipt{}, false, err
			}
			return Event{}, FinishReceipt{}, false, ErrEventConflict
		}
		if err := applyFrameTerminalStatusConn(ctx, tx.conn, stream, status, detail, receipt.FinishedAt); err != nil {
			return Event{}, FinishReceipt{}, false, err
		}
		return existing, receipt, false, nil
	}
	if currentStatus != "running" {
		return Event{}, FinishReceipt{}, false, ErrEventConflict
	}
	now := tx.repository.now().UTC()
	event, created, err := appendEventConn(ctx, tx.conn, stream, eventRecord{
		clientMessageID: clientMessageID, eventType: "runner_finished", source: EventSourcePayload,
		runnerAttempt: &attempt, payloadJSON: payload, destinations: destinations, createdAt: now,
	})
	if err != nil || !created {
		return Event{}, FinishReceipt{}, false, err
	}
	if _, err := bindCommittedArtifactReferencesConn(ctx, tx.conn, stream, attempt, event.EventID, now); err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	updated, err := tx.conn.ExecContext(ctx, `
		UPDATE transcript_runner_attempts SET status=?,phase='terminal',phase_sequence=phase_sequence+1,
			finished_event_id=?,finished_at=?
		WHERE stream_uid=? AND attempt=? AND runner_id=? AND status='running'`,
		status, event.EventID, now, stream.UID, attempt, runnerID)
	if err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		return Event{}, FinishReceipt{}, false, ErrClaimStale
	}
	if _, err := tx.conn.ExecContext(ctx, `
		INSERT INTO transcript_runner_receipts(stream_uid,attempt,event_id,status,finished_at)
		VALUES(?,?,?,?,?)`, stream.UID, attempt, event.EventID, status, now); err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	if _, err := tx.conn.ExecContext(ctx, `
		UPDATE transcript_streams SET consumed_input_revision=MAX(consumed_input_revision,?) WHERE stream_uid=?`,
		claimedRevision, stream.UID); err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	if err := applyFrameTerminalStatusConn(ctx, tx.conn, stream, status, detail, now); err != nil {
		return Event{}, FinishReceipt{}, false, err
	}
	return event, FinishReceipt{
		StreamUID: stream.UID, Attempt: attempt, EventID: event.EventID, Status: status, FinishedAt: now,
	}, true, nil
}

func latestFrameStreamConn(ctx context.Context, conn *sql.Conn, ownerID, frameID string) (Stream, error) {
	var streamUID string
	err := conn.QueryRowContext(ctx, `
		SELECT authority.active_stream_uid
		FROM transcript_frame_authority authority
		JOIN transcript_streams stream ON stream.stream_uid=authority.active_stream_uid
			AND stream.owner_id=authority.owner_id AND stream.session_id=authority.session_id
			AND stream.epoch=authority.active_epoch
		WHERE authority.owner_id=? AND stream.kind='frame_ref' AND stream.frame_id=?`, ownerID, frameID,
	).Scan(&streamUID)
	if errors.Is(err, sql.ErrNoRows) {
		return Stream{}, ErrSchemaUnavailable
	}
	if err != nil {
		return Stream{}, err
	}
	return getStreamConn(ctx, conn, streamUID, ownerID)
}

func normalizeCancelRunnerInput(input *CancelRunnerInput) error {
	if input == nil {
		return errors.New("runner cancellation input is required")
	}
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	if input.StreamUID == "" || input.OwnerID == "" || input.ExpectedAttempt <= 0 || input.ClientMessageID == "" ||
		!validReasonCode(input.ReasonCode) {
		return errors.New("stream, owner, expected attempt, client message id, and reason code are required")
	}
	return nil
}

func cancelRunnerConn(
	ctx context.Context,
	conn *sql.Conn,
	now time.Time,
	input CancelRunnerInput,
) (CancelRunnerResult, error) {
	payload, err := json.Marshal(cancellationPayload{Status: "cancelled", ReasonCode: input.ReasonCode})
	if err != nil {
		return CancelRunnerResult{}, err
	}
	var result CancelRunnerResult
	err = func() error {
		stream, err := getStreamConn(ctx, conn, input.StreamUID, input.OwnerID)
		if err != nil {
			return err
		}
		input.Destinations = terminalDeliveryDestinations(stream, input.Destinations)
		var attempt, claimedRevision int64
		var runnerID, status string
		err = conn.QueryRowContext(ctx, `
			SELECT attempt,runner_id,claimed_input_revision,status
			FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt DESC LIMIT 1`, stream.UID,
		).Scan(&attempt, &runnerID, &claimedRevision, &status)
		if errors.Is(err, sql.ErrNoRows) || attempt != input.ExpectedAttempt {
			return ErrClaimStale
		}
		if err != nil {
			return err
		}
		result.CurrentStatus = status
		if existing, found, err := findEventByClientID(ctx, conn, stream.UID, input.ClientMessageID); err != nil {
			return err
		} else if found {
			record := eventRecord{
				clientMessageID: input.ClientMessageID, eventType: "runner_finished", source: EventSourcePayload,
				runnerAttempt: &attempt, payloadJSON: payload, destinations: input.Destinations, createdAt: existing.CreatedAt,
			}
			if !eventMatches(existing, record) {
				return ErrEventConflict
			}
			destinations, err := deliveryDestinations(ctx, conn, existing.StreamUID, existing.PublicationSeq)
			if err != nil {
				return err
			}
			if !stringSlicesEqual(destinations, normalizedDestinations(input.Destinations)) {
				return ErrEventConflict
			}
			result.Event = existing
			result.Receipt, err = loadRunnerReceiptConn(ctx, conn, stream.UID, attempt)
			if err != nil || result.Receipt.EventID != existing.EventID || result.Receipt.Status != "cancelled" {
				if err != nil {
					return err
				}
				return ErrEventConflict
			}
			if _, err := bindCommittedArtifactReferencesConn(
				ctx, conn, stream, attempt, existing.EventID, result.Receipt.FinishedAt,
			); err != nil {
				return err
			}
			result.CurrentStatus = "cancelled"
			return applyFrameTerminalStatusConn(ctx, conn, stream, "cancelled", "", result.Receipt.FinishedAt)
		}
		if status != "running" {
			return nil
		}
		event, created, err := appendEventConn(ctx, conn, stream, eventRecord{
			clientMessageID: input.ClientMessageID, eventType: "runner_finished", source: EventSourcePayload,
			runnerAttempt: &attempt, payloadJSON: payload, destinations: input.Destinations, createdAt: now,
		})
		if err != nil || !created {
			return err
		}
		if _, err := bindCommittedArtifactReferencesConn(ctx, conn, stream, attempt, event.EventID, now); err != nil {
			return err
		}
		updated, err := conn.ExecContext(ctx, `
			UPDATE transcript_runner_attempts SET status='cancelled',phase='terminal',phase_sequence=phase_sequence+1,
				finished_event_id=?,finished_at=?
			WHERE stream_uid=? AND attempt=? AND runner_id=? AND status='running'`,
			event.EventID, now, stream.UID, attempt, runnerID)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			return ErrClaimStale
		}
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO transcript_runner_receipts(stream_uid,attempt,event_id,status,finished_at)
			VALUES(?,?,?,'cancelled',?)`, stream.UID, attempt, event.EventID, now); err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `
			UPDATE transcript_streams SET consumed_input_revision=MAX(consumed_input_revision,?) WHERE stream_uid=?`,
			claimedRevision, stream.UID); err != nil {
			return err
		}
		if err := applyFrameTerminalStatusConn(ctx, conn, stream, "cancelled", "", now); err != nil {
			return err
		}
		result.Applied = true
		result.CurrentStatus = "cancelled"
		result.Event = event
		result.Receipt = FinishReceipt{StreamUID: stream.UID, Attempt: attempt, EventID: event.EventID, Status: "cancelled", FinishedAt: now}
		return nil
	}()
	return result, err
}

func (r *Repository) AppendRunnerCheckpoint(
	ctx context.Context,
	input AppendRunnerCheckpointInput,
) (RunnerCheckpoint, Event, bool, error) {
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	if err := validateClaimInput(input.Claim); err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	if input.ClientMessageID == "" || !validCheckpointPhase(input.Phase) {
		return RunnerCheckpoint{}, Event{}, false, errors.New("client message id and active runner phase are required")
	}
	if err := validatePayload(EventSourcePayload, input.PayloadJSON, nil); err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	if input.CommitHook == nil && runnerCheckpointContainsModelToolCalls(input.PayloadJSON) {
		return RunnerCheckpoint{}, Event{}, false, errors.New("model tool-call checkpoints require a durable batch commit hook")
	}
	var checkpoint RunnerCheckpoint
	var event Event
	var created bool
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		checkpoint, event, created, err = appendRunnerCheckpointConn(ctx, conn, input, r.now().UTC())
		if err != nil {
			return err
		}
		if input.CommitHook != nil {
			receipt, err := input.CommitHook(ctx, &ImmediateTransaction{repository: r, conn: conn}, event, created)
			if err != nil {
				return err
			}
			return validateRunnerCheckpointCommitReceipt(ctx, conn, event, input.PayloadJSON, receipt)
		}
		return nil
	})
	if err != nil {
		return RunnerCheckpoint{}, Event{}, false, schemaError(err)
	}
	return checkpoint, event, created, nil
}

type runnerCheckpointDurableCall struct {
	ID                      string          `json:"id"`
	Name                    string          `json:"name"`
	Arguments               json.RawMessage `json:"arguments"`
	RejectedBeforeExecution bool            `json:"rejectedBeforeExecution,omitempty"`
}

func validateRunnerCheckpointCommitReceipt(
	ctx context.Context,
	conn *sql.Conn,
	event Event,
	payload []byte,
	receipt RunnerCheckpointCommitReceipt,
) error {
	var checkpoint struct {
		ModelToolCalls []runnerCheckpointDurableCall `json:"modelToolCalls"`
	}
	if err := json.Unmarshal(payload, &checkpoint); err != nil {
		return err
	}
	if err := validateRunnerCheckpointToolBatchReceipt(ctx, conn, event, checkpoint.ModelToolCalls, receipt.ToolBatch); err != nil {
		return err
	}
	type expectedOperation struct {
		receipt   RunnerCheckpointKernelOperationReceipt
		inputJSON []byte
	}
	expected := map[int64]expectedOperation{}
	for ordinal, call := range checkpoint.ModelToolCalls {
		if call.RejectedBeforeExecution {
			continue
		}
		call.Name = strings.TrimSpace(call.Name)
		if !kernelcontract.IsTool(call.Name) {
			continue
		}
		canonical, _, err := kernelcontract.CanonicalInput(call.Name, call.Arguments)
		if err != nil {
			// An invalid model call is intentionally excluded from the local
			// execution ledger. Its ordinary durable tool-batch item remains the
			// authority for returning the validation failure to the model.
			continue
		}
		digest := sha256.Sum256(canonical)
		expected[int64(ordinal)] = expectedOperation{
			receipt: RunnerCheckpointKernelOperationReceipt{
				Ordinal: int64(ordinal), ToolCallID: strings.TrimSpace(call.ID), Tool: call.Name,
				InputSHA256: hex.EncodeToString(digest[:]),
			},
			inputJSON: canonical,
		}
	}
	if len(expected) != len(receipt.KernelOperations) {
		return errors.New("kernel local operation commit receipt count is invalid")
	}
	receiptByOrdinal := map[int64]RunnerCheckpointKernelOperationReceipt{}
	for _, item := range receipt.KernelOperations {
		want, found := expected[item.Ordinal]
		if !found || strings.TrimSpace(item.OperationID) == "" || item.ToolCallID != want.receipt.ToolCallID ||
			item.Tool != want.receipt.Tool || item.InputSHA256 != want.receipt.InputSHA256 ||
			strings.TrimSpace(item.ApprovalRequestID) == "" || strings.TrimSpace(item.RequestedEventID) == "" ||
			item.InitialStateVersion != 1 {
			return errors.New("kernel local operation commit receipt is invalid")
		}
		if _, duplicate := receiptByOrdinal[item.Ordinal]; duplicate {
			return errors.New("kernel local operation commit receipt is duplicated")
		}
		receiptByOrdinal[item.Ordinal] = item
	}
	rows, err := conn.QueryContext(ctx, `SELECT operation_id,tool_call_ordinal,tool_call_id,tool,input_json,input_sha256,
		approval_request_id,state_version,frame_id
		FROM kernel_local_operations WHERE stream_uid=? AND source_event_id=? ORDER BY tool_call_ordinal`,
		event.StreamUID, event.EventID)
	if err != nil {
		return err
	}
	defer rows.Close()
	storedCount := 0
	for rows.Next() {
		var operationID, callID, tool, inputJSON, inputSHA, approvalRequestID, frameID string
		var ordinal, stateVersion int64
		if err := rows.Scan(&operationID, &ordinal, &callID, &tool, &inputJSON, &inputSHA,
			&approvalRequestID, &stateVersion, &frameID); err != nil {
			return err
		}
		want, found := expected[ordinal]
		item, received := receiptByOrdinal[ordinal]
		if !found || !received || operationID != item.OperationID || callID != want.receipt.ToolCallID ||
			tool != want.receipt.Tool || inputSHA != want.receipt.InputSHA256 ||
			approvalRequestID != item.ApprovalRequestID || stateVersion != item.InitialStateVersion {
			return errors.New("kernel local operation durable postcondition is invalid")
		}
		storedCanonical, _, err := kernelcontract.CanonicalInput(tool, []byte(inputJSON))
		if err != nil {
			return errors.New("kernel local operation durable input is invalid")
		}
		if !bytes.Equal(storedCanonical, []byte(inputJSON)) || !bytes.Equal(storedCanonical, want.inputJSON) {
			return errors.New("kernel local operation durable input does not match checkpoint")
		}
		storedDigest := sha256.Sum256(storedCanonical)
		if hex.EncodeToString(storedDigest[:]) != inputSHA {
			return errors.New("kernel local operation durable input digest is invalid")
		}
		var requestedFrameID, requestedType, requestedOperationID, requestedApprovalID string
		if err := conn.QueryRowContext(ctx, `SELECT frame_id,event_type,
			COALESCE(json_extract(payload,'$.operation_id'),''),COALESCE(json_extract(payload,'$.requestId'),'')
			FROM frame_events WHERE id=?`, item.RequestedEventID).Scan(
			&requestedFrameID, &requestedType, &requestedOperationID, &requestedApprovalID,
		); err != nil {
			return err
		}
		if requestedFrameID != frameID || requestedType != "kernel_local_exec_approval_requested" ||
			requestedOperationID != operationID || requestedApprovalID != approvalRequestID {
			return errors.New("kernel local operation approval projection is invalid")
		}
		var outboxCount int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM workspace_outbox WHERE idempotency_key=?`,
			"frame-event:"+item.RequestedEventID).Scan(&outboxCount); err != nil || outboxCount != 1 {
			if err != nil {
				return err
			}
			return errors.New("kernel local operation approval outbox is missing")
		}
		storedCount++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if storedCount != len(expected) {
		return errors.New("kernel local operation durable postcondition count is invalid")
	}
	return nil
}

func validateRunnerCheckpointToolBatchReceipt(
	ctx context.Context,
	conn *sql.Conn,
	event Event,
	calls []runnerCheckpointDurableCall,
	receipt *RunnerCheckpointToolBatchReceipt,
) error {
	if len(calls) == 0 {
		if receipt != nil {
			return errors.New("tool batch receipt is present without model tool calls")
		}
		return nil
	}
	if receipt == nil || strings.TrimSpace(receipt.BatchID) == "" || receipt.CallCount != int64(len(calls)) {
		return errors.New("model tool-call batch commit receipt is required")
	}
	var streamUID string
	var sourceEventID, callCount int64
	if err := conn.QueryRowContext(ctx, `SELECT stream_uid,source_event_id,call_count
		FROM transcript_tool_call_batches WHERE batch_id=?`, receipt.BatchID).Scan(
		&streamUID, &sourceEventID, &callCount,
	); err != nil {
		return err
	}
	if streamUID != event.StreamUID || sourceEventID != event.EventID || callCount != int64(len(calls)) {
		return errors.New("model tool-call batch durable identity is invalid")
	}
	rows, err := conn.QueryContext(ctx, `SELECT ordinal,tool_call_id,tool_name,arguments_json,arguments_sha256
		FROM transcript_tool_call_items WHERE batch_id=? ORDER BY ordinal`, receipt.BatchID)
	if err != nil {
		return err
	}
	defer rows.Close()
	ordinal := 0
	for rows.Next() {
		if ordinal >= len(calls) {
			return errors.New("model tool-call batch contains excess durable items")
		}
		var storedOrdinal int64
		var callID, toolName, argumentsJSON, argumentsSHA string
		if err := rows.Scan(&storedOrdinal, &callID, &toolName, &argumentsJSON, &argumentsSHA); err != nil {
			return err
		}
		call := calls[ordinal]
		var arguments any
		if err := json.Unmarshal(call.Arguments, &arguments); err != nil || arguments == nil {
			return errors.New("model tool-call batch arguments are invalid")
		}
		canonical, err := json.Marshal(arguments)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(canonical)
		if storedOrdinal != int64(ordinal) || callID != strings.TrimSpace(call.ID) ||
			toolName != strings.TrimSpace(call.Name) || argumentsJSON != string(canonical) ||
			argumentsSHA != hex.EncodeToString(digest[:]) {
			return errors.New("model tool-call batch durable item is invalid")
		}
		ordinal++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if ordinal != len(calls) {
		return errors.New("model tool-call batch durable item count is invalid")
	}
	return nil
}

func runnerCheckpointContainsModelToolCalls(payload []byte) bool {
	var checkpoint struct {
		ModelToolCalls []struct {
			Name string `json:"name"`
		} `json:"modelToolCalls"`
	}
	if json.Unmarshal(payload, &checkpoint) != nil {
		return false
	}
	return len(checkpoint.ModelToolCalls) > 0
}

func (r *Repository) PauseRunnerForInput(
	ctx context.Context,
	input AppendRunnerCheckpointInput,
) (RunnerCheckpoint, Event, bool, error) {
	if err := validatePauseRunnerForInput(&input); err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	var checkpoint RunnerCheckpoint
	var event Event
	var created bool
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		checkpoint, event, created, err = pauseRunnerForInputConn(ctx, conn, r, input)
		return err
	})
	return checkpoint, event, created, schemaError(err)
}

// PauseRunnerForApproval persists a resumable approval boundary and releases
// the exact runner lease. Approval is a durable scheduling boundary, not an
// in-process waiter; a later approval decision admits a new input revision.
func (r *Repository) PauseRunnerForApproval(
	ctx context.Context,
	input AppendRunnerCheckpointInput,
) (RunnerCheckpoint, Event, bool, error) {
	if err := validatePauseRunnerForApproval(&input); err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	var checkpoint RunnerCheckpoint
	var event Event
	var created bool
	err := r.withImmediate(ctx, func(conn *sql.Conn) error {
		var err error
		checkpoint, event, created, err = pauseRunnerForInputConn(ctx, conn, r, input)
		return err
	})
	return checkpoint, event, created, schemaError(err)
}

// ReleaseLatestFrameRunnerForResume fences an explicit user control decision
// to the latest durable waiting checkpoint and expires that exact lease inside
// the caller's workspace/Transcript transaction. It never creates a new
// runner attempt or consumes the newly appended input revision.
func (tx *ImmediateTransaction) ReleaseLatestFrameRunnerForResume(
	ctx context.Context,
	streamUID, ownerID string,
	expectedPhase RunnerPhase,
) (RunnerCheckpoint, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return RunnerCheckpoint{}, errors.New("transcript transaction is required")
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" ||
		(expectedPhase != RunnerPhaseWaitingUser && expectedPhase != RunnerPhaseWaitingApproval) {
		return RunnerCheckpoint{}, errors.New("stream, owner, and waiting phase are required")
	}
	stream, err := getStreamConn(ctx, tx.conn, streamUID, ownerID)
	if err != nil {
		return RunnerCheckpoint{}, schemaError(err)
	}
	if stream.Kind != StreamKindFrameRef {
		return RunnerCheckpoint{}, ErrEventConflict
	}
	var attempt, checkpointSequence int64
	var status, phase string
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT attempt,status,phase,last_checkpoint_sequence
		FROM transcript_runner_attempts WHERE stream_uid=?
		ORDER BY attempt DESC LIMIT 1`, stream.UID,
	).Scan(&attempt, &status, &phase, &checkpointSequence); err != nil {
		return RunnerCheckpoint{}, schemaError(err)
	}
	if attempt <= 0 || status != "running" || RunnerPhase(phase) != expectedPhase || checkpointSequence <= 0 {
		return RunnerCheckpoint{}, ErrCheckpointUnavailable
	}
	var checkpoint RunnerCheckpoint
	var checkpointPhase string
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT stream_uid,checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at
		FROM transcript_runner_checkpoints
		WHERE stream_uid=? AND runner_attempt=? AND checkpoint_sequence=?`,
		stream.UID, attempt, checkpointSequence,
	).Scan(&checkpoint.StreamUID, &checkpoint.Sequence, &checkpoint.Attempt, &checkpoint.EventID,
		&checkpointPhase, &checkpoint.Resumable, &checkpoint.CreatedAt); err != nil {
		return RunnerCheckpoint{}, schemaError(err)
	}
	checkpoint.Phase = RunnerPhase(checkpointPhase)
	if checkpoint.Attempt != attempt || checkpoint.Phase != expectedPhase || !checkpoint.Resumable {
		return RunnerCheckpoint{}, ErrCheckpointUnavailable
	}
	var receipts int
	if err := tx.conn.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transcript_runner_receipts WHERE stream_uid=? AND attempt=?`,
		stream.UID, attempt,
	).Scan(&receipts); err != nil {
		return RunnerCheckpoint{}, schemaError(err)
	}
	if receipts != 0 {
		return RunnerCheckpoint{}, ErrEventConflict
	}
	now := tx.repository.now().UTC()
	updated, err := tx.conn.ExecContext(ctx, `
		UPDATE transcript_runner_attempts SET expires_at=?
		WHERE stream_uid=? AND attempt=? AND status='running' AND phase=?
			AND last_checkpoint_sequence=?`,
		now, stream.UID, attempt, string(expectedPhase), checkpoint.Sequence,
	)
	if err != nil {
		return RunnerCheckpoint{}, schemaError(err)
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return RunnerCheckpoint{}, schemaError(err)
	}
	if changed != 1 {
		return RunnerCheckpoint{}, ErrClaimStale
	}
	return checkpoint, nil
}

// PauseRunnerForInput settles the exact claimed runner in the caller's
// existing workspace/transcript transaction.
func (tx *ImmediateTransaction) PauseRunnerForInput(
	ctx context.Context,
	input AppendRunnerCheckpointInput,
) (RunnerCheckpoint, Event, bool, error) {
	if tx == nil || tx.repository == nil || tx.conn == nil {
		return RunnerCheckpoint{}, Event{}, false, errors.New("transcript transaction is required")
	}
	if err := validatePauseRunnerForInput(&input); err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	checkpoint, event, created, err := pauseRunnerForInputConn(ctx, tx.conn, tx.repository, input)
	return checkpoint, event, created, schemaError(err)
}

func validatePauseRunnerForInput(input *AppendRunnerCheckpointInput) error {
	if input == nil {
		return errors.New("waiting-user resumable checkpoint is required")
	}
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	if err := validateClaimInput(input.Claim); err != nil {
		return err
	}
	if input.ClientMessageID == "" || input.Phase != RunnerPhaseWaitingUser || !input.Resumable {
		return errors.New("waiting-user resumable checkpoint is required")
	}
	return validatePayload(EventSourcePayload, input.PayloadJSON, nil)
}

func validatePauseRunnerForApproval(input *AppendRunnerCheckpointInput) error {
	if input == nil {
		return errors.New("waiting-approval resumable checkpoint is required")
	}
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	if err := validateClaimInput(input.Claim); err != nil {
		return err
	}
	if input.ClientMessageID == "" || input.Phase != RunnerPhaseWaitingApproval || !input.Resumable {
		return errors.New("waiting-approval resumable checkpoint is required")
	}
	return validatePayload(EventSourcePayload, input.PayloadJSON, nil)
}

func pauseRunnerForInputConn(
	ctx context.Context,
	conn *sql.Conn,
	repository *Repository,
	input AppendRunnerCheckpointInput,
) (RunnerCheckpoint, Event, bool, error) {
	now := repository.now().UTC()
	checkpoint, event, created, err := appendRunnerCheckpointConn(ctx, conn, input, now)
	if err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	if input.CommitHook != nil {
		receipt, err := input.CommitHook(ctx, &ImmediateTransaction{repository: repository, conn: conn}, event, created)
		if err != nil {
			return RunnerCheckpoint{}, Event{}, false, err
		}
		if err := validateRunnerCheckpointCommitReceipt(ctx, conn, event, input.PayloadJSON, receipt); err != nil {
			return RunnerCheckpoint{}, Event{}, false, err
		}
	}
	updated, err := conn.ExecContext(ctx, `
		UPDATE transcript_runner_attempts SET expires_at=?
		WHERE stream_uid=? AND attempt=? AND runner_id=? AND status='running' AND phase=?`,
		now, input.Claim.StreamUID, input.Claim.Attempt, input.Claim.RunnerID, input.Phase)
	if err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		return RunnerCheckpoint{}, Event{}, false, ErrClaimStale
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE transcript_streams SET consumed_input_revision=MAX(consumed_input_revision,?) WHERE stream_uid=?`,
		input.Claim.ClaimedInputRevision, input.Claim.StreamUID); err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	return checkpoint, event, created, nil
}

func appendRunnerCheckpointConn(
	ctx context.Context,
	conn *sql.Conn,
	input AppendRunnerCheckpointInput,
	now time.Time,
) (RunnerCheckpoint, Event, bool, error) {
	stream, err := getStreamConn(ctx, conn, input.Claim.StreamUID, input.Claim.OwnerID)
	if err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	record := eventRecord{
		clientMessageID: input.ClientMessageID, eventType: "runner_checkpoint", source: EventSourcePayload,
		runnerAttempt: &input.Claim.Attempt, payloadJSON: input.PayloadJSON,
		destinations: input.Destinations, createdAt: now,
	}
	if existing, found, err := findEventByClientID(ctx, conn, stream.UID, input.ClientMessageID); err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	} else if found {
		if _, err := validateClaimConn(ctx, conn, input.Claim, now, false); err != nil {
			return RunnerCheckpoint{}, Event{}, false, err
		}
		if !eventMatches(existing, record) {
			return RunnerCheckpoint{}, Event{}, false, ErrEventConflict
		}
		destinations, err := deliveryDestinations(ctx, conn, existing.StreamUID, existing.PublicationSeq)
		if err != nil {
			return RunnerCheckpoint{}, Event{}, false, err
		}
		if !stringSlicesEqual(destinations, normalizedDestinations(input.Destinations)) {
			return RunnerCheckpoint{}, Event{}, false, ErrEventConflict
		}
		checkpoint, err := loadCheckpointByEventConn(ctx, conn, stream.UID, input.Claim.Attempt, existing.EventID)
		if err != nil || checkpoint.Phase != input.Phase || checkpoint.Resumable != input.Resumable {
			if err != nil {
				return RunnerCheckpoint{}, Event{}, false, err
			}
			return RunnerCheckpoint{}, Event{}, false, ErrEventConflict
		}
		return checkpoint, existing, false, nil
	}
	stream, err = validateClaimConn(ctx, conn, input.Claim, now, true)
	if err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	event, created, err := appendEventConn(ctx, conn, stream, record)
	if err != nil || !created {
		if err != nil {
			return RunnerCheckpoint{}, Event{}, false, err
		}
		return RunnerCheckpoint{}, Event{}, false, ErrEventConflict
	}
	checkpoint := RunnerCheckpoint{
		StreamUID: stream.UID, Sequence: stream.NextCheckpoint, Attempt: input.Claim.Attempt,
		EventID: event.EventID, Phase: input.Phase, Resumable: input.Resumable, CreatedAt: now,
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO transcript_runner_checkpoints(
			stream_uid,checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at
		) VALUES(?,?,?,?,?,?,?)`, checkpoint.StreamUID, checkpoint.Sequence, checkpoint.Attempt, checkpoint.EventID,
		string(checkpoint.Phase), checkpoint.Resumable, checkpoint.CreatedAt); err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	updated, err := conn.ExecContext(ctx, `
		UPDATE transcript_runner_attempts SET phase=?,phase_sequence=phase_sequence+1,last_checkpoint_sequence=?
		WHERE stream_uid=? AND attempt=? AND runner_id=? AND status='running'`,
		string(input.Phase), checkpoint.Sequence, stream.UID, input.Claim.Attempt, input.Claim.RunnerID)
	if err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		return RunnerCheckpoint{}, Event{}, false, ErrClaimStale
	}
	if _, err := conn.ExecContext(ctx, `UPDATE transcript_streams SET next_checkpoint_sequence=? WHERE stream_uid=?`,
		checkpoint.Sequence+1, stream.UID); err != nil {
		return RunnerCheckpoint{}, Event{}, false, err
	}
	return checkpoint, event, true, nil
}

func (r *Repository) GetRunnerRuntimeState(
	ctx context.Context,
	streamUID, ownerID string,
	attempt int64,
) (RunnerRuntimeState, error) {
	db := r.readDatabase()
	if db == nil {
		return RunnerRuntimeState{}, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" || attempt <= 0 {
		return RunnerRuntimeState{}, errors.New("stream, owner, and positive attempt are required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerRuntimeState{}, err
	}
	var state RunnerRuntimeState
	var phase, resumeSource string
	var finishedEventID sql.NullInt64
	var finishedAt sql.NullTime
	err := db.QueryRowContext(ctx, `
		SELECT stream_uid,attempt,runner_id,claimed_input_revision,status,phase,phase_sequence,last_checkpoint_sequence,
			resume_source,resume_checkpoint_sequence,claimed_at,expires_at,finished_event_id,finished_at
		FROM transcript_runner_attempts WHERE stream_uid=? AND attempt=?`, streamUID, attempt,
	).Scan(
		&state.StreamUID, &state.Attempt, &state.RunnerID, &state.ClaimedInputRevision, &state.Status, &phase,
		&state.PhaseSequence, &state.LastCheckpointSequence, &resumeSource, &state.ResumeCheckpoint,
		&state.ClaimedAt, &state.ExpiresAt, &finishedEventID, &finishedAt,
	)
	if err != nil {
		return RunnerRuntimeState{}, schemaError(err)
	}
	state.Phase = RunnerPhase(phase)
	state.ResumeSource = ResumeSource(resumeSource)
	if finishedEventID.Valid {
		state.FinishedEventID = finishedEventID.Int64
	}
	state.FinishedAt = nullableTimePointer(finishedAt)
	return state, nil
}

// NextRunnerReviewIndex returns the next durable completion-review ordinal for
// one logical task attempt. Lease rotation and bounded execution units reuse
// the attempt, so reviewer child identities advance independently of the
// worker process and runner ID.
func (r *Repository) NextRunnerReviewIndex(ctx context.Context, streamUID, ownerID string, attempt int64) (int, error) {
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if r == nil || r.db == nil || streamUID == "" || ownerID == "" || attempt <= 0 {
		return 0, ErrEventConflict
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return 0, err
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT CAST(json_extract(payload_json,'$.reviewIndex') AS INTEGER),
			json_extract(payload_json,'$.status')
		FROM transcript_events
		WHERE stream_uid=? AND runner_attempt=? AND event_type='runner_checkpoint'
			AND json_type(payload_json,'$.reviewIndex')='integer'
			AND json_extract(payload_json,'$.toolPhase')='verification'
		ORDER BY event_id`, streamUID, attempt)
	if err != nil {
		return 0, schemaError(err)
	}
	defer rows.Close()
	states := map[int64]string{}
	latest := int64(-1)
	for rows.Next() {
		var index int64
		var status string
		if err := rows.Scan(&index, &status); err != nil {
			return 0, schemaError(err)
		}
		status = strings.TrimSpace(status)
		if index < 0 || (index >= 0 && uint64(index) >= uint64(^uint(0)>>1)) {
			return 0, ErrEventConflict
		}
		switch status {
		case "running", "completed", "failed", "cancelled", "canceled":
		default:
			return 0, ErrEventConflict
		}
		if previous := states[index]; previous != "" && previous != "running" {
			return 0, ErrEventConflict
		}
		states[index] = status
		if index > latest {
			latest = index
		}
	}
	if err := rows.Err(); err != nil {
		return 0, schemaError(err)
	}
	for index := int64(0); index <= latest; index++ {
		status, found := states[index]
		if !found {
			return 0, ErrEventConflict
		}
		if status == "running" {
			if index != latest {
				return 0, ErrEventConflict
			}
			return int(index), nil
		}
	}
	return int(latest + 1), nil
}

func (r *Repository) GetLatestRunnerRuntimeState(
	ctx context.Context,
	streamUID, ownerID string,
) (RunnerRuntimeState, bool, error) {
	db := r.readDatabase()
	if db == nil {
		return RunnerRuntimeState{}, false, ErrSchemaUnavailable
	}
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if streamUID == "" || ownerID == "" {
		return RunnerRuntimeState{}, false, errors.New("stream and owner are required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerRuntimeState{}, false, err
	}
	var attempt int64
	if err := db.QueryRowContext(ctx, `
		SELECT attempt FROM transcript_runner_attempts
		WHERE stream_uid=? ORDER BY attempt DESC LIMIT 1`, streamUID,
	).Scan(&attempt); errors.Is(err, sql.ErrNoRows) {
		return RunnerRuntimeState{}, false, nil
	} else if err != nil {
		return RunnerRuntimeState{}, false, schemaError(err)
	}
	state, err := r.GetRunnerRuntimeState(ctx, streamUID, ownerID, attempt)
	return state, err == nil, err
}

func (r *Repository) GetLatestRunnerTimingSummary(
	ctx context.Context,
	streamUID, ownerID string,
) (RunnerTimingSummary, bool, error) {
	db := r.readDatabase()
	streamUID = strings.TrimSpace(streamUID)
	ownerID = strings.TrimSpace(ownerID)
	if db == nil || streamUID == "" || ownerID == "" {
		return RunnerTimingSummary{}, false, errors.New("stream and owner are required")
	}
	if _, err := r.GetStream(ctx, streamUID, ownerID); err != nil {
		return RunnerTimingSummary{}, false, err
	}
	var summary RunnerTimingSummary
	var latestFinishedEventID sql.NullInt64
	var latestFinishedAt sql.NullTime
	var latestExpiresAt time.Time
	err := db.QueryRowContext(ctx, `
		SELECT stream_uid,attempt,claimed_input_revision,status,expires_at,finished_event_id,finished_at
		FROM transcript_runner_attempts
		WHERE stream_uid=? ORDER BY attempt DESC LIMIT 1`, streamUID,
	).Scan(
		&summary.StreamUID, &summary.Attempt, &summary.InputRevision, &summary.Status,
		&latestExpiresAt, &latestFinishedEventID, &latestFinishedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerTimingSummary{}, false, nil
	}
	if err != nil {
		return RunnerTimingSummary{}, false, schemaError(err)
	}
	summary.ObservedAt = r.now().UTC()
	if latestFinishedEventID.Valid {
		summary.FinishedEventID = latestFinishedEventID.Int64
	}
	if latestFinishedAt.Valid {
		finishedAt := latestFinishedAt.Time.UTC()
		summary.FinishedAt = &finishedAt
	}
	summary.Active = summary.Status == "running" && summary.FinishedAt == nil && latestExpiresAt.After(summary.ObservedAt)
	if summary.Status == "running" && summary.FinishedAt != nil || summary.Status != "running" && summary.FinishedAt == nil {
		return RunnerTimingSummary{}, false, ErrEventConflict
	}

	rows, err := db.QueryContext(ctx, `
		SELECT attempt,status,claimed_at,expires_at,finished_at
		FROM transcript_runner_attempts
		WHERE stream_uid=? AND claimed_input_revision=?
		ORDER BY attempt`, streamUID, summary.InputRevision)
	if err != nil {
		return RunnerTimingSummary{}, false, schemaError(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var attempt int64
		var status string
		var claimedAt time.Time
		var expiresAt time.Time
		var finishedAt sql.NullTime
		if err := rows.Scan(&attempt, &status, &claimedAt, &expiresAt, &finishedAt); err != nil {
			return RunnerTimingSummary{}, false, schemaError(err)
		}
		claimedAt = claimedAt.UTC()
		if !found {
			summary.StartedAt = claimedAt
			found = true
		}
		end := summary.ObservedAt
		if finishedAt.Valid {
			end = finishedAt.Time.UTC()
		} else if attempt != summary.Attempt || status != "running" {
			return RunnerTimingSummary{}, false, ErrEventConflict
		} else if expiresAt.Before(end) {
			end = expiresAt.UTC()
		}
		if end.Before(claimedAt) {
			return RunnerTimingSummary{}, false, ErrEventConflict
		}
		segment := end.Sub(claimedAt)
		if segment < 0 || summary.Elapsed > time.Duration(1<<63-1)-segment {
			return RunnerTimingSummary{}, false, ErrEventConflict
		}
		summary.Elapsed += segment
	}
	if err := rows.Err(); err != nil {
		return RunnerTimingSummary{}, false, schemaError(err)
	}
	if !found || summary.StartedAt.IsZero() {
		return RunnerTimingSummary{}, false, ErrEventConflict
	}
	return summary, true, nil
}

func validateResumeRequest(source ResumeSource, checkpointSequence int64) error {
	switch source {
	case ResumeSourceCheckpoint:
		if checkpointSequence <= 0 {
			return errors.New("checkpoint resume requires a positive checkpoint sequence")
		}
	case ResumeSourceFresh, ResumeSourceRetry, ResumeSourceUserInput:
		if checkpointSequence != 0 {
			return errors.New("non-checkpoint resume cannot name a checkpoint")
		}
	default:
		return errors.New("explicit runner resume source is required")
	}
	return nil
}

func validateResumeCheckpointConn(
	ctx context.Context,
	conn *sql.Conn,
	streamUID string,
	source ResumeSource,
	checkpointSequence int64,
	claimedInputRevision int64,
	allowEarlierRevision bool,
	now time.Time,
) error {
	if source != ResumeSourceCheckpoint {
		return nil
	}
	var latestSequence, latestRevision int64
	var latestStatus string
	var latestExpires time.Time
	if err := conn.QueryRowContext(ctx, `
		SELECT checkpoint.checkpoint_sequence,attempt.claimed_input_revision,attempt.status,attempt.expires_at
		FROM transcript_runner_checkpoints checkpoint
		JOIN transcript_runner_attempts attempt
			ON attempt.stream_uid=checkpoint.stream_uid AND attempt.attempt=checkpoint.runner_attempt
		WHERE checkpoint.stream_uid=? AND checkpoint.resumable=1
			AND attempt.attempt=(SELECT MAX(attempt) FROM transcript_runner_attempts WHERE stream_uid=?)
		ORDER BY checkpoint.checkpoint_sequence DESC LIMIT 1`, streamUID, streamUID,
	).Scan(&latestSequence, &latestRevision, &latestStatus, &latestExpires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCheckpointUnavailable
		}
		return err
	}
	eligibleAttempt := latestStatus == "failed" || latestStatus == "cancelled" || latestStatus == "reclaimed" ||
		latestStatus == "running" && !latestExpires.After(now)
	if !eligibleAttempt && !allowEarlierRevision {
		return ErrCheckpointUnavailable
	}
	if latestSequence != checkpointSequence || latestRevision != claimedInputRevision &&
		(!allowEarlierRevision || latestRevision <= 0 || latestRevision > claimedInputRevision) {
		return ErrCheckpointUnavailable
	}
	return nil
}

func claimableInputRevisionConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	resumeSource ResumeSource,
	now time.Time,
) (int64, bool, error) {
	if stream.InputRevision > stream.ConsumedInputRevision {
		return stream.InputRevision, true, nil
	}
	if resumeSource != ResumeSourceCheckpoint && resumeSource != ResumeSourceRetry && resumeSource != ResumeSourceUserInput {
		return 0, false, nil
	}
	var status string
	var claimedRevision int64
	var expiresAt time.Time
	err := conn.QueryRowContext(ctx, `
		SELECT status,claimed_input_revision,expires_at
		FROM transcript_runner_attempts WHERE stream_uid=? ORDER BY attempt DESC LIMIT 1`, stream.UID,
	).Scan(&status, &claimedRevision, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	if status == "failed" || status == "cancelled" || status == "reclaimed" || status == "running" && !expiresAt.After(now) {
		return claimedRevision, true, nil
	}
	return 0, false, nil
}

func loadCheckpointByEventConn(
	ctx context.Context,
	conn *sql.Conn,
	streamUID string,
	attempt, eventID int64,
) (RunnerCheckpoint, error) {
	var checkpoint RunnerCheckpoint
	var phase string
	err := conn.QueryRowContext(ctx, `
		SELECT stream_uid,checkpoint_sequence,runner_attempt,event_id,phase,resumable,created_at
		FROM transcript_runner_checkpoints
		WHERE stream_uid=? AND runner_attempt=? AND event_id=?`, streamUID, attempt, eventID,
	).Scan(
		&checkpoint.StreamUID, &checkpoint.Sequence, &checkpoint.Attempt, &checkpoint.EventID,
		&phase, &checkpoint.Resumable, &checkpoint.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerCheckpoint{}, ErrEventConflict
	}
	checkpoint.Phase = RunnerPhase(phase)
	return checkpoint, err
}

func validCheckpointPhase(phase RunnerPhase) bool {
	switch phase {
	case RunnerPhasePlanning, RunnerPhaseExecuting, RunnerPhaseWaitingUser,
		RunnerPhaseWaitingApproval, RunnerPhaseWaitingExternal:
		return true
	default:
		return false
	}
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func loadRunnerReceiptConn(ctx context.Context, conn *sql.Conn, streamUID string, attempt int64) (FinishReceipt, error) {
	var receipt FinishReceipt
	err := conn.QueryRowContext(ctx, `
		SELECT stream_uid,attempt,event_id,status,finished_at
		FROM transcript_runner_receipts WHERE stream_uid=? AND attempt=?`, streamUID, attempt,
	).Scan(&receipt.StreamUID, &receipt.Attempt, &receipt.EventID, &receipt.Status, &receipt.FinishedAt)
	return receipt, err
}

func validReasonCode(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func reclaimExpiredRunnerConn(
	ctx context.Context,
	conn *sql.Conn,
	stream Stream,
	attempt int64,
	now time.Time,
) (Stream, error) {
	payload := []byte(`{"status":"reclaimed","reason_code":"lease_expired"}`)
	event, created, err := appendEventConn(ctx, conn, stream, eventRecord{
		clientMessageID: "runner-reclaimed-" + strconv.FormatInt(attempt, 10),
		eventType:       "runner_reclaimed", source: EventSourcePayload, runnerAttempt: &attempt,
		payloadJSON: payload, destinations: []string{"ws"}, createdAt: now,
	})
	if err != nil {
		return Stream{}, err
	}
	if !created {
		return Stream{}, ErrEventConflict
	}
	if _, err := bindCommittedArtifactReferencesConn(ctx, conn, stream, attempt, event.EventID, now); err != nil {
		return Stream{}, err
	}
	updated, err := conn.ExecContext(ctx, `
		UPDATE transcript_runner_attempts SET status='reclaimed',phase='terminal',phase_sequence=phase_sequence+1,
			finished_event_id=?,finished_at=?
		WHERE stream_uid=? AND attempt=? AND status='running' AND expires_at<=?`,
		event.EventID, now, stream.UID, attempt, now)
	if err != nil {
		return Stream{}, err
	}
	if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
		return Stream{}, ErrClaimStale
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO transcript_runner_receipts(stream_uid,attempt,event_id,status,finished_at)
		VALUES(?,?,?,'reclaimed',?)`, stream.UID, attempt, event.EventID, now); err != nil {
		return Stream{}, err
	}
	stream.NextEventID = event.EventID + 1
	stream.NextPublication = event.PublicationSeq + 1
	stream.UpdatedAt = now
	return stream, nil
}
