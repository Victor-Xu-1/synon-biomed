package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	KernelLocalExecApprovalRequestedEventType = "kernel_local_exec_approval_requested"
	KernelLocalExecApprovalResolvedEventType  = "kernel_local_exec_approval_resolved"
	KernelLocalExecApprovalConsumedEventType  = "kernel_local_exec_approval_consumed"
)

var (
	ErrKernelLocalExecApprovalConsumed    = errors.New("kernel local execution approval was already consumed")
	ErrKernelLocalExecApprovalConflict    = errors.New("kernel local execution approval conflicts with durable state")
	ErrKernelLocalExecApprovalStale       = errors.New("kernel local execution approval authority is stale")
	ErrKernelLocalExecApprovalUnavailable = errors.New("kernel local execution approval is unavailable")
	ErrKernelLocalExecApprovalRunnerLive  = errors.New("kernel local execution approval runner is still live")
)

type KernelLocalExecApprovalRequestInput struct {
	OwnerUserID            string
	ProjectID              string
	FrameID                string
	FrameIncarnationID     string
	RootFrameID            string
	RootFrameIncarnationID string
	RequestID              string
	Tool                   string
	ToolCallID             string
	Environment            string
	InputSHA256            string
	StreamUID              string
	RunnerID               string
	RunnerAttempt          int64
	ClaimTokenSHA256       string
	KernelID               string
	ExpectedGeneration     uint64
	Code                   string
	WorkingDir             string
	Background             bool
	Fresh                  bool
}

type KernelLocalExecApprovalResolutionInput struct {
	OwnerUserID        string
	ProjectID          string
	FrameID            string
	FrameIncarnationID string
	RootFrameID        string
	RootIncarnationID  string
	RequestID          string
	Tool               string
	Environment        string
	InputSHA256        string
	StreamUID          string
	RunnerID           string
	RunnerAttempt      int64
	ClaimTokenSHA256   string
	KernelID           string
	ExpectedGeneration uint64
	Approved           bool
	Scope              string
	RequireStaleRunner bool
	HandoffLeaseTTL    time.Duration
}

type KernelLocalExecApprovalDecision struct {
	RequestID          string
	Tool               string
	Environment        string
	InputSHA256        string
	StreamUID          string
	RunnerID           string
	RunnerAttempt      int64
	KernelID           string
	ExpectedGeneration uint64
	Approved           bool
	Scope              string
	ResolvedAt         time.Time
}

type ApprovalPolicyDecision struct {
	Found         bool
	Tier          string
	Scope         string
	ScopeTargetID string
}

type ApprovalPolicyGrant struct {
	Kind              string
	Key               string
	Scope             string
	Tier              string
	ScopeTargetID     string
	OriginProjectID   string
	OriginRootFrameID string
}

func (s *Store) AddKernelLocalExecApprovalRequest(
	ctx context.Context,
	input KernelLocalExecApprovalRequestInput,
) (FrameEvent, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return FrameEvent{}, errors.New("kernel local execution approval context is required")
	}
	if err := normalizeKernelLocalExecApprovalRequest(&input); err != nil {
		return FrameEvent{}, err
	}
	request := map[string]any{
		"version": 1, "requestId": input.RequestID, "kind": "local_exec", "tool": input.Tool,
		"tool_name": input.Tool, "tool_call_id": input.ToolCallID,
		"environment": input.Environment, "input_sha256": input.InputSHA256,
		"stream_uid": input.StreamUID,
		"runner_id":  input.RunnerID, "runner_attempt": input.RunnerAttempt,
		"kernel_id": input.KernelID, "kernel_generation": strconv.FormatUint(input.ExpectedGeneration, 10),
		"frame_incarnation_id":      input.FrameIncarnationID,
		"root_frame_id":             input.RootFrameID,
		"root_frame_incarnation_id": input.RootFrameIncarnationID,
		"code":                      input.Code, "mode": "live", "rememberable": true,
	}
	if input.WorkingDir != "" {
		request["working_dir"] = input.WorkingDir
	}
	if input.Background {
		request["background"] = true
	}
	if input.Fresh {
		request["fresh"] = true
	}

	var event FrameEvent
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := verifyKernelLocalExecApprovalFrame(ctx, tx, input.OwnerUserID, input.ProjectID,
			input.FrameID, input.FrameIncarnationID, input.RootFrameID, input.RootFrameIncarnationID); err != nil {
			return err
		}
		if err := verifyKernelLocalExecApprovalRunner(ctx, tx, input.OwnerUserID, input.ProjectID,
			input.FrameID, input.RootFrameID, input.StreamUID, input.RunnerID, input.RunnerAttempt,
			input.ClaimTokenSHA256, s.now().UTC()); err != nil {
			return err
		}
		if existing, resolved, err := kernelLocalExecApprovalDecisionTx(ctx, tx, input.FrameID, input.RequestID); err != nil {
			return err
		} else if resolved {
			if !kernelLocalExecApprovalDecisionMatchesRequest(existing, input) {
				return fmt.Errorf("%w: resolved request authority differs", ErrKernelLocalExecApprovalConflict)
			}
			if _, consumed, err := kernelLocalExecApprovalEventTx(ctx, tx, input.FrameID,
				KernelLocalExecApprovalConsumedEventType, input.RequestID); err != nil {
				return err
			} else if consumed {
				return ErrKernelLocalExecApprovalConsumed
			}
			if !existing.Approved {
				return fmt.Errorf("%w: request was denied", ErrKernelLocalExecApprovalConflict)
			}
			return nil
		}
		contextData, err := kernelApprovalContextData(ctx, tx, input.FrameID)
		if err != nil {
			return err
		}
		pending := compatibilityPendingInputRequests(contextData)
		for _, item := range pending {
			if compatibilityPendingInputID(item) != input.RequestID {
				continue
			}
			if !mapsEqualJSON(item, request) {
				return fmt.Errorf("%w: request payload differs", ErrKernelLocalExecApprovalConflict)
			}
			existing, found, err := kernelLocalExecApprovalEventTx(ctx, tx, input.FrameID,
				KernelLocalExecApprovalRequestedEventType, input.RequestID)
			if err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("%w: pending request has no durable event", ErrKernelLocalExecApprovalConflict)
			}
			event = existing
			return nil
		}
		pending = append(pending, request)
		contextData["_pending_input_requests"] = compatibilityMapsToAny(pending)
		if err := writeKernelApprovalContextData(ctx, tx, input.FrameID, contextData); err != nil {
			return err
		}
		event, err = appendFrameLifecycleEvent(ctx, tx, input.FrameID, KernelLocalExecApprovalRequestedEventType,
			kernelLocalExecApprovalRequestEventPayload(input), s.now().UTC())
		if err != nil {
			return err
		}
		return enqueueKernelLocalExecApprovalRealtime(ctx, s, tx, input.OwnerUserID, event)
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return event, err
}

