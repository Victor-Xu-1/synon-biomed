package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestTerminalFrameToolRecoverySettlesLateDurableKernelResult(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	checkpointPayload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{map[string]any{
			"id": "terminal-late-result-python", "type": "function", "name": "python",
			"arguments": map[string]any{"code": "print(1)", "environment": "science"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var batch ToolCallBatch
	var operations []KernelLocalOperation
	_, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim, ClientMessageID: "terminal-late-result-source", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: checkpointPayload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			var createErr error
			batch, _, _, createErr = store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			if createErr != nil {
				return transcriptstore.RunnerCheckpointCommitReceipt{}, createErr
			}
			operations, createErr = store.CreateKernelLocalOperationsForCheckpointTx(ctx, tx, event)
			return kernelLocalOperationCommitReceiptWithBatch(batch, operations), createErr
		},
	})
	if err != nil || !created || len(operations) != 1 || batch.CallCount != 1 {
		t.Fatalf("batch=%#v operations=%#v created=%t err=%v", batch, operations, created, err)
	}
	operation := operations[0]
	batchID := batch.BatchID
	items, err := store.ListToolCallBatchItems(context.Background(), claim.OwnerID, batchID)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	batch, err = store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{
		Claim: claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, items[0] = appendToolCallBatchStart(
		t, store, repo, claim, batch, items[0], "terminal-late-result-start",
	)
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: claim.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "terminal-late-result-decision", Scope: "once",
		Source: "user", ActorID: claim.OwnerID, CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: claim.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: approved.StateVersion, Claim: claim,
		BootID: "terminal-late-result-boot", KernelID: "terminal-late-result-kernel", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartKernelLocalOperation(context.Background(), StartKernelLocalOperationInput{
		OwnerUserID: claim.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: prepared.StateVersion, Claim: claim,
		BootID: "terminal-late-result-boot", ExecutionID: "terminal-late-result-execution",
	})
	if err != nil {
		t.Fatal(err)
	}
	terminalResult := json.RawMessage(`{"ok":true,"exec_id":"terminal-late-result-execution","stdout":"done\\n"}`)
	finished, err := store.FinishKernelLocalOperation(context.Background(), FinishKernelLocalOperationInput{
		OwnerUserID: claim.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: started.StateVersion, Claim: claim,
		BootID: "terminal-late-result-boot", ExecutionID: "terminal-late-result-execution",
		TerminalState: KernelLocalOperationStateCompleted, ReasonCode: "execution_completed",
		TerminalResultJSON: terminalResult,
		ExecutionLog: SaveExecutionLogInput{
			Record: ExecutionLogRecord{
				ID: "terminal-late-result-execution", FrameID: operation.FrameID, CellIndex: 0,
				KernelID: "terminal-late-result-kernel", CondaEnv: operation.Environment,
				Language: "python", Source: "print('done')", Stdout: "done\n",
				ExitStatus: "completed", Origin: "agent", ExecutedAt: time.Unix(1_700_500_000, 0).UTC(),
			},
			ExpectedOwnerID: claim.OwnerID, ExpectedProjectID: operation.ProjectID,
			ExpectedFrameIncarnationID:     operation.FrameIncarnationID,
			ExpectedRootFrameIncarnationID: operation.RootFrameIncarnationID,
		},
	})
	if err != nil || finished.Operation.State != KernelLocalOperationStateCompleted {
		t.Fatalf("finished=%#v err=%v", finished, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim, ClientMessageID: "terminal-late-result-finish", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}

	batches, more, err := store.ListTerminalFrameToolBatchRecoveryCandidates(context.Background(), 10)
	if err != nil || more || len(batches) != 1 || batches[0].BatchID != batchID {
		t.Fatalf("batches=%#v more=%t err=%v", batches, more, err)
	}
	reconciled, err := store.ReconcileTerminalFrameToolCallBatch(context.Background(), claim.OwnerID, batchID)
	if err != nil || !reconciled {
		t.Fatalf("reconciled=%t err=%v", reconciled, err)
	}
	stored, found, err := store.GetToolCallBatch(context.Background(), claim.OwnerID, batchID)
	storedItems, itemsErr := store.ListToolCallBatchItems(context.Background(), claim.OwnerID, batchID)
	if err != nil || itemsErr != nil || !found || stored.State != ToolCallBatchStateSettled ||
		stored.ReasonCode != terminalFrameLateResultRecoveryReasonCode ||
		stored.NextOrdinal != stored.CallCount || len(storedItems) != 1 ||
		storedItems[0].State != ToolCallBatchItemStateCompleted {
		t.Fatalf("stored=%#v items=%#v found=%t err=%v itemsErr=%v", stored, storedItems, found, err, itemsErr)
	}
	var eventType string
	var runnerAttempt any
	var payload []byte
	if err := store.db.QueryRow(`SELECT event_type,runner_attempt,payload_json FROM transcript_events
		WHERE stream_uid=? AND event_id=?`, claim.StreamUID, storedItems[0].TerminalEventID).Scan(
		&eventType, &runnerAttempt, &payload,
	); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Status     string          `json:"status"`
		ReasonCode string          `json:"reasonCode"`
		ToolResult json.RawMessage `json:"toolResult"`
	}
	if eventType != "tool_recovery_settlement" || runnerAttempt != nil || json.Unmarshal(payload, &decoded) != nil ||
		decoded.Status != ToolCallBatchItemStateCompleted ||
		decoded.ReasonCode != terminalFrameLateResultRecoveryReasonCode ||
		string(decoded.ToolResult) != `{"exec_id":"terminal-late-result-execution","ok":true,"stdout":"done\\n"}` {
		t.Fatalf("event type=%q attempt=%#v payload=%s", eventType, runnerAttempt, payload)
	}
}

func TestTerminalFrameToolRecoveryDeniesOrphanedApprovalAndSettlesWholeBatch(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(
		t, store, repo, claim, "terminal-recovery-source", "terminal-recovery-python",
	)
	batchID := toolCallBatchID(claim.StreamUID, operation.SourceEventID)
	batch, found, err := store.GetToolCallBatch(context.Background(), claim.OwnerID, batchID)
	if err != nil || !found {
		t.Fatalf("batch=%#v found=%t err=%v", batch, found, err)
	}
	items, err := store.ListToolCallBatchItems(context.Background(), claim.OwnerID, batchID)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	batch, err = store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{
		Claim: claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	batch, items[0] = appendToolCallBatchStart(
		t, store, repo, claim, batch, items[0], "terminal-recovery-start",
	)
	if batch.State != ToolCallBatchStateRunning || items[0].State != ToolCallBatchItemStateRunning {
		t.Fatalf("started batch=%#v item=%#v", batch, items[0])
	}

	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim, ClientMessageID: "terminal-recovery-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}

	approvals, more, err := store.ListTerminalFrameKernelApprovalRecoveryCandidates(context.Background(), 10)
	if err != nil || more || len(approvals) != 1 || approvals[0].OperationID != operation.OperationID {
		t.Fatalf("approvals=%#v more=%t err=%v", approvals, more, err)
	}
	resolved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: false, DecisionID: "terminal-frame-recovery:" + operation.OperationID,
		Scope: "once", Source: "stale_reconcile", ActorID: "system:kernel-operation-recovery",
		ReasonCode: "terminal_frame_orphaned_approval",
	})
	if err != nil || resolved.State != KernelLocalOperationStateFailed ||
		resolved.ApprovalSource != "stale_reconcile" || resolved.ReasonCode != "terminal_frame_orphaned_approval" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(operation.FrameID)
	if err != nil || !found {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	if pending := compatibilityPendingInputRequests(metadata.ContextData); len(pending) != 0 {
		t.Fatalf("pending projections=%#v", pending)
	}

	batches, more, err := store.ListTerminalFrameToolBatchRecoveryCandidates(context.Background(), 10)
	if err != nil || more || len(batches) != 1 || batches[0].BatchID != batchID {
		t.Fatalf("batches=%#v more=%t err=%v", batches, more, err)
	}
	reconciled, err := store.ReconcileTerminalFrameToolCallBatch(context.Background(), claim.OwnerID, batchID)
	if err != nil || !reconciled {
		t.Fatalf("reconciled=%t err=%v", reconciled, err)
	}
	stored, found, err := store.GetToolCallBatch(context.Background(), claim.OwnerID, batchID)
	storedItems, itemsErr := store.ListToolCallBatchItems(context.Background(), claim.OwnerID, batchID)
	if err != nil || itemsErr != nil || !found || stored.State != ToolCallBatchStateSettled ||
		stored.ReasonCode != terminalFrameApprovalCancelReasonCode || len(storedItems) != 2 {
		t.Fatalf("stored=%#v items=%#v found=%t err=%v itemsErr=%v", stored, storedItems, found, err, itemsErr)
	}
	for _, item := range storedItems {
		if item.State != ToolCallBatchItemStateCancelled || item.TerminalEventID <= 0 || item.TerminalResultSHA256 == "" {
			t.Fatalf("recovered item=%#v", item)
		}
		var eventType string
		var runnerAttempt any
		var payload []byte
		if err := store.db.QueryRow(`SELECT event_type,runner_attempt,payload_json FROM transcript_events
			WHERE stream_uid=? AND event_id=?`, claim.StreamUID, item.TerminalEventID).Scan(
			&eventType, &runnerAttempt, &payload,
		); err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Status     string         `json:"status"`
			ToolCallID string         `json:"toolCallId"`
			ReasonCode string         `json:"reasonCode"`
			ToolResult map[string]any `json:"toolResult"`
		}
		if eventType != "tool_recovery_settlement" || runnerAttempt != nil || json.Unmarshal(payload, &decoded) != nil ||
			decoded.Status != ToolCallBatchItemStateCancelled || decoded.ToolCallID != item.ToolCallID ||
			decoded.ReasonCode != terminalFrameApprovalCancelReasonCode ||
			decoded.ToolResult["code"] != "operation_cancelled_before_approval" {
			t.Fatalf("event type=%q attempt=%#v payload=%s", eventType, runnerAttempt, payload)
		}
	}
	if again, err := store.ReconcileTerminalFrameToolCallBatch(context.Background(), claim.OwnerID, batchID); err != nil || again {
		t.Fatalf("idempotent reconcile=%t err=%v", again, err)
	}
}

