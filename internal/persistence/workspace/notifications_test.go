package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func openNotificationTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "owner-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "root-1", ProjectID: "project-1", AgentName: "GENERAL", Status: "processing", ConversationType: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{ID: "child-1", ProjectID: "project-1", ParentFrameID: "root-1", AgentName: "GENERAL", Status: "processing", ConversationType: "delegate"}); err != nil {
		t.Fatal(err)
	}
	return store, path
}

func TestNotificationsPersistConsumeFIFOAndIdempotency(t *testing.T) {
	store, path := openNotificationTestStore(t)
	ctx := context.Background()
	currentTime := time.Now().UTC()
	store.now = func() time.Time { return currentTime }
	for index := 1; index <= 2; index++ {
		input := CreateNotificationInput{
			ID: fmt.Sprintf("notification-%d", index), SenderFrameID: "child-1", RecipientFrameID: "root-1",
			RootFrameID: "root-1", OwnerUserID: "owner-1", NotificationType: "child_message",
			Payload: map[string]any{"ordinal": index},
		}
		first, _, err := store.CreateNotification(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		second, _, err := store.CreateNotification(ctx, input)
		if err != nil || first.ID != second.ID || !first.CreatedAt.Equal(second.CreatedAt) {
			t.Fatalf("idempotent notification first=%#v second=%#v err=%v", first, second, err)
		}
	}
	if _, _, err := store.CreateNotification(ctx, CreateNotificationInput{
		ID: "notification-1", SenderFrameID: "child-1", RecipientFrameID: "root-1",
		RootFrameID: "root-1", OwnerUserID: "owner-1", NotificationType: "child_message",
		Payload: map[string]any{"ordinal": 9},
	}); err == nil {
		t.Fatal("conflicting notification retry succeeded")
	}
	if count, err := store.CountUnreadNotifications(ctx, "root-1", "root-1", "owner-1"); err != nil || count != 2 {
		t.Fatalf("unread before reopen=%d err=%v", count, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for index := 1; index <= 2; index++ {
		items, err := store.ConsumeUnreadNotifications(ctx, "root-1", "root-1", "owner-1", 1)
		if err != nil || len(items) != 1 || items[0].ID != fmt.Sprintf("notification-%d", index) || items[0].ReadAt == nil {
			t.Fatalf("consume %d items=%#v err=%v", index, items, err)
		}
	}
	items, err := store.ConsumeUnreadNotifications(ctx, "root-1", "root-1", "owner-1", 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("final consume=%#v err=%v", items, err)
	}
	events, err := store.ListFrameEvents("root-1", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var notificationEvents int
	for _, event := range events {
		if event.Type == "notification" {
			notificationEvents++
		}
	}
	if notificationEvents != 2 {
		t.Fatalf("notification frame events=%d events=%#v", notificationEvents, events)
	}
}

func TestNotificationClaimRetryLeaseAndAck(t *testing.T) {
	store, _ := openNotificationTestStore(t)
	defer store.Close()
	ctx := context.Background()
	currentTime := time.Now().UTC()
	store.now = func() time.Time { return currentTime }
	for index := 1; index <= 2; index++ {
		_, _, err := store.CreateNotification(ctx, CreateNotificationInput{
			ID: fmt.Sprintf("claim-%d", index), SenderFrameID: "child-1", RecipientFrameID: "root-1",
			RootFrameID: "root-1", OwnerUserID: "owner-1", NotificationType: "child_message",
			Payload: map[string]any{"ordinal": index},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.ClaimUnreadNotifications(ctx, "root-1", "root-1", "owner-1", "tool-call-a", 10)
	if err != nil || len(first) != 2 || first[0].ID != "claim-1" || first[1].ID != "claim-2" || first[0].Sequence >= first[1].Sequence {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	retry, err := store.ClaimUnreadNotifications(ctx, "root-1", "root-1", "owner-1", "tool-call-a", 10)
	if err != nil || len(retry) != 2 || retry[0].ID != "claim-1" || retry[1].ID != "claim-2" {
		t.Fatalf("same-token retry=%#v err=%v", retry, err)
	}
	if _, _, err := store.CreateNotification(ctx, CreateNotificationInput{
		ID: "claim-3", SenderFrameID: "child-1", RecipientFrameID: "root-1",
		RootFrameID: "root-1", OwnerUserID: "owner-1", NotificationType: "child_message",
		Payload: map[string]any{"ordinal": 3},
	}); err != nil {
		t.Fatal(err)
	}
	retry, err = store.ClaimUnreadNotifications(ctx, "root-1", "root-1", "owner-1", "tool-call-a", 10)
	if err != nil || len(retry) != 2 || retry[0].ID != "claim-1" || retry[1].ID != "claim-2" {
		t.Fatalf("same-token manifest changed=%#v err=%v", retry, err)
	}
	other, err := store.ClaimUnreadNotifications(ctx, "root-1", "root-1", "owner-1", "tool-call-b", 10)
	if err != nil || len(other) != 0 {
		t.Fatalf("competing claim=%#v err=%v", other, err)
	}
	currentTime = currentTime.Add(notificationClaimLease + time.Second)
	reclaimed, err := store.ClaimUnreadNotifications(ctx, "root-1", "root-1", "owner-1", "tool-call-b", 10)
	if err != nil || len(reclaimed) != 3 || reclaimed[0].ID != "claim-1" || reclaimed[2].ID != "claim-3" {
		t.Fatalf("expired claim=%#v err=%v", reclaimed, err)
	}
	if _, err := store.AckClaimedNotifications(ctx, "root-1", "root-1", "owner-1", "tool-call-b", 2); err == nil {
		t.Fatal("acknowledgement accepted the wrong claim manifest")
	}
	acknowledged, err := store.AckClaimedNotifications(ctx, "root-1", "root-1", "owner-1", "tool-call-b", 3)
	if err != nil || acknowledged != 3 {
		t.Fatalf("acknowledged=%d err=%v", acknowledged, err)
	}
	if count, err := store.CountUnreadNotifications(ctx, "root-1", "root-1", "owner-1"); err != nil || count != 0 {
		t.Fatalf("unread after ack=%d err=%v", count, err)
	}
}

func TestCompatibilityAsideNotificationResolvesNestedMainAuthority(t *testing.T) {
	store, _ := openNotificationTestStore(t)
	defer store.Close()
	first, err := store.CreateCompatibilityAside(CompatibilityAsideInput{
		ID: "aside-1", ParentRootFrameID: "root-1", Request: "first aside",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateCompatibilityAside(CompatibilityAsideInput{
		ID: "aside-2", ParentRootFrameID: first.Frame.ID, Request: "nested aside",
	})
	if err != nil {
		t.Fatal(err)
	}
	notification, _, err := store.CreateCompatibilityAsideMainNotification(context.Background(), CreateNotificationInput{
		ID: "aside-main-notification", SenderFrameID: second.Frame.ID, OwnerUserID: "owner-1",
		NotificationType: "child_message", Payload: map[string]any{"text": "nested result", "kind": "info"},
	})
	if err != nil || notification.RecipientFrameID != "root-1" || notification.RootFrameID != "root-1" {
		t.Fatalf("nested aside notification=%#v err=%v", notification, err)
	}
	if _, _, err := store.CreateCompatibilityAsideMainNotification(context.Background(), CreateNotificationInput{
		ID: "aside-wrong-owner", SenderFrameID: second.Frame.ID, OwnerUserID: "owner-2",
		NotificationType: "child_message", Payload: map[string]any{"text": "denied"},
	}); err == nil {
		t.Fatal("cross-owner aside notification succeeded")
	}
	if err := store.DeleteFrame(second.Frame.ID); err != nil {
		t.Fatal(err)
	}
	items, err := store.ConsumeUnreadNotifications(context.Background(), "root-1", "root-1", "owner-1", 10)
	if err != nil || len(items) != 1 || items[0].ID != "aside-main-notification" {
		t.Fatalf("main notifications=%#v err=%v", items, err)
	}
}

func TestKernelChildLandingFlushAndCollectionAreDurable(t *testing.T) {
	store, _ := openNotificationTestStore(t)
	defer store.Close()
	children, err := store.CreateKernelSupervisedChildren(context.Background(), CreateKernelDelegatesInput{
		ParentFrameID: "root-1", OwnerUserID: "owner-1", ToolUseID: "landing-flush",
		Requests: []KernelDelegateRequest{{Task: "finish", Name: "reviewer"}},
	})
	if err != nil || len(children) != 1 {
		t.Fatalf("children=%#v err=%v", children, err)
	}
	child, err := store.CompleteKernelSupervisedChild(context.Background(), children[0].FrameID, "owner-1", "completed", map[string]any{"response": "done"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`DELETE FROM notifications WHERE sender_frame_id=? AND notification_type='child_landed'`, child.FrameID); err != nil {
		t.Fatal(err)
	}
	if count, err := store.CountUndeliveredKernelChildLandings(context.Background(), "root-1", "root-1", "owner-1"); err != nil || count != 1 {
		t.Fatalf("undelivered=%d err=%v", count, err)
	}
	if flushed, err := store.FlushUndeliveredKernelChildLandings(context.Background(), "root-1", "root-1", "owner-1"); err != nil || flushed != 1 {
		t.Fatalf("flushed=%d err=%v", flushed, err)
	}
	landings, err := store.ListKernelChildLandings(context.Background(), "root-1", "root-1", "owner-1", 100)
	if err != nil || len(landings) != 1 || landings[0].SenderFrameID != child.FrameID {
		t.Fatalf("landings=%#v err=%v", landings, err)
	}
	if err := store.CollectKernelChildLanding(context.Background(), "root-1", "root-1", "owner-1", child.FrameID); err != nil {
		t.Fatal(err)
	}
	if flushed, err := store.FlushUndeliveredKernelChildLandings(context.Background(), "root-1", "root-1", "owner-1"); err != nil || flushed != 0 {
		t.Fatalf("post-collect flushed=%d err=%v", flushed, err)
	}
}

func TestNotificationsConcurrentConsumersNeverDuplicate(t *testing.T) {
	store, _ := openNotificationTestStore(t)
	defer store.Close()
	ctx := context.Background()
	for index := 0; index < 32; index++ {
		_, _, err := store.CreateNotification(ctx, CreateNotificationInput{
			ID: fmt.Sprintf("concurrent-%02d", index), SenderFrameID: "child-1", RecipientFrameID: "root-1",
			RootFrameID: "root-1", OwnerUserID: "owner-1", NotificationType: "child_message",
			Payload: map[string]any{"ordinal": index},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var wait sync.WaitGroup
	results := make(chan []Notification, 8)
	errors := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			items, err := store.ConsumeUnreadNotifications(ctx, "root-1", "root-1", "owner-1", 4)
			results <- items
			errors <- err
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]bool{}
	for items := range results {
		for _, item := range items {
			if seen[item.ID] {
				t.Fatalf("notification %s delivered twice", item.ID)
			}
			seen[item.ID] = true
		}
	}
	if len(seen) != 32 {
		t.Fatalf("consumed notifications=%d", len(seen))
	}
}

func TestNotificationAuthorityAndAtomicBackgroundCompletion(t *testing.T) {
	store, _ := openNotificationTestStore(t)
	defer store.Close()
	ctx := context.Background()
	if _, _, err := store.CreateNotification(ctx, CreateNotificationInput{
		ID: "wrong-owner", SenderFrameID: "child-1", RecipientFrameID: "root-1",
		RootFrameID: "root-1", OwnerUserID: "owner-2", NotificationType: "child_message",
	}); err == nil {
		t.Fatal("wrong-owner notification succeeded")
	}
	access, found, err := store.GetKernelFrameAccess("root-1")
	if err != nil || !found {
		t.Fatalf("kernel access found=%t err=%v", found, err)
	}
	startedAt := time.Now().UTC().Add(-time.Second)
	execution := BackgroundKernelExecution{
		ExecID: "exec-1", ToolID: "tool-1", ToolName: "python", FrameID: "root-1", RootFrameID: "root-1",
		FrameIncarnationID: access.Frame.IncarnationID, RootFrameIncarnationID: access.RootFrameIncarnationID,
		StartedAt: startedAt,
	}
	startedEvent, err := store.RecordBackgroundKernelExecutionStarted(ctx, access, execution)
	if err != nil {
		t.Fatal(err)
	}
	recoveredExecution := execution
	recoveredExecution.StartedAt = recoveredExecution.StartedAt.Add(7 * time.Millisecond)
	recoveredEvent, err := store.RecordBackgroundKernelExecutionStarted(ctx, access, recoveredExecution)
	if err != nil || recoveredEvent.ID != startedEvent.ID || recoveredEvent.Sequence != startedEvent.Sequence {
		t.Fatalf("background start recovery was not idempotent: event=%#v err=%v", recoveredEvent, err)
	}
	logInput := SaveExecutionLogInput{
		Record:          ExecutionLogRecord{ID: "exec-1", FrameID: "root-1", CellIndex: 0, KernelID: "kernel-1", CondaEnv: "default", Language: "python", Source: "print(7)", Stdout: "7\n", ExitStatus: "ok", ExecutedAt: time.Now().UTC()},
		ExpectedOwnerID: "owner-1", ExpectedProjectID: "project-1",
		ExpectedFrameIncarnationID: access.Frame.IncarnationID, ExpectedRootFrameIncarnationID: access.RootFrameIncarnationID,
	}
	notificationInput := CreateNotificationInput{
		ID: "cell-result-1", SenderFrameID: "root-1", RecipientFrameID: "root-1", RootFrameID: "root-1",
		OwnerUserID: "owner-1", NotificationType: "cell_result",
		Payload: map[string]any{"exec_id": "exec-1", "tool_id": "tool-1", "status": "completed", "output": "7"},
	}
	_, notification, _, err := store.CompleteBackgroundKernelExecution(ctx, logInput, notificationInput)
	if err != nil || notification.ID != "cell-result-1" {
		t.Fatalf("background completion notification=%#v err=%v", notification, err)
	}
	_, retryNotification, _, err := store.CompleteBackgroundKernelExecution(ctx, logInput, notificationInput)
	if err != nil || retryNotification.ID != notification.ID || retryNotification.Sequence != notification.Sequence {
		t.Fatalf("background completion retry=%#v err=%v", retryNotification, err)
	}
	if event, marked, err := store.MarkBackgroundKernelExecutionLostIfPending(ctx, access, execution); err != nil || marked || event.ID != "" {
		t.Fatalf("completed execution marked lost event=%#v marked=%t err=%v", event, marked, err)
	}
	lostFirst := execution
	lostFirst.ExecID = "exec-lost-first"
	lostFirst.ToolID = "tool-lost-first"
	if _, err := store.RecordBackgroundKernelExecutionStarted(ctx, access, lostFirst); err != nil {
		t.Fatal(err)
	}
	if event, marked, err := store.MarkBackgroundKernelExecutionLostIfPending(ctx, access, lostFirst); err != nil || !marked || event.Type != "notification" {
		t.Fatalf("lost-first marked=%t err=%v", marked, err)
	}
	lostNotificationID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("kernel-background-lost-notification:"+lostFirst.FrameID+":"+lostFirst.ExecID)).String()
	var lostNotificationType, lostPayloadJSON string
	if err := store.db.QueryRow(`SELECT notification_type,payload_json FROM notifications WHERE id=?`, lostNotificationID).
		Scan(&lostNotificationType, &lostPayloadJSON); err != nil {
		t.Fatal(err)
	}
	var lostPayload map[string]any
	if err := json.Unmarshal([]byte(lostPayloadJSON), &lostPayload); err != nil {
		t.Fatal(err)
	}
	if lostNotificationType != "cell_result" || lostPayload["status"] != "interrupted" ||
		lostPayload["output"] != "[CANCELLED] This python cell was running when the session restarted; kernel state was lost." {
		t.Fatalf("lost notification type=%q payload=%#v", lostNotificationType, lostPayload)
	}
	lostLog := logInput
	lostLog.Record.ID = lostFirst.ExecID
	lostLog.Record.Source = "print('late')"
	if _, _, _, err := store.CompleteBackgroundKernelExecution(ctx, lostLog, CreateNotificationInput{
		ID: "cell-result-lost-first", SenderFrameID: "root-1", RecipientFrameID: "root-1", RootFrameID: "root-1",
		OwnerUserID: "owner-1", NotificationType: "cell_result",
		Payload: map[string]any{"exec_id": lostFirst.ExecID, "tool_id": lostFirst.ToolID, "status": "completed", "output": "late"},
	}); err == nil {
		t.Fatal("lost execution accepted a later completion")
	}
	var lostLogCount, lostNotificationCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_log WHERE id=?`, lostFirst.ExecID).Scan(&lostLogCount); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM notifications WHERE id='cell-result-lost-first'`).Scan(&lostNotificationCount); err != nil {
		t.Fatal(err)
	}
	if lostLogCount != 0 || lostNotificationCount != 0 {
		t.Fatalf("lost terminal conflict mutated log=%d notification=%d", lostLogCount, lostNotificationCount)
	}
	pending, err := store.ListPendingBackgroundKernelExecutions(ctx, access)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after completion=%#v err=%v", pending, err)
	}
	if _, _, _, err := store.CompleteBackgroundKernelExecution(ctx, SaveExecutionLogInput{
		Record: ExecutionLogRecord{ID: "exec-2", FrameID: "root-1", CellIndex: 1, KernelID: "kernel-1", CondaEnv: "default", Language: "python", Source: "print(8)", ExitStatus: "ok"},
	}, CreateNotificationInput{ID: "bad-result", SenderFrameID: "root-1", RecipientFrameID: "root-1", RootFrameID: "root-1", OwnerUserID: "wrong", NotificationType: "cell_result"}); err == nil {
		t.Fatal("invalid atomic completion succeeded")
	}
	var logCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM execution_log WHERE id='exec-2'`).Scan(&logCount); err != nil || logCount != 0 {
		t.Fatalf("rolled-back execution log count=%d err=%v", logCount, err)
	}
}

func TestNotificationsV28UpgradeReopenAndPollution(t *testing.T) {
	db, path := openTargetTwentyTwoProviderDB(t)
	applyTranscriptMigration(t, db, 27)
	applyTranscriptMigration(t, db, 28)
	applyTranscriptMigration(t, db, 28)
	assertSchemaJournalVersion(t, db, 28)
	for _, object := range notificationV28ObjectNames {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name=?`, object).Scan(&count); err != nil || count != 1 {
			t.Fatalf("v28 object %s count=%d err=%v", object, count, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	db, _ = openTargetTwentyTwoProviderDB(t)
	applyTranscriptMigration(t, db, 27)
	if _, err := db.Exec(`CREATE TABLE notifications(id TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(context.Background(), db, time.Now, 28); err == nil {
		t.Fatal("polluted notification cohort upgraded")
	}
	assertSchemaJournalVersion(t, db, 27)
	if err := validateMigrationObjects(context.Background(), db,
		transcriptstore.HistoryClassificationV27ObjectNames(), transcriptstore.HistoryClassificationV27Statements()); err != nil {
		t.Fatalf("v27 contract changed after failed v28 upgrade: %v", err)
	}
}