func (s *Store) ResolveKernelLocalExecApproval(
	ctx context.Context,
	input KernelLocalExecApprovalResolutionInput,
) (KernelLocalExecApprovalDecision, FrameEvent, bool, error) {
	if s == nil || s.db == nil {
		return KernelLocalExecApprovalDecision{}, FrameEvent{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return KernelLocalExecApprovalDecision{}, FrameEvent{}, false, errors.New("kernel local execution approval context is required")
	}
	if err := normalizeKernelLocalExecApprovalResolution(&input); err != nil {
		return KernelLocalExecApprovalDecision{}, FrameEvent{}, false, err
	}
	var decision KernelLocalExecApprovalDecision
	var event FrameEvent
	created := false
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if err := verifyKernelLocalExecApprovalFrame(ctx, tx, input.OwnerUserID, input.ProjectID,
			input.FrameID, input.FrameIncarnationID, input.RootFrameID, input.RootIncarnationID); err != nil {
			return err
		}
		existing, found, err := kernelLocalExecApprovalDecisionTx(ctx, tx, input.FrameID, input.RequestID)
		if err != nil {
			return err
		}
		if found {
			if existing.Tool != input.Tool || existing.Environment != input.Environment ||
				existing.InputSHA256 != input.InputSHA256 || existing.StreamUID != input.StreamUID || existing.RunnerID != input.RunnerID ||
				existing.RunnerAttempt != input.RunnerAttempt || existing.KernelID != input.KernelID ||
				existing.ExpectedGeneration != input.ExpectedGeneration || existing.Approved != input.Approved ||
				existing.Scope != input.Scope {
				return fmt.Errorf("%w: decision payload differs", ErrKernelLocalExecApprovalConflict)
			}
			decision = existing
			return nil
		}
		if input.Approved {
			if err := verifyKernelLocalExecApprovalRunner(ctx, tx, input.OwnerUserID, input.ProjectID,
				input.FrameID, input.RootFrameID, input.StreamUID, input.RunnerID, input.RunnerAttempt,
				input.ClaimTokenSHA256, s.now().UTC()); err != nil {
				return err
			}
		} else if input.RequireStaleRunner {
			err := verifyKernelLocalExecApprovalRunner(ctx, tx, input.OwnerUserID, input.ProjectID,
				input.FrameID, input.RootFrameID, input.StreamUID, input.RunnerID, input.RunnerAttempt,
				input.ClaimTokenSHA256, s.now().UTC())
			switch {
			case err == nil:
				return ErrKernelLocalExecApprovalRunnerLive
			case errors.Is(err, ErrKernelLocalExecApprovalStale):
			default:
				return err
			}
		}
		contextData, err := kernelApprovalContextData(ctx, tx, input.FrameID)
		if err != nil {
			return err
		}
		pending := compatibilityPendingInputRequests(contextData)
		next := make([]map[string]any, 0, len(pending))
		matched := false
		for _, item := range pending {
			if compatibilityPendingInputID(item) != input.RequestID {
				next = append(next, item)
				continue
			}
			if strings.TrimSpace(compatibilityStringValue(item["kind"])) != "local_exec" ||
				strings.TrimSpace(compatibilityStringValue(item["tool"])) != input.Tool ||
				strings.TrimSpace(compatibilityStringValue(item["environment"])) != input.Environment ||
				strings.TrimSpace(compatibilityStringValue(item["input_sha256"])) != input.InputSHA256 ||
				strings.TrimSpace(compatibilityStringValue(item["stream_uid"])) != input.StreamUID ||
				strings.TrimSpace(compatibilityStringValue(item["runner_id"])) != input.RunnerID ||
				compatibilityInt64Value(item["runner_attempt"]) != input.RunnerAttempt ||
				strings.TrimSpace(compatibilityStringValue(item["kernel_id"])) != input.KernelID ||
				compatibilityUint64String(item["kernel_generation"]) != input.ExpectedGeneration ||
				strings.TrimSpace(compatibilityStringValue(item["root_frame_id"])) != input.RootFrameID ||
				strings.TrimSpace(compatibilityStringValue(item["root_frame_incarnation_id"])) != input.RootIncarnationID ||
				strings.TrimSpace(compatibilityStringValue(item["frame_incarnation_id"])) != input.FrameIncarnationID {
				return fmt.Errorf("%w: pending request authority changed", ErrKernelLocalExecApprovalStale)
			}
			matched = true
		}
		if !matched {
			return ErrKernelLocalExecApprovalUnavailable
		}
		contextData["_pending_input_requests"] = compatibilityMapsToAny(next)
		if err := writeKernelApprovalContextData(ctx, tx, input.FrameID, contextData); err != nil {
			return err
		}
		if input.Approved && input.Scope != "once" {
			if err := upsertKernelLocalExecApprovalGrant(ctx, tx, input, s.now().UTC()); err != nil {
				return err
			}
		}
		resolvedAt := s.now().UTC()
		payload := map[string]any{
			"version": 1, "request_id": input.RequestID, "tool": input.Tool,
			"environment": input.Environment, "input_sha256": input.InputSHA256,
			"stream_uid": input.StreamUID,
			"runner_id":  input.RunnerID, "runner_attempt": input.RunnerAttempt,
			"kernel_id": input.KernelID, "kernel_generation": strconv.FormatUint(input.ExpectedGeneration, 10),
			"root_frame_id": input.RootFrameID, "root_frame_incarnation_id": input.RootIncarnationID,
			"frame_incarnation_id": input.FrameIncarnationID,
			"approved":             input.Approved, "scope": input.Scope,
		}
		event, err = appendFrameLifecycleEvent(ctx, tx, input.FrameID,
			KernelLocalExecApprovalResolvedEventType, payload, resolvedAt)
		if err != nil {
			return err
		}
		if err := enqueueKernelLocalExecApprovalRealtime(ctx, s, tx, input.OwnerUserID, event); err != nil {
			return err
		}
		decision = KernelLocalExecApprovalDecision{
			RequestID: input.RequestID, Tool: input.Tool, Environment: input.Environment,
			InputSHA256: input.InputSHA256, StreamUID: input.StreamUID, RunnerID: input.RunnerID,
			RunnerAttempt: input.RunnerAttempt, KernelID: input.KernelID,
			ExpectedGeneration: input.ExpectedGeneration, Approved: input.Approved,
			Scope: input.Scope, ResolvedAt: resolvedAt,
		}
		created = true
		return nil
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return decision, event, created, err
}

func (s *Store) ConsumeKernelLocalExecApproval(
	ctx context.Context,
	input KernelLocalExecApprovalResolutionInput,
) (FrameEvent, error) {
	if s == nil || s.db == nil {
		return FrameEvent{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return FrameEvent{}, errors.New("kernel local execution approval context is required")
	}
	if err := normalizeKernelLocalExecApprovalResolution(&input); err != nil {
		return FrameEvent{}, err
	}
	if !input.Approved {
		return FrameEvent{}, errors.New("denied kernel local execution approval cannot be consumed")
	}
	if input.HandoffLeaseTTL <= 0 {
		return FrameEvent{}, errors.New("kernel local execution approval handoff lease is required")
	}
	payload := map[string]any{
		"version": 1, "request_id": input.RequestID, "tool": input.Tool,
		"environment": input.Environment, "input_sha256": input.InputSHA256,
		"stream_uid": input.StreamUID, "runner_id": input.RunnerID,
		"runner_attempt": input.RunnerAttempt,
		"kernel_id":      input.KernelID, "kernel_generation": strconv.FormatUint(input.ExpectedGeneration, 10),
		"frame_incarnation_id": input.FrameIncarnationID, "root_frame_id": input.RootFrameID,
		"root_frame_incarnation_id": input.RootIncarnationID, "scope": input.Scope,
	}
	eventID := "kernel-local-exec-consumed:" + input.RequestID
	var event FrameEvent
	err := transcriptstore.NewRepository(s.db).RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		now := s.now().UTC()
		handoffLeaseExpiresAt := now.Add(input.HandoffLeaseTTL)
		if err := verifyKernelLocalExecApprovalFrame(ctx, tx, input.OwnerUserID, input.ProjectID,
			input.FrameID, input.FrameIncarnationID, input.RootFrameID, input.RootIncarnationID); err != nil {
			return err
		}
		decision, found, err := kernelLocalExecApprovalDecisionTx(ctx, tx, input.FrameID, input.RequestID)
		if err != nil {
			return err
		}
		if !found || !decision.Approved || !kernelLocalExecApprovalDecisionMatches(decision, input) {
			return fmt.Errorf("%w: decision authority changed", ErrKernelLocalExecApprovalStale)
		}
		if err := verifyKernelLocalExecApprovalRunner(ctx, tx, input.OwnerUserID, input.ProjectID,
			input.FrameID, input.RootFrameID, input.StreamUID, input.RunnerID, input.RunnerAttempt,
			input.ClaimTokenSHA256, now); err != nil {
			return err
		}
		var existingPayload string
		var existingSequence int64
		var existingCreated time.Time
		err = tx.QueryRowContext(ctx, `SELECT sequence,payload,created_at FROM frame_events WHERE id=?`, eventID).
			Scan(&existingSequence, &existingPayload, &existingCreated)
		if err == nil {
			var existing map[string]any
			if json.Unmarshal([]byte(existingPayload), &existing) != nil || !mapsEqualJSON(existing, payload) {
				return fmt.Errorf("%w: consumption payload differs", ErrKernelLocalExecApprovalConflict)
			}
			event = FrameEvent{ID: eventID, FrameID: input.FrameID, Sequence: existingSequence,
				Type: KernelLocalExecApprovalConsumedEventType, Payload: payload, CreatedAt: existingCreated}
			return ErrKernelLocalExecApprovalConsumed
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		updated, err := tx.ExecContext(ctx, `
			UPDATE transcript_runner_attempts
			SET expires_at=CASE WHEN expires_at<? THEN ? ELSE expires_at END
			WHERE stream_uid=? AND attempt=? AND runner_id=?
				AND lower(hex(claim_token_sha256))=? AND status='running' AND expires_at>?
				AND NOT EXISTS (
					SELECT 1 FROM transcript_runner_attempts newer
					WHERE newer.stream_uid=transcript_runner_attempts.stream_uid
						AND newer.attempt>transcript_runner_attempts.attempt
				)`, handoffLeaseExpiresAt, handoffLeaseExpiresAt,
			input.StreamUID, input.RunnerAttempt, input.RunnerID, input.ClaimTokenSHA256, now)
		if err != nil {
			return fmt.Errorf("renew kernel local execution approval handoff lease: %w", err)
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return err
			}
			return ErrKernelLocalExecApprovalStale
		}
		var sequence int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM frame_events WHERE frame_id=?`, input.FrameID).
			Scan(&sequence); err != nil {
			return err
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
			VALUES(?,?,?,?,?,?)`, eventID, input.FrameID, sequence, KernelLocalExecApprovalConsumedEventType,
			string(raw), now); err != nil {
			return err
		}
		event = FrameEvent{ID: eventID, FrameID: input.FrameID, Sequence: sequence,
			Type: KernelLocalExecApprovalConsumedEventType, Payload: payload, CreatedAt: now}
		return enqueueKernelLocalExecApprovalRealtime(ctx, s, tx, input.OwnerUserID, event)
	})
	return event, err
}

