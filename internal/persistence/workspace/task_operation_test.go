package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func taskOperationFixture(t *testing.T) (*Store, TaskOperation) {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "operations.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	frame, err := store.CreateFrame(CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	return store, TaskOperation{Version: 1, ID: "operation", OwnerID: "owner", FrameID: frame.ID, FrameIncarnationID: frame.IncarnationID, RootFrameID: frame.RootFrameID, RootFrameIncarnationID: frame.IncarnationID, Tool: "manage_environments", NotificationID: "operation-result", Request: json.RawMessage(`{"kind":"create"}`)}
}

func TestTaskOperationAtomicResultAndClaimFencing(t *testing.T) {
	store, operation := taskOperationFixture(t)
	ctx := context.Background()
	event, err := store.EnqueueTaskOperation(ctx, operation)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.EnqueueTaskOperation(ctx, operation)
	if err != nil || duplicate.ID != event.ID {
		t.Fatalf("duplicate=%#v err=%v", duplicate, err)
	}
	claimed, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "worker", Topics: []string{TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v %v", claimed, err)
	}
	stale := claimed[0]
	stale.ClaimToken = "stale"
	if err := store.SettleTaskOperation(ctx, stale, map[string]any{"status": "completed"}); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("stale result=%v", err)
	}
	if err := store.SettleTaskOperation(ctx, claimed[0], map[string]any{"status": "completed"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetOutboxEvent(ctx, event.ID)
	if err != nil || got.Status != OutboxStatusDelivered {
		t.Fatalf("event=%#v %v", got, err)
	}
	notifications, err := store.ConsumeUnreadNotifications(ctx, operation.FrameID, operation.RootFrameID, operation.OwnerID, 10)
	if err != nil || len(notifications) != 1 {
		t.Fatalf("notifications=%#v %v", notifications, err)
	}
	if err := store.SettleTaskOperation(ctx, claimed[0], map[string]any{"status": "failed"}); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("late result=%v", err)
	}
}

func TestTaskOperationAdmissionRejectsForeignAndCancelledFrames(t *testing.T) {
	store, operation := taskOperationFixture(t)
	ctx := context.Background()
	foreign := operation
	foreign.OwnerID = "other"
	if _, err := store.EnqueueTaskOperation(ctx, foreign); err == nil {
		t.Fatal("foreign owner admitted")
	}
	if _, err := store.CancelCompatibilityFrameTree(operation.FrameID, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnqueueTaskOperation(ctx, operation); err == nil {
		t.Fatal("cancelled frame admitted")
	}
}

func TestOutboxLeaseCannotReviveExpiredOwnership(t *testing.T) {
	store, operation := taskOperationFixture(t)
	ctx := context.Background()
	if _, err := store.EnqueueTaskOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "first", Topics: []string{TaskOperationOutboxTopic}, Limit: 1, Lease: time.Second})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v %v", claimed, err)
	}
	event := claimed[0]
	if err := store.RenewOutboxClaim(ctx, event.ID, event.ClaimToken, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE workspace_outbox SET lease_expires_at_ms=0 WHERE event_id=?`, event.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.BeginTaskOperation(ctx, event); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("expired claim admitted a side effect: %v", err)
	}
	if err := store.RenewOutboxClaim(ctx, event.ID, event.ClaimToken, time.Minute); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("expired claim revived: %v", err)
	}
	if err := store.SettleTaskOperation(ctx, event, map[string]any{"status": "completed"}); !errors.Is(err, ErrOutboxClaimLost) {
		t.Fatalf("expired owner published result: %v", err)
	}
}

func TestTaskOperationCancellationSurvivesFrameResume(t *testing.T) {
	store, operation := taskOperationFixture(t)
	ctx := context.Background()
	if _, err := store.EnqueueTaskOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "worker", Topics: []string{TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v %v", claimed, err)
	}
	if _, err := store.CancelCompatibilityFrameTree(operation.FrameID, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE frames SET status='processing' WHERE id=?`, operation.FrameID); err != nil {
		t.Fatal(err)
	}
	if cancelled, err := store.CheckTaskOperation(ctx, claimed[0]); err != nil || !cancelled {
		t.Fatalf("resume revived cancelled operation: %v %v", cancelled, err)
	}
}

func TestTaskOperationNotificationConflictRollsBackSettlement(t *testing.T) {
	store, operation := taskOperationFixture(t)
	ctx := context.Background()
	event, err := store.EnqueueTaskOperation(ctx, operation)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "worker", Topics: []string{TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v %v", claimed, err)
	}
	if _, _, err := store.CreateNotification(ctx, CreateNotificationInput{ID: operation.NotificationID, OwnerUserID: operation.OwnerID, SenderFrameID: operation.FrameID, RecipientFrameID: operation.FrameID, RootFrameID: operation.RootFrameID, NotificationType: "cell_result", Payload: map[string]any{"status": "completed"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleTaskOperation(ctx, claimed[0], map[string]any{"status": "failed"}); err == nil {
		t.Fatal("conflicting result replaced receipt")
	}
	current, err := store.GetOutboxEvent(ctx, event.ID)
	if err != nil || current.Status != OutboxStatusInflight {
		t.Fatalf("failed settlement consumed claim: %#v %v", current, err)
	}
}

func TestTaskOperationResultAtomicallyWakesOriginalResumeDispatch(t *testing.T) {
	store, operation := taskOperationFixture(t)
	ctx := context.Background()
	if _, err := store.EnqueueTaskOperation(ctx, operation); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimOutbox(ctx, ClaimOutboxInput{WorkerID: "worker", Topics: []string{TaskOperationOutboxTopic}, Limit: 1, Lease: time.Minute})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%#v %v", claimed, err)
	}
	raw, _ := json.Marshal(map[string]any{"dispatch": map[string]any{"status": "registered", "attempt": 0, "notBefore": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano), "waitingFor": "background_result"}})
	if _, err := store.db.Exec(`INSERT INTO frame_events(id,frame_id,sequence,event_type,payload,created_at)
		SELECT 'resume-install',?,COALESCE(MAX(sequence),0)+1,'frame_resumed',?,? FROM frame_events WHERE frame_id=?`, operation.FrameID, string(raw), time.Now().UTC(), operation.FrameID); err != nil {
		t.Fatal(err)
	}
	if err := store.SettleTaskOperation(ctx, claimed[0], map[string]any{"status": "completed"}); err != nil {
		t.Fatal(err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatch("resume-install")
	if err != nil || !found || !dispatch.NotBefore.IsZero() {
		t.Fatalf("result left original task parked: %#v %v", dispatch, err)
	}
}
