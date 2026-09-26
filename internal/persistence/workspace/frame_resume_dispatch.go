package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	frameResumeDispatchRegistered = "registered"
	frameResumeDispatchClaimed    = "claimed"
	frameResumeDispatchBlocked    = "blocked"
	frameResumeDispatchCompleted  = "completed"
	frameResumeDispatchFailed     = "failed"
	frameResumeDispatchCancelled  = "cancelled"

	// CompatibilityFrameResumeDispatchWaitModelSelection parks one durable
	// dispatch until an explicit model selection or continue action wakes it.
	// It is not a timer and therefore cannot expire a long-running task.
	CompatibilityFrameResumeDispatchWaitModelSelection    = "model_selection"
	CompatibilityFrameResumeDispatchWaitRecoveryCondition = "recovery_condition"
)

type CompatibilityFrameResumeDispatch struct {
	ResumeEvent    FrameEvent
	RootFrameID    string
	ProjectID      string
	FrameID        string
	AgentName      string
	FrameStatus    string
	PreviousStatus string
	Status         string
	ClaimToken     string
	ClaimOwner     string
	Attempt        int
	Recovered      bool
	LeaseExpiresAt time.Time
	NotBefore      time.Time
	WaitingFor     string
	Error          string
	AuditEvents    []FrameEvent
}

type CompatibilityFrameResumeDispatchWakeState string

const (
	CompatibilityFrameResumeDispatchWakeNone    CompatibilityFrameResumeDispatchWakeState = ""
	CompatibilityFrameResumeDispatchWakePending CompatibilityFrameResumeDispatchWakeState = "pending"
	CompatibilityFrameResumeDispatchWakeArmed   CompatibilityFrameResumeDispatchWakeState = "armed"
	CompatibilityFrameResumeDispatchWakeWoken   CompatibilityFrameResumeDispatchWakeState = "woken"
)

type CompleteCompatibilityFrameResumeDispatchInput struct {
	ResumeEventID   string
	ExpectedAttempt int
	ClaimToken      string
	Status          string
	Message         string
	Details         map[string]any
}

type RequeueCompatibilityFrameResumeDispatchInput struct {
	ResumeEventID            string
	ExpectedAttempt          int
	ClaimToken               string
	ReasonCode               string
	RunnerAttempt            int
	CheckpointEventID        int64
	NotBefore                time.Time
	WaitingFor               string
	RecoveryContractRevision int
}

// ConvergeCompatibilityFrameResumeDispatchInput retires a resume dispatch
// after a newer authoritative runner has already claimed every current input
// revision and entered execution. The Frame itself remains active; only the
// redundant dispatch is terminalized.
type ConvergeCompatibilityFrameResumeDispatchInput struct {
	ResumeEventID        string
	ExpectedAttempt      int
	ClaimToken           string
	RunnerID             string
	RunnerAttempt        int64
	ClaimedInputRevision int64
}

type AutoResumeDispatchTranscriptCoordinate struct {
	RunnerAttempt     int64
	CheckpointEventID int64
}

type compatibilityFrameResumeDispatchRow struct {
	dispatch   CompatibilityFrameResumeDispatch
	rawPayload string
}

func (s *Store) GetCompatibilityFrameResumeDispatch(eventID string) (CompatibilityFrameResumeDispatch, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityFrameResumeDispatch{}, false, errors.New("workspace store is closed")
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return CompatibilityFrameResumeDispatch{}, false, errors.New("resume event id is required")
	}
	row, found, err := scanCompatibilityFrameResumeDispatch(s.db.QueryRowContext(context.Background(), `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.id = ? AND e.event_type = 'frame_resumed'`, eventID))
	if err != nil || !found {
		return CompatibilityFrameResumeDispatch{}, found, err
	}
	return row.dispatch, true, nil
}

