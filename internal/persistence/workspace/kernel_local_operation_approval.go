package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const KernelLocalExecutionApprovalDeniedMessage = "kernel local execution was denied by an approval authority; this decision is not transient, so do not retry the same or an equivalent operation unless a later user message explicitly requests it"

var errKernelLocalOperationPendingProjectionMissing = errors.New("kernel local operation pending projection is missing")

func kernelLocalOperationApprovalRequestedEventID(operationID string) string {
	return "kernel-local-operation-requested:" + operationID
}

func (s *Store) resolveKernelLocalOperationApprovalAndProject(
	ctx context.Context,
	input ResolveKernelLocalOperationApprovalInput,
	target, decision string,
) (KernelLocalOperation, error) {
	deniedMessageJSON, err := json.Marshal(KernelLocalExecutionApprovalDeniedMessage)
	if err != nil {
		return KernelLocalOperation{}, err
	}
	deniedResult := []byte(`{"ok":false,"error":{"code":"approval_denied","message":` + string(deniedMessageJSON) + `}}`)
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return KernelLocalOperation{}, err
	}
	var resolved KernelLocalOperation
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		operation, found, err := getKernelLocalOperationQuery(ctx, tx, input.OperationID)
		if err != nil {
			return err
		}
		if !found || operation.OwnerUserID != input.OwnerUserID || operation.ApprovalRequestID != input.ApprovalRequestID {
			return ErrKernelLocalOperationConflict
		}
		matchesDecision := operation.ApprovalDecisionID == input.DecisionID && operation.ApprovalDecision == decision &&
			operation.ApprovalScope == input.Scope && operation.ApprovalSource == input.Source &&
			operation.ApprovalActorID == input.ActorID && operation.ReasonCode == input.ReasonCode
		if operation.State != KernelLocalOperationStatePendingApproval {
			if operation.State != target || operation.StateVersion != input.ExpectedStateVersion+1 || !matchesDecision {
				return ErrKernelLocalOperationConflict
			}
			if _, err := appendKernelLocalOperationApprovalResolvedEvent(ctx, s, tx, operation); err != nil {
				return err
			}
			if decision == "deny" {
				expected, err := normalizeKernelToolResultMaterialization(
					operation.OperationID, deniedResult, "", "", "native_v41", operation.UpdatedAt,
				)
				if err != nil {
					return err
				}
				existing, found, err := getKernelToolResultMaterializationQuery(ctx, tx, operation.OperationID)
				if err != nil || !found || !sameKernelToolResultMaterialization(existing, expected) {
					return ErrKernelLocalOperationConflict
				}
			}
			resolved = operation
			return nil
		}
		if operation.StateVersion != input.ExpectedStateVersion {
			return ErrKernelLocalOperationStale
		}
		if err := validateKernelLocalOperationCurrentAuthority(ctx, tx, operation, true); err != nil {
			return err
		}
		if input.Approved && input.Scope != "once" {
			if err := upsertKernelLocalExecApprovalGrant(ctx, tx, KernelLocalExecApprovalResolutionInput{
				OwnerUserID: operation.OwnerUserID, ProjectID: operation.ProjectID, FrameID: operation.FrameID,
				RootFrameID: operation.RootFrameID, Tool: operation.Tool, Environment: operation.Environment,
				Approved: true, Scope: input.Scope,
			}, s.now().UTC()); err != nil {
				return err
			}
		}
		if err := removeKernelLocalOperationPendingProjection(ctx, tx, operation); err != nil {
			projectionAlreadyRemovedByTerminalRecovery :=
				errors.Is(err, errKernelLocalOperationPendingProjectionMissing) &&
					!input.Approved && input.Source == "stale_reconcile" &&
					input.ActorID == "system:kernel-operation-recovery" &&
					input.ReasonCode == "terminal_frame_orphaned_approval"
			if !projectionAlreadyRemovedByTerminalRecovery {
				return err
			}
		}
		now := s.now().UTC().Format(time.RFC3339Nano)
		var admittedInputRevision any
		if input.AdmitRunnerRevision {
			// The user's durable approval decision is a real input response:
			// append it as a transcript user_input_response so checkpoint
			// resume authority can be reclaimed after the runner parked at
			// WaitingApproval. The event is deterministic and idempotent.
			responsePayload, encodeErr := json.Marshal(map[string]any{
				"kind": "kernel_local_exec_approval", "operation_id": operation.OperationID,
				"decision": decision, "decision_id": input.DecisionID, "scope": input.Scope,
				"source": input.Source, "actor_id": input.ActorID,
			})
			if encodeErr != nil {
				return encodeErr
			}
			if _, created, appendErr := tx.AppendFrameInputResponse(ctx, transcriptstore.AppendFrameInputResponseInput{
				StreamUID: operation.StreamUID, OwnerID: operation.OwnerUserID, FrameID: operation.FrameID,
				ClientMessageID: "kernel-approval-response:" + operation.OperationID + ":" + input.DecisionID,
				PayloadJSON:     responsePayload, Destinations: []string{"ws"},
			}); appendErr != nil {
				return appendErr
			} else if !created {
				// A previous decision already advanced the revision; the
				// operation state machine below still validates idempotency.
			}
			var revision int64
			if err := tx.QueryRowContext(ctx, `SELECT input_revision FROM transcript_streams
				WHERE stream_uid=?`, operation.StreamUID).Scan(&revision); err != nil {
				return err
			}
			if revision <= 0 {
				return ErrKernelLocalOperationConflict
			}
			admittedInputRevision = revision
		} else if strings.TrimSpace(input.CurrentClaim.ClaimToken) != "" {
			if err := validateKernelLocalOperationLiveClaim(ctx, tx, operation, input.CurrentClaim); err != nil {
				return err
			}
			if input.CurrentClaim.ClaimedInputRevision <= 0 {
				return ErrKernelLocalOperationConflict
			}
			admittedInputRevision = input.CurrentClaim.ClaimedInputRevision
		}
		var resultJSON, resultSHA any
		if decision == "deny" {
			digest := sha256.Sum256(deniedResult)
			resultJSON = string(deniedResult)
			resultSHA = hex.EncodeToString(digest[:])
		}
		result, err := tx.ExecContext(ctx, `UPDATE kernel_local_operations SET
			approval_decision_id=?,approval_decision=?,approval_scope=?,approval_source=?,approval_actor_id=?,
			decided_at=?,approved_at=CASE WHEN ?='allow' THEN ? ELSE NULL END,
			terminal_at=CASE WHEN ?='failed' THEN ? ELSE NULL END,admitted_input_revision=?,
			result_json=?,result_sha256=?,state=?,state_version=state_version+1,
			reason_code=?,updated_at=?
			WHERE operation_id=? AND owner_user_id=? AND state='pending_approval' AND state_version=?
				AND approval_request_id=?`, input.DecisionID, decision, input.Scope, input.Source, input.ActorID,
			now, decision, now, target, now, admittedInputRevision, resultJSON, resultSHA,
			target, input.ReasonCode, now, input.OperationID, input.OwnerUserID,
			input.ExpectedStateVersion, input.ApprovalRequestID)
		if err != nil {
			return err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return ErrKernelLocalOperationStale
		}
		resolved, found, err = getKernelLocalOperationQuery(ctx, tx, input.OperationID)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return ErrKernelLocalOperationConflict
		}
		if decision == "deny" {
			materialization, err := normalizeKernelToolResultMaterialization(
				resolved.OperationID, deniedResult, "", "", "native_v41", resolved.UpdatedAt,
			)
			if err != nil {
				return err
			}
			if err := insertKernelToolResultMaterializationTx(ctx, tx, materialization); err != nil {
				return err
			}
		}
		if _, err = appendKernelLocalOperationApprovalResolvedEvent(ctx, s, tx, resolved); err != nil {
			return err
		}
		return nil
	})
	if err == nil {
		s.signalKernelRetentionWake()
	}
	return resolved, err
}

