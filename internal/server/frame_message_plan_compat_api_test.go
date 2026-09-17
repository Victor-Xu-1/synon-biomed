package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityFrameMessageQueuesPersistsControlsAndRecoversAcrossRestart(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	direct := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/message", "local", map[string]any{
		"input_data": map[string]any{
			"request": "First continuation.", "evidence": "preserved",
			"_model": "forged", "_intent_id": "forged",
		},
		"model": "runner-model", "effort": "high", "thinking": false,
	}, http.StatusOK)
	if direct["status"] != "accepted" {
		t.Fatalf("direct response = %#v", direct)
	}
	frame, found, err := store.GetFrame("frame")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("activated frame = %#v, found=%t, err=%v", frame, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame")
	if err != nil || !found || metadata.ContextData["_is_manual_continuation"] != true ||
		metadata.ContextData["_model"] != "runner-model" || metadata.ContextData["_effort"] != "high" ||
		metadata.ContextData["_thinking"] != false {
		t.Fatalf("continuation metadata = %#v, found=%t, err=%v", metadata, found, err)
	}
	session, found, err := server.sessionStore.Get("frame")
	if err != nil || !found || session.LastRole != "user" || session.MessageCount != 1 {
		t.Fatalf("direct runner session = %#v, found=%t, err=%v", session, found, err)
	}
	inputData := session.Orchestration["inputData"].(map[string]any)
	if inputData["evidence"] != "preserved" || inputData["_model"] != nil || inputData["_intent_id"] != nil {
		t.Fatalf("sanitized runner input = %#v", inputData)
	}
	config := session.Orchestration["sessionConfig"].(map[string]any)
	if config["model"] != "runner-model" || config["effort"] != "high" || config["thinking_enabled"] != false {
		t.Fatalf("runner controls = %#v", config)
	}

	queued := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/message", "local", map[string]any{
		"input_data": map[string]any{"request": "Queued continuation.", "trace": "durable"},
		"model":      "queued-model", "thinking": true,
	}, http.StatusOK)
	if queued["status"] != "message_queued" {
		t.Fatalf("queued response = %#v", queued)
	}
	queuedFrames, err := store.CompatibilityQueuedFrameIDs()
	if err != nil || len(queuedFrames) != 1 || queuedFrames[0] != "frame" {
		t.Fatalf("queued frames = %#v, err=%v", queuedFrames, err)
	}

	restarted := New(Options{FileRoot: root, Workspace: store})
	if recovered, err := restarted.recoverCompatibilityQueuedMessages(); err != nil || recovered != 0 {
		t.Fatalf("active runner recovery = %d, err=%v", recovered, err)
	}
	delivered, err := restarted.advanceCompatibilityFrameAfterRunner("frame", "completed")
	if err != nil || delivered != 1 {
		t.Fatalf("queued delivery = %d, err=%v", delivered, err)
	}
	session, found, err = restarted.sessionStore.Get("frame")
	if err != nil || !found || session.MessageCount != 2 || session.LastRole != "user" {
		t.Fatalf("delivered session = %#v, found=%t, err=%v", session, found, err)
	}
	inputData = session.Orchestration["inputData"].(map[string]any)
	config = session.Orchestration["sessionConfig"].(map[string]any)
	if inputData["trace"] != "durable" || config["model"] != "queued-model" || config["thinking_enabled"] != true {
		t.Fatalf("delivered runtime = input:%#v config:%#v", inputData, config)
	}
	if delivered, err = restarted.advanceCompatibilityFrameAfterRunner("frame", "completed"); err != nil || delivered != 0 {
		t.Fatalf("terminal advance = %d, err=%v", delivered, err)
	}
	frame, _, _ = store.GetFrame("frame")
	if frame.Status != "completed" {
		t.Fatalf("terminal frame = %#v", frame)
	}
}

