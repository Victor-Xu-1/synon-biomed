package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

type kernelSettlementTestFileWrite struct {
	Zeta  string `json:"zeta"`
	Alpha string `json:"alpha"`
}

func TestKernelBackgroundSettlementStagesSmallEnvelopeAndDeliversOperationIdempotently(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "settlement", "call-settlement")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: operation.StateVersion,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: true,
		DecisionID: "decision-settlement", Scope: "once", Source: "user", ActorID: "owner", CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: approved.StateVersion,
		Claim: claim, BootID: "boot-settlement", KernelID: "kernel-settlement", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("d", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartKernelLocalOperation(context.Background(), StartKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: prepared.StateVersion,
		Claim: claim, BootID: "boot-settlement", ExecutionID: "execution-settlement",
	})
	if err != nil {
		t.Fatal(err)
	}
	largeOutput := "private-result-" + strings.Repeat("x", 128*1024)
	terminalResult, err := json.Marshal(map[string]any{
		"ok": true, "exec_id": started.ExecutionID, "tool_use_id": started.ToolCallID,
		"kernel_id": started.KernelID, "kernel_kind": "python", "reused": true,
		"stdout": largeOutput, "stderr": "", "exit_status": "ok", "cell_index": 2,
		"files_written": []string{"result.csv"}, "dropped_roots": []string{"/blocked"},
	})
	if err != nil {
		t.Fatal(err)
	}
	logInput := SaveExecutionLogInput{
		Record: ExecutionLogRecord{
			ID: started.ExecutionID, FrameID: started.FrameID, CellIndex: 2, KernelID: started.KernelID,
			KernelKind: "python", CondaEnv: started.Environment, Language: "python", Source: "print('private')",
			Stdout: largeOutput, ExitStatus: "ok", Origin: "agent", ExecutedAt: time.Unix(1_700_100_000, 0).UTC(),
			FilesWritten: []kernelSettlementTestFileWrite{{Zeta: "result.csv", Alpha: "created"}},
		},
		ExpectedOwnerID: started.OwnerUserID, ExpectedProjectID: started.ProjectID,
		ExpectedFrameIncarnationID:     started.FrameIncarnationID,
		ExpectedRootFrameIncarnationID: started.RootFrameIncarnationID,
	}
	notification := CreateNotificationInput{
		ID: "settlement-notification", SenderFrameID: started.FrameID, RecipientFrameID: started.FrameID,
		RootFrameID: started.RootFrameID, OwnerUserID: started.OwnerUserID, NotificationType: "cell_result",
		Payload: kernelBackgroundSettlementExpectedNotificationPayload(t, started.ExecutionID, started.ToolCallID, terminalResult, 4096),
	}
	input := EnqueueKernelBackgroundSettlementInput{
		ExecutionLog: logInput,
		Operation: &KernelBackgroundSettlementOperationInput{
			Operation: started, Claim: claim, TerminalState: KernelLocalOperationStateCompleted,
			ReasonCode: "execution_completed", TerminalResultJSON: terminalResult,
		},
		Notification: notification, Reused: true, DroppedRoots: []string{"/blocked"},
		DurationMS: 1234, OutputLimitBytes: 4096,
	}
	realtimeBefore := countKernelSettlementOutboxTopic(t, store, RealtimeOutboxTopic)
	event, err := store.EnqueueKernelBackgroundSettlement(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if event.Topic != KernelResultSettlementOutboxTopic || event.Status != OutboxStatusPending ||
		event.MaxAttempts != kernelResultSettlementMaxAttempts || len(event.Payload) > 2048 {
		t.Fatalf("event topic=%q status=%q max_attempts=%d payload_bytes=%d payload=%s",
			event.Topic, event.Status, event.MaxAttempts, len(event.Payload), event.Payload)
	}
	if bytes.Contains(event.Payload, []byte(claim.ClaimToken)) || bytes.Contains(event.Payload, []byte(largeOutput[:1024])) ||
		bytes.Contains(event.Payload, []byte("print('private')")) {
		t.Fatalf("settlement envelope leaked claim or execution content: %s", event.Payload)
	}
	replayedEvent, err := store.EnqueueKernelBackgroundSettlement(context.Background(), input)
	if err != nil || replayedEvent.ID != event.ID || replayedEvent.Sequence != event.Sequence ||
		countKernelSettlementOutboxTopic(t, store, KernelResultSettlementOutboxTopic) != 1 {
		t.Fatalf("enqueue replay=%#v original=%#v err=%v", replayedEvent, event, err)
	}
	if realtimeAfterStage := countKernelSettlementOutboxTopic(t, store, RealtimeOutboxTopic); realtimeAfterStage != realtimeBefore {
		t.Fatalf("staging emitted realtime projection before=%d after=%d", realtimeBefore, realtimeAfterStage)
	}
	if _, err := store.db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.StreamUID, claim.Attempt); err != nil {
		t.Fatal(err)
	}
	assertKernelSettlementOperationExcludedFromRecovery(t, store, started.OperationID, "boot-after-restart")
	deadLetterKernelSettlementForRestartGuard(t, store, event.ID)
	assertKernelSettlementOperationExcludedFromRecovery(t, store, started.OperationID, "boot-after-dead-letter")
	if err := store.RequeueOutboxDeadLetter(context.Background(), event.ID); err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeKernelBackgroundSettlement(event)
	if err != nil || decoded.OperationID != started.OperationID || decoded.ExecutionID != started.ExecutionID || decoded.NotificationID != notification.ID {
		t.Fatalf("decoded=%#v err=%v", decoded, err)
	}
	if stored, found, err := store.GetExecutionLog(started.FrameID, started.ExecutionID); err != nil || !found || stored.Stdout != largeOutput {
		t.Fatalf("staged log found=%t stdout=%d err=%v", found, len(stored.Stdout), err)
	}
	materialized, found, err := store.GetKernelToolResultMaterialization(context.Background(), started.OperationID)
	if err != nil || !found || len(materialized.TerminalResultJSON) == 0 || materialized.ExecutionLogSHA256 == "" {
		t.Fatalf("staged materialization=%#v found=%t err=%v", materialized, found, err)
	}
	staged, found, err := store.GetKernelLocalOperation(context.Background(), started.OwnerUserID, started.OperationID)
	if err != nil || !found || staged.State != KernelLocalOperationStateStarted || staged.ExecutionLogID != "" {
		t.Fatalf("staged operation=%#v found=%t err=%v", staged, found, err)
	}

	if err := store.DeliverKernelBackgroundSettlement(context.Background(), event); err == nil {
		t.Fatal("pending outbox event authorized delayed settlement")
	}
	claimed := claimKernelBackgroundSettlementEvent(t, store)
	wrongClaim := claimed
	wrongClaim.ClaimToken = "wrong-claim"
	if err := store.DeliverKernelBackgroundSettlement(context.Background(), wrongClaim); err == nil {
		t.Fatal("wrong outbox claim token authorized delayed settlement")
	}
	if err := store.DeliverKernelBackgroundSettlement(context.Background(), claimed); err != nil {
		t.Fatal(err)
	}
	if realtimeAfterDelivery := countKernelSettlementOutboxTopic(t, store, RealtimeOutboxTopic); realtimeAfterDelivery < realtimeBefore+2 {
		t.Fatalf("delivery did not transactionally enqueue realtime projection before=%d after=%d", realtimeBefore, realtimeAfterDelivery)
	}
	assertKernelSettlementDoneOutbox(t, store, started.ExecutionID)
	if err := store.DeliverKernelBackgroundSettlement(context.Background(), claimed); err != nil {
		t.Fatalf("inflight redelivery was not idempotent: %v", err)
	}
	finished, found, err := store.GetKernelLocalOperation(context.Background(), started.OwnerUserID, started.OperationID)
	if err != nil || !found || finished.State != KernelLocalOperationStateCompleted || finished.ExecutionLogID != started.ExecutionID {
		t.Fatalf("finished operation=%#v found=%t err=%v", finished, found, err)
	}
	var notifications int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE id=?`, notification.ID).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("notifications=%d err=%v", notifications, err)
	}
	if err := store.AckOutbox(context.Background(), claimed.ID, claimed.ClaimToken); err != nil {
		t.Fatal(err)
	}
	if err := store.DeliverKernelBackgroundSettlement(context.Background(), claimed); err == nil {
		t.Fatal("acked outbox claim remained authorized")
	}
}

func TestKernelBackgroundSettlementRejectsNotificationFromPreMaterializedResult(t *testing.T) {
	store, repo, claim := newKernelLocalOperationFixture(t)
	operation := createKernelLocalOperationForTest(t, store, repo, claim, "settlement-authority", "call-settlement-authority")
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: operation.StateVersion,
		ApprovalRequestID: operation.ApprovalRequestID, Approved: true,
		DecisionID: "decision-settlement-authority", Scope: "once", Source: "user", ActorID: "owner", CurrentClaim: claim,
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := store.PrepareKernelLocalOperation(context.Background(), PrepareKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: approved.StateVersion,
		Claim: claim, BootID: "boot-settlement-authority", KernelID: "kernel-settlement-authority", KernelGeneration: 1,
		ConfinementSHA256: strings.Repeat("e", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := store.StartKernelLocalOperation(context.Background(), StartKernelLocalOperationInput{
		OwnerUserID: "owner", OperationID: operation.OperationID, ExpectedStateVersion: prepared.StateVersion,
		Claim: claim, BootID: "boot-settlement-authority", ExecutionID: "execution-settlement-authority",
	})
	if err != nil {
		t.Fatal(err)
	}
	materialized := json.RawMessage(`{"ok":true,"result_ref":"artifact-version:terminal-result"}`)
	preMaterialized := json.RawMessage(`{"ok":true,"stdout":"original terminal result"}`)
	logInput := SaveExecutionLogInput{
		Record: ExecutionLogRecord{
			ID: started.ExecutionID, FrameID: started.FrameID, KernelID: started.KernelID,
			KernelKind: "python", CondaEnv: started.Environment, Language: "python", Source: "print('result')",
			Stdout: "original terminal result", ExitStatus: "ok", Origin: "agent", ExecutedAt: time.Unix(1_700_100_100, 0).UTC(),
		},
		ExpectedOwnerID: started.OwnerUserID, ExpectedProjectID: started.ProjectID,
		ExpectedFrameIncarnationID: started.FrameIncarnationID, ExpectedRootFrameIncarnationID: started.RootFrameIncarnationID,
	}
	notification := CreateNotificationInput{
		ID: "settlement-authority-notification", SenderFrameID: started.FrameID, RecipientFrameID: started.FrameID,
		RootFrameID: started.RootFrameID, OwnerUserID: started.OwnerUserID, NotificationType: "cell_result",
		Payload: kernelBackgroundSettlementExpectedNotificationPayload(t, started.ExecutionID, started.ToolCallID, preMaterialized, 4096),
	}
	_, err = store.EnqueueKernelBackgroundSettlement(context.Background(), EnqueueKernelBackgroundSettlementInput{
		ExecutionLog: logInput,
		Operation: &KernelBackgroundSettlementOperationInput{
			Operation: started, Claim: claim, TerminalState: KernelLocalOperationStateCompleted,
			ReasonCode: "execution_completed", TerminalResultJSON: materialized,
		},
		Notification: notification, OutputLimitBytes: 4096,
	})
	if err == nil || !strings.Contains(err.Error(), "notification does not match") {
		t.Fatalf("pre-materialized notification error=%v", err)
	}
}

func TestKernelBackgroundSettlementSupportsNonOperationAndStrictDecode(t *testing.T) {
	store, _ := openNotificationTestStore(t)
	defer store.Close()
	ctx := context.Background()
	access, found, err := store.GetKernelFrameAccess("root-1")
	if err != nil || !found {
		t.Fatalf("access found=%t err=%v", found, err)
	}
	execution := BackgroundKernelExecution{
		ExecID: "settlement-non-operation", ToolID: "tool-non-operation", ToolName: "python",
		FrameID: "root-1", RootFrameID: "root-1", FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameIncarnationID: access.RootFrameIncarnationID, StartedAt: time.Unix(1_700_200_000, 0).UTC(),
	}
	if _, err := store.RecordBackgroundKernelExecutionStarted(ctx, access, execution); err != nil {
		t.Fatal(err)
	}
	largeOutput := "large-non-operation-" + strings.Repeat("z", 1024*1024+128*1024)
	logInput := SaveExecutionLogInput{
		Record: ExecutionLogRecord{
			ID: execution.ExecID, FrameID: execution.FrameID, CellIndex: 0, KernelID: "kernel-non-operation",
			KernelKind: "python", CondaEnv: "default", Language: "python", Source: "print(7)", Stdout: largeOutput,
			ExitStatus: "ok", Origin: "agent", ExecutedAt: time.Unix(1_700_200_001, 0).UTC(),
		},
		ExpectedOwnerID: "owner-1", ExpectedProjectID: "project-1",
		ExpectedFrameIncarnationID: access.Frame.IncarnationID, ExpectedRootFrameIncarnationID: access.RootFrameIncarnationID,
	}
	resultJSON := kernelBackgroundSettlementResultJSON(t, logInput.Record, execution.ToolID, false, nil)
	notification := CreateNotificationInput{
		ID: "settlement-non-operation-notification", SenderFrameID: execution.FrameID,
		RecipientFrameID: execution.FrameID, RootFrameID: execution.RootFrameID,
		OwnerUserID: "owner-1", NotificationType: "cell_result",
		Payload: kernelBackgroundSettlementExpectedNotificationPayload(t, execution.ExecID, execution.ToolID, resultJSON, 4096),
	}
	realtimeBefore := countKernelSettlementOutboxTopic(t, store, RealtimeOutboxTopic)
	event, err := store.EnqueueKernelBackgroundSettlement(ctx, EnqueueKernelBackgroundSettlementInput{
		ExecutionLog: logInput, Notification: notification, OutputLimitBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if event.Status != OutboxStatusPending || event.Topic != KernelResultSettlementOutboxTopic ||
		countKernelSettlementOutboxTopic(t, store, KernelResultSettlementOutboxTopic) != 1 {
		t.Fatalf("unbound enqueue event=%#v", event)
	}
	if realtimeAfterStage := countKernelSettlementOutboxTopic(t, store, RealtimeOutboxTopic); realtimeAfterStage != realtimeBefore {
		t.Fatalf("non-operation staging emitted realtime projection before=%d after=%d", realtimeBefore, realtimeAfterStage)
	}
	assertUnboundKernelSettlementExcludedFromLostRecovery(t, store, access, execution)
	deadLetterKernelSettlementForRestartGuard(t, store, event.ID)
	assertUnboundKernelSettlementExcludedFromLostRecovery(t, store, access, execution)
	if err := store.RequeueOutboxDeadLetter(ctx, event.ID); err != nil {
		t.Fatal(err)
	}
	unknown := event
	unknown.Payload = append([]byte(nil), event.Payload[:len(event.Payload)-1]...)
	unknown.Payload = append(unknown.Payload, []byte(`,"unknown":true}`)...)
	if _, err := DecodeKernelBackgroundSettlement(unknown); err == nil {
		t.Fatal("strict decode accepted an unknown field")
	}
	claimed := claimKernelBackgroundSettlementEvent(t, store)
	if current, err := store.GetOutboxEvent(ctx, claimed.ID); err != nil || current.Status != OutboxStatusInflight ||
		current.ClaimToken != claimed.ClaimToken {
		t.Fatalf("unbound inflight event=%#v err=%v", current, err)
	}
	if err := store.DeliverKernelBackgroundSettlement(ctx, claimed); err != nil {
		t.Fatal(err)
	}
	if realtimeAfterDelivery := countKernelSettlementOutboxTopic(t, store, RealtimeOutboxTopic); realtimeAfterDelivery < realtimeBefore+2 {
		t.Fatalf("non-operation delivery did not enqueue realtime projection before=%d after=%d", realtimeBefore, realtimeAfterDelivery)
	}
	assertKernelSettlementDoneOutbox(t, store, execution.ExecID)
	if stored, found, err := store.GetExecutionLog(execution.FrameID, execution.ExecID); err != nil || !found || stored.Stdout != largeOutput {
		t.Fatalf("large staged output found=%t bytes=%d err=%v", found, len(stored.Stdout), err)
	}
	var realtimePayloadBytes int
	if err := store.db.QueryRow(`SELECT length(CAST(payload_json AS BLOB)) FROM workspace_outbox
		WHERE topic=? AND idempotency_key=?`, RealtimeOutboxTopic,
		"kernel-execution:"+execution.ExecID+":done").Scan(&realtimePayloadBytes); err != nil || realtimePayloadBytes > 256*1024 {
		t.Fatalf("bounded realtime payload bytes=%d err=%v", realtimePayloadBytes, err)
	}
	if err := store.DeliverKernelBackgroundSettlement(ctx, claimed); err != nil {
		t.Fatalf("non-operation redelivery failed: %v", err)
	}
	var notifications int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE id=?`, notification.ID).Scan(&notifications); err != nil || notifications != 1 {
		t.Fatalf("notifications=%d err=%v", notifications, err)
	}
	if unread, err := store.CountUnreadNotifications(ctx, execution.FrameID, execution.RootFrameID, "owner-1"); err != nil || unread != 1 {
		t.Fatalf("unbound unread=%d err=%v", unread, err)
	}
	if pending, err := store.ListPendingBackgroundKernelExecutions(ctx, access); err != nil || len(pending) != 0 {
		t.Fatalf("unbound pending=%#v err=%v", pending, err)
	}
	if err := store.AckOutbox(ctx, claimed.ID, claimed.ClaimToken); err != nil {
		t.Fatal(err)
	}
	if current, err := store.GetOutboxEvent(ctx, claimed.ID); err != nil || current.Status != OutboxStatusDelivered {
		t.Fatalf("unbound delivered event=%#v err=%v", current, err)
	}
}