func kernelLocalOperationApprovalResolvedEventID(operationID, decisionID string) string {
	return "kernel-local-operation-resolved:" + operationID + ":" + decisionID
}

func appendKernelLocalOperationApprovalConsumedEvent(
	ctx context.Context,
	store *Store,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
) error {
	payload := map[string]any{
		"version": 2, "operation_id": operation.OperationID, "request_id": operation.ApprovalRequestID,
		"decision_id": operation.ApprovalDecisionID, "stream_uid": operation.StreamUID,
		"runner_id": operation.RunnerID, "runner_attempt": operation.RunnerAttempt,
		"admitted_input_revision": operation.AdmittedInputRevision,
		"state":                   operation.State, "state_version": operation.StateVersion,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	eventID := fmt.Sprintf("kernel-local-operation-consumed:%s:%d", operation.OperationID, operation.StateVersion)
	if existing, stored, found, err := frameEventByID(ctx, tx, eventID); err != nil {
		return err
	} else if found {
		if existing.FrameID != operation.FrameID || existing.Type != KernelLocalExecApprovalConsumedEventType ||
			stored != string(raw) {
			return ErrKernelLocalOperationConflict
		}
		return nil
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM frame_events WHERE frame_id=?`,
		operation.FrameID).Scan(&sequence); err != nil {
		return err
	}
	event := FrameEvent{ID: eventID, FrameID: operation.FrameID, Sequence: sequence,
		Type: KernelLocalExecApprovalConsumedEventType, Payload: payload, CreatedAt: operation.UpdatedAt}
	if _, err := tx.ExecContext(ctx, `INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?,?,?,?,?,?)`, event.ID, event.FrameID, event.Sequence, event.Type, string(raw), event.CreatedAt); err != nil {
		return err
	}
	return enqueueKernelLocalExecApprovalRealtime(ctx, store, tx, operation.OwnerUserID, event)
}

func appendKernelLocalOperationApprovalResolvedEvent(
	ctx context.Context,
	store *Store,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
) (FrameEvent, error) {
	payload := map[string]any{
		"version": 2, "operation_id": operation.OperationID, "request_id": operation.ApprovalRequestID,
		"decision_id": operation.ApprovalDecisionID, "decision": operation.ApprovalDecision,
		"scope": operation.ApprovalScope, "source": operation.ApprovalSource,
		"actor_id": operation.ApprovalActorID, "state": operation.State,
		"state_version": operation.StateVersion, "reason_code": operation.ReasonCode,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, err
	}
	eventID := kernelLocalOperationApprovalResolvedEventID(operation.OperationID, operation.ApprovalDecisionID)
	if existing, stored, found, err := frameEventByID(ctx, tx, eventID); err != nil {
		return FrameEvent{}, err
	} else if found {
		if existing.FrameID != operation.FrameID || existing.Type != KernelLocalExecApprovalResolvedEventType || stored != string(raw) {
			return FrameEvent{}, ErrKernelLocalOperationConflict
		}
		return existing, nil
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM frame_events WHERE frame_id=?`,
		operation.FrameID).Scan(&sequence); err != nil {
		return FrameEvent{}, err
	}
	createdAt := operation.UpdatedAt
	event := FrameEvent{ID: eventID, FrameID: operation.FrameID, Sequence: sequence,
		Type: KernelLocalExecApprovalResolvedEventType, Payload: payload, CreatedAt: createdAt}
	if _, err := tx.ExecContext(ctx, `INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?,?,?,?,?,?)`, event.ID, event.FrameID, event.Sequence, event.Type, string(raw), event.CreatedAt); err != nil {
		return FrameEvent{}, err
	}
	if err := enqueueKernelLocalExecApprovalRealtime(ctx, store, tx, operation.OwnerUserID, event); err != nil {
		return FrameEvent{}, err
	}
	return event, nil
}