func (s *Store) GetCompatibilityFrameResumeDispatchByFrame(frameID string) (CompatibilityFrameResumeDispatch, bool, error) {
	// Resume status is mutation authority. Read it from the primary connection
	// so a just-blocked dispatch cannot be mistaken for an older registered
	// snapshot and strand newly admitted user input.
	if s == nil || s.db == nil {
		return CompatibilityFrameResumeDispatch{}, false, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	if frameID == "" {
		return CompatibilityFrameResumeDispatch{}, false, errors.New("frame id is required")
	}
	row, found, err := scanCompatibilityFrameResumeDispatch(s.db.QueryRowContext(context.Background(), `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.frame_id = ? AND e.event_type = 'frame_resumed'
			AND json_extract(e.payload, '$.dispatch.status') IS NOT NULL
		ORDER BY CASE WHEN json_extract(e.payload, '$.dispatch.status') IN ('registered','claimed','blocked') THEN 0 ELSE 1 END,
			e.sequence DESC LIMIT 1`, frameID))
	if err != nil || !found {
		return CompatibilityFrameResumeDispatch{}, found, err
	}
	return row.dispatch, true, nil
}

// ExpireCompatibilityFrameResumeDispatchesClaimedBefore releases dispatcher
// leases owned by a previous server process. The startup timestamp is captured
// before this process starts dispatch workers, so a newly claimed dispatch is
// never fenced. Expired claims are recovered by the existing atomic claim path
// with a new token and attempt number.
func (s *Store) ExpireCompatibilityFrameResumeDispatchesClaimedBefore(startup time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	startup = startup.UTC()
	if startup.IsZero() {
		return 0, errors.New("runtime startup timestamp is required")
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(context.Background(), `
		UPDATE frame_events
		SET payload=json_set(payload,'$.dispatch.leaseExpiresAt',?)
		WHERE event_type='frame_resumed'
		  AND json_extract(payload,'$.dispatch.status')='claimed'
		  AND julianday(json_extract(payload,'$.dispatch.claimedAt'))<julianday(?)
		  AND julianday(json_extract(payload,'$.dispatch.leaseExpiresAt'))>julianday(?)`,
		now.Format(time.RFC3339Nano), startup.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("expire previous runtime resume dispatches: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count expired previous runtime resume dispatches: %w", err)
	}
	return count, nil
}

// ListLegacyBlockedCompatibilityFrameResumeDispatches exists only to migrate
// pre-terminal-state rows. New code never creates blocked dispatches.
func (s *Store) ListLegacyBlockedCompatibilityFrameResumeDispatches(limit int) ([]CompatibilityFrameResumeDispatch, error) {
	// The recovery scan participates in the resume control plane and therefore
	// must observe the same primary state that Wake mutates.
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if limit <= 0 {
		return nil, errors.New("positive resume dispatch limit is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.event_type = 'frame_resumed'
			AND json_extract(e.payload, '$.dispatch.status') = 'blocked'
			AND NOT EXISTS (
				SELECT 1 FROM frame_events later
				WHERE later.frame_id = e.frame_id AND later.event_type = 'frame_resumed'
					AND json_extract(later.payload, '$.dispatch.status') IN ('registered','claimed','blocked')
					AND later.sequence > e.sequence
			)
		ORDER BY e.created_at, e.id
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list blocked resume dispatches: %w", err)
	}
	defer rows.Close()
	dispatches := make([]CompatibilityFrameResumeDispatch, 0)
	for rows.Next() {
		row, found, scanErr := scanCompatibilityFrameResumeDispatch(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if found {
			dispatches = append(dispatches, row.dispatch)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return dispatches, nil
}

func (s *Store) ClaimNextCompatibilityFrameResumeDispatch(owner string, ttl time.Duration) (CompatibilityFrameResumeDispatch, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityFrameResumeDispatch{}, false, errors.New("workspace store is closed")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return CompatibilityFrameResumeDispatch{}, false, errors.New("resume dispatch owner is required")
	}
	if ttl <= 0 {
		return CompatibilityFrameResumeDispatch{}, false, errors.New("resume dispatch lease must be positive")
	}
	for retry := 0; retry < 3; retry++ {
		result, claimed, retryable, err := s.claimNextCompatibilityFrameResumeDispatch(owner, ttl)
		if err != nil {
			return CompatibilityFrameResumeDispatch{}, false, err
		}
		if !retryable {
			return result, claimed, nil
		}
	}
	return CompatibilityFrameResumeDispatch{}, false, errors.New("resume dispatch claim changed concurrently")
}

func (s *Store) claimNextCompatibilityFrameResumeDispatch(owner string, ttl time.Duration) (CompatibilityFrameResumeDispatch, bool, bool, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CompatibilityFrameResumeDispatch{}, false, false, fmt.Errorf("begin resume dispatch claim: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.event_type = 'frame_resumed'
			AND (
				(json_extract(e.payload, '$.dispatch.status') = 'registered' AND f.status IN ('processing', 'running'))
				OR json_extract(e.payload, '$.dispatch.status') = 'claimed'
			)
			AND NOT EXISTS (
				SELECT 1 FROM frame_events later
				WHERE later.frame_id = e.frame_id AND later.event_type = 'frame_resumed'
					AND json_extract(later.payload, '$.dispatch.status') IN ('registered','claimed','blocked')
					AND later.sequence > e.sequence
			)
		ORDER BY e.created_at, e.id`)
	if err != nil {
		return CompatibilityFrameResumeDispatch{}, false, false, fmt.Errorf("list resume dispatch claims: %w", err)
	}
	candidates := make([]compatibilityFrameResumeDispatchRow, 0)
	for rows.Next() {
		row, found, err := scanCompatibilityFrameResumeDispatch(rows)
		if err != nil {
			_ = rows.Close()
			return CompatibilityFrameResumeDispatch{}, false, false, err
		}
		if found {
			candidates = append(candidates, row)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CompatibilityFrameResumeDispatch{}, false, false, fmt.Errorf("iterate resume dispatch claims: %w", err)
	}
	if err := rows.Close(); err != nil {
		return CompatibilityFrameResumeDispatch{}, false, false, fmt.Errorf("close resume dispatch claims: %w", err)
	}

	now := s.now().UTC()
	var selected *compatibilityFrameResumeDispatchRow
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.dispatch.Status == frameResumeDispatchRegistered && candidate.dispatch.WaitingFor == "" && !candidate.dispatch.NotBefore.After(now) ||
			candidate.dispatch.Status == frameResumeDispatchClaimed && !candidate.dispatch.LeaseExpiresAt.After(now) {
			selected = candidate
			break
		}
	}
	if selected == nil {
		if err := tx.Commit(); err != nil {
			return CompatibilityFrameResumeDispatch{}, false, false, fmt.Errorf("commit empty resume dispatch claim: %w", err)
		}
		return CompatibilityFrameResumeDispatch{}, false, false, nil
	}

	recovered := selected.dispatch.Status == frameResumeDispatchClaimed || selected.dispatch.Attempt > 0
	payload := selected.dispatch.ResumeEvent.Payload
	dispatchPayload, ok := payload["dispatch"].(map[string]any)
	if !ok {
		return CompatibilityFrameResumeDispatch{}, false, false, errors.New("resume event dispatch payload is invalid")
	}
	attempt := selected.dispatch.Attempt + 1
	claimToken := uuid.NewString()
	leaseExpiresAt := now.Add(ttl)
	dispatchPayload["status"] = frameResumeDispatchClaimed
	dispatchPayload["claimOwner"] = owner
	dispatchPayload["claimToken"] = claimToken
	dispatchPayload["attempt"] = attempt
	dispatchPayload["claimedAt"] = now.Format(time.RFC3339Nano)
	dispatchPayload["leaseExpiresAt"] = leaseExpiresAt.Format(time.RFC3339Nano)
	delete(dispatchPayload, "notBefore")
	delete(dispatchPayload, "waitingFor")
	delete(dispatchPayload, "modelSwitchRevision")
	delete(dispatchPayload, "modelSwitchRequestedAt")
	delete(dispatchPayload, "wakeReasonCode")
	if recovered {
		dispatchPayload["recoveryCount"] = resumeDispatchInt(dispatchPayload["recoveryCount"]) + 1
	}
	rawUpdated, err := json.Marshal(payload)
	if err != nil {
		return CompatibilityFrameResumeDispatch{}, false, false, fmt.Errorf("encode claimed resume dispatch: %w", err)
	}
	update, err := tx.ExecContext(ctx,
		`UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`,
		string(rawUpdated), selected.dispatch.ResumeEvent.ID, selected.rawPayload,
	)
	if err != nil {
		return CompatibilityFrameResumeDispatch{}, false, false, fmt.Errorf("claim resume dispatch: %w", err)
	}
	if changed, err := update.RowsAffected(); err != nil {
		return CompatibilityFrameResumeDispatch{}, false, false, fmt.Errorf("count resume dispatch claim: %w", err)
	} else if changed != 1 {
		return CompatibilityFrameResumeDispatch{}, false, true, nil
	}

	claimEvent, err := appendCompatibilityDispatchAuditEvent(ctx, tx,
		fmt.Sprintf("frame-resume-dispatch-claim:%s:%d", selected.dispatch.ResumeEvent.ID, attempt),
		selected.dispatch.FrameID, "frame_resume_dispatch_claimed",
		map[string]any{
			"resumeEventId": selected.dispatch.ResumeEvent.ID,
			"claimToken":    claimToken, "claimOwner": owner, "attempt": attempt,
		}, now,
	)
	if err != nil {
		return CompatibilityFrameResumeDispatch{}, false, false, err
	}
	auditEvents := []FrameEvent{claimEvent}
	if recovered {
		recoveryEvent, err := appendCompatibilityDispatchAuditEvent(ctx, tx,
			fmt.Sprintf("frame-resume-dispatch-recovery:%s:%d", selected.dispatch.ResumeEvent.ID, attempt),
			selected.dispatch.FrameID, "frame_resume_dispatch_recovered",
			map[string]any{
				"resumeEventId": selected.dispatch.ResumeEvent.ID,
				"claimToken":    claimToken, "claimOwner": owner, "attempt": attempt,
			}, now,
		)
		if err != nil {
			return CompatibilityFrameResumeDispatch{}, false, false, err
		}
		auditEvents = append(auditEvents, recoveryEvent)
	}
	if err := tx.Commit(); err != nil {
		return CompatibilityFrameResumeDispatch{}, false, false, fmt.Errorf("commit resume dispatch claim: %w", err)
	}

	selected.dispatch.ResumeEvent.Payload = payload
	selected.dispatch.Status = frameResumeDispatchClaimed
	selected.dispatch.ClaimOwner = owner
	selected.dispatch.ClaimToken = claimToken
	selected.dispatch.Attempt = attempt
	selected.dispatch.Recovered = recovered
	selected.dispatch.LeaseExpiresAt = leaseExpiresAt
	selected.dispatch.AuditEvents = auditEvents
	return selected.dispatch, true, false, nil
}

func (s *Store) RenewCompatibilityFrameResumeDispatch(eventID string, expectedAttempt int, claimToken string, ttl time.Duration) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	eventID, claimToken = strings.TrimSpace(eventID), strings.TrimSpace(claimToken)
	if eventID == "" || expectedAttempt <= 0 || claimToken == "" {
		return false, errors.New("resume event id, positive expected attempt, and claim token are required")
	}
	if ttl <= 0 {
		return false, errors.New("resume dispatch lease must be positive")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin resume dispatch renewal: %w", err)
	}
	defer tx.Rollback()
	row, found, err := scanCompatibilityFrameResumeDispatch(tx.QueryRowContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.id = ? AND e.event_type = 'frame_resumed'`, eventID))
	if err != nil || !found {
		return false, err
	}
	if row.dispatch.Status != frameResumeDispatchClaimed || row.dispatch.Attempt != expectedAttempt || row.dispatch.ClaimToken != claimToken {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	dispatchPayload := row.dispatch.ResumeEvent.Payload["dispatch"].(map[string]any)
	dispatchPayload["leaseExpiresAt"] = s.now().UTC().Add(ttl).Format(time.RFC3339Nano)
	rawUpdated, err := json.Marshal(row.dispatch.ResumeEvent.Payload)
	if err != nil {
		return false, err
	}
	update, err := tx.ExecContext(ctx,
		`UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`,
		string(rawUpdated), eventID, row.rawPayload,
	)
	if err != nil {
		return false, fmt.Errorf("renew resume dispatch: %w", err)
	}
	changed, err := update.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed == 1, nil
}

// RequeueCompatibilityFrameResumeDispatch releases one exact dispatcher claim
// after its runner persisted a resumable interruption. It does not terminalize
// the Frame or reset the dispatch generation; a later worker must claim the
// same durable resume event with a new attempt and token.
func (s *Store) RequeueCompatibilityFrameResumeDispatch(input RequeueCompatibilityFrameResumeDispatchInput) (FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, false, errors.New("workspace store is closed")
	}
	input.ResumeEventID = strings.TrimSpace(input.ResumeEventID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	input.WaitingFor = strings.TrimSpace(input.WaitingFor)
	if input.ResumeEventID == "" || input.ExpectedAttempt <= 0 || input.ClaimToken == "" || input.ReasonCode == "" {
		return FrameEvent{}, false, errors.New("resume event id, positive expected attempt, claim token, and interruption reason are required")
	}
	if input.WaitingFor != "" && input.WaitingFor != CompatibilityFrameResumeDispatchWaitModelSelection && input.WaitingFor != CompatibilityFrameResumeDispatchWaitRecoveryCondition {
		return FrameEvent{}, false, errors.New("resume dispatch waiting condition is invalid")
	}
	if input.WaitingFor == CompatibilityFrameResumeDispatchWaitRecoveryCondition && input.RecoveryContractRevision <= 0 {
		return FrameEvent{}, false, errors.New("recovery condition wait requires its runtime contract revision")
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("begin resume dispatch requeue: %w", err)
	}
	defer tx.Rollback()
	row, found, err := scanCompatibilityFrameResumeDispatch(tx.QueryRowContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.id = ? AND e.event_type = 'frame_resumed'`, input.ResumeEventID))
	if err != nil {
		return FrameEvent{}, false, err
	}
	if !found {
		return FrameEvent{}, false, fmt.Errorf("resume event %q does not exist", input.ResumeEventID)
	}

	auditID := fmt.Sprintf("frame-resume-dispatch-interrupted:%s:%d", input.ResumeEventID, input.ExpectedAttempt)
	claimDigest := sha256.Sum256([]byte(input.ClaimToken))
	claimTokenSHA256 := hex.EncodeToString(claimDigest[:])
	if row.dispatch.Status == frameResumeDispatchRegistered && row.dispatch.Attempt == input.ExpectedAttempt {
		dispatchPayload, _ := row.dispatch.ResumeEvent.Payload["dispatch"].(map[string]any)
		if resumeDispatchString(dispatchPayload["interruptedClaimTokenSHA256"]) != claimTokenSHA256 {
			return FrameEvent{}, false, errors.New("resume dispatch interruption was committed by a different claim")
		}
		event, auditFound, auditErr := compatibilityDispatchAuditEventByID(ctx, tx, auditID)
		if auditErr != nil {
			return FrameEvent{}, false, auditErr
		}
		if !auditFound {
			return FrameEvent{}, false, errors.New("requeued resume dispatch is missing its interruption audit event")
		}
		if err := tx.Commit(); err != nil {
			return FrameEvent{}, false, err
		}
		return event, true, nil
	}
	if row.dispatch.Status != frameResumeDispatchClaimed || row.dispatch.Attempt != input.ExpectedAttempt || row.dispatch.ClaimToken != input.ClaimToken {
		return FrameEvent{}, false, errors.New("resume dispatch claim is not owned by this worker")
	}
	if row.dispatch.FrameStatus == frameResumeDispatchCancelled {
		return FrameEvent{}, false, errors.New("cancelled frame resume dispatch cannot be requeued")
	}

	now := s.now().UTC()
	payload := row.dispatch.ResumeEvent.Payload
	dispatchPayload := payload["dispatch"].(map[string]any)
	dispatchPayload["status"] = frameResumeDispatchRegistered
	dispatchPayload["interruptedAt"] = now.Format(time.RFC3339Nano)
	dispatchPayload["interruptionReasonCode"] = input.ReasonCode
	dispatchPayload["interruptedClaimTokenSHA256"] = claimTokenSHA256
	if input.RunnerAttempt > 0 {
		dispatchPayload["runnerAttempt"] = input.RunnerAttempt
	}
	if input.CheckpointEventID > 0 {
		dispatchPayload["checkpointEventId"] = input.CheckpointEventID
	}
	if input.NotBefore.After(now) {
		dispatchPayload["notBefore"] = input.NotBefore.UTC().Format(time.RFC3339Nano)
	} else {
		delete(dispatchPayload, "notBefore")
	}
	if input.WaitingFor != "" {
		dispatchPayload["waitingFor"] = input.WaitingFor
		if input.WaitingFor == CompatibilityFrameResumeDispatchWaitRecoveryCondition {
			dispatchPayload["recoveryContractRevision"] = input.RecoveryContractRevision
		}
	} else {
		delete(dispatchPayload, "waitingFor")
		delete(dispatchPayload, "recoveryContractRevision")
	}
	delete(dispatchPayload, "claimOwner")
	delete(dispatchPayload, "claimToken")
	delete(dispatchPayload, "claimedAt")
	delete(dispatchPayload, "leaseExpiresAt")
	delete(dispatchPayload, "modelSwitchRevision")
	delete(dispatchPayload, "modelSwitchRequestedAt")
	delete(dispatchPayload, "wakeReasonCode")
	rawUpdated, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("encode requeued resume dispatch: %w", err)
	}
	update, err := tx.ExecContext(ctx,
		`UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`,
		string(rawUpdated), input.ResumeEventID, row.rawPayload,
	)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("requeue resume dispatch: %w", err)
	}
	if changed, err := update.RowsAffected(); err != nil {
		return FrameEvent{}, false, err
	} else if changed != 1 {
		return FrameEvent{}, false, errors.New("resume dispatch requeue changed concurrently")
	}
	auditEvent, err := appendCompatibilityDispatchAuditEvent(ctx, tx, auditID, row.dispatch.FrameID,
		"frame_resume_dispatch_interrupted", map[string]any{
			"resumeEventId":     input.ResumeEventID,
			"dispatchAttempt":   input.ExpectedAttempt,
			"runnerAttempt":     input.RunnerAttempt,
			"checkpointEventId": input.CheckpointEventID,
			"reasonCode":        input.ReasonCode,
			"notBefore":         resumeDispatchTimeString(input.NotBefore),
			"waitingFor":        input.WaitingFor,
		}, now)
	if err != nil {
		return FrameEvent{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, false, fmt.Errorf("commit resume dispatch requeue: %w", err)
	}
	return auditEvent, false, nil
}

// ConvergeCompatibilityFrameResumeDispatch completes only the dispatch control
// record. It deliberately does not mutate the Frame status because the newer
// runner remains the sole execution authority for that active Frame.
func (s *Store) ConvergeCompatibilityFrameResumeDispatch(
	input ConvergeCompatibilityFrameResumeDispatchInput,
) (FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, false, errors.New("workspace store is closed")
	}
	input.ResumeEventID = strings.TrimSpace(input.ResumeEventID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.RunnerID = strings.TrimSpace(input.RunnerID)
	if input.ResumeEventID == "" || input.ExpectedAttempt <= 0 || input.ClaimToken == "" ||
		input.RunnerID == "" || input.RunnerAttempt <= 0 || input.ClaimedInputRevision <= 0 {
		return FrameEvent{}, false, errors.New(
			"resume event id, positive expected attempt, claim token, runner id, runner attempt, and claimed input revision are required",
		)
	}

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("begin resume dispatch convergence: %w", err)
	}
	defer tx.Rollback()
	row, found, err := scanCompatibilityFrameResumeDispatch(tx.QueryRowContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.id = ? AND e.event_type = 'frame_resumed'`, input.ResumeEventID))
	if err != nil {
		return FrameEvent{}, false, err
	}
	if !found {
		return FrameEvent{}, false, fmt.Errorf("resume event %q does not exist", input.ResumeEventID)
	}
	auditID := "frame-resume-dispatch-terminal:" + input.ResumeEventID
	if isCompatibilityResumeDispatchTerminal(row.dispatch.Status) {
		if row.dispatch.Status != frameResumeDispatchCompleted {
			return FrameEvent{}, false, fmt.Errorf(
				"resume dispatch already terminated with status %q", row.dispatch.Status,
			)
		}
		event, auditFound, auditErr := compatibilityDispatchAuditEventByID(ctx, tx, auditID)
		if auditErr != nil {
			return FrameEvent{}, false, auditErr
		}
		if !auditFound {
			return FrameEvent{}, false, errors.New("converged resume dispatch is missing its terminal audit event")
		}
		if err := tx.Commit(); err != nil {
			return FrameEvent{}, false, err
		}
		return event, true, nil
	}
	if row.dispatch.Status != frameResumeDispatchClaimed || row.dispatch.Attempt != input.ExpectedAttempt ||
		row.dispatch.ClaimToken != input.ClaimToken {
		return FrameEvent{}, false, errors.New("resume dispatch claim is not owned by this worker")
	}
	if row.dispatch.FrameStatus == frameResumeDispatchCancelled {
		return FrameEvent{}, false, errors.New("cancelled frame resume dispatch cannot converge into a runner")
	}

	now := s.now().UTC()
	details := map[string]any{
		"convergedIntoActiveRunner": true,
		"runnerId":                  input.RunnerID,
		"runnerAttempt":             input.RunnerAttempt,
		"claimedInputRevision":      input.ClaimedInputRevision,
	}
	payload := row.dispatch.ResumeEvent.Payload
	dispatchPayload := payload["dispatch"].(map[string]any)
	dispatchPayload["status"] = frameResumeDispatchCompleted
	dispatchPayload["terminalAt"] = now.Format(time.RFC3339Nano)
	dispatchPayload["message"] = "execution continues under the active runner authority"
	dispatchPayload["details"] = details
	rawUpdated, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("encode converged resume dispatch: %w", err)
	}
	update, err := tx.ExecContext(ctx,
		`UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`,
		string(rawUpdated), input.ResumeEventID, row.rawPayload,
	)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("converge resume dispatch: %w", err)
	}
	if changed, err := update.RowsAffected(); err != nil {
		return FrameEvent{}, false, err
	} else if changed != 1 {
		return FrameEvent{}, false, errors.New("resume dispatch convergence changed concurrently")
	}
	terminalEvent, err := appendCompatibilityDispatchAuditEvent(
		ctx, tx, auditID, row.dispatch.FrameID, "frame_resume_dispatch_completed",
		map[string]any{
			"resumeEventId": input.ResumeEventID,
			"attempt":       row.dispatch.Attempt,
			"status":        frameResumeDispatchCompleted,
			"message":       "execution continues under the active runner authority",
			"details":       details,
		}, now,
	)
	if err != nil {
		return FrameEvent{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, false, fmt.Errorf("commit resume dispatch convergence: %w", err)
	}
	return terminalEvent, false, nil
}

// WakeCompatibilityFrameResumeDispatch makes a paused registered dispatch
// claimable immediately by clearing its notBefore fence. It is the durable wake signal used when a kernel local
// execution approval (or any other waiting checkpoint) becomes resolvable
// while no runner is holding the dispatch. Other dispatches are a no-op: an
// active runner claim is already making progress and will observe the durable
// decision itself.
func (s *Store) WakeCompatibilityFrameResumeDispatch(eventID string) (FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, false, errors.New("workspace store is closed")
	}
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return FrameEvent{}, false, errors.New("resume event id is required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("begin resume dispatch wake: %w", err)
	}
	defer tx.Rollback()
	event, changed, err := wakeCompatibilityFrameResumeDispatchTx(ctx, tx, eventID, s.now().UTC())
	if err != nil || !changed {
		return event, changed, err
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, false, fmt.Errorf("commit resume dispatch wake: %w", err)
	}
	return event, true, nil
}

func wakeCompatibilityFrameResumeDispatchTx(ctx context.Context, tx workspaceTransaction, eventID string, now time.Time) (FrameEvent, bool, error) {
	row, found, err := scanCompatibilityFrameResumeDispatch(tx.QueryRowContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.id = ? AND e.event_type = 'frame_resumed'`, eventID))
	if err != nil {
		return FrameEvent{}, false, err
	}
	if !found {
		return FrameEvent{}, false, fmt.Errorf("resume event %q does not exist", eventID)
	}
	if row.dispatch.Status != frameResumeDispatchRegistered {
		return FrameEvent{}, false, nil
	}
	payload := row.dispatch.ResumeEvent.Payload
	dispatchPayload, ok := payload["dispatch"].(map[string]any)
	if !ok {
		return FrameEvent{}, false, errors.New("resume event dispatch payload is invalid")
	}
	_, timeFenced := dispatchPayload["notBefore"]
	waitingFor := resumeDispatchString(dispatchPayload["waitingFor"])
	if !timeFenced && waitingFor == "" {
		return FrameEvent{}, false, nil
	}
	delete(dispatchPayload, "notBefore")
	delete(dispatchPayload, "waitingFor")
	delete(dispatchPayload, "recoveryContractRevision")
	delete(dispatchPayload, "blockedAt")
	delete(dispatchPayload, "error")
	delete(dispatchPayload, "errorCode")
	delete(dispatchPayload, "message")
	delete(dispatchPayload, "modelSwitchRevision")
	delete(dispatchPayload, "modelSwitchRequestedAt")
	delete(dispatchPayload, "wakeReasonCode")
	dispatchPayload["status"] = frameResumeDispatchRegistered
	dispatchPayload["wokenAt"] = now.Format(time.RFC3339Nano)
	rawUpdated, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("encode woken resume dispatch: %w", err)
	}
	update, err := tx.ExecContext(ctx,
		`UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`,
		string(rawUpdated), eventID, row.rawPayload,
	)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("wake resume dispatch: %w", err)
	}
	if changed, err := update.RowsAffected(); err != nil {
		return FrameEvent{}, false, err
	} else if changed != 1 {
		return FrameEvent{}, false, errors.New("resume dispatch wake changed concurrently")
	}
	auditEvent, err := appendCompatibilityDispatchAuditEvent(ctx, tx, "frame-resume-dispatch-woken:"+eventID,
		row.dispatch.FrameID, "frame_resume_dispatch_woken", map[string]any{
			"resumeEventId": eventID, "dispatchAttempt": row.dispatch.Attempt,
			"priorStatus": row.dispatch.Status,
		}, now)
	if err != nil {
		return FrameEvent{}, false, err
	}
	return auditEvent, true, nil
}

// SignalCompatibilityFrameResumeDispatchModelSwitch durably joins a model
// selection change to the latest resume dispatch for a frame. A currently claimed dispatch records
// a wake intent that FailCompatibilityFrameResumeDispatch must consume if the
// in-flight provider call subsequently settles as the matching failure. This
// closes the switch-versus-failure race without cancelling or recreating the
// task runner.
func (s *Store) SignalCompatibilityFrameResumeDispatchModelSwitch(
	frameID string,
	modelRevision int64,
	providerFailureReasonCode string,
) (FrameEvent, CompatibilityFrameResumeDispatchWakeState, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, errors.New("workspace store is closed")
	}
	frameID = strings.TrimSpace(frameID)
	providerFailureReasonCode = strings.TrimSpace(providerFailureReasonCode)
	if frameID == "" || modelRevision <= 0 || providerFailureReasonCode == "" {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone,
			errors.New("frame id, positive model revision, and provider failure reason are required")
	}
	for attempt := 0; attempt < 4; attempt++ {
		event, state, retryable, err := s.signalCompatibilityFrameResumeDispatchModelSwitch(
			frameID, modelRevision, providerFailureReasonCode,
		)
		if err != nil || !retryable {
			return event, state, err
		}
	}
	return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone,
		errors.New("resume dispatch model-switch signal changed concurrently")
}

func (s *Store) signalCompatibilityFrameResumeDispatchModelSwitch(
	frameID string,
	modelRevision int64,
	providerFailureReasonCode string,
) (FrameEvent, CompatibilityFrameResumeDispatchWakeState, bool, error) {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false,
			fmt.Errorf("begin resume dispatch model-switch signal: %w", err)
	}
	defer tx.Rollback()
	row, found, err := scanCompatibilityFrameResumeDispatch(tx.QueryRowContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.frame_id = ? AND e.event_type = 'frame_resumed'
			AND json_extract(e.payload, '$.dispatch.status') IS NOT NULL
		ORDER BY e.sequence DESC LIMIT 1`, frameID))
	if err != nil {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false, err
	}
	if !found || isCompatibilityResumeDispatchTerminal(row.dispatch.Status) {
		if err := tx.Commit(); err != nil {
			return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false, err
		}
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false, nil
	}
	payload := row.dispatch.ResumeEvent.Payload
	dispatchPayload, ok := payload["dispatch"].(map[string]any)
	if !ok {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false,
			errors.New("resume event dispatch payload is invalid")
	}
	if row.dispatch.Status == frameResumeDispatchRegistered {
		_, timeFenced := dispatchPayload["notBefore"]
		waitingFor := resumeDispatchString(dispatchPayload["waitingFor"])
		if !timeFenced && waitingFor == "" {
			if err := tx.Commit(); err != nil {
				return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false, err
			}
			return FrameEvent{}, CompatibilityFrameResumeDispatchWakePending, false, nil
		}
	}
	if row.dispatch.Status == frameResumeDispatchClaimed &&
		int64(resumeDispatchInt(dispatchPayload["modelSwitchRevision"])) >= modelRevision {
		if err := tx.Commit(); err != nil {
			return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false, err
		}
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeArmed, false, nil
	}

	now := s.now().UTC()
	state := CompatibilityFrameResumeDispatchWakeWoken
	eventType := "frame_resume_dispatch_woken"
	eventID := fmt.Sprintf("frame-resume-dispatch-model-switch-woken:%s:%d", row.dispatch.ResumeEvent.ID, modelRevision)
	if row.dispatch.Status == frameResumeDispatchClaimed {
		state = CompatibilityFrameResumeDispatchWakeArmed
		eventType = "frame_resume_dispatch_model_switch_armed"
		eventID = fmt.Sprintf("frame-resume-dispatch-model-switch-armed:%s:%d", row.dispatch.ResumeEvent.ID, modelRevision)
		dispatchPayload["modelSwitchRevision"] = modelRevision
		dispatchPayload["modelSwitchRequestedAt"] = now.Format(time.RFC3339Nano)
		dispatchPayload["wakeReasonCode"] = providerFailureReasonCode
	} else if row.dispatch.Status == frameResumeDispatchRegistered {
		delete(dispatchPayload, "notBefore")
		delete(dispatchPayload, "waitingFor")
		delete(dispatchPayload, "blockedAt")
		delete(dispatchPayload, "error")
		delete(dispatchPayload, "errorCode")
		delete(dispatchPayload, "message")
		dispatchPayload["status"] = frameResumeDispatchRegistered
		dispatchPayload["wokenAt"] = now.Format(time.RFC3339Nano)
	} else {
		if err := tx.Commit(); err != nil {
			return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false, err
		}
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false, nil
	}
	rawUpdated, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false,
			fmt.Errorf("encode resume dispatch model-switch signal: %w", err)
	}
	update, err := tx.ExecContext(ctx,
		`UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`,
		string(rawUpdated), row.dispatch.ResumeEvent.ID, row.rawPayload,
	)
	if err != nil {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false,
			fmt.Errorf("persist resume dispatch model-switch signal: %w", err)
	}
	if changed, err := update.RowsAffected(); err != nil {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false, err
	} else if changed != 1 {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, true, nil
	}
	auditEvent, err := appendCompatibilityDispatchAuditEvent(ctx, tx, eventID,
		row.dispatch.FrameID, eventType, map[string]any{
			"resumeEventId":   row.dispatch.ResumeEvent.ID,
			"dispatchAttempt": row.dispatch.Attempt,
			"priorStatus":     row.dispatch.Status,
			"modelRevision":   modelRevision,
			"reasonCode":      providerFailureReasonCode,
		}, now)
	if err != nil {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false, err
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, CompatibilityFrameResumeDispatchWakeNone, false,
			fmt.Errorf("commit resume dispatch model-switch signal: %w", err)
	}
	return auditEvent, state, false, nil
}