func enqueueKernelLocalExecApprovalRealtime(
	ctx context.Context,
	store *Store,
	tx workspaceTransaction,
	ownerUserID string,
	event FrameEvent,
) error {
	frame, err := frameForRealtimeInTransaction(ctx, tx, event.FrameID)
	if err != nil {
		return err
	}
	_, err = store.enqueueRealtimeOutboxTransaction(ctx, tx,
		FrameRealtimeEventInput("frame-event:"+event.ID, ownerUserID, frame, event), event.ID, "")
	return err
}

func kernelLocalExecApprovalEventTx(
	ctx context.Context,
	tx workspaceTransaction,
	frameID, eventType, requestID string,
) (FrameEvent, bool, error) {
	var event FrameEvent
	var raw string
	err := tx.QueryRowContext(ctx, `
		SELECT id,sequence,payload,created_at FROM frame_events
		WHERE frame_id=? AND event_type=? AND json_extract(payload,'$.request_id')=?
		ORDER BY sequence DESC LIMIT 1`, frameID, eventType, requestID,
	).Scan(&event.ID, &event.Sequence, &raw, &event.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return FrameEvent{}, false, nil
	}
	if err != nil {
		return FrameEvent{}, false, err
	}
	event.FrameID, event.Type = frameID, eventType
	if json.Unmarshal([]byte(raw), &event.Payload) != nil {
		return FrameEvent{}, false, errors.New("kernel local execution approval event is invalid")
	}
	return event, true, nil
}

