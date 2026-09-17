package server

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestSubmitRequestCompatibilityPersistsV11FrameAndRunnerQueueAcrossRestart(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project", UserID: "local", Name: "Submit Request",
	}); err != nil {
		t.Fatal(err)
	}
	server := newV11TestServer(t, Options{FileRoot: runtimeRoot, Workspace: store})
	app := server.Handler()
	payload := map[string]any{
		"target_agent": "ONBOARDING", "project_id": "project",
		"input_data": map[string]any{"request": "Record this isolated submission without external tools."},
		"plan_mode":  false, "intent_id": "00000000-0000-4000-8000-000000000011",
	}
	response := compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", payload, http.StatusOK)
	frameID, _ := response["frame_id"].(string)
	if len(response) != 3 || frameID == "" || response["root_frame_id"] != frameID || response["status"] != "accepted" {
		t.Fatalf("submit response = %#v", response)
	}
	retry := compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", payload, http.StatusConflict)
	if retry["detail"] != "intent_id 00000000-0000-4000-8000-000000000011 was already used \u2014 ids are minted once per send" {
		t.Fatalf("retry response = %#v", retry)
	}
	frame := compatJSONRequest(t, app, http.MethodGet, "/api/frames/"+frameID+"?shallow=true", "local", nil, http.StatusOK)
	inputData, _ := frame["input_data"].(map[string]any)
	if frame["agent_name"] != "ONBOARDING" || frame["status"] != "processing" ||
		frame["conversation_type"] != "agent" || frame["is_hidden"] != true ||
		inputData["request"] != "Record this isolated submission without external tools." {
		t.Fatalf("submitted frame = %#v", frame)
	}
	session, found, err := server.sessionStore.Get(frameID)
	if err != nil || !found || session.MessageCount != 1 || session.LastRole != "user" {
		t.Fatalf("runner session = %#v found=%v error=%v", session, found, err)
	}
	config, _ := session.Orchestration["sessionConfig"].(map[string]any)
	if config["targetAgent"] != "ONBOARDING" || config["intentId"] != "00000000-0000-4000-8000-000000000011" {
		t.Fatalf("runner session config = %#v", config)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted := newV11TestServer(t, Options{FileRoot: runtimeRoot, Workspace: store}).Handler()
	reloaded := compatJSONRequest(t, restarted, http.MethodGet, "/api/frames/"+frameID+"?shallow=true", "local", nil, http.StatusOK)
	reloadedInput, _ := reloaded["input_data"].(map[string]any)
	if reloaded["is_hidden"] != true || reloadedInput["request"] != "Record this isolated submission without external tools." {
		t.Fatalf("restarted submitted frame = %#v", reloaded)
	}
}

