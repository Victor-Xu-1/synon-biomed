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

type FinishKernelLocalOperationInput struct {
	OwnerUserID          string
	OperationID          string
	ExpectedStateVersion int64
	Claim                transcriptstore.RunnerClaim
	BootID               string
	ExecutionID          string
	TerminalState        string
	ReasonCode           string
	ExecutionLog         SaveExecutionLogInput
	TerminalResultJSON   json.RawMessage
	TerminalResultRef    string
	Notification         *CreateNotificationInput
}

type FinishKernelLocalOperationResult struct {
	Operation         KernelLocalOperation
	Event             FrameEvent
	Notification      Notification
	NotificationEvent FrameEvent
	Created           bool
}

// FinishKernelLocalOperation atomically persists the execution record, the
// terminal operation head/transition, the durable Frame event, and its outbox
// projection. Large stdout/stderr stays in execution_log; the operation stores
// only an immutable reference and digest.
func (s *Store) FinishKernelLocalOperation(
	ctx context.Context,
	input FinishKernelLocalOperationInput,
) (FinishKernelLocalOperationResult, error) {
	return s.finishKernelLocalOperation(ctx, input, nil, "")
}

// FinishDetachedKernelLocalOperation uses the immutable terminal receipt as
// settlement authority. Unlike an in-process runner claim, this authority
// survives a Web-service restart and is bound to one execution and operation
// by the detached-execution schema.
func (s *Store) FinishDetachedKernelLocalOperation(
	ctx context.Context,
	input FinishKernelLocalOperationInput,
	receiptID string,
) (FinishKernelLocalOperationResult, error) {
	receiptID = strings.TrimSpace(receiptID)
	if !validDetachedIdentity(receiptID) {
		return FinishKernelLocalOperationResult{}, errors.New("detached kernel terminal receipt is required")
	}
	return s.finishKernelLocalOperation(ctx, input, nil, receiptID)
}