func (s *Store) GetKernelLocalExecApprovalDecision(
	ctx context.Context,
	ownerUserID, projectID, frameID, frameIncarnationID, requestID string,
) (KernelLocalExecApprovalDecision, bool, error) {
	if s == nil || s.db == nil {
		return KernelLocalExecApprovalDecision{}, false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return KernelLocalExecApprovalDecision{}, false, errors.New("kernel local execution approval context is required")
	}
	ownerUserID, projectID, frameID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID), strings.TrimSpace(frameID)
	frameIncarnationID, requestID = strings.TrimSpace(frameIncarnationID), strings.TrimSpace(requestID)
	if ownerUserID == "" || projectID == "" || frameID == "" || frameIncarnationID == "" || requestID == "" {
		return KernelLocalExecApprovalDecision{}, false, errors.New("kernel local execution approval identity is required")
	}
	var owned string
	if err := s.db.QueryRowContext(ctx, `
		SELECT f.id FROM frames f JOIN projects p ON p.id=f.project_id AND p.user_id=?
		WHERE f.id=? AND f.project_id=? AND f.incarnation_id=?`,
		ownerUserID, frameID, projectID, frameIncarnationID).Scan(&owned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return KernelLocalExecApprovalDecision{}, false, nil
		}
		return KernelLocalExecApprovalDecision{}, false, err
	}
	return kernelLocalExecApprovalDecisionQuery(ctx, s.db, frameID, requestID)
}