// FailCompatibilityFrameResumeDispatch settles a claimed dispatch that cannot
// continue. A matching model-switch signal wins the race and atomically
// requeues the same dispatch; otherwise both the dispatch and Frame become a
// durable failed terminal. There is deliberately no non-terminal "blocked"
// state: an execution with no owner must never keep the task looking active or
// require a magic continuation message.
func (s *Store) FailCompatibilityFrameResumeDispatch(eventID string, expectedAttempt int, claimToken, code string) (FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, false, errors.New("workspace store is closed")
	}
	eventID, claimToken, code = strings.TrimSpace(eventID), strings.TrimSpace(claimToken), strings.TrimSpace(code)
	if eventID == "" || expectedAttempt <= 0 || claimToken == "" {
		return FrameEvent{}, false, errors.New("resume event id, positive expected attempt, and claim token are required")
	}
	if code == "" {
		code = "outcome_uncertain"
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("begin resume dispatch block: %w", err)
	}
	defer tx.Rollback()
	row, found, err := scanCompatibilityFrameResumeDispatch(tx.QueryRowContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.id = ? AND e.event_type = 'frame_resumed'`, eventID))
	if err != nil {
		return FrameEvent{}, false, err
	}
	if !found {
		return FrameEvent{}, false, fmt.Errorf("resume event %q does not exist", eventID)
	}
	if row.dispatch.Attempt != expectedAttempt || row.dispatch.ClaimToken != claimToken {
		return FrameEvent{}, false, errors.New("resume dispatch claim is not owned by this worker")
	}
	auditID := "frame-resume-dispatch-terminal:" + eventID
	if row.dispatch.Status == frameResumeDispatchFailed {
		event, auditFound, auditErr := compatibilityDispatchAuditEventByID(ctx, tx, auditID)
		if auditErr != nil {
			return FrameEvent{}, false, auditErr
		}
		if !auditFound {
			return FrameEvent{}, false, errors.New("failed resume dispatch is missing its audit event")
		}
		if err := tx.Commit(); err != nil {
			return FrameEvent{}, false, err
		}
		return event, false, nil
	}
	if row.dispatch.Status != frameResumeDispatchClaimed && row.dispatch.Status != frameResumeDispatchBlocked {
		return FrameEvent{}, false, errors.New("resume dispatch claim is not owned by this worker")
	}
	now := s.now().UTC()
	payload := row.dispatch.ResumeEvent.Payload
	dispatchPayload := payload["dispatch"].(map[string]any)
	if int64(resumeDispatchInt(dispatchPayload["modelSwitchRevision"])) > 0 &&
		resumeDispatchString(dispatchPayload["wakeReasonCode"]) == code {
		claimDigest := sha256.Sum256([]byte(claimToken))
		dispatchPayload["status"] = frameResumeDispatchRegistered
		dispatchPayload["interruptedAt"] = now.Format(time.RFC3339Nano)
		dispatchPayload["interruptionReasonCode"] = code
		dispatchPayload["interruptedClaimTokenSHA256"] = hex.EncodeToString(claimDigest[:])
		delete(dispatchPayload, "notBefore")
		delete(dispatchPayload, "waitingFor")
		delete(dispatchPayload, "blockedAt")
		delete(dispatchPayload, "error")
		delete(dispatchPayload, "errorCode")
		delete(dispatchPayload, "message")
		delete(dispatchPayload, "claimOwner")
		delete(dispatchPayload, "claimToken")
		delete(dispatchPayload, "claimedAt")
		delete(dispatchPayload, "leaseExpiresAt")
		modelRevision := resumeDispatchInt(dispatchPayload["modelSwitchRevision"])
		delete(dispatchPayload, "modelSwitchRevision")
		delete(dispatchPayload, "modelSwitchRequestedAt")
		delete(dispatchPayload, "wakeReasonCode")
		rawUpdated, err := json.Marshal(payload)
		if err != nil {
			return FrameEvent{}, false, fmt.Errorf("encode model-switch requeued resume dispatch: %w", err)
		}
		update, err := tx.ExecContext(ctx,
			`UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`,
			string(rawUpdated), eventID, row.rawPayload,
		)
		if err != nil {
			return FrameEvent{}, false, fmt.Errorf("requeue model-switch resume dispatch: %w", err)
		}
		if changed, err := update.RowsAffected(); err != nil {
			return FrameEvent{}, false, err
		} else if changed != 1 {
			return FrameEvent{}, false, errors.New("resume dispatch changed while consuming model-switch signal")
		}
		auditEvent, err := appendCompatibilityDispatchAuditEvent(ctx, tx,
			fmt.Sprintf("frame-resume-dispatch-interrupted:%s:%d", eventID, expectedAttempt),
			row.dispatch.FrameID, "frame_resume_dispatch_interrupted", map[string]any{
				"resumeEventId": eventID, "dispatchAttempt": expectedAttempt,
				"reasonCode": code, "modelRevision": modelRevision,
				"wakeReason": "model_switch",
			}, now)
		if err != nil {
			return FrameEvent{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return FrameEvent{}, false, fmt.Errorf("commit model-switch resume dispatch requeue: %w", err)
		}
		return auditEvent, true, nil
	}
	dispatchPayload["status"] = frameResumeDispatchFailed
	dispatchPayload["terminalAt"] = now.Format(time.RFC3339Nano)
	dispatchPayload["error"] = code
	dispatchPayload["errorCode"] = code
	dispatchPayload["message"] = code
	delete(dispatchPayload, "blockedAt")
	delete(dispatchPayload, "waitingFor")
	rawUpdated, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("encode failed resume dispatch: %w", err)
	}
	update, err := tx.ExecContext(ctx, `UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`, string(rawUpdated), eventID, row.rawPayload)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("fail resume dispatch: %w", err)
	}
	if changed, err := update.RowsAffected(); err != nil {
		return FrameEvent{}, false, err
	} else if changed != 1 {
		return FrameEvent{}, false, errors.New("resume dispatch claim changed while failing")
	}
	if row.dispatch.FrameStatus != FrameStatusCancelled {
		if _, err := tx.ExecContext(ctx, `UPDATE frames SET status = ?, updated_at = ? WHERE id = ?`,
			FrameStatusFailed, now, row.dispatch.FrameID); err != nil {
			return FrameEvent{}, false, fmt.Errorf("fail resumed frame: %w", err)
		}
	}
	if err := persistFrameTerminalStatusDescription(ctx, tx, row.dispatch.FrameID, frameResumeDispatchFailed, code); err != nil {
		return FrameEvent{}, false, err
	}
	auditEvent, err := appendCompatibilityDispatchAuditEvent(ctx, tx, auditID, row.dispatch.FrameID,
		"frame_resume_dispatch_failed", map[string]any{
			"resumeEventId": eventID, "errorCode": code, "status": frameResumeDispatchFailed,
		}, now)
	if err != nil {
		return FrameEvent{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, false, fmt.Errorf("commit failed resume dispatch: %w", err)
	}
	return auditEvent, false, nil
}

func (s *Store) CompleteCompatibilityFrameResumeDispatch(input CompleteCompatibilityFrameResumeDispatchInput) (FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, false, errors.New("workspace store is closed")
	}
	input.ResumeEventID = strings.TrimSpace(input.ResumeEventID)
	input.ClaimToken = strings.TrimSpace(input.ClaimToken)
	input.Status = strings.TrimSpace(input.Status)
	if input.ResumeEventID == "" || input.ExpectedAttempt <= 0 || input.ClaimToken == "" {
		return FrameEvent{}, false, errors.New("resume event id, positive expected attempt, and claim token are required")
	}
	if input.Status != frameResumeDispatchCompleted && input.Status != frameResumeDispatchFailed && input.Status != frameResumeDispatchCancelled {
		return FrameEvent{}, false, fmt.Errorf("invalid resume dispatch terminal status %q", input.Status)
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("begin resume dispatch completion: %w", err)
	}
	defer tx.Rollback()
	row, found, err := scanCompatibilityFrameResumeDispatch(tx.QueryRowContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE e.id = ? AND e.event_type = 'frame_resumed'`, input.ResumeEventID))
	if err != nil {
		return FrameEvent{}, false, err
	}
	if !found {
		return FrameEvent{}, false, fmt.Errorf("resume event %q does not exist", input.ResumeEventID)
	}
	if row.dispatch.Attempt != input.ExpectedAttempt || row.dispatch.ClaimToken != input.ClaimToken {
		return FrameEvent{}, false, errors.New("resume dispatch claim is not owned by this worker")
	}
	if isCompatibilityResumeDispatchTerminal(row.dispatch.Status) {
		if err := persistFrameTerminalStatusDescription(ctx, tx, row.dispatch.FrameID, row.dispatch.Status, row.dispatch.Error); err != nil {
			return FrameEvent{}, false, err
		}
		event, found, err := compatibilityDispatchAuditEventByID(ctx, tx,
			"frame-resume-dispatch-terminal:"+input.ResumeEventID)
		if err != nil {
			return FrameEvent{}, false, err
		}
		if !found {
			return FrameEvent{}, false, errors.New("terminal resume dispatch is missing its audit event")
		}
		if err := tx.Commit(); err != nil {
			return FrameEvent{}, false, err
		}
		return event, true, nil
	}
	if row.dispatch.Status != frameResumeDispatchClaimed {
		return FrameEvent{}, false, errors.New("resume dispatch claim is not owned by this worker")
	}

	status := input.Status
	if row.dispatch.FrameStatus == "cancelled" {
		status = frameResumeDispatchCancelled
	}
	now := s.now().UTC()
	payload := row.dispatch.ResumeEvent.Payload
	dispatchPayload := payload["dispatch"].(map[string]any)
	dispatchPayload["status"] = status
	dispatchPayload["terminalAt"] = now.Format(time.RFC3339Nano)
	dispatchPayload["message"] = strings.TrimSpace(input.Message)
	if status == frameResumeDispatchFailed {
		dispatchPayload["error"] = strings.TrimSpace(input.Message)
	}
	if len(input.Details) > 0 {
		dispatchPayload["details"] = input.Details
	}
	rawUpdated, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("encode terminal resume dispatch: %w", err)
	}
	update, err := tx.ExecContext(ctx,
		`UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`,
		string(rawUpdated), input.ResumeEventID, row.rawPayload,
	)
	if err != nil {
		return FrameEvent{}, false, fmt.Errorf("complete resume dispatch: %w", err)
	}
	if changed, err := update.RowsAffected(); err != nil {
		return FrameEvent{}, false, err
	} else if changed != 1 {
		return FrameEvent{}, false, errors.New("resume dispatch completion changed concurrently")
	}
	if row.dispatch.FrameStatus != "cancelled" {
		if _, err := tx.ExecContext(ctx,
			`UPDATE frames SET status = ?, updated_at = ? WHERE id = ?`,
			status, now, row.dispatch.FrameID,
		); err != nil {
			return FrameEvent{}, false, fmt.Errorf("update resumed frame terminal status: %w", err)
		}
	}
	if err := persistFrameTerminalStatusDescription(ctx, tx, row.dispatch.FrameID, status, input.Message); err != nil {
		return FrameEvent{}, false, err
	}
	eventType := "frame_resume_dispatch_" + status
	terminalEvent, err := appendCompatibilityDispatchAuditEvent(ctx, tx,
		"frame-resume-dispatch-terminal:"+input.ResumeEventID,
		row.dispatch.FrameID, eventType,
		map[string]any{
			"resumeEventId": input.ResumeEventID,
			"claimToken":    input.ClaimToken,
			"attempt":       row.dispatch.Attempt,
			"status":        status,
			"message":       strings.TrimSpace(input.Message),
			"details":       input.Details,
		}, now,
	)
	if err != nil {
		return FrameEvent{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return FrameEvent{}, false, fmt.Errorf("commit resume dispatch completion: %w", err)
	}
	return terminalEvent, false, nil
}