func (s *Store) finishKernelLocalOperation(
	ctx context.Context,
	input FinishKernelLocalOperationInput,
	settlement *kernelSettlementDelivery,
	detachedReceiptID string,
) (FinishKernelLocalOperationResult, error) {
	if s == nil || s.db == nil || ctx == nil {
		return FinishKernelLocalOperationResult{}, errors.New("workspace store and context are required")
	}
	input.OwnerUserID = strings.TrimSpace(input.OwnerUserID)
	input.OperationID = strings.TrimSpace(input.OperationID)
	input.BootID = strings.TrimSpace(input.BootID)
	input.ExecutionID = strings.TrimSpace(input.ExecutionID)
	input.TerminalState = strings.TrimSpace(input.TerminalState)
	reason, err := normalizeKernelLocalOperationReason(input.ReasonCode)
	if err != nil {
		return FinishKernelLocalOperationResult{}, err
	}
	input.ReasonCode = reason
	if input.OwnerUserID == "" || input.OperationID == "" || input.ExpectedStateVersion <= 0 ||
		input.BootID == "" || input.ExecutionID == "" || input.ReasonCode == "" ||
		(input.TerminalState != KernelLocalOperationStateCompleted &&
			input.TerminalState != KernelLocalOperationStateFailed &&
			input.TerminalState != KernelLocalOperationStateCancelled) {
		return FinishKernelLocalOperationResult{}, errors.New("complete kernel local operation terminal authority is required")
	}
	if input.ExecutionLog.Record.ExecutedAt.IsZero() {
		return FinishKernelLocalOperationResult{}, errors.New("kernel local operation execution time is required")
	}
	prepared, err := s.prepareExecutionLog(input.ExecutionLog)
	if err != nil {
		return FinishKernelLocalOperationResult{}, err
	}
	if prepared.record.ID != input.ExecutionID {
		return FinishKernelLocalOperationResult{}, errors.New("execution log identity does not match kernel operation execution")
	}
	resultMaterial, err := json.Marshal(prepared.record)
	if err != nil {
		return FinishKernelLocalOperationResult{}, err
	}
	resultDigest := sha256.Sum256(resultMaterial)
	executionLogSHA := hex.EncodeToString(resultDigest[:])
	canonicalExecutionLogSHA, err := kernelSettlementExecutionLogSHA256(prepared.record)
	if err != nil {
		return FinishKernelLocalOperationResult{}, err
	}
	if existing, found, existingErr := s.GetKernelToolResultMaterialization(ctx, input.OperationID); existingErr != nil {
		return FinishKernelLocalOperationResult{}, existingErr
	} else if found && existing.ExecutionLogSHA256 != "" {
		switch existing.ExecutionLogSHA256 {
		case executionLogSHA:
		case canonicalExecutionLogSHA:
			executionLogSHA = canonicalExecutionLogSHA
		default:
			return FinishKernelLocalOperationResult{}, ErrKernelLocalOperationConflict
		}
	}
	executionLogRef := "execution-log:" + input.ExecutionID
	materialization, err := normalizeKernelToolResultMaterialization(
		input.OperationID, input.TerminalResultJSON, input.TerminalResultRef,
		executionLogSHA, "native_v41", prepared.record.ExecutedAt,
	)
	if err != nil {
		return FinishKernelLocalOperationResult{}, err
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return FinishKernelLocalOperationResult{}, err
	}
	var result FinishKernelLocalOperationResult
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		if settlement != nil {
			if err := validateKernelSettlementOutboxClaimTx(ctx, tx, settlement.Claim); err != nil {
				return err
			}
		}
		operation, found, err := getKernelLocalOperationQuery(ctx, tx, input.OperationID)
		if err != nil {
			return err
		}
		if !found || operation.OwnerUserID != input.OwnerUserID {
			return ErrKernelLocalOperationConflict
		}
		if kernelLocalOperationTerminal(operation.State) {
			if operation.State != input.TerminalState || operation.StateVersion != input.ExpectedStateVersion+1 ||
				operation.ExecutionID != input.ExecutionID || operation.ExecutionLogID != input.ExecutionID ||
				operation.ExecutionLogRef != executionLogRef || operation.ResultSHA256 != executionLogSHA || operation.ReasonCode != input.ReasonCode {
				return ErrKernelLocalOperationConflict
			}
			existing, found, err := getKernelToolResultMaterializationQuery(ctx, tx, operation.OperationID)
			if err != nil || !found || !sameKernelToolResultMaterialization(existing, materialization) {
				return ErrKernelLocalOperationConflict
			}
			event, err := appendKernelLocalOperationTerminalEvent(ctx, s, tx, operation, operation.UpdatedAt)
			if err != nil {
				return err
			}
			result = FinishKernelLocalOperationResult{Operation: operation, Event: event}
			if input.Notification != nil {
				notification, notificationEvent, err := createNotificationTx(ctx, tx, *input.Notification, operation.UpdatedAt)
				if err != nil {
					return err
				}
				result.Notification, result.NotificationEvent = notification, notificationEvent
			}
			if settlement != nil {
				if err := enqueueKernelSettlementRealtimeTx(
					ctx, s, tx, settlement.Envelope, prepared.record, result.NotificationEvent,
				); err != nil {
					return fmt.Errorf("kernel result settlement realtime_projection: %w", err)
				}
			}
			return nil
		}
		if operation.State != KernelLocalOperationStateStarted || operation.StateVersion != input.ExpectedStateVersion ||
			operation.ExecutionID != input.ExecutionID || operation.BootID != input.BootID ||
			prepared.record.FrameID != operation.FrameID || prepared.record.KernelID != operation.KernelID ||
			prepared.record.CondaEnv != operation.Environment {
			return ErrKernelLocalOperationStale
		}
		if detachedReceiptID != "" {
			if err := validateDetachedKernelTerminalAuthorityTx(
				ctx, tx, operation, input, detachedReceiptID,
			); err != nil {
				return err
			}
		} else if settlement == nil {
			if input.Claim.StreamUID != operation.StreamUID || input.Claim.OwnerID != operation.OwnerUserID ||
				input.Claim.RunnerID != operation.RunnerID || input.Claim.Attempt != operation.RunnerAttempt ||
				kernelLocalOperationClaimSHA256(input.Claim.ClaimToken) != operation.RunnerClaimSHA256 {
				return ErrKernelLocalOperationConflict
			}
		}
		// A started cell may legitimately outlive its runner lease by hours or
		// days. Its immutable operation binding, not a still-live scheduler
		// lease, authorizes terminal settlement. Branch changes do not erase
		// audit evidence for an already-started side effect.
		if err := validateKernelLocalOperationCurrentAuthority(ctx, tx, operation, false); err != nil {
			return err
		}
		if _, err := ensureKernelSettlementExecutionLogTx(ctx, tx, prepared); err != nil {
			return err
		}
		now := s.now().UTC()
		updated, err := tx.ExecContext(ctx, `UPDATE kernel_local_operations SET
			execution_log_id=?,result_ref=?,result_sha256=?,state=?,state_version=state_version+1,
			reason_code=?,terminal_at=?,updated_at=?
			WHERE operation_id=? AND owner_user_id=? AND state='started' AND state_version=?
				AND execution_id=? AND boot_id=?`,
			input.ExecutionID, executionLogRef, executionLogSHA, input.TerminalState, input.ReasonCode,
			now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), input.OperationID, input.OwnerUserID,
			input.ExpectedStateVersion, input.ExecutionID, input.BootID)
		if err != nil {
			return err
		}
		if rows, err := updated.RowsAffected(); err != nil || rows != 1 {
			return ErrKernelLocalOperationStale
		}
		operation, found, err = getKernelLocalOperationQuery(ctx, tx, input.OperationID)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return ErrKernelLocalOperationConflict
		}
		if err := insertKernelToolResultMaterializationTx(ctx, tx, materialization); err != nil {
			return err
		}
		event, err := appendKernelLocalOperationTerminalEvent(ctx, s, tx, operation, now)
		if err != nil {
			return err
		}
		result = FinishKernelLocalOperationResult{Operation: operation, Event: event, Created: true}
		if input.Notification != nil {
			notification, notificationEvent, err := createNotificationTx(ctx, tx, *input.Notification, now)
			if err != nil {
				return err
			}
			result.Notification, result.NotificationEvent = notification, notificationEvent
		}
		if settlement != nil {
			if err := enqueueKernelSettlementRealtimeTx(
				ctx, s, tx, settlement.Envelope, prepared.record, result.NotificationEvent,
			); err != nil {
				return fmt.Errorf("kernel result settlement realtime_projection: %w", err)
			}
		}
		return nil
	})
	if err == nil {
		// Terminal materialization is the authoritative wake for a runner that
		// retained its lease while waiting on this exact foreground operation.
		// Every nonterminal transition already signals this generation; omitting
		// the final transition forced recovery code to poll or repeatedly reclaim
		// the same checkpoint after a service restart.
		s.signalKernelRetentionWake()
	}
	return result, err
}

