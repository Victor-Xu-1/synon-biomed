package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	terminalFrameToolRecoveryReasonCode       = "terminal_frame_orphaned_tool"
	terminalFrameLateResultRecoveryReasonCode = "terminal_frame_late_tool_result"
	terminalFrameApprovalCancelReasonCode     = "terminal_frame_orphaned_approval"
)

// ListTerminalFrameKernelApprovalRecoveryCandidates returns only approvals
// whose authoritative Frame is already terminal and whose source branch is
// still active. A genuine paused approval on a live task is never selected.
func (s *Store) ListTerminalFrameKernelApprovalRecoveryCandidates(
	ctx context.Context,
	limit int,
) ([]KernelLocalOperation, bool, error) {
	if s == nil || s.db == nil || ctx == nil || limit <= 0 || limit > 1000 {
		return nil, false, errors.New("terminal frame approval recovery limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT operation.operation_id
		FROM kernel_local_operations operation
		JOIN frames frame ON frame.id=operation.frame_id AND frame.project_id=operation.project_id
		JOIN transcript_branch_state branch ON branch.stream_uid=operation.stream_uid
			AND branch.active_branch_id=operation.branch_id AND branch.generation=operation.branch_generation
		JOIN transcript_branch_events source ON source.stream_uid=operation.stream_uid
			AND source.branch_id=operation.branch_id AND source.event_id=operation.source_event_id
		WHERE operation.state='pending_approval'
			AND lower(frame.status) IN ('completed','failed','cancelled','canceled')
			AND NOT EXISTS (
				SELECT 1 FROM transcript_runner_attempts attempt
				WHERE attempt.stream_uid=operation.stream_uid AND attempt.attempt=operation.source_runner_attempt
					AND attempt.status='running'
			)
		ORDER BY operation.updated_at,operation.operation_id LIMIT ?`, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	result := make([]KernelLocalOperation, 0, len(ids))
	for _, id := range ids {
		operation, found, err := getKernelLocalOperationQuery(ctx, s.db, id)
		if err != nil {
			return nil, false, err
		}
		if !found || operation.State != KernelLocalOperationStatePendingApproval {
			return nil, false, ErrKernelLocalOperationConflict
		}
		result = append(result, operation)
	}
	return result, more, nil
}

func (s *Store) ListTerminalFrameToolBatchRecoveryCandidates(
	ctx context.Context,
	limit int,
) ([]ToolCallBatch, bool, error) {
	if s == nil || s.db == nil || ctx == nil || limit <= 0 || limit > 1000 {
		return nil, false, errors.New("terminal frame tool batch recovery limit must be between 1 and 1000")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT batch.batch_id
		FROM transcript_tool_call_batches batch
		JOIN transcript_streams stream ON stream.stream_uid=batch.stream_uid AND stream.owner_id=batch.owner_user_id
		JOIN frames frame ON frame.id=stream.frame_id AND frame.project_id=stream.project_id
		JOIN transcript_branch_state branch ON branch.stream_uid=batch.stream_uid
			AND branch.active_branch_id=batch.branch_id AND branch.generation=batch.branch_generation
		JOIN transcript_branch_events source ON source.stream_uid=batch.stream_uid
			AND source.branch_id=batch.branch_id AND source.event_id=batch.source_event_id
		WHERE batch.state IN ('ready','running','waiting')
			AND lower(frame.status) IN ('completed','failed','cancelled','canceled')
			AND NOT EXISTS (
				SELECT 1 FROM kernel_local_operations operation
				LEFT JOIN kernel_local_operation_protocol_receipts receipt
					ON receipt.operation_id=operation.operation_id
				LEFT JOIN kernel_local_operation_materializations materialization
					ON materialization.operation_id=operation.operation_id
				WHERE operation.stream_uid=batch.stream_uid AND operation.source_event_id=batch.source_event_id
					AND receipt.operation_id IS NULL
					AND NOT (operation.state IN ('completed','failed','cancelled')
						AND operation.execution_log_id IS NOT NULL
						AND materialization.operation_id IS NOT NULL)
					AND NOT (operation.state='failed' AND operation.approval_source='stale_reconcile'
						AND operation.reason_code='terminal_frame_orphaned_approval')
			)
			AND NOT EXISTS (
				SELECT 1 FROM transcript_runner_attempts attempt
				WHERE attempt.stream_uid=batch.stream_uid AND attempt.attempt=batch.source_runner_attempt
					AND attempt.status='running'
			)
		ORDER BY batch.updated_at,batch.batch_id LIMIT ?`, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(ids) > limit
	if more {
		ids = ids[:limit]
	}
	result := make([]ToolCallBatch, 0, len(ids))
	for _, id := range ids {
		batch, found, err := getToolCallBatchQuery(ctx, s.db, id)
		if err != nil {
			return nil, false, err
		}
		if !found || toolCallBatchTerminal(batch.State) {
			return nil, false, ErrToolCallBatchConflict
		}
		result = append(result, batch)
	}
	return result, more, nil
}

// ReconcileTerminalFrameToolCallBatch closes every nonterminal item without
// replaying a tool. The recovery events are immutable audit evidence; they are
// not model-visible tool results and cannot be mistaken for successful work.
func (s *Store) ReconcileTerminalFrameToolCallBatch(
	ctx context.Context,
	ownerUserID, batchID string,
) (bool, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	batchID = strings.TrimSpace(batchID)
	if s == nil || s.db == nil || ctx == nil || ownerUserID == "" || batchID == "" {
		return false, errors.New("terminal frame tool batch recovery authority is required")
	}
	repository, err := s.TranscriptRepository(ctx)
	if err != nil {
		return false, err
	}
	reconciled := false
	err = repository.RunImmediate(ctx, func(tx *transcriptstore.ImmediateTransaction) error {
		batch, found, err := getToolCallBatchQuery(ctx, tx, batchID)
		if err != nil {
			return err
		}
		if !found || batch.OwnerUserID != ownerUserID {
			return ErrToolCallBatchConflict
		}
		if toolCallBatchTerminal(batch.State) {
			return nil
		}
		var frameStatus, attemptStatus, activeBranch string
		var generation int64
		err = tx.QueryRowContext(ctx, `SELECT lower(frame.status),attempt.status,branch.active_branch_id,branch.generation
			FROM transcript_streams stream
			JOIN frames frame ON frame.id=stream.frame_id AND frame.project_id=stream.project_id
			JOIN transcript_runner_attempts attempt ON attempt.stream_uid=stream.stream_uid
				AND attempt.attempt=?
			JOIN transcript_branch_state branch ON branch.stream_uid=stream.stream_uid
			WHERE stream.stream_uid=? AND stream.owner_id=?`,
			batch.SourceRunnerAttempt, batch.StreamUID, batch.OwnerUserID).Scan(
			&frameStatus, &attemptStatus, &activeBranch, &generation,
		)
		if err != nil {
			return err
		}
		if !terminalFrameRecoveryStatus(frameStatus) || attemptStatus == "running" ||
			activeBranch != batch.BranchID || generation != batch.BranchGeneration {
			return ErrToolCallBatchStale
		}
		// Do not discard a kernel call that still has exact durable recovery
		// authority. Approved/prepared operations can resume, while a terminal
		// operation with no protocol receipt can still publish its exact result.
		// The sole exception is an approval that this terminal-frame recovery
		// worker itself denied immediately before settling the abandoned batch.
		var recoverableKernelOperations int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
			FROM kernel_local_operations operation
			LEFT JOIN kernel_local_operation_protocol_receipts receipt
				ON receipt.operation_id=operation.operation_id
			LEFT JOIN kernel_local_operation_materializations materialization
				ON materialization.operation_id=operation.operation_id
			WHERE operation.stream_uid=? AND operation.source_event_id=?
				AND receipt.operation_id IS NULL
				AND NOT (operation.state IN ('completed','failed','cancelled')
					AND operation.execution_log_id IS NOT NULL
					AND materialization.operation_id IS NOT NULL)
				AND NOT (operation.state='failed' AND operation.approval_source='stale_reconcile'
					AND operation.reason_code='terminal_frame_orphaned_approval')`,
			batch.StreamUID, batch.SourceEventID).Scan(&recoverableKernelOperations); err != nil {
			return err
		}
		if recoverableKernelOperations != 0 {
			return ErrToolCallBatchStale
		}
		var orphanedApprovalCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
			FROM kernel_local_operations operation
			WHERE operation.stream_uid=? AND operation.source_event_id=?
				AND operation.state='failed' AND operation.approval_source='stale_reconcile'
				AND operation.reason_code=?`,
			batch.StreamUID, batch.SourceEventID, terminalFrameApprovalCancelReasonCode,
		).Scan(&orphanedApprovalCount); err != nil {
			return err
		}
		items, err := listToolCallBatchItemsQuery(ctx, tx, batch.BatchID)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		batchReasonCode := terminalFrameLateResultRecoveryReasonCode
		if orphanedApprovalCount > 0 {
			batchReasonCode = terminalFrameApprovalCancelReasonCode
		}
		for _, item := range items {
			if toolCallBatchItemTerminal(item.State) {
				continue
			}
			status := ToolCallBatchItemStateOutcomeUnknown
			reasonCode := terminalFrameToolRecoveryReasonCode
			toolResult := json.RawMessage(`{"ok":false,"error":{"code":"tool_outcome_unknown","message":"Tool execution ended without a committed terminal receipt before the task became terminal."}}`)
			if orphanedApprovalCount > 0 {
				status = ToolCallBatchItemStateCancelled
				reasonCode = terminalFrameApprovalCancelReasonCode
				toolResult = json.RawMessage(`{"ok":false,"status":"cancelled","code":"operation_cancelled_before_approval","message":"The operation was cancelled before approval because the task stopped."}`)
			}
			var operationID, operationState string
			err := tx.QueryRowContext(ctx, `SELECT operation.operation_id,operation.state
				FROM kernel_local_operations operation
				WHERE operation.stream_uid=? AND operation.source_event_id=? AND operation.tool_call_id=?
					AND operation.tool=? AND operation.state IN ('completed','failed','cancelled')
					AND operation.execution_log_id IS NOT NULL`,
				batch.StreamUID, batch.SourceEventID, item.ToolCallID, item.ToolName).Scan(&operationID, &operationState)
			if err == nil {
				materialization, found, materializationErr := getKernelToolResultMaterializationQuery(ctx, tx, operationID)
				if materializationErr != nil {
					return materializationErr
				}
				if found {
					toolResult = append(json.RawMessage(nil), materialization.TerminalResultJSON...)
					status = terminalFrameKernelResultStatus(operationState)
					reasonCode = terminalFrameLateResultRecoveryReasonCode
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if status == ToolCallBatchItemStateOutcomeUnknown {
				batchReasonCode = terminalFrameToolRecoveryReasonCode
			}
			canonicalResult, err := canonicalToolCallBatchJSON(toolResult, false)
			if err != nil {
				return err
			}
			payload, err := json.Marshal(map[string]any{
				"version": 1, "status": status,
				"toolCallId": item.ToolCallID, "toolPhase": status,
				"toolResult": json.RawMessage(canonicalResult), "reasonCode": reasonCode,
			})
			if err != nil {
				return err
			}
			event, _, err := tx.AppendTerminalToolRecoveryEvent(ctx,
				transcriptstore.AppendTerminalToolRecoveryEventInput{
					StreamUID: batch.StreamUID, OwnerID: batch.OwnerUserID, BatchID: batch.BatchID,
					Ordinal: item.Ordinal, ToolCallID: item.ToolCallID, PayloadJSON: payload,
					Destinations: []string{"ws"},
				})
			if err != nil {
				return err
			}
			digest := sha256.Sum256(canonicalResult)
			if err := updateToolCallBatchItemState(ctx, tx, batch, item,
				status,
				`terminal_event_id=?,terminal_result_sha256=?,result_ref=NULL`,
				[]any{event.EventID, hex.EncodeToString(digest[:])}, now); err != nil {
				return err
			}
		}
		batchState := ToolCallBatchStateSettled
		if batchReasonCode == terminalFrameToolRecoveryReasonCode {
			batchState = ToolCallBatchStateOutcomeUnknown
		}
		if batchState == ToolCallBatchStateSettled {
			if err := settleTerminalFrameRecoveredToolCallBatch(ctx, tx, batch, batchReasonCode, now); err != nil {
				return err
			}
			reconciled = true
			return nil
		}
		result, err := tx.ExecContext(ctx, `UPDATE transcript_tool_call_batches SET
			state=?,state_version=state_version+1,waiting_ordinal=NULL,reason_code=?,updated_at=?
			WHERE batch_id=? AND state_version=? AND state IN ('ready','running','waiting')`,
			batchState, batchReasonCode, now.Format("2006-01-02T15:04:05.999999999Z07:00"), batch.BatchID, batch.StateVersion)
		if err != nil {
			return err
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			if err != nil {
				return err
			}
			return ErrToolCallBatchStale
		}
		reconciled = true
		return nil
	})
	if err == nil && reconciled {
		s.signalKernelRetentionWake()
	}
	return reconciled, err
}

// settleTerminalFrameRecoveredToolCallBatch advances the durable cursor through
// every now-terminal item without weakening the normal one-step transition
// trigger. A settled batch must reach call_count, and only a running batch may
// enter settled, so abandoned ready/waiting heads are normalized first.
func settleTerminalFrameRecoveredToolCallBatch(
	ctx context.Context,
	tx *transcriptstore.ImmediateTransaction,
	batch ToolCallBatch,
	reasonCode string,
	now time.Time,
) error {
	advance := func(state string, nextOrdinal int64) error {
		if err := updateToolCallBatchHead(ctx, tx, batch, state, nextOrdinal, nil, now); err != nil {
			return err
		}
		batch.State = state
		batch.StateVersion++
		batch.NextOrdinal = nextOrdinal
		batch.WaitingOrdinal = nil
		return nil
	}
	if batch.State == ToolCallBatchStateWaiting {
		if err := advance(ToolCallBatchStateReady, batch.NextOrdinal); err != nil {
			return err
		}
	}
	if batch.State == ToolCallBatchStateReady {
		if err := advance(ToolCallBatchStateRunning, batch.NextOrdinal); err != nil {
			return err
		}
	}
	if batch.State != ToolCallBatchStateRunning || batch.NextOrdinal >= batch.CallCount {
		return ErrToolCallBatchConflict
	}
	for batch.NextOrdinal < batch.CallCount-1 {
		if err := advance(ToolCallBatchStateRunning, batch.NextOrdinal+1); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE transcript_tool_call_batches SET
		state=?,state_version=state_version+1,next_ordinal=next_ordinal+1,waiting_ordinal=NULL,reason_code=?,updated_at=?
		WHERE batch_id=? AND state_version=? AND state='running' AND next_ordinal=call_count-1`,
		ToolCallBatchStateSettled, reasonCode, now.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		batch.BatchID, batch.StateVersion)
	if err != nil {
		return err
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		if err != nil {
			return err
		}
		return ErrToolCallBatchStale
	}
	return nil
}

func terminalFrameKernelResultStatus(operationState string) string {
	switch operationState {
	case KernelLocalOperationStateCompleted:
		return ToolCallBatchItemStateCompleted
	case KernelLocalOperationStateFailed:
		return ToolCallBatchItemStateFailed
	case KernelLocalOperationStateCancelled:
		return ToolCallBatchItemStateCancelled
	default:
		return ToolCallBatchItemStateOutcomeUnknown
	}
}

func terminalFrameRecoveryStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}