func persistFrameTerminalStatusDescription(
	ctx context.Context,
	tx *sql.Tx,
	frameID, status, detail string,
) error {
	description := ""
	if strings.TrimSpace(status) == frameResumeDispatchFailed {
		description = strings.TrimSpace(detail)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata (frame_id,status_description,context_data)
		VALUES(?,?,'{}')
		ON CONFLICT(frame_id) DO UPDATE SET status_description=excluded.status_description`,
		strings.TrimSpace(frameID), description); err != nil {
		return fmt.Errorf("persist frame terminal status description: %w", err)
	}
	return nil
}

func cancelCompatibilityFrameResumeDispatches(ctx context.Context, tx workspaceTransaction, rootID, reason string, now time.Time) ([]FrameEvent, []string, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, e.frame_id, e.sequence, e.payload, e.created_at,
			f.root_frame_id, f.project_id, f.agent_name, f.status
		FROM frame_events e JOIN frames f ON f.id = e.frame_id
		WHERE f.root_frame_id = ? AND e.event_type = 'frame_resumed'
			AND json_extract(e.payload, '$.dispatch.status') IN ('registered', 'claimed', 'blocked')
		ORDER BY e.created_at, e.id`, rootID)
	if err != nil {
		return nil, nil, fmt.Errorf("list resume dispatches for cancellation: %w", err)
	}
	dispatches := make([]compatibilityFrameResumeDispatchRow, 0)
	for rows.Next() {
		row, found, err := scanCompatibilityFrameResumeDispatch(rows)
		if err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if found {
			dispatches = append(dispatches, row)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}

	events := make([]FrameEvent, 0, len(dispatches))
	eventIDs := make([]string, 0, len(dispatches))
	for _, row := range dispatches {
		payload := row.dispatch.ResumeEvent.Payload
		dispatchPayload := payload["dispatch"].(map[string]any)
		dispatchPayload["status"] = frameResumeDispatchCancelled
		dispatchPayload["terminalAt"] = now.Format(time.RFC3339Nano)
		dispatchPayload["message"] = strings.TrimSpace(reason)
		rawUpdated, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, err
		}
		update, err := tx.ExecContext(ctx,
			`UPDATE frame_events SET payload = ? WHERE id = ? AND payload = ?`,
			string(rawUpdated), row.dispatch.ResumeEvent.ID, row.rawPayload,
		)
		if err != nil {
			return nil, nil, err
		}
		if changed, err := update.RowsAffected(); err != nil || changed != 1 {
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, errors.New("resume dispatch cancellation changed concurrently")
		}
		terminalEvent, err := appendCompatibilityDispatchAuditEvent(ctx, tx,
			"frame-resume-dispatch-terminal:"+row.dispatch.ResumeEvent.ID,
			row.dispatch.FrameID, "frame_resume_dispatch_cancelled",
			map[string]any{
				"resumeEventId": row.dispatch.ResumeEvent.ID,
				"claimToken":    row.dispatch.ClaimToken,
				"attempt":       row.dispatch.Attempt,
				"status":        frameResumeDispatchCancelled,
				"message":       strings.TrimSpace(reason),
			}, now,
		)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, terminalEvent)
		eventIDs = append(eventIDs, row.dispatch.ResumeEvent.ID)
	}
	return events, eventIDs, nil
}