func TestSubmitRequestCompatibilityDeliversQueuedMessagesOnePerRealRunnerTurn(t *testing.T) {
	runtimeRoot := t.TempDir()
	store, err := workspace.Open(filepath.Join(runtimeRoot, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Delivery"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "ONBOARDING", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: runtimeRoot, Workspace: store})
	app := server.Handler()
	request := func(intentID, text string) map[string]any {
		return map[string]any{
			"target_agent": "ONBOARDING", "project_id": "project", "root_frame_id": "root",
			"input_data": map[string]any{"request": text}, "intent_id": intentID,
		}
	}
	firstID := "00000000-0000-4000-8000-000000000031"
	secondID := "00000000-0000-4000-8000-000000000032"
	thirdID := "00000000-0000-4000-8000-000000000033"
	accepted := compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", request(firstID, "First turn."), http.StatusOK)
	if accepted["status"] != "accepted" {
		t.Fatalf("accepted = %#v", accepted)
	}
	for _, queued := range []struct{ id, text string }{{secondID, "Second turn."}, {thirdID, "Third turn."}} {
		response := compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", request(queued.id, queued.text), http.StatusOK)
		if response["status"] != "message_queued" {
			t.Fatalf("queued %s = %#v", queued.id, response)
		}
	}
	duplicate := compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", request(secondID, "Second turn."), http.StatusOK)
	if duplicate["status"] != "message_queued" {
		t.Fatalf("duplicate queued = %#v", duplicate)
	}
	mismatch := compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", request(secondID, "Edited second turn."), http.StatusConflict)
	if mismatch["detail"] != "intent_id "+secondID+" is already queued with a different payload \u2014 retries must be byte-identical; mint a new id for an edited send" {
		t.Fatalf("payload mismatch = %#v", mismatch)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	firstCycle, err := server.RunSessionRunnerChatOnce(ctx, SessionRunnerChatOptions{
		SessionID: "root", RunnerID: "compat-runner", Endpoint: BuiltinSessionRunnerChatEndpoint,
		Model: BuiltinSessionRunnerChatModel, LeaseTTL: time.Minute,
	})
	if err != nil || !firstCycle.Claimed || firstCycle.Status != "completed" {
		t.Fatalf("first runner cycle = %#v err=%v", firstCycle, err)
	}
	secondDisposition, found, err := store.GetCompatibilityMessageIntent(secondID)
	if err != nil || !found || secondDisposition.State != "drained" {
		t.Fatalf("intent %s = %#v found=%v err=%v", secondID, secondDisposition, found, err)
	}
	thirdDisposition, found, err := store.GetCompatibilityMessageIntent(thirdID)
	if err != nil || !found || thirdDisposition.State != "queued" {
		t.Fatalf("intent %s = %#v found=%v err=%v", thirdID, thirdDisposition, found, err)
	}
	frame, found, err := store.GetFrame("root")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("frame after queue delivery = %#v found=%v err=%v", frame, found, err)
	}
	events, err := store.ListFrameEvents("root", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	messageIDs := make([]string, 0, 3)
	for _, event := range events {
		if event.Type == "queued_message" && stringValue(event.Payload["messageUuid"]) != thirdID {
			t.Fatalf("unexpected queued event remained: %#v", event)
		}
		if event.Type == "user_message" {
			messageIDs = append(messageIDs, stringValue(event.Payload["messageUuid"]))
		}
	}
	if len(messageIDs) != 2 || messageIDs[0] != firstID || messageIDs[1] != secondID {
		t.Fatalf("delivered user message order = %#v", messageIDs)
	}
	tooLate := compatJSONRequest(t, app, http.MethodDelete, "/api/frames/root/queued-messages/"+secondID, "local", nil, http.StatusConflict)
	if tooLate["detail"] != "Queued message "+secondID+" was already picked up for delivery on frame root" {
		t.Fatalf("late retraction = %#v", tooLate)
	}
	alreadyDelivered := compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", request(secondID, "Second turn."), http.StatusOK)
	if alreadyDelivered["status"] != "already_delivered" {
		t.Fatalf("delivered retry = %#v", alreadyDelivered)
	}

	secondCycle, err := server.RunSessionRunnerChatOnce(ctx, SessionRunnerChatOptions{
		SessionID: "root", RunnerID: "compat-runner", Endpoint: BuiltinSessionRunnerChatEndpoint,
		Model: BuiltinSessionRunnerChatModel, LeaseTTL: time.Minute,
	})
	if err != nil || !secondCycle.Claimed || secondCycle.Status != "completed" {
		t.Fatalf("second runner cycle = %#v err=%v", secondCycle, err)
	}
	thirdDisposition, found, err = store.GetCompatibilityMessageIntent(thirdID)
	if err != nil || !found || thirdDisposition.State != "drained" {
		t.Fatalf("second-cycle intent %s = %#v found=%v err=%v", thirdID, thirdDisposition, found, err)
	}
	events, err = store.ListFrameEvents("root", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	messageIDs = messageIDs[:0]
	for _, event := range events {
		if event.Type == "queued_message" {
			t.Fatalf("delivered queue event remained after second cycle: %#v", event)
		}
		if event.Type == "user_message" {
			messageIDs = append(messageIDs, stringValue(event.Payload["messageUuid"]))
		}
	}
	if len(messageIDs) != 3 || messageIDs[0] != firstID || messageIDs[1] != secondID || messageIDs[2] != thirdID {
		t.Fatalf("all delivered user message order = %#v", messageIDs)
	}
	thirdCycle, err := server.RunSessionRunnerChatOnce(ctx, SessionRunnerChatOptions{
		SessionID: "root", RunnerID: "compat-runner", Endpoint: BuiltinSessionRunnerChatEndpoint,
		Model: BuiltinSessionRunnerChatModel, LeaseTTL: time.Minute,
	})
	if err != nil || !thirdCycle.Claimed || thirdCycle.Status != "completed" {
		t.Fatalf("third runner cycle = %#v err=%v", thirdCycle, err)
	}
	frame, found, err = store.GetFrame("root")
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("completed frame = %#v found=%v err=%v", frame, found, err)
	}
}

func TestCompatibilityQueueRejectsStaleFinishDuringNextRunnerLease(t *testing.T) {
	runtimeRoot := t.TempDir()
	store, err := workspace.Open(filepath.Join(runtimeRoot, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Finish"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "ONBOARDING", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: runtimeRoot, Workspace: store})
	app := server.Handler()
	request := func(intentID, text string) map[string]any {
		return map[string]any{
			"target_agent": "ONBOARDING", "project_id": "project", "root_frame_id": "root",
			"input_data": map[string]any{"request": text}, "intent_id": intentID,
		}
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", request(
		"00000000-0000-4000-8000-000000000051", "Active.",
	), http.StatusOK)
	compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", request(
		"00000000-0000-4000-8000-000000000052", "Queued.",
	), http.StatusOK)
	claimed, ok, err := server.sessionStore.ClaimRunner("root", "runner", time.Minute)
	if err != nil || !ok || claimed.Runner == nil {
		t.Fatalf("first claim=%#v ok=%v err=%v", claimed, ok, err)
	}
	finishClientID := runnerCommandClientMessageID("runner", "root", claimed.Runner.Attempt, runnerCommandClaimToken(claimed), "chat-finish")
	finishInput := map[string]any{
		"sessionId": "root", "runnerId": "runner", "status": "completed",
		"runnerAttempt": claimed.Runner.Attempt, "claimToken": runnerCommandClaimToken(claimed),
		"message": "done", "clientMessageId": finishClientID,
	}
	if _, err := server.finishSessionRunner(finishInput); err != nil {
		t.Fatal(err)
	}
	secondClaim, ok, err := server.sessionStore.ClaimRunner("root", "runner", time.Minute)
	if err != nil || !ok || secondClaim.Runner == nil || secondClaim.Runner.Status != "running" {
		t.Fatalf("second claim=%#v ok=%v err=%v", secondClaim, ok, err)
	}
	if _, err := server.finishSessionRunner(finishInput); !errors.Is(err, sessionstore.ErrRunnerClaimStale) {
		t.Fatalf("stale finish error: %v", err)
	}
	current, found, err := server.sessionStore.Get("root")
	if err != nil || !found || current.Runner == nil || current.Runner.Status != "running" {
		t.Fatalf("runner after stale finish=%#v found=%v err=%v", current, found, err)
	}
	frame, found, err := store.GetFrame("root")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("frame after stale finish=%#v found=%v err=%v", frame, found, err)
	}
}

func TestCompatibilityQueueRecoversClaimedDeliveryAcrossServerRestart(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Recovery"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "ONBOARDING", Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: runtimeRoot, Workspace: store})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "root", MessageUUID: "active", ClientMessageID: "active", Text: "Active before crash.",
	}); err != nil {
		t.Fatal(err)
	}
	intentID := "00000000-0000-4000-8000-000000000062"
	if _, _, _, err := store.QueueCompatibilityMessage(
		"root", intentID, map[string]any{"text": "Recover after crash."},
		map[string]any{
			"messageUuid": intentID, "clientMessageId": intentID,
			"text": "Recover after crash.", "role": "user", "sessionConfig": map[string]any{"targetAgent": "ONBOARDING"},
			"inputData": map[string]any{"request": "Recover after crash."},
		},
	); err != nil {
		t.Fatal(err)
	}
	claimedQueue, err := store.ClaimCompatibilityQueuedMessages("root")
	if err != nil || len(claimedQueue) != 1 || claimedQueue[0].State != "delivering" {
		t.Fatalf("claimed queue=%#v err=%v", claimedQueue, err)
	}
	claimedRunner, ok, err := server.sessionStore.ClaimRunner("root", "crashed-runner", time.Minute)
	if err != nil || !ok || claimedRunner.Runner == nil {
		t.Fatalf("claimed runner=%#v ok=%v err=%v", claimedRunner, ok, err)
	}
	claimedRunner.Runner.Status = "failed"
	claimedRunner.Runner.ExpiresAt = time.Now().Add(-time.Second)
	if err := server.sessionStore.Save(claimedRunner); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted := New(Options{FileRoot: runtimeRoot, Workspace: store})
	advanced, err := restarted.recoverCompatibilityQueuedMessages()
	if err != nil || advanced != 1 {
		t.Fatalf("recovered advanced=%d err=%v", advanced, err)
	}
	disposition, found, err := store.GetCompatibilityMessageIntent(intentID)
	if err != nil || !found || disposition.State != "drained" {
		t.Fatalf("recovered intent=%#v found=%v err=%v", disposition, found, err)
	}
	journaled, err := restarted.eventJournal.HasClientMessage("root", intentID)
	if err != nil || !journaled {
		t.Fatalf("recovered journaled=%v err=%v", journaled, err)
	}
	advanced, err = restarted.recoverCompatibilityQueuedMessages()
	if err != nil || advanced != 0 {
		t.Fatalf("idempotent recovery advanced=%d err=%v", advanced, err)
	}
}