func validateDetachedKernelTerminalAuthorityTx(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	operation KernelLocalOperation,
	input FinishKernelLocalOperationInput,
	receiptID string,
) error {
	execution, found, err := getDetachedKernelExecutionQuery(ctx, tx, input.ExecutionID)
	if err != nil {
		return err
	}
	if !found || execution.OperationID != operation.OperationID ||
		execution.State != DetachedKernelExecutionStateTerminal ||
		execution.TerminalReceiptID != receiptID {
		return fmt.Errorf("%w: execution binding", ErrDetachedKernelExecutionConflict)
	}
	receipt, found, err := getKernelExecutionResultReceiptQuery(ctx, tx, receiptID)
	if err != nil {
		return err
	}
	if !found || receipt.ExecutionID != execution.ExecutionID ||
		receipt.OperationID != operation.OperationID || receipt.BackendID != execution.BackendID ||
		receipt.BackendGeneration != execution.BackendGeneration {
		return fmt.Errorf("%w: receipt binding", ErrDetachedKernelExecutionConflict)
	}
	if !receipt.StartedAt.Equal(input.ExecutionLog.Record.ExecutedAt.UTC()) {
		return fmt.Errorf("%w: execution start time", ErrDetachedKernelExecutionConflict)
	}
	if !detachedKernelReceiptTerminalStateCompatible(
		receipt.Outcome, input.TerminalState, input.ExecutionLog.Record.ExitStatus,
		input.ExecutionLog.Record.FilesWritten,
	) {
		return fmt.Errorf("%w: terminal state mapping", ErrDetachedKernelExecutionConflict)
	}
	return nil
}