func TestKernelBackgroundSettlementDecodeAcceptsLegacyV1WithoutResultCode(t *testing.T) {
	store, _ := openNotificationTestStore(t)
	defer store.Close()
	ctx := context.Background()
	access, found, err := store.GetKernelFrameAccess("root-1")
	if err != nil || !found {
		t.Fatalf("access found=%t err=%v", found, err)
	}
	execution := BackgroundKernelExecution{
		ExecID: "settlement-legacy-v1", ToolID: "tool-legacy-v1", ToolName: "python",
		FrameID: "root-1", RootFrameID: "root-1", FrameIncarnationID: access.Frame.IncarnationID,
		RootFrameIncarnationID: access.RootFrameIncarnationID, StartedAt: time.Unix(1_700_200_200, 0).UTC(),
	}
	if _, err := store.RecordBackgroundKernelExecutionStarted(ctx, access, execution); err != nil {
		t.Fatal(err)
	}
	logInput := SaveExecutionLogInput{
		Record: ExecutionLogRecord{
			ID: execution.ExecID, FrameID: execution.FrameID, KernelID: "kernel-legacy-v1",
			KernelKind: "python", CondaEnv: "default", Language: "python", Source: "print(1)",
			Stdout: "1\n", ExitStatus: "ok", Origin: "agent", ExecutedAt: time.Unix(1_700_200_201, 0).UTC(),
		},
		ExpectedOwnerID: "owner-1", ExpectedProjectID: "project-1",
		ExpectedFrameIncarnationID: access.Frame.IncarnationID, ExpectedRootFrameIncarnationID: access.RootFrameIncarnationID,
	}
	resultJSON := kernelBackgroundSettlementResultJSON(t, logInput.Record, execution.ToolID, false, nil)
	event, err := store.EnqueueKernelBackgroundSettlement(ctx, EnqueueKernelBackgroundSettlementInput{
		ExecutionLog: logInput,
		Notification: CreateNotificationInput{
			ID: "settlement-legacy-v1-notification", SenderFrameID: execution.FrameID,
			RecipientFrameID: execution.FrameID, RootFrameID: execution.RootFrameID,
			OwnerUserID: "owner-1", NotificationType: "cell_result",
			Payload: kernelBackgroundSettlementExpectedNotificationPayload(t, execution.ExecID, execution.ToolID, resultJSON, 4096),
		},
		OutputLimitBytes: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(event.Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope["schemaVersion"] = float64(kernelResultSettlementLegacySchemaVersion)
	delete(envelope, "resultCode")
	event.Payload, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeKernelBackgroundSettlement(event)
	if err != nil || decoded.SchemaVersion != kernelResultSettlementLegacySchemaVersion || decoded.ResultCode != "" {
		t.Fatalf("legacy decoded=%#v err=%v", decoded, err)
	}
}

func claimKernelBackgroundSettlementEvent(t *testing.T, store *Store) OutboxEvent {
	t.Helper()
	events, err := store.ClaimOutbox(context.Background(), ClaimOutboxInput{
		WorkerID: "kernel-settlement-test", Topics: []string{KernelResultSettlementOutboxTopic}, Limit: 1, Lease: time.Minute,
	})
	if err != nil || len(events) != 1 {
		t.Fatalf("claimed events=%#v err=%v", events, err)
	}
	return events[0]
}

func countKernelSettlementOutboxTopic(t *testing.T, store *Store, topic string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workspace_outbox WHERE topic=?`, topic).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertKernelSettlementDoneOutbox(t *testing.T, store *Store, executionID string) {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM workspace_outbox
		WHERE topic=? AND event_type='execution_cell_update' AND idempotency_key=?`,
		RealtimeOutboxTopic, "kernel-execution:"+executionID+":done").Scan(&count); err != nil || count != 1 {
		t.Fatalf("execution done outbox count=%d err=%v", count, err)
	}
}

func assertKernelSettlementOperationExcludedFromRecovery(
	t *testing.T,
	store *Store,
	operationID, currentBootID string,
) {
	t.Helper()
	candidates, _, err := store.ListKernelLocalOperationRecoveryCandidates(
		context.Background(), currentBootID, 100,
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.Operation.OperationID == operationID {
			t.Fatalf("staged operation was offered to restart recovery: %#v", candidate)
		}
	}
}

func assertUnboundKernelSettlementExcludedFromLostRecovery(
	t *testing.T,
	store *Store,
	access KernelFrameAccess,
	execution BackgroundKernelExecution,
) {
	t.Helper()
	pending, err := store.ListPendingBackgroundKernelExecutions(context.Background(), access)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range pending {
		if candidate.ExecID == execution.ExecID {
			t.Fatalf("staged unbound execution was offered to lost recovery: %#v", candidate)
		}
	}
	if event, marked, err := store.MarkBackgroundKernelExecutionLostIfPending(
		context.Background(), access, execution,
	); err != nil || marked || event.ID != "" {
		t.Fatalf("staged unbound execution marked lost event=%#v marked=%t err=%v", event, marked, err)
	}
}

func deadLetterKernelSettlementForRestartGuard(t *testing.T, store *Store, eventID string) {
	t.Helper()
	result, err := store.db.Exec(`UPDATE workspace_outbox SET status='dead_letter',dead_lettered_at_ms=1
		WHERE event_id=? AND topic=? AND status='pending'`, eventID, KernelResultSettlementOutboxTopic)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		t.Fatalf("dead-letter settlement rows=%d err=%v", rows, err)
	}
}

func kernelBackgroundSettlementExpectedNotificationPayload(
	t *testing.T, executionID, toolID string, resultJSON []byte, outputLimit int64,
) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	if int64(len(output)) > outputLimit {
		output = output[:outputLimit]
	}
	status := "completed"
	if result["exit_status"] == "cancelled" {
		status = "interrupted"
	} else if result["exit_status"] != "ok" {
		status = "errored"
	}
	payload := map[string]any{"exec_id": executionID, "tool_id": toolID, "status": status, "output": output}
	if len(output) < len(encoded) {
		payload["output_truncated_from_chars"] = len([]rune(string(encoded)))
	}
	return payload
}

func kernelBackgroundSettlementResultJSON(
	t *testing.T, record ExecutionLogRecord, toolID string, reused bool, droppedRoots []string,
) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"ok": record.ExitStatus == "ok", "exec_id": record.ID, "tool_use_id": toolID,
		"kernel_id": record.KernelID, "kernel_kind": record.KernelKind, "reused": reused,
		"stdout": record.Stdout, "stderr": record.Stderr, "exit_status": record.ExitStatus,
		"cell_index": record.CellIndex, "files_written": record.FilesWritten, "dropped_roots": droppedRoots,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
