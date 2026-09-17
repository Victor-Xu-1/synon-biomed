package workspace

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestActivateCompatibilityFrameRequestHasSingleConcurrentWinner(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Queue"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}

	const contenders = 12
	start := make(chan struct{})
	errors := make(chan error, contenders)
	var winners atomic.Int32
	var wait sync.WaitGroup
	wait.Add(contenders)
	for range contenders {
		go func() {
			defer wait.Done()
			<-start
			active, err := store.ActivateCompatibilityFrameRequest("root")
			if err != nil {
				errors <- err
				return
			}
			if active {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Errorf("ActivateCompatibilityFrameRequest() error = %v", err)
	}
	if winners.Load() != 1 {
		t.Fatalf("active winners = %d, want 1", winners.Load())
	}
	frame, found, err := store.GetFrame("root")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("activated frame = %#v found=%v err=%v", frame, found, err)
	}
}

func TestActivateCompatibilityFrameRequestDoesNotPreemptWaitingFrame(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "waiting-project", UserID: "local", Name: "Waiting"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"awaiting_user_response", "awaiting_plan_approval"} {
		frameID := "waiting-" + status
		if _, err := store.CreateFrame(CreateFrameInput{
			ID: frameID, ProjectID: "waiting-project", AgentName: "OPERON", Status: status, ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
		activated, err := store.ActivateCompatibilityFrameRequest(frameID)
		if err != nil || activated {
			t.Fatalf("waiting status=%q activated=%t err=%v", status, activated, err)
		}
		frame, found, err := store.GetFrame(frameID)
		if err != nil || !found || frame.Status != status {
			t.Fatalf("waiting frame=%#v found=%v err=%v", frame, found, err)
		}
	}
}

func TestCompatibilityMessageQueueClaimsOnlyOldestMessagePerRunnerTurn(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createCompatibilityQueueFixtures(t, store)
	for index, intentID := range []string{"serial-1", "serial-2"} {
		delivery := map[string]any{"messageUuid": intentID, "text": intentID, "role": "user"}
		if _, _, _, err := store.QueueCompatibilityMessage(
			"root", intentID, map[string]any{"text": intentID, "ordinal": index}, delivery,
		); err != nil {
			t.Fatal(err)
		}
	}
	claimed, err := store.ClaimCompatibilityQueuedMessages("root")
	if err != nil || len(claimed) != 1 || claimed[0].IntentID != "serial-1" {
		t.Fatalf("first serial claim=%#v err=%v", claimed, err)
	}
	second, found, err := store.GetCompatibilityMessageIntent("serial-2")
	if err != nil || !found || second.State != "queued" {
		t.Fatalf("second intent after first claim=%#v found=%v err=%v", second, found, err)
	}
	if err := store.CompleteCompatibilityQueuedMessageDelivery("serial-1"); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimCompatibilityQueuedMessages("root")
	if err != nil || len(claimed) != 1 || claimed[0].IntentID != "serial-2" {
		t.Fatalf("second serial claim=%#v err=%v", claimed, err)
	}
}

func TestCompatibilityFrameActivationClearsOnlyTheWinningAttemptDescription(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "activation-project", UserID: "local", Name: "Activation"}); err != nil {
		t.Fatal(err)
	}
	for _, frameID := range []string{"idle-activation", "completed-activation", "not-activated"} {
		status := "completed"
		if frameID == "not-activated" {
			status = "processing"
		}
		if _, err := store.CreateFrame(CreateFrameInput{
			ID: frameID, ProjectID: "activation-project", AgentName: "OPERON", Status: status, ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`INSERT INTO frame_runtime_metadata(frame_id,context_data,status_description,completed_at)
			VALUES(?, '{}', 'previous attempt failed', ?)`, frameID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}

	activated, err := store.ActivateCompatibilityFrameRequest("idle-activation")
	if err != nil || !activated {
		t.Fatalf("activate idle frame activated=%t err=%v", activated, err)
	}
	assertResumeDescription(t, store, "idle-activation", "processing", "")
	assertCompatibilityFrameCompletionCleared(t, store, "idle-activation", true)

	activated, err = store.ActivateCompletedCompatibilityFrameRequest("completed-activation")
	if err != nil || !activated {
		t.Fatalf("activate completed frame activated=%t err=%v", activated, err)
	}
	assertResumeDescription(t, store, "completed-activation", "processing", "")
	assertCompatibilityFrameCompletionCleared(t, store, "completed-activation", true)

	activated, err = store.ActivateCompletedCompatibilityFrameRequest("not-activated")
	if err != nil || activated {
		t.Fatalf("activate processing frame activated=%t err=%v", activated, err)
	}
	assertResumeDescription(t, store, "not-activated", "processing", "previous attempt failed")
	assertCompatibilityFrameCompletionCleared(t, store, "not-activated", false)
}

func assertCompatibilityFrameCompletionCleared(t *testing.T, store *Store, frameID string, wantCleared bool) {
	t.Helper()
	var completedAtIsNull int
	if err := store.db.QueryRow(`SELECT completed_at IS NULL FROM frame_runtime_metadata WHERE frame_id=?`, frameID).
		Scan(&completedAtIsNull); err != nil {
		t.Fatal(err)
	}
	if (completedAtIsNull == 1) != wantCleared {
		t.Fatalf("frame %q completed_at cleared=%t want=%t", frameID, completedAtIsNull == 1, wantCleared)
	}
}

func TestCompatibilityMessageQueuePersistsIntentLifecycleAndRecoversDelivery(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	createCompatibilityQueueFixtures(t, store)
	dedupe := map[string]any{"text": "queued", "plan_mode": nil, "ultra_mode": nil, "control": nil}
	delivery := map[string]any{"messageUuid": "intent-1", "text": "queued", "role": "user"}
	record, event, idempotent, err := store.QueueCompatibilityMessage("root", "intent-1", dedupe, delivery)
	if err != nil || idempotent || record.State != "queued" || event.Type != "queued_message" {
		t.Fatalf("queue record=%#v event=%#v idempotent=%v err=%v", record, event, idempotent, err)
	}
	duplicate, _, idempotent, err := store.QueueCompatibilityMessage("root", "intent-1", dedupe, delivery)
	if err != nil || !idempotent || duplicate.Sequence != record.Sequence || duplicate.State != "queued" {
		t.Fatalf("duplicate record=%#v idempotent=%v err=%v", duplicate, idempotent, err)
	}
	_, _, _, err = store.QueueCompatibilityMessage("root", "intent-1", map[string]any{"text": "edited"}, delivery)
	if !errors.Is(err, ErrCompatibilityIntentPayloadMismatch) {
		t.Fatalf("payload mismatch error = %v", err)
	}
	_, _, _, err = store.QueueCompatibilityMessage("other", "intent-1", dedupe, delivery)
	if !errors.Is(err, ErrCompatibilityIntentUsed) {
		t.Fatalf("cross-frame intent error = %v", err)
	}

	claimed, err := store.ClaimCompatibilityQueuedMessages("root")
	if err != nil || len(claimed) != 1 || claimed[0].State != "delivering" {
		t.Fatalf("claimed=%#v err=%v", claimed, err)
	}
	_, err = store.RetractCompatibilityQueuedMessage("root", "intent-1")
	if !errors.Is(err, ErrCompatibilityQueuedMessageTooLate) {
		t.Fatalf("retract delivering error = %v", err)
	}
	if err := store.ReleaseCompatibilityQueuedMessageDelivery("intent-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetractCompatibilityQueuedMessage("root", "intent-1"); err != nil {
		t.Fatal(err)
	}
	retracted, found, err := store.GetCompatibilityMessageIntent("intent-1")
	if err != nil || !found || retracted.State != "retracted" {
		t.Fatalf("retracted=%#v found=%v err=%v", retracted, found, err)
	}
	if _, err := store.RetractCompatibilityQueuedMessage("root", "intent-1"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("repeated retraction error = %v", err)
	}

	delivery2 := map[string]any{"messageUuid": "intent-2", "text": "recover", "role": "user"}
	if _, _, _, err := store.QueueCompatibilityMessage("root", "intent-2", map[string]any{"text": "recover"}, delivery2); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimCompatibilityQueuedMessages("root")
	if err != nil || len(claimed) != 1 || claimed[0].IntentID != "intent-2" {
		t.Fatalf("recovery claim=%#v err=%v", claimed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	recovered, found, err := store.GetCompatibilityMessageIntent("intent-2")
	if err != nil || !found || recovered.State != "queued" {
		t.Fatalf("recovered=%#v found=%v err=%v", recovered, found, err)
	}
	claimed, err = store.ClaimCompatibilityQueuedMessages("root")
	if err != nil || len(claimed) != 1 {
		t.Fatalf("reclaimed=%#v err=%v", claimed, err)
	}
	if err := store.CompleteCompatibilityQueuedMessageDelivery("intent-2"); err != nil {
		t.Fatal(err)
	}
	drained, found, err := store.GetCompatibilityMessageIntent("intent-2")
	if err != nil || !found || drained.State != "drained" {
		t.Fatalf("drained=%#v found=%v err=%v", drained, found, err)
	}
}

func TestCompatibilityMessageQueueEnforcesV11Capacity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createCompatibilityQueueFixtures(t, store)
	for index := 0; index < CompatibilityMessageQueueLimit; index++ {
		intentID := fmt.Sprintf("intent-%02d", index)
		if _, _, _, err := store.QueueCompatibilityMessage(
			"root", intentID, map[string]any{"text": intentID},
			map[string]any{"messageUuid": intentID, "text": intentID, "role": "user"},
		); err != nil {
			t.Fatalf("queue %d: %v", index, err)
		}
	}
	_, _, _, err = store.QueueCompatibilityMessage(
		"root", "overflow", map[string]any{"text": "overflow"},
		map[string]any{"messageUuid": "overflow", "text": "overflow", "role": "user"},
	)
	if !errors.Is(err, ErrCompatibilityMessageQueueFull) {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestCompatibilityMessageQueueConsumesImportedV11RowsWithoutParallelSchema(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	createCompatibilityQueueFixtures(t, store)
	if _, err := store.db.Exec(`
		INSERT INTO queued_user_messages (
			sequence, frame_id, payload, intent_id, state, resolved_at, created_at
		) VALUES (91, 'root', '{"text":"Migrated v1.1 message","plan_mode":true}',
			'intent-v11', 'queued', NULL, ?)`, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.GetCompatibilityMessageIntent("intent-v11")
	if err != nil || !found || record.State != "queued" || record.DedupePayload["text"] != "Migrated v1.1 message" || record.DedupePayload["plan_mode"] != true {
		t.Fatalf("imported record=%#v found=%v err=%v", record, found, err)
	}
	if record.DeliveryPayload["messageUuid"] != "intent-v11" || record.DeliveryPayload["role"] != "user" {
		t.Fatalf("imported delivery payload=%#v", record.DeliveryPayload)
	}
	claimed, err := store.ClaimCompatibilityQueuedMessages("root")
	if err != nil || len(claimed) != 1 || claimed[0].IntentID != "intent-v11" {
		t.Fatalf("imported claim=%#v err=%v", claimed, err)
	}
	if err := store.ReleaseCompatibilityQueuedMessageDelivery("intent-v11"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetractCompatibilityQueuedMessage("root", "intent-v11"); err != nil {
		t.Fatal(err)
	}
}

func createCompatibilityQueueFixtures(t *testing.T, store *Store) {
	t.Helper()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "local", Name: "Queue"}); err != nil {
		t.Fatal(err)
	}
	for _, frameID := range []string{"root", "other"} {
		if _, err := store.CreateFrame(CreateFrameInput{
			ID: frameID, ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
	}
}