func TestTerminalFrameToolRecoveryDeniesOrphanedApprovalWhenProjectionAlreadyMissing(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(
		t, store, repo, claim, "terminal-missing-projection-source", "terminal-missing-projection-python",
	)
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim, ClientMessageID: "terminal-missing-projection-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	if err := repo.RunImmediate(context.Background(), func(tx *transcriptstore.ImmediateTransaction) error {
		return writeKernelApprovalContextData(context.Background(), tx, operation.FrameID, map[string]any{})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: false, DecisionID: "missing-projection-user-decision", Scope: "once",
		Source: "user", ActorID: operation.OwnerUserID, ReasonCode: "approval_denied",
	}); !errors.Is(err, errKernelLocalOperationPendingProjectionMissing) {
		t.Fatalf("ordinary approval must retain projection consistency, err=%v", err)
	}

	approvals, more, err := store.ListTerminalFrameKernelApprovalRecoveryCandidates(context.Background(), 10)
	if err != nil || more || len(approvals) != 1 || approvals[0].OperationID != operation.OperationID {
		t.Fatalf("approvals=%#v more=%t err=%v", approvals, more, err)
	}
	resolved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: false, DecisionID: "terminal-frame-recovery:" + operation.OperationID,
		Scope: "once", Source: "stale_reconcile", ActorID: "system:kernel-operation-recovery",
		ReasonCode: "terminal_frame_orphaned_approval",
	})
	if err != nil || resolved.State != KernelLocalOperationStateFailed ||
		resolved.ApprovalSource != "stale_reconcile" || resolved.ReasonCode != "terminal_frame_orphaned_approval" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(operation.FrameID)
	if err != nil || !found || len(compatibilityPendingInputRequests(metadata.ContextData)) != 0 {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
}