func scanCompatibilityFrameResumeDispatch(scanner interface{ Scan(...any) error }) (compatibilityFrameResumeDispatchRow, bool, error) {
	var row compatibilityFrameResumeDispatchRow
	var rawPayload string
	err := scanner.Scan(
		&row.dispatch.ResumeEvent.ID,
		&row.dispatch.ResumeEvent.FrameID,
		&row.dispatch.ResumeEvent.Sequence,
		&rawPayload,
		&row.dispatch.ResumeEvent.CreatedAt,
		&row.dispatch.RootFrameID,
		&row.dispatch.ProjectID,
		&row.dispatch.AgentName,
		&row.dispatch.FrameStatus,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return compatibilityFrameResumeDispatchRow{}, false, nil
	}
	if err != nil {
		return compatibilityFrameResumeDispatchRow{}, false, fmt.Errorf("scan frame resume dispatch: %w", err)
	}
	row.dispatch.ResumeEvent.Type = "frame_resumed"
	if err := json.Unmarshal([]byte(rawPayload), &row.dispatch.ResumeEvent.Payload); err != nil {
		return compatibilityFrameResumeDispatchRow{}, false, fmt.Errorf("decode frame resume dispatch: %w", err)
	}
	dispatchPayload, ok := row.dispatch.ResumeEvent.Payload["dispatch"].(map[string]any)
	if !ok {
		return compatibilityFrameResumeDispatchRow{}, false, nil
	}
	row.rawPayload = rawPayload
	row.dispatch.FrameID = row.dispatch.ResumeEvent.FrameID
	row.dispatch.PreviousStatus = resumeDispatchString(row.dispatch.ResumeEvent.Payload["previousStatus"])
	row.dispatch.Status = resumeDispatchString(dispatchPayload["status"])
	row.dispatch.ClaimToken = resumeDispatchString(dispatchPayload["claimToken"])
	row.dispatch.ClaimOwner = resumeDispatchString(dispatchPayload["claimOwner"])
	row.dispatch.Attempt = resumeDispatchInt(dispatchPayload["attempt"])
	row.dispatch.Error = resumeDispatchString(dispatchPayload["error"])
	if row.dispatch.Error == "" {
		row.dispatch.Error = resumeDispatchString(dispatchPayload["errorCode"])
	}
	row.dispatch.LeaseExpiresAt = resumeDispatchTime(dispatchPayload["leaseExpiresAt"])
	row.dispatch.NotBefore = resumeDispatchTime(dispatchPayload["notBefore"])
	row.dispatch.WaitingFor = resumeDispatchString(dispatchPayload["waitingFor"])
	return row, true, nil
}