func TestSubmitRequestCompatibilityQueuesAndRetractsActiveFrameMessageAcrossRestart(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Queue"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root", ProjectID: "project", AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: runtimeRoot, Workspace: store})
	app := server.Handler()
	request := func(intentID, text string) map[string]any {
		return map[string]any{
			"target_agent": "ONBOARDING", "project_id": "project", "root_frame_id": "root",
			"input_data": map[string]any{"request": text}, "intent_id": intentID,
		}
	}
	accepted := compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", request(
		"00000000-0000-4000-8000-000000000021", "Process this message.",
	), http.StatusOK)
	if accepted["status"] != "accepted" || accepted["frame_id"] != "root" {
		t.Fatalf("accepted response = %#v", accepted)
	}
	queuedID := "00000000-0000-4000-8000-000000000022"
	queued := compatJSONRequest(t, app, http.MethodPost, "/api/request", "local", request(
		queuedID, "Withdraw this queued message before delivery.",
	), http.StatusOK)
	if queued["status"] != "message_queued" || queued["frame_id"] != "root" {
		t.Fatalf("queued response = %#v", queued)
	}
	session, found, err := server.sessionStore.Get("root")
	if err != nil || !found || session.MessageCount != 1 {
		t.Fatalf("queued message entered runner early: session=%#v found=%v err=%v", session, found, err)
	}
	events, err := store.ListFrameEvents("root", 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	queuedEvents := 0
	for _, event := range events {
		if event.Type == "queued_message" {
			queuedEvents++
			if event.Payload["messageUuid"] != queuedID || event.Payload["text"] != "Withdraw this queued message before delivery." {
				t.Fatalf("queued payload = %#v", event.Payload)
			}
		}
	}
	if queuedEvents != 1 {
		t.Fatalf("queued event count = %d, events=%#v", queuedEvents, events)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app = New(Options{FileRoot: runtimeRoot, Workspace: store}).Handler()
	retracted := compatJSONRequest(t, app, http.MethodDelete, "/api/frames/root/queued-messages/"+queuedID, "local", nil, http.StatusOK)
	if retracted["removed"] != true || retracted["id"] != queuedID || retracted["frame_id"] != "root" {
		t.Fatalf("retracted response = %#v", retracted)
	}
	repeated := compatJSONRequest(t, app, http.MethodDelete, "/api/frames/root/queued-messages/"+queuedID, "local", nil, http.StatusNotFound)
	if repeated["detail"] != "Queued message "+queuedID+" not found on frame root" {
		t.Fatalf("repeated retraction = %#v", repeated)
	}
}