func kernelLocalOperationApprovalProjection(operation KernelLocalOperation) map[string]any {
	return map[string]any{
		"version": 2, "requestId": operation.ApprovalRequestID, "kind": "local_exec",
		"operation_id": operation.OperationID, "state_version": operation.StateVersion,
		"tool": operation.Tool, "tool_name": operation.Tool, "tool_call_id": operation.ToolCallID,
		"environment": operation.Environment, "input_sha256": operation.InputSHA256,
		"stream_uid": operation.StreamUID, "source_event_id": operation.SourceEventID,
		"tool_call_ordinal": operation.ToolCallOrdinal,
	}
}

func projectKernelLocalOperationApprovalRequestedTx(
	ctx context.Context,
	store *Store,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
) (FrameEvent, error) {
	if operation.ApprovalRequestID == "" {
		return FrameEvent{}, ErrKernelLocalOperationConflict
	}
	projection := kernelLocalOperationApprovalProjection(operation)
	contextData, err := kernelApprovalContextData(ctx, tx, operation.FrameID)
	if err != nil {
		return FrameEvent{}, err
	}
	pending := compatibilityPendingInputRequests(contextData)
	foundPending := false
	for _, item := range pending {
		if compatibilityPendingInputID(item) != operation.ApprovalRequestID {
			continue
		}
		if !mapsEqualJSON(item, projection) {
			return FrameEvent{}, ErrKernelLocalOperationConflict
		}
		foundPending = true
		break
	}
	if operation.State == KernelLocalOperationStatePendingApproval && !foundPending {
		pending = append(pending, projection)
		contextData["_pending_input_requests"] = compatibilityMapsToAny(pending)
		if err := writeKernelApprovalContextData(ctx, tx, operation.FrameID, contextData); err != nil {
			return FrameEvent{}, err
		}
	}
	eventID := kernelLocalOperationApprovalRequestedEventID(operation.OperationID)
	raw, err := json.Marshal(projection)
	if err != nil {
		return FrameEvent{}, err
	}
	if existing, stored, found, err := frameEventByID(ctx, tx, eventID); err != nil {
		return FrameEvent{}, err
	} else if found {
		if existing.FrameID != operation.FrameID || existing.Type != KernelLocalExecApprovalRequestedEventType ||
			stored != string(raw) {
			return FrameEvent{}, ErrKernelLocalOperationConflict
		}
		return existing, nil
	}
	if operation.State != KernelLocalOperationStatePendingApproval {
		return FrameEvent{}, ErrKernelLocalOperationConflict
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM frame_events WHERE frame_id=?`,
		operation.FrameID).Scan(&sequence); err != nil {
		return FrameEvent{}, err
	}
	event := FrameEvent{ID: eventID, FrameID: operation.FrameID, Sequence: sequence,
		Type: KernelLocalExecApprovalRequestedEventType, Payload: projection, CreatedAt: operation.CreatedAt}
	if _, err := tx.ExecContext(ctx, `INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?,?,?,?,?,?)`, event.ID, event.FrameID, event.Sequence, event.Type, string(raw), event.CreatedAt); err != nil {
		return FrameEvent{}, fmt.Errorf("insert kernel local operation approval event: %w", err)
	}
	if err := enqueueKernelLocalExecApprovalRealtime(ctx, store, tx, operation.OwnerUserID, event); err != nil {
		return FrameEvent{}, err
	}
	return event, nil
}

func removeKernelLocalOperationPendingProjection(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
) error {
	contextData, err := kernelApprovalContextData(ctx, tx, operation.FrameID)
	if err != nil {
		return err
	}
	pending := compatibilityPendingInputRequests(contextData)
	filtered := make([]map[string]any, 0, len(pending))
	found := false
	for _, item := range pending {
		if compatibilityPendingInputID(item) == operation.ApprovalRequestID {
			if !mapsEqualJSON(item, kernelLocalOperationApprovalProjection(operation)) {
				return ErrKernelLocalOperationConflict
			}
			found = true
			continue
		}
		filtered = append(filtered, item)
	}
	if !found {
		return errKernelLocalOperationPendingProjectionMissing
	}
	if len(filtered) == 0 {
		delete(contextData, "_pending_input_requests")
	} else {
		contextData["_pending_input_requests"] = compatibilityMapsToAny(filtered)
	}
	return writeKernelApprovalContextData(ctx, tx, operation.FrameID, contextData)
}

func normalizeKernelLocalOperationApprovalIdentity(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 512 {
		return "", errors.New("kernel local operation approval identity is invalid")
	}
	return value, nil
}
