package workspace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func observedTaskOperationFixture(t *testing.T) (*Store, *transcriptstore.Repository, TaskOperation) {
	t.Helper()
	store, repo, claim := newToolCallBatchFixture(t)
	batch, items := createToolCallBatchForTest(t, store, repo, claim, "durable-observed", toolCallBatchTestCall{
		id: "observed-create", name: "manage_environments", arguments: map[string]any{"background": true, "mode": "create", "name": "test"},
	})
	batch, err := store.ClaimToolCallBatch(context.Background(), ClaimToolCallBatchInput{Claim: claim, BatchID: batch.BatchID, ExpectedStateVersion: batch.StateVersion})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = appendToolCallBatchStart(t, store, repo, claim, batch, items[0], "durable-observed-start")
	stream, err := repo.GetStream(context.Background(), claim.StreamUID, claim.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	access, found, err := store.GetKernelFrameAccessContext(context.Background(), stream.FrameID)
	if err != nil || !found {
		t.Fatalf("access=%v err=%v", found, err)
	}
	return store, repo, TaskOperation{
		Version: 1, ID: "durable-observed-operation", OwnerID: access.UserID, FrameID: access.Frame.ID,
		FrameIncarnationID: access.Frame.IncarnationID, RootFrameID: access.Frame.RootFrameID,
		RootFrameIncarnationID: access.RootFrameIncarnationID, Tool: "manage_environments", NotificationID: "observed-result",
		Request: json.RawMessage(`{"kind":"create"}`), ObservationAdmission: &transcriptstore.ToolOperationAdmission{
			Claim: claim, CallID: "observed-create", ToolName: "manage_environments", BootID: "first-boot",
		},
	}
}

func claimObservedTaskOperation(t *testing.T, store *Store) OutboxEvent {
	t.Helper()
	events, err := store.ClaimOutbox(context.Background(), ClaimOutboxInput{WorkerID: "observer-test", Topics: []string{TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
	if err != nil || len(events) != 1 {
		t.Fatalf("claim=%#v err=%v", events, err)
	}
	return events[0]
}

func latestTaskObservationStatus(t *testing.T, store *Store, operation TaskOperation) string {
	t.Helper()
	var status string
	err := store.db.QueryRow(`SELECT json_extract(payload_json,'$.status') FROM transcript_events
		WHERE event_type=? AND json_extract(payload_json,'$.durableOperationId')=? ORDER BY event_id DESC LIMIT 1`,
		transcriptstore.ToolOperationObservationEventType, operation.ID).Scan(&status)
	if err != nil {
		t.Fatal(err)
	}
	return status
}

func TestTaskOperationObservationAdmissionRollsBackWithDispatch(t *testing.T) {
	store, _, operation := observedTaskOperationFixture(t)
	ctx := context.Background()
	if _, err := store.db.Exec(`CREATE TEMP TRIGGER reject_observed_dispatch BEFORE INSERT ON workspace_outbox WHEN NEW.topic='task.operation' BEGIN SELECT RAISE(ABORT,'injected dispatch failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTaskOperation(ctx, operation); err == nil {
		t.Fatal("injected dispatch failure was ignored")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE event_type=?`, transcriptstore.ToolOperationObservationEventType).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan observation survived rollback: count=%d err=%v", count, err)
	}
	if _, err := store.db.Exec(`DROP TRIGGER reject_observed_dispatch`); err != nil {
		t.Fatal(err)
	}
	event, err := store.EnqueueTaskOperation(ctx, operation)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(event.Payload, []byte(operation.ObservationAdmission.Claim.ClaimToken)) {
		t.Fatal("runner claim was serialized into the durable request")
	}
	duplicate, err := store.EnqueueTaskOperation(ctx, operation)
	if err != nil || !bytes.Equal(event.Payload, duplicate.Payload) {
		t.Fatalf("duplicate admission changed observer identity: %v", err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE event_type=?`, transcriptstore.ToolOperationObservationEventType).Scan(&count); err != nil || count != 1 {
		t.Fatalf("admission created duplicate observations: count=%d err=%v", count, err)
	}
}

func TestTaskOperationObservationRestartFencesOldOwnerAndKeepsOneTerminal(t *testing.T) {
	store, repo, operation := observedTaskOperationFixture(t)
	ctx := context.Background()
	if _, err := store.EnqueueTaskOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	first := claimObservedTaskOperation(t, store)
	if err := store.AppendTaskOperationProgress(ctx, first, map[string]any{"phase": "downloading_packages", "bytesCompleted": 1024}); err != nil {
		t.Fatal(err)
	}
	if events, err := repo.RecoverInterruptedToolObservations(ctx, "second-boot"); err != nil || len(events) != 0 {
		t.Fatalf("service restart manufactured a terminal result: %#v %v", events, err)
	}
	if _, err := store.RetryOutbox(ctx, first.ID, first.ClaimToken, "controller restart", 0); err != nil {
		t.Fatal(err)
	}
	second := claimObservedTaskOperation(t, store)
	if err := store.AppendTaskOperationProgress(ctx, first, map[string]any{"phase": "stale"}); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("old progress owner was not fenced: %v", err)
	}
	if err := store.SettleTaskOperation(ctx, first, map[string]any{"status": "failed"}); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("old terminal owner was not fenced: %v", err)
	}
	admitted, err := DecodeTaskOperation(second)
	if err != nil || admitted.Observation == nil {
		t.Fatalf("observer lost on replay: %v", err)
	}
	if _, err := repo.AppendToolOperationObservation(ctx, *admitted.Observation, "running", 2, nil); err == nil {
		t.Fatal("durable observer bypassed its outbox lease authority")
	}
	if err := store.AppendTaskOperationProgress(ctx, second, map[string]any{"phase": "verifying_environment"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleTaskOperation(ctx, second, map[string]any{"status": "completed", "ok": true}); err != nil {
		t.Fatal(err)
	}
	if got := latestTaskObservationStatus(t, store, operation); got != "completed" {
		t.Fatalf("terminal status=%s", got)
	}
	if err := store.AppendTaskOperationProgress(ctx, second, map[string]any{"phase": "late"}); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("late progress reopened terminal: %v", err)
	}
	items, err := store.ConsumeUnreadNotifications(ctx, operation.FrameID, operation.RootFrameID, operation.OwnerID, 10)
	if err != nil || len(items) != 1 || items[0].Payload["status"] != "completed" {
		t.Fatalf("terminal notification mismatch: %#v %v", items, err)
	}
}

func TestTaskOperationObservationTerminalRollsBackWithNotification(t *testing.T) {
	store, _, operation := observedTaskOperationFixture(t)
	ctx := context.Background()
	if _, err := store.EnqueueTaskOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	event := claimObservedTaskOperation(t, store)
	if _, _, err := store.CreateNotification(ctx, CreateNotificationInput{ID: operation.NotificationID, OwnerUserID: operation.OwnerID,
		SenderFrameID: operation.FrameID, RecipientFrameID: operation.FrameID, RootFrameID: operation.RootFrameID,
		NotificationType: "cell_result", Payload: map[string]any{"status": "completed"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleTaskOperation(ctx, event, map[string]any{"status": "failed"}); err == nil {
		t.Fatal("conflicting notification was overwritten")
	}
	if got := latestTaskObservationStatus(t, store, operation); got != "running" {
		t.Fatalf("terminal observation escaped rollback: %s", got)
	}
	if current, err := store.GetOutboxEvent(ctx, event.ID); err != nil || current.Status != OutboxStatusInflight {
		t.Fatalf("claim escaped rollback: %#v %v", current, err)
	}
}