func TestTranscriptCompatibilityFrameMessageDoesNotWriteRuntimeMetadata(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-message", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-message", ProjectID: "project-message", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Frame",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame-message", workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"preserved": true, "_model": "stale-legacy-model",
	}}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	response := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-message/message", "local", map[string]any{
		"input_data": map[string]any{"request": "Use canonical controls."},
		"model":      "transcript-model", "effort": "high", "thinking": true,
	}, http.StatusOK)
	if response["status"] != "accepted" {
		t.Fatalf("response=%#v", response)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-message")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	config, found, err := repo.LatestFrameRuntimeConfig(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || config["model"] != "transcript-model" || config["effort"] != "high" || config["thinking_enabled"] != true {
		t.Fatalf("config=%#v found=%t err=%v", config, found, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-message")
	if err != nil || !found || metadata.ContextData["preserved"] != true || metadata.ContextData["_model"] != "stale-legacy-model" ||
		metadata.ContextData["_is_manual_continuation"] != nil || metadata.ContextData["_effort"] != nil || metadata.ContextData["_thinking"] != nil {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	if legacy, found, err := server.sessionStore.Get("frame-message"); err != nil || found {
		t.Fatalf("legacy session=%#v found=%t err=%v", legacy, found, err)
	}
}

func TestTranscriptQueuedDeliveryFailureDoesNotReopenTerminalFrame(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-queue-failure", "frame-queue-failure")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-queue-failure", OwnerID: "local", ExternalID: "frame-queue-failure",
		SessionID: "frame-queue-failure", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-queue-failure", RootFrameID: "frame-queue-failure", FrameID: "frame-queue-failure", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "first", FrameEventID: "first-event",
		MessageUUID: "first-message", Text: "Finish before queued delivery.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("input created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "first-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	if _, _, _, err := store.QueueCompatibilityMessage(
		"frame-queue-failure", "queued-failure", map[string]any{"request": "queued"},
		map[string]any{
			"text": "Queued task", "inputData": map[string]any{"request": "Queued task"},
			"sessionConfig": "invalid",
		},
	); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if delivered, err := server.advanceCompatibilityFrameAfterRunner("frame-queue-failure", "completed"); err == nil || delivered != 0 {
		t.Fatalf("delivered=%d err=%v", delivered, err)
	}
	frame, found, err := store.GetFrame("frame-queue-failure")
	if err != nil || !found || frame.Status != workspace.FrameStatusCompleted {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	intent, found, err := store.GetCompatibilityMessageIntent("queued-failure")
	if err != nil || !found || intent.State != "queued" {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
}

func TestTranscriptQueuedDeliveryReopensFromCommittedInput(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-queue-success", "frame-queue-success")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-queue-success", OwnerID: "local", ExternalID: "frame-queue-success",
		SessionID: "frame-queue-success", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-queue-success", RootFrameID: "frame-queue-success", FrameID: "frame-queue-success", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "first", FrameEventID: "first-event",
		MessageUUID: "first-message", Text: "Finish before queued delivery.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("input created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "first-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("finish created=%t err=%v", created, err)
	}
	if _, _, _, err := store.QueueCompatibilityMessage(
		"frame-queue-success", "queued-success", map[string]any{"request": "queued"},
		map[string]any{
			"text": "Run queued task", "inputData": map[string]any{"request": "Run queued task"},
			"sessionConfig": map[string]any{"model": "queued-model"},
		},
	); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if err := server.sessionStore.Upsert(sessionstore.Session{ID: "frame-queue-success", Title: "legacy projection"}); err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := server.sessionStore.ClaimRunner("frame-queue-success", "stale-legacy-runner", time.Minute); err != nil || !claimed {
		t.Fatalf("legacy claim=%t err=%v", claimed, err)
	}
	if recovered, err := server.recoverCompatibilityQueuedMessages(); err != nil || recovered != 1 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	frame, found, err := store.GetFrame("frame-queue-success")
	if err != nil || !found || frame.Status != workspace.FrameStatusProcessing {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	intent, found, err := store.GetCompatibilityMessageIntent("queued-success")
	if err != nil || !found || intent.State != "drained" {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	taskIntent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || taskIntent.Revision != 2 || taskIntent.Text != "Run queued task" {
		t.Fatalf("taskIntent=%#v found=%t err=%v", taskIntent, found, err)
	}
	runtimeConfig, found, err := repo.LatestFrameRuntimeConfig(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || runtimeConfig["model"] != "queued-model" {
		t.Fatalf("runtime config=%#v found=%t err=%v", runtimeConfig, found, err)
	}
	legacy, found, err := server.sessionStore.Get("frame-queue-success")
	if err != nil || !found || legacy.Orchestration["sessionConfig"] != nil {
		t.Fatalf("legacy session=%#v found=%t err=%v", legacy, found, err)
	}
	entries, err := server.eventJournal.ReadAfter("frame-queue-success", 0, 20)
	if err != nil || len(entries) != 0 {
		t.Fatalf("legacy journal entries=%#v err=%v", entries, err)
	}
}

func TestTranscriptQueuedCompletionFailureReleasesIntentForIdempotentRetry(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-queue-retry", "frame-queue-retry")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-queue-retry", OwnerID: "local", ExternalID: "frame-queue-retry",
		SessionID: "frame-queue-retry", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-queue-retry", RootFrameID: "frame-queue-retry", FrameID: "frame-queue-retry", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "initial", FrameEventID: "initial-event",
		MessageUUID: "initial-message", Text: "Initial task", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("initial input created=%t err=%v", created, err)
	}
	first, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-first", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "initial-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("initial finish created=%t err=%v", created, err)
	}
	if _, _, _, err := store.QueueCompatibilityMessage(
		"frame-queue-retry", "queued-retry", map[string]any{"request": "queued"},
		map[string]any{"text": "Retry task", "inputData": map[string]any{"request": "Retry task"}},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_queue_drain BEFORE UPDATE OF state ON queued_user_messages
		WHEN OLD.intent_id='queued-retry' AND NEW.state='drained'
		BEGIN SELECT RAISE(FAIL, 'injected queue drain failure'); END`); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if delivered, err := server.advanceCompatibilityFrameAfterRunner("frame-queue-retry", "completed"); err == nil || delivered != 0 {
		t.Fatalf("first delivery=%d err=%v", delivered, err)
	}
	intent, found, err := store.GetCompatibilityMessageIntent("queued-retry")
	if err != nil || !found || intent.State != "queued" {
		t.Fatalf("released intent=%#v found=%t err=%v", intent, found, err)
	}
	taskIntent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || taskIntent.Revision != 2 || taskIntent.Text != "Retry task" {
		t.Fatalf("taskIntent=%#v found=%t err=%v", taskIntent, found, err)
	}
	second, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-second", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !second.Claimed {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: second.Claim, ClientMessageID: "retry-finish", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("retry finish created=%t err=%v", created, err)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_queue_drain`); err != nil {
		t.Fatal(err)
	}
	if delivered, err := server.advanceCompatibilityFrameAfterRunner("frame-queue-retry", "completed"); err != nil || delivered != 1 {
		t.Fatalf("retry delivery=%d err=%v", delivered, err)
	}
	intent, found, err = store.GetCompatibilityMessageIntent("queued-retry")
	if err != nil || !found || intent.State != "drained" {
		t.Fatalf("drained intent=%#v found=%t err=%v", intent, found, err)
	}
	taskIntent, found, err = repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || taskIntent.Revision != 2 || taskIntent.Text != "Retry task" {
		t.Fatalf("retried taskIntent=%#v found=%t err=%v", taskIntent, found, err)
	}
}

func TestTranscriptQueueRecoveryDoesNotDeliverBeforeRunnerTerminal(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-queue-live", "frame-queue-live")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-queue-live", OwnerID: "local", ExternalID: "frame-queue-live",
		SessionID: "frame-queue-live", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-queue-live", RootFrameID: "frame-queue-live", FrameID: "frame-queue-live", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "active", FrameEventID: "active-event",
		MessageUUID: "active-message", Text: "Active task", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("input created=%t err=%v", created, err)
	}
	if claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "active-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	}); err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, _, _, err := store.QueueCompatibilityMessage(
		"frame-queue-live", "queued-live", map[string]any{"request": "queued"},
		map[string]any{"text": "Wait for active task", "inputData": map[string]any{"request": "Wait for active task"}},
	); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if recovered, err := server.recoverCompatibilityQueuedMessages(); err != nil || recovered != 0 {
		t.Fatalf("recovered=%d err=%v", recovered, err)
	}
	intent, found, err := store.GetCompatibilityMessageIntent("queued-live")
	if err != nil || !found || intent.State != "queued" {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	activeIntent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || activeIntent.Revision != 1 || activeIntent.Text != "Active task" {
		t.Fatalf("activeIntent=%#v found=%t err=%v", activeIntent, found, err)
	}
}

func TestCompatibilityFrameMessageEnforcesV11AddressableStatesAndGoalRules(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON",
		Status: "awaiting_plan_approval", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	body := map[string]any{"input_data": map[string]any{"request": "blocked"}}
	textDecision := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/message", "local", body, http.StatusOK)
	if textDecision["status"] != "accepted" {
		t.Fatalf("plan text decision = %#v", textDecision)
	}
	for _, check := range []struct {
		status string
		code   int
		detail string
	}{
		{"awaiting_user_response", http.StatusConflict, "is awaiting input"},
		{"cancelled", http.StatusConflict, "resume it from its parent"},
		{"failed", http.StatusConflict, "resume it from its parent"},
		{"success", http.StatusBadRequest, "Only 'completed' frames"},
	} {
		status := check.status
		if _, err := store.UpdateFrame("frame", workspace.UpdateFrameInput{Status: &status}); err != nil {
			t.Fatal(err)
		}
		response := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/message", "local", body, check.code)
		if detail := stringValue(response["detail"]); !stringsContains(detail, check.detail) {
			t.Fatalf("status %s detail = %q", check.status, detail)
		}
	}
	completed := "completed"
	if _, err := store.UpdateFrame("frame", workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	response := compatJSONRequest(t, app, http.MethodPost, "/api/frames/frame/message", "local", map[string]any{
		"input_data": map[string]any{"request": "goal", "goal_text": "forbidden"},
	}, http.StatusBadRequest)
	if stringValue(response["detail"]) != "goal_text is not available in this build" {
		t.Fatalf("goal detail = %#v", response)
	}
}

func TestCompatibilityApproveAndDiscardPlanPersistStateAndRunnerSemantics(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"approve", "discard"} {
		if _, err := store.CreateFrame(workspace.CreateFrameInput{
			ID: id, ProjectID: "project", AgentName: "OPERON",
			Status: "awaiting_plan_approval", ConversationType: "agent",
		}); err != nil {
			t.Fatal(err)
		}
		_, planVersion, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
			ArtifactID: id + "-artifact", ProjectID: "project", Name: "plan.json",
			ContentType: "application/json", Content: strings.NewReader(`{"title":"Original"}`),
			MaxBytes: 1 << 20, RootFrameID: id, FrameID: id,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetFrameRuntimeMetadata(id, workspace.FrameRuntimeMetadata{
			ContextData: map[string]any{
				"_plan_artifact_id": id + "-artifact", "_plan_version_id": planVersion.ID,
				"_plan_json": map[string]any{"title": "Original"}, "_plan_steps": []any{"one"},
				"_step_statuses": map[string]any{"one": "pending"},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	approved := compatJSONRequest(t, app, http.MethodPost, "/api/frames/approve/approve-plan", "local", map[string]any{
		"edited_plan":   map[string]any{"title": "Edited", "steps": []any{"verified"}},
		"verifier_mode": "on", "memory_mode": "off",
	}, http.StatusOK)
	if approved["status"] != "accepted" {
		t.Fatalf("approve response = %#v", approved)
	}
	approveFrame, _, _ := store.GetFrame("approve")
	if approveFrame.Status != "processing" {
		t.Fatalf("approved frame = %#v", approveFrame)
	}
	approveMetadata, _, err := store.GetFrameRuntimeMetadata("approve")
	if err != nil || approveMetadata.ContextData["_plan_approved"] != true ||
		approveMetadata.ContextData["_plan_steps"] != nil {
		t.Fatalf("approved metadata = %#v, err=%v", approveMetadata, err)
	}
	edited := approveMetadata.ContextData["_plan_json"].(map[string]any)
	if edited["title"] != "Edited" {
		t.Fatalf("edited plan = %#v", edited)
	}
	approveSession, found, err := server.sessionStore.Get("approve")
	_, history, found, err := store.ListArtifactVersionHistory("approve-artifact")
	if err != nil || !found || len(history) != 2 ||
		history[1].ParentVersionID == nil || *history[1].ParentVersionID != history[0].VersionID ||
		approveMetadata.ContextData["_plan_version_id"] != history[1].VersionID {
		t.Fatalf("edited plan history = %#v, metadata=%#v, found=%t, err=%v", history, approveMetadata, found, err)
	}
	if err != nil || !found || approveSession.LastRole != "user" {
		t.Fatalf("approve session = %#v, found=%t, err=%v", approveSession, found, err)
	}
	duplicate := compatJSONRequest(t, app, http.MethodPost, "/api/frames/approve/approve-plan", "local", map[string]any{}, http.StatusBadRequest)
	if stringValue(duplicate["detail"]) != "This plan has already been approved." ||
		stringValue(duplicate["code"]) != "plan_already_approved" {
		t.Fatalf("duplicate approval = %#v", duplicate)
	}

	discarded := compatJSONRequest(t, app, http.MethodPost, "/api/frames/discard/discard-plan", "local", map[string]any{}, http.StatusOK)
	if discarded["status"] != "completed" {
		t.Fatalf("discard response = %#v", discarded)
	}
	discardFrame, _, _ := store.GetFrame("discard")
	discardMetadata, _, err := store.GetFrameRuntimeMetadata("discard")
	if err != nil || discardFrame.Status != "completed" || discardMetadata.ContextData["_plan_artifact_id"] != nil ||
		discardMetadata.ContextData["_plan_json"] != nil || discardMetadata.ContextData["_step_statuses"] != nil {
		t.Fatalf("discarded frame = %#v metadata=%#v err=%v", discardFrame, discardMetadata, err)
	}
	discardSession, found, err := server.sessionStore.Get("discard")
	if err != nil || !found || discardSession.LastRole != "system" || discardSession.MessageCount != 1 {
		t.Fatalf("discard session = %#v, found=%t, err=%v", discardSession, found, err)
	}
	restarted := New(Options{FileRoot: root, Workspace: store})
	discardSession, found, err = restarted.sessionStore.Get("discard")
	if err != nil || !found || discardSession.LastRole != "system" {
		t.Fatalf("restarted discard session = %#v, found=%t, err=%v", discardSession, found, err)
	}
}

func TestCompatibilityApprovePlanPreservesCanonicalTaskIntent(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame", OwnerID: "local", ExternalID: "frame", SessionID: "frame",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task", FrameEventID: "task-event",
		MessageUUID: "task-message", Text: "Build the CRBN analysis.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "planning-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	intentBeforePause, found, err := repo.EnsureActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("task intent before pause=%#v found=%t err=%v", intentBeforePause, found, err)
	}
	memorySnapshot, err := transcriptstore.SealRunnerTaskMemorySnapshot(transcriptstore.RunnerTaskMemorySnapshot{
		TaskIntentID: intentBeforePause.ID, TaskIntentRevision: intentBeforePause.Revision,
		InitialInputRevision: claimed.Claim.ClaimedInputRevision, PolicyVersion: runnerTaskMemoryPolicyVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	memorySnapshotJSON, err := json.Marshal(memorySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "plan-task-memory-snapshot-v1", Phase: transcriptstore.RunnerPhasePlanning,
		Resumable: true, PayloadJSON: memorySnapshotJSON,
	}); err != nil || !created {
		t.Fatalf("memory snapshot created=%t err=%v", created, err)
	}
	if _, _, created, err := repo.PauseRunnerForApproval(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "plan-paused", Phase: transcriptstore.RunnerPhaseWaitingApproval,
		Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_plan_approval","detail":"review plan"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("pause created=%t err=%v", created, err)
	}
	planFixture := func(summary string) map[string]any {
		return map[string]any{
			"version": float64(generatePlanSchemaVersion), "task_summary": summary,
			"phases": []any{map[string]any{
				"id": "phase-1", "name": "Execution", "delegations": []any{map[string]any{
					"id": "phase-1-delegation-1", "name": "Primary", "steps": []any{map[string]any{
						"id": "phase-1-delegation-1-step-1", "title": "Execute CRBN analysis", "description": "Run the approved CRBN analysis.",
					}},
				}},
			}},
			"desired_outputs": []any{"CRBN analysis result"},
			"feasibility":     map[string]any{"confidence": "high", "rationale": "The test provider is available."},
		}
	}
	basePlan := planFixture("Build the CRBN analysis.")
	basePlanJSON, err := json.Marshal(basePlan)
	if err != nil {
		t.Fatal(err)
	}
	_, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: "plan-artifact", ProjectID: "project", Name: "plan.json", ContentType: "application/json",
		Content: strings.NewReader(string(basePlanJSON)), MaxBytes: 1 << 20, RootFrameID: "frame", FrameID: "frame",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame", workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_plan_artifact_id": "plan-artifact", "_plan_version_id": version.ID, "_plan_json": basePlan,
	}}); err != nil {
		t.Fatal(err)
	}
	status := "awaiting_plan_approval"
	if _, err := store.UpdateFrame("frame", workspace.UpdateFrameInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		messages, _ := json.Marshal(payload["messages"])
		if !strings.Contains(string(messages), "Build the CRBN analysis") || !strings.Contains(string(messages), "approved the proposed plan") {
			t.Fatalf("messages=%s", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		callNumber := providerCalls.Load()
		if callNumber == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"plan-step-complete","type":"function","function":{"name":"update_step_status","arguments":"{\"step\":\"Execute CRBN analysis\",\"status\":\"completed\"}"}}]}}]}`))
			return
		}
		progress, found, err := store.GetFrameRuntimeMetadata("frame")
		step := mapValue(mapValue(progress.ContextData["_step_statuses"])["phase-1-delegation-1-step-1"])
		expectedStatus := "completed"
		if callNumber == 2 {
			// Approval authorizes execution, not completion. The first premature
			// completion request must only start the approved step; the provider
			// must observe that durable receipt before reporting completion again.
			expectedStatus = "in_progress"
		}
		if err != nil || !found || step["status"] != expectedStatus {
			t.Errorf("approved plan progress: call=%d step=%#v want=%s err=%v", callNumber, step, expectedStatus, err)
		}
		modelMessages := anySliceValue(payload["messages"])
		lastMessage := mapValue(modelMessages[len(modelMessages)-1])
		var receipt map[string]any
		if lastMessage["role"] != "tool" || json.Unmarshal([]byte(stringValue(lastMessage["content"])), &receipt) != nil ||
			receipt["status"] != expectedStatus || receipt["plan_version_id"] != progress.ContextData["_plan_version_id"] {
			t.Errorf("provider did not receive the approved plan progress receipt: %#v", lastMessage)
		}
		if callNumber == 2 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"plan-step-terminal","type":"function","function":{"name":"update_step_status","arguments":"{\"step\":\"Execute CRBN analysis\",\"status\":\"completed\"}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Plan executed."}}]}`))
	}))
	defer provider.Close()
	server := New(Options{FileRoot: root, Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	approvalRequest := map[string]any{
		"ultra_mode": true, "edited_plan": planFixture("Execute the approved CRBN analysis."),
	}
	response := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame/approve-plan", "local", approvalRequest,
		http.StatusOK)
	if response["status"] != "accepted" {
		t.Fatalf("response=%#v", response)
	}
	replayed := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame/approve-plan", "local", approvalRequest,
		http.StatusOK)
	if replayed["status"] != "accepted" {
		t.Fatalf("idempotent replay=%#v", replayed)
	}
	approvedMetadata, found, err := store.GetFrameRuntimeMetadata("frame")
	if err != nil || !found || stringValue(approvedMetadata.ContextData["_plan_version_id"]) == version.ID {
		t.Fatalf("approved metadata=%#v found=%t err=%v", approvedMetadata, found, err)
	}
	_, planHistory, found, err := store.ListArtifactVersionHistory("plan-artifact")
	if err != nil || !found || len(planHistory) != 2 {
		t.Fatalf("plan history=%#v found=%t err=%v", planHistory, found, err)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || intent.Revision != 1 || intent.Text != "Build the CRBN analysis." {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	runtimeConfig, found, err := repo.LatestFrameRuntimeConfig(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || runtimeConfig["agentName"] != "OPERON" || runtimeConfig["ultra_mode"] != true {
		t.Fatalf("runtime config=%#v found=%t err=%v", runtimeConfig, found, err)
	}
	if legacy, found, err := server.sessionStore.Get("frame"); err != nil || found {
		t.Fatalf("legacy runtime session=%#v found=%t err=%v", legacy, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil || len(events) != 4 || events[1].Event.Type != "runner_checkpoint" ||
		events[2].Event.Type != "runner_checkpoint" || events[3].Event.Type != "user_input_response" ||
		!strings.Contains(string(events[3].ResolvedPayloadJSON), "approved the proposed plan") {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	chat := SessionRunnerChatOptions{
		RunnerID: "plan-resumer", Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
		AllowedTools: []string{updateStepStatusToolName}, LeaseTTL: time.Minute, MaxAttempts: 1, MaxToolRounds: 3,
		ReplayLimit: 100, DisableSkillDiscovery: true,
	}
	if generic, err := server.RunSessionRunnerChatOnce(context.Background(), chat); err != nil || generic.Claimed || providerCalls.Load() != 0 {
		t.Fatalf("generic runner claimed dedicated plan resume: result=%#v providerCalls=%d err=%v", generic, providerCalls.Load(), err)
	}
	resumed, err := server.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "plan-resume-dispatch", ClaimTTL: time.Second, Chat: chat,
	})
	if err != nil || !resumed.Claimed || !resumed.Runner.Claimed || resumed.Runner.Attempt != 1 ||
		resumed.Status != "completed" || providerCalls.Load() != 3 {
		t.Fatalf("resumed=%#v providerCalls=%d err=%v", resumed, providerCalls.Load(), err)
	}
}

func TestCompatibilityDiscardPlanSettlesCanonicalTranscript(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-discard", "frame-discard")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-discard", OwnerID: "local", ExternalID: "frame-discard", SessionID: "frame-discard",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-discard", RootFrameID: "frame-discard", FrameID: "frame-discard", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "task", FrameEventID: "task-event",
		MessageUUID: "task-message", Text: "Prepare a plan but do not execute without approval.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("task created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "planning-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	if _, _, created, err := repo.PauseRunnerForInput(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "plan-paused", Phase: transcriptstore.RunnerPhaseWaitingUser,
		Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_plan_approval","detail":"review plan"}`), Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("pause created=%t err=%v", created, err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame-discard", workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_plan_artifact_id": "plan-artifact", "_plan_version_id": "plan-version", "_plan_json": map[string]any{"title": "Plan"},
	}}); err != nil {
		t.Fatal(err)
	}
	status := "awaiting_plan_approval"
	if _, err := store.UpdateFrame("frame-discard", workspace.UpdateFrameInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	response := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-discard/discard-plan", "local", map[string]any{}, http.StatusOK)
	if response["status"] != "completed" {
		t.Fatalf("response=%#v", response)
	}
	frame, found, err := store.GetFrame("frame-discard")
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	runtime, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claimed.Claim.Attempt)
	if err != nil || runtime.Status != "completed" || runtime.Phase != transcriptstore.RunnerPhaseTerminal {
		t.Fatalf("runtime=%#v err=%v", runtime, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100,
	})
	if err != nil || len(projected) == 0 || projected[len(projected)-1].Event.Type != "runner_finished" {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
	projection, err := repo.GetTerminalProjection(
		context.Background(), stream.OwnerID, stream.UID, projected[len(projected)-1].Event.EventID,
	)
	if err != nil || projection.StreamType != "finish" || projection.TerminalStatus != "completed" {
		t.Fatalf("projection=%#v err=%v", projection, err)
	}
	if err := server.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	terminalCounts := map[string]int{}
	for _, event := range transcriptWebEvents(t, store, "local") {
		if event.Payload["terminal_status"] != "completed" {
			continue
		}
		switch event.Type {
		case "message.stream":
			if event.Payload["stream_type"] == "finish" {
				terminalCounts["stream"]++
			}
		case "runtime.statusChanged":
			terminalCounts["runtime"]++
		case "turn.completed":
			terminalCounts["turn"]++
		}
	}
	if terminalCounts["stream"] != 1 || terminalCounts["runtime"] != 1 || terminalCounts["turn"] != 1 {
		t.Fatalf("terminal projection counts=%#v", terminalCounts)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata("frame-discard")
	if err != nil || !found || metadata.ContextData["_plan_json"] != nil || metadata.ContextData["_plan_artifact_id"] != nil {
		t.Fatalf("metadata=%#v found=%t err=%v", metadata, found, err)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || intent.Revision != 1 || intent.Text != "Prepare a plan but do not execute without approval." {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
}

func stringsContains(value, fragment string) bool {
	return len(fragment) == 0 || len(value) >= len(fragment) && strings.Contains(value, fragment)
}