func detachedKernelReceiptTerminalStateCompatible(receiptOutcome, terminalState, executionExitStatus string, files any) bool {
	receiptState := detachedKernelReceiptTerminalState(receiptOutcome)
	if receiptState == terminalState {
		return true
	}
	// The detached executor's immutable receipt records that the physical kernel
	// request returned. Server-side interpretation can still classify the
	// returned result as a failed tool call: Bash preserves a subprocess exit
	// marker inside a successful Python wrapper, and workspace policy can reject
	// an otherwise completed write. Both classifications are derived from the
	// exact receipt-backed execution log; arbitrary state changes remain denied.
	return receiptState == KernelLocalOperationStateCompleted &&
		terminalState == KernelLocalOperationStateFailed &&
		(strings.TrimSpace(executionExitStatus) != "ok" || hasWorkspaceJSONPolicyViolation(files))
}

func hasWorkspaceJSONPolicyViolation(files any) bool {
	raw, err := json.Marshal(files)
	if err != nil {
		return false
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		return false
	}
	for _, entry := range entries {
		if code, _ := entry["policy_code"].(string); code == "json_workspace_file_forbidden" {
			return true
		}
	}
	return false
}

func detachedKernelReceiptTerminalState(outcome string) string {
	switch strings.TrimSpace(outcome) {
	case "completed":
		return KernelLocalOperationStateCompleted
	case "failed":
		return KernelLocalOperationStateFailed
	case "cancelled":
		return KernelLocalOperationStateCancelled
	default:
		return ""
	}
}

func kernelLocalOperationTerminal(state string) bool {
	return state == KernelLocalOperationStateCompleted || state == KernelLocalOperationStateFailed ||
		state == KernelLocalOperationStateCancelled || state == KernelLocalOperationStateOutcomeUnknown
}

func KernelLocalOperationIsTerminal(state string) bool {
	return kernelLocalOperationTerminal(strings.TrimSpace(state))
}

func kernelLocalOperationTerminalEventID(operationID string) string {
	return "kernel-local-operation-terminal:" + operationID
}

func appendKernelLocalOperationTerminalEvent(
	ctx context.Context,
	store *Store,
	tx workspaceTransaction,
	operation KernelLocalOperation,
	now time.Time,
) (FrameEvent, error) {
	payload := map[string]any{
		"operation_id": operation.OperationID, "state": operation.State,
		"state_version": operation.StateVersion, "execution_log_id": operation.ExecutionLogID,
		"execution_log_ref": operation.ExecutionLogRef, "execution_log_sha256": operation.ResultSHA256,
		"reason_code": operation.ReasonCode,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return FrameEvent{}, err
	}
	eventID := kernelLocalOperationTerminalEventID(operation.OperationID)
	if existing, stored, found, err := frameEventByID(ctx, tx, eventID); err != nil {
		return FrameEvent{}, err
	} else if found {
		if existing.FrameID != operation.FrameID || existing.Type != "kernel_local_operation_terminal" || stored != string(raw) {
			return FrameEvent{}, ErrKernelLocalOperationConflict
		}
		return existing, nil
	}
	var sequence int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0)+1 FROM frame_events WHERE frame_id=?`,
		operation.FrameID).Scan(&sequence); err != nil {
		return FrameEvent{}, err
	}
	event := FrameEvent{ID: eventID, FrameID: operation.FrameID, Sequence: sequence,
		Type: "kernel_local_operation_terminal", Payload: payload, CreatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		VALUES(?,?,?,?,?,?)`, event.ID, event.FrameID, event.Sequence, event.Type, string(raw), event.CreatedAt); err != nil {
		return FrameEvent{}, fmt.Errorf("insert kernel local operation terminal event: %w", err)
	}
	if err := enqueueKernelLocalExecApprovalRealtime(ctx, store, tx, operation.OwnerUserID, event); err != nil {
		return FrameEvent{}, err
	}
	return event, nil
}