func (s *Store) KernelLocalExecApprovalPolicy(
	ctx context.Context,
	ownerUserID, projectID, rootFrameID, tool, environment string,
) (ApprovalPolicyDecision, error) {
	if s == nil || s.db == nil {
		return ApprovalPolicyDecision{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return ApprovalPolicyDecision{}, errors.New("kernel local execution approval context is required")
	}
	ownerUserID, projectID, rootFrameID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID), strings.TrimSpace(rootFrameID)
	tool, environment = strings.ToLower(strings.TrimSpace(tool)), strings.TrimSpace(environment)
	if ownerUserID == "" || projectID == "" || rootFrameID == "" || tool == "" || environment == "" {
		return ApprovalPolicyDecision{}, errors.New("kernel local execution grant identity is required")
	}
	var result ApprovalPolicyDecision
	err := s.db.QueryRowContext(ctx, `
		SELECT tier,scope,scope_target_id FROM approval_policy_grants
		WHERE owner_user_id=? AND kind='local_exec' AND grant_key=? AND (
			(scope='conversation' AND scope_target_id=?) OR
			(scope='project' AND scope_target_id=?) OR
			(scope='always' AND scope_target_id='')
		)
		ORDER BY CASE scope WHEN 'conversation' THEN 0 WHEN 'project' THEN 1 ELSE 2 END
		LIMIT 1`, ownerUserID, kernelLocalExecGrantKey(tool, environment), rootFrameID, projectID,
	).Scan(&result.Tier, &result.Scope, &result.ScopeTargetID)
	if errors.Is(err, sql.ErrNoRows) {
		return ApprovalPolicyDecision{}, nil
	}
	if err != nil {
		return ApprovalPolicyDecision{}, fmt.Errorf("read kernel local execution approval policy: %w", err)
	}
	result.Found = true
	return result, nil
}