func appendCompatibilityDispatchAuditEvent(ctx context.Context, tx workspaceTransaction, id, frameID, eventType string, payload map[string]any, now time.Time) (FrameEvent, error) {
	if existing, found, err := compatibilityDispatchAuditEventByID(ctx, tx, id); err != nil {
		return FrameEvent{}, err
	} else if found {
		if existing.FrameID != frameID || existing.Type != eventType {
			return FrameEvent{}, fmt.Errorf("dispatch audit event id %q has conflicting identity", id)
		}
		return existing, nil
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(sequence), 0) + 1 FROM frame_events WHERE frame_id = ?`,
		frameID,
	).Scan(&sequence); err != nil {
		return FrameEvent{}, fmt.Errorf("allocate dispatch audit event sequence: %w", err)
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, fmt.Errorf("encode dispatch audit event: %w", err)
	}
	event := FrameEvent{
		ID: id, FrameID: frameID, Sequence: sequence,
		Type: eventType, Payload: payload, CreatedAt: now,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO frame_events (id, frame_id, sequence, event_type, payload, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		event.ID, event.FrameID, event.Sequence, event.Type, string(rawPayload), event.CreatedAt,
	); err != nil {
		return FrameEvent{}, fmt.Errorf("insert dispatch audit event: %w", err)
	}
	return event, nil
}

func compatibilityDispatchAuditEventByID(ctx context.Context, tx workspaceTransaction, id string) (FrameEvent, bool, error) {
	event, _, found, err := frameEventByID(ctx, tx, id)
	return event, found, err
}

func isCompatibilityResumeDispatchTerminal(status string) bool {
	return status == frameResumeDispatchCompleted || status == frameResumeDispatchFailed || status == frameResumeDispatchCancelled
}

func resumeDispatchString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func resumeDispatchInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		number, _ := typed.Int64()
		return int(number)
	default:
		return 0
	}
}

func resumeDispatchTime(value any) time.Time {
	text := resumeDispatchString(value)
	if text == "" {
		return time.Time{}
	}
	parsed, _ := time.Parse(time.RFC3339Nano, text)
	return parsed
}

func resumeDispatchTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