func TestTerminalFrameToolRecoveryPreservesApprovedKernelBatchForResume(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(
		t, store, repo, claim, "terminal-approved-source", "terminal-approved-python",
	)
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "terminal-approved-decision", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: claim,
	})
	if err != nil || approved.State != KernelLocalOperationStateApproved {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim, ClientMessageID: "terminal-approved-finish", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","reasonCode":"provider_interrupted"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}

	batches, more, err := store.ListTerminalFrameToolBatchRecoveryCandidates(context.Background(), 10)
	if err != nil || more || len(batches) != 0 {
		t.Fatalf("approved kernel batch was selected as orphaned: batches=%#v more=%t err=%v", batches, more, err)
	}
	batchID := toolCallBatchID(claim.StreamUID, operation.SourceEventID)
	if reconciled, err := store.ReconcileTerminalFrameToolCallBatch(
		context.Background(), claim.OwnerID, batchID,
	); reconciled || !errors.Is(err, ErrToolCallBatchStale) {
		t.Fatalf("approved kernel batch reconcile=%t err=%v", reconciled, err)
	}
	batch, found, err := store.GetToolCallBatch(context.Background(), claim.OwnerID, batchID)
	items, itemsErr := store.ListToolCallBatchItems(context.Background(), claim.OwnerID, batchID)
	if err != nil || itemsErr != nil || !found || batch.State != ToolCallBatchStateReady ||
		len(items) != 2 || items[0].State != ToolCallBatchItemStatePending {
		t.Fatalf("batch=%#v items=%#v found=%t err=%v itemsErr=%v", batch, items, found, err, itemsErr)
	}
}