func (s *Store) ListApprovalPolicyGrants(ctx context.Context, ownerUserID string) ([]ApprovalPolicyGrant, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return nil, errors.New("approval policy context is required")
	}
	ownerUserID = strings.TrimSpace(ownerUserID)
	if ownerUserID == "" {
		return nil, errors.New("approval policy owner is required")
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT kind,grant_key,scope,tier,scope_target_id,
			COALESCE(origin_project_id,''),COALESCE(origin_root_frame_id,'')
		FROM approval_policy_grants WHERE owner_user_id=?
		ORDER BY kind,grant_key,scope,scope_target_id,tier`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("list approval policy grants: %w", err)
	}
	defer rows.Close()
	grants := make([]ApprovalPolicyGrant, 0)
	for rows.Next() {
		var grant ApprovalPolicyGrant
		if err := rows.Scan(&grant.Kind, &grant.Key, &grant.Scope, &grant.Tier, &grant.ScopeTargetID,
			&grant.OriginProjectID, &grant.OriginRootFrameID); err != nil {
			return nil, fmt.Errorf("scan approval policy grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list approval policy grants: %w", err)
	}
	return grants, nil
}

func (s *Store) DeleteApprovalPolicyGrant(
	ctx context.Context,
	ownerUserID, kind, key, scope, tier, scopeTargetID string,
) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	if ctx == nil {
		return false, errors.New("approval policy context is required")
	}
	ownerUserID, kind, key = strings.TrimSpace(ownerUserID), strings.TrimSpace(kind), strings.TrimSpace(key)
	scope, tier, scopeTargetID = strings.TrimSpace(scope), strings.TrimSpace(tier), strings.TrimSpace(scopeTargetID)
	if ownerUserID == "" || kind == "" || key == "" || scope == "" || tier == "" {
		return false, errors.New("complete approval policy grant coordinate is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM approval_policy_grants
		WHERE owner_user_id=? AND kind=? AND grant_key=? AND scope=? AND tier=? AND scope_target_id=?`,
		ownerUserID, kind, key, scope, tier, scopeTargetID)
	if err != nil {
		return false, fmt.Errorf("delete approval policy grant: %w", err)
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func normalizeKernelLocalExecApprovalRequest(input *KernelLocalExecApprovalRequestInput) error {
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.FrameIncarnationID = strings.TrimSpace(input.FrameIncarnationID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.RootFrameIncarnationID = strings.TrimSpace(input.RootFrameIncarnationID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Tool = strings.ToLower(strings.TrimSpace(input.Tool))
	input.ToolCallID = strings.TrimSpace(input.ToolCallID)
	input.Environment = strings.TrimSpace(input.Environment)
	input.InputSHA256 = strings.ToLower(strings.TrimSpace(input.InputSHA256))
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.RunnerID = strings.TrimSpace(input.RunnerID)
	input.ClaimTokenSHA256 = strings.ToLower(strings.TrimSpace(input.ClaimTokenSHA256))
	input.KernelID = strings.TrimSpace(input.KernelID)
	input.WorkingDir = strings.TrimSpace(input.WorkingDir)
	if input.OwnerUserID == "" || input.ProjectID == "" || input.FrameID == "" ||
		input.FrameIncarnationID == "" || input.RootFrameID == "" || input.RootFrameIncarnationID == "" ||
		input.RequestID == "" || input.ToolCallID == "" || input.InputSHA256 == "" || input.StreamUID == "" ||
		input.RunnerID == "" || input.RunnerAttempt <= 0 || len(input.ClaimTokenSHA256) != sha256.Size*2 ||
		input.KernelID == "" ||
		input.ExpectedGeneration == 0 || input.Code == "" {
		return errors.New("complete kernel local execution approval identity is required")
	}
	if input.Tool != "python" && input.Tool != "r" && input.Tool != "repl" && input.Tool != "software_runtime" {
		return errors.New("kernel local execution approval tool is invalid")
	}
	if input.Environment == "" {
		return errors.New("kernel local execution approval environment is required")
	}
	if len(input.InputSHA256) != sha256.Size*2 {
		return errors.New("kernel local execution approval input digest is invalid")
	}
	return nil
}

func normalizeKernelLocalExecApprovalResolution(input *KernelLocalExecApprovalResolutionInput) error {
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.FrameIncarnationID = strings.TrimSpace(input.FrameIncarnationID)
	input.RootFrameID = strings.TrimSpace(input.RootFrameID)
	input.RootIncarnationID = strings.TrimSpace(input.RootIncarnationID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Tool = strings.ToLower(strings.TrimSpace(input.Tool))
	input.Environment = strings.TrimSpace(input.Environment)
	input.InputSHA256 = strings.ToLower(strings.TrimSpace(input.InputSHA256))
	input.StreamUID = strings.TrimSpace(input.StreamUID)
	input.RunnerID = strings.TrimSpace(input.RunnerID)
	input.ClaimTokenSHA256 = strings.ToLower(strings.TrimSpace(input.ClaimTokenSHA256))
	input.KernelID = strings.TrimSpace(input.KernelID)
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	if input.OwnerUserID == "" || input.ProjectID == "" || input.FrameID == "" ||
		input.FrameIncarnationID == "" || input.RootFrameID == "" || input.RootIncarnationID == "" ||
		input.RequestID == "" || input.Tool == "" ||
		input.Environment == "" || len(input.InputSHA256) != sha256.Size*2 || input.StreamUID == "" ||
		input.RunnerID == "" || input.RunnerAttempt <= 0 || input.KernelID == "" || input.ExpectedGeneration == 0 {
		return errors.New("complete kernel local execution approval resolution is required")
	}
	if input.ClaimTokenSHA256 != "" && len(input.ClaimTokenSHA256) != sha256.Size*2 {
		return errors.New("kernel local execution approval claim fingerprint is invalid")
	}
	if input.Approved && input.RequireStaleRunner {
		return errors.New("approved kernel local execution cannot require a stale runner")
	}
	switch input.Scope {
	case "once", "conversation", "project", "always":
	default:
		return errors.New("kernel local execution approval scope is invalid")
	}
	return nil
}

func verifyKernelLocalExecApprovalFrame(
	ctx context.Context, tx workspaceTransaction,
	ownerUserID, projectID, frameID, frameIncarnationID, rootFrameID, rootFrameIncarnationID string,
) error {
	var currentRootID, currentRootIncarnation string
	err := tx.QueryRowContext(ctx, `
		SELECT f.root_frame_id,root.incarnation_id FROM frames f
		JOIN projects p ON p.id=f.project_id AND p.user_id=?
		JOIN frames root ON root.id=f.root_frame_id AND root.project_id=f.project_id
		WHERE f.id=? AND f.project_id=? AND f.incarnation_id=?`,
		ownerUserID, frameID, projectID, frameIncarnationID,
	).Scan(&currentRootID, &currentRootIncarnation)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrKernelLocalExecApprovalUnavailable
	}
	if err != nil {
		return fmt.Errorf("verify kernel local execution approval frame: %w", err)
	}
	if rootFrameID != "" && (currentRootID != rootFrameID || currentRootIncarnation != rootFrameIncarnationID) {
		return fmt.Errorf("%w: root authority changed", ErrKernelLocalExecApprovalStale)
	}
	return nil
}

func verifyKernelLocalExecApprovalRunner(
	ctx context.Context,
	tx workspaceTransaction,
	ownerUserID, projectID, frameID, rootFrameID, streamUID, runnerID string,
	runnerAttempt int64,
	claimTokenSHA256 string,
	now time.Time,
) error {
	var owned string
	baseQuery := `
		SELECT attempt.stream_uid FROM transcript_runner_attempts attempt
		JOIN transcript_streams stream ON stream.stream_uid=attempt.stream_uid
		WHERE attempt.stream_uid=? AND attempt.attempt=? AND attempt.runner_id=?
			AND attempt.status='running' AND attempt.expires_at>?
			AND stream.owner_id=? AND stream.project_id=? AND stream.frame_id=? AND stream.root_frame_id=?
			AND NOT EXISTS (
				SELECT 1 FROM transcript_runner_attempts newer
				WHERE newer.stream_uid=attempt.stream_uid AND newer.attempt>attempt.attempt
			)`
	var err error
	if claimTokenSHA256 == "" {
		err = tx.QueryRowContext(ctx, baseQuery,
			streamUID, runnerAttempt, runnerID, now, ownerUserID, projectID, frameID, rootFrameID,
		).Scan(&owned)
	} else {
		err = tx.QueryRowContext(ctx, strings.Replace(baseQuery,
			"AND attempt.status='running'", "AND lower(hex(attempt.claim_token_sha256))=? AND attempt.status='running'", 1),
			streamUID, runnerAttempt, runnerID, claimTokenSHA256, now, ownerUserID, projectID, frameID, rootFrameID,
		).Scan(&owned)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrKernelLocalExecApprovalStale
	}
	if err != nil {
		return fmt.Errorf("verify kernel local execution approval runner: %w", err)
	}
	return nil
}

func kernelApprovalContextData(ctx context.Context, tx workspaceTransaction, frameID string) (map[string]any, error) {
	contextData := map[string]any{}
	var encoded sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT context_data FROM frame_runtime_metadata WHERE frame_id=?`, frameID).Scan(&encoded); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read kernel local execution approval state: %w", err)
	}
	if encoded.Valid && strings.TrimSpace(encoded.String) != "" && json.Unmarshal([]byte(encoded.String), &contextData) != nil {
		return nil, errors.New("kernel local execution approval state is invalid")
	}
	return contextData, nil
}

func writeKernelApprovalContextData(ctx context.Context, tx workspaceTransaction, frameID string, contextData map[string]any) error {
	raw, err := json.Marshal(contextData)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO frame_runtime_metadata(frame_id,context_data) VALUES(?,?)
		ON CONFLICT(frame_id) DO UPDATE SET context_data=excluded.context_data`, frameID, string(raw))
	return err
}

func kernelLocalExecApprovalRequestEventPayload(input KernelLocalExecApprovalRequestInput) map[string]any {
	return map[string]any{
		"version": 1, "request_id": input.RequestID, "tool": input.Tool,
		"environment": input.Environment, "input_sha256": input.InputSHA256,
		"stream_uid": input.StreamUID,
		"runner_id":  input.RunnerID, "runner_attempt": input.RunnerAttempt,
		"kernel_id": input.KernelID, "kernel_generation": strconv.FormatUint(input.ExpectedGeneration, 10),
		"frame_incarnation_id":      input.FrameIncarnationID,
		"root_frame_id":             input.RootFrameID,
		"root_frame_incarnation_id": input.RootFrameIncarnationID,
	}
}

type kernelApprovalQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func kernelLocalExecApprovalDecisionQuery(
	ctx context.Context, queryer kernelApprovalQueryer, frameID, requestID string,
) (KernelLocalExecApprovalDecision, bool, error) {
	var raw string
	var createdAt time.Time
	err := queryer.QueryRowContext(ctx, `
		SELECT payload,created_at FROM frame_events
		WHERE frame_id=? AND event_type=? AND json_extract(payload,'$.request_id')=?
		ORDER BY sequence DESC LIMIT 1`, frameID, KernelLocalExecApprovalResolvedEventType, requestID,
	).Scan(&raw, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return KernelLocalExecApprovalDecision{}, false, nil
	}
	if err != nil {
		return KernelLocalExecApprovalDecision{}, false, err
	}
	var payload struct {
		Version          int    `json:"version"`
		RequestID        string `json:"request_id"`
		Tool             string `json:"tool"`
		Environment      string `json:"environment"`
		InputSHA256      string `json:"input_sha256"`
		StreamUID        string `json:"stream_uid"`
		RunnerID         string `json:"runner_id"`
		RunnerAttempt    int64  `json:"runner_attempt"`
		KernelID         string `json:"kernel_id"`
		KernelGeneration string `json:"kernel_generation"`
		Approved         bool   `json:"approved"`
		Scope            string `json:"scope"`
	}
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.Version != 1 ||
		payload.RequestID != requestID || payload.Tool == "" || payload.Environment == "" ||
		len(payload.InputSHA256) != sha256.Size*2 || payload.StreamUID == "" || payload.RunnerID == "" ||
		payload.RunnerAttempt <= 0 || payload.KernelID == "" {
		return KernelLocalExecApprovalDecision{}, false, errors.New("kernel local execution approval decision is invalid")
	}
	generation, err := strconv.ParseUint(payload.KernelGeneration, 10, 64)
	if err != nil || generation == 0 {
		return KernelLocalExecApprovalDecision{}, false, errors.New("kernel local execution approval decision generation is invalid")
	}
	switch payload.Scope {
	case "once", "conversation", "project", "always":
	default:
		return KernelLocalExecApprovalDecision{}, false, errors.New("kernel local execution approval decision scope is invalid")
	}
	return KernelLocalExecApprovalDecision{
		RequestID: payload.RequestID, Tool: payload.Tool, Environment: payload.Environment,
		InputSHA256: payload.InputSHA256, StreamUID: payload.StreamUID, RunnerID: payload.RunnerID,
		RunnerAttempt: payload.RunnerAttempt, KernelID: payload.KernelID, ExpectedGeneration: generation,
		Approved: payload.Approved,
		Scope:    payload.Scope, ResolvedAt: createdAt,
	}, true, nil
}

func kernelLocalExecApprovalDecisionMatches(
	decision KernelLocalExecApprovalDecision,
	input KernelLocalExecApprovalResolutionInput,
) bool {
	return decision.RequestID == input.RequestID && decision.Tool == input.Tool &&
		decision.Environment == input.Environment && decision.InputSHA256 == input.InputSHA256 &&
		decision.StreamUID == input.StreamUID && decision.RunnerID == input.RunnerID &&
		decision.RunnerAttempt == input.RunnerAttempt && decision.KernelID == input.KernelID &&
		decision.ExpectedGeneration == input.ExpectedGeneration &&
		decision.Approved == input.Approved && decision.Scope == input.Scope
}

func kernelLocalExecApprovalDecisionMatchesRequest(
	decision KernelLocalExecApprovalDecision,
	input KernelLocalExecApprovalRequestInput,
) bool {
	return decision.RequestID == input.RequestID && decision.Tool == input.Tool &&
		decision.Environment == input.Environment && decision.InputSHA256 == input.InputSHA256 &&
		decision.StreamUID == input.StreamUID && decision.RunnerID == input.RunnerID &&
		decision.RunnerAttempt == input.RunnerAttempt && decision.KernelID == input.KernelID &&
		decision.ExpectedGeneration == input.ExpectedGeneration
}

func kernelLocalExecApprovalDecisionTx(
	ctx context.Context, tx workspaceTransaction, frameID, requestID string,
) (KernelLocalExecApprovalDecision, bool, error) {
	return kernelLocalExecApprovalDecisionQuery(ctx, tx, frameID, requestID)
}

func upsertKernelLocalExecApprovalGrant(
	ctx context.Context, tx workspaceTransaction,
	input KernelLocalExecApprovalResolutionInput, now time.Time,
) error {
	targetID := ""
	switch input.Scope {
	case "conversation":
		var rootFrameID string
		if err := tx.QueryRowContext(ctx, `SELECT root_frame_id FROM frames WHERE id=?`, input.FrameID).Scan(&rootFrameID); err != nil {
			return err
		}
		targetID = rootFrameID
	case "project":
		targetID = input.ProjectID
	case "always":
	default:
		return errors.New("one-time approval cannot be persisted")
	}
	key := kernelLocalExecGrantKey(input.Tool, input.Environment)
	idDigest := sha256.Sum256([]byte(input.OwnerUserID + "\x00local_exec\x00" + key + "\x00" + input.Scope + "\x00" + targetID))
	id := "local-exec:" + hex.EncodeToString(idDigest[:16])
	_, err := tx.ExecContext(ctx, `
		INSERT INTO approval_policy_grants(
			id,owner_user_id,kind,grant_key,scope,tier,scope_target_id,
			origin_user_id,origin_project_id,origin_root_frame_id,created_at_ms,updated_at_ms
		) VALUES(?,?,'local_exec',?,?,'allow',?,?,?,?,?,?)
		ON CONFLICT(owner_user_id,kind,grant_key,scope,scope_target_id) DO UPDATE SET
			tier='allow',origin_user_id=excluded.origin_user_id,
			origin_project_id=excluded.origin_project_id,
			origin_root_frame_id=excluded.origin_root_frame_id,
			updated_at_ms=excluded.updated_at_ms`,
		id, input.OwnerUserID, key, input.Scope, targetID,
		input.OwnerUserID, input.ProjectID, input.RootFrameID, now.UnixMilli(), now.UnixMilli())
	return err
}

func kernelLocalExecGrantKey(tool, environment string) string {
	_ = environment
	return strings.ToLower(strings.TrimSpace(tool))
}

func compatibilityInt64Value(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	default:
		return 0
	}
}

func compatibilityUint64String(value any) uint64 {
	parsed, _ := strconv.ParseUint(strings.TrimSpace(compatibilityStringValue(value)), 10, 64)
	return parsed
}
