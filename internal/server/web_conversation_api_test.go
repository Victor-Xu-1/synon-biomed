package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestP3WebConversationLifecycleUsesDurableFrameRuntime(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "project-p3", "local")

	create := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Evidence review",
		"assistant": map[string]any{
			"id":                     "synonbiomed:OPERON",
			"conversation_overrides": map[string]any{"model": "test-model"},
		},
		"extra": map[string]any{"project_id": project.ID, "project_name": project.Name},
	}, "")
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	created := p3DecodeObject(t, create)
	conversationID := webString(created["id"])
	if conversationID == "" {
		t.Fatalf("created conversation has no id: %#v", created)
	}
	if extra, _ := created["extra"].(map[string]any); webString(extra["project_id"]) != project.ID {
		t.Fatalf("created conversation project mismatch: %#v", created)
	}
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + conversationID, OwnerID: "local", ExternalID: conversationID, SessionID: conversationID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: project.ID,
		RootFrameID: conversationID, FrameID: conversationID, Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}

	get := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+conversationID, nil, "")
	if get.Code != http.StatusOK || webString(p3DecodeObject(t, get)["name"]) != "Evidence review" {
		t.Fatalf("get status=%d body=%s", get.Code, get.Body.String())
	}

	update := p3JSONRequest(t, app, http.MethodPatch, "/api/conversations/"+conversationID, map[string]any{
		"name": "Evidence review updated", "desc": "Traceable summary",
		"extra": map[string]any{"selected_view": "timeline"}, "merge_extra": true,
	}, "")
	if update.Code != http.StatusOK || update.Body.String() != "true\n" {
		t.Fatalf("update status=%d body=%s", update.Code, update.Body.String())
	}
	frameBeforeBranchSeed, found, err := store.GetFrame(conversationID)
	if err != nil || !found {
		t.Fatalf("frame before branch seed found=%v err=%v", found, err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: conversationID, Type: "user_message", Payload: map[string]any{
		"role": "user", "content": []any{map[string]any{"type": "text", "text": "Seed branch"}}, "_uuid": "seed-branch-message",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForkCompatibilityRoot(workspace.CompatibilityRootForkInput{
		RootFrameID: conversationID, MessageIndex: 0, EditedContent: "Active branch seed",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateFrame(conversationID, workspace.UpdateFrameInput{Status: &frameBeforeBranchSeed.Status}); err != nil {
		t.Fatal(err)
	}
	inactiveBranchID := inactiveCompatibilityBranchID(t, store, conversationID)
	foreignProject := createP3Project(t, store, "foreign-branch-project", "foreign")
	foreignFrame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "foreign-branch-frame", ProjectID: foreignProject.ID, AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{FrameID: foreignFrame.ID, Type: "user_message", Payload: map[string]any{"role": "user", "text": "Foreign seed"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForkCompatibilityRoot(workspace.CompatibilityRootForkInput{RootFrameID: foreignFrame.ID, MessageIndex: 0, EditedContent: "Foreign active"}); err != nil {
		t.Fatal(err)
	}
	foreignBranchID := inactiveCompatibilityBranchID(t, store, foreignFrame.ID)
	beforeBranch := branchAuthorityStateHash(t, app, store, conversationID, "local")
	beforeForeignBranch := branchAuthorityStateHash(t, app, store, foreignFrame.ID, "foreign")
	malformedBranch := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Malformed branch", "session_options": map[string]any{"target_branch_id": 42},
	}, "")
	if malformedBranch.Code != http.StatusBadRequest {
		t.Fatalf("malformed branch status=%d body=%s", malformedBranch.Code, malformedBranch.Body.String())
	}
	for _, targetBranchID := range []string{"invalid", "br_deadbeef", foreignBranchID} {
		response := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
			"content": "Reject branch", "session_options": map[string]any{"target_branch_id": targetBranchID},
		}, "")
		wantStatus := http.StatusNotFound
		if targetBranchID == "invalid" {
			wantStatus = http.StatusBadRequest
		}
		if response.Code != wantStatus {
			t.Fatalf("target branch %q status=%d body=%s", targetBranchID, response.Code, response.Body.String())
		}
	}
	branch := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Switch branch", "session_options": map[string]any{"target_branch_id": inactiveBranchID},
	}, "")
	if branch.Code != http.StatusConflict || webString(p3DecodeObject(t, branch)["message"]) != branchAuthorityUnavailableTestDetail {
		t.Fatalf("branch status=%d body=%s", branch.Code, branch.Body.String())
	}
	if after := branchAuthorityStateHash(t, app, store, conversationID, "local"); after != beforeBranch {
		t.Fatalf("rejected web branch mutated state: before=%s after=%s", beforeBranch, after)
	}
	if after := branchAuthorityStateHash(t, app, store, foreignFrame.ID, "foreign"); after != beforeForeignBranch {
		t.Fatalf("rejected web branch mutated foreign state: before=%s after=%s", beforeForeignBranch, after)
	}
	send := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/messages", map[string]any{
		"content": "Summarize the evidence", "files": []string{"source.pdf"},
		"inject_skills": []string{"literature-review"},
		"session_options": map[string]any{
			"model": "test-model", "subagent_model": "test-model", "effort": "high",
			"plan_mode": true, "ultra_mode": false,
			"verifier_mode": "on", "memory_mode": "on", "target_agent": "OPERON",
		},
	}, "")
	if send.Code != http.StatusAccepted {
		t.Fatalf("send status=%d body=%s", send.Code, send.Body.String())
	}
	sent := p3DecodeObject(t, send)
	messageID := webString(sent["msg_id"])
	if messageID == "" || webString(sent["turn_id"]) != conversationID {
		t.Fatalf("send contract=%#v", sent)
	}
	session, found, err := app.sessionStore.Get(conversationID)
	if err != nil || !found {
		t.Fatalf("runner session=%#v found=%v err=%v", session, found, err)
	}
	config, ok := session.Orchestration["sessionConfig"].(map[string]any)
	if !ok || config["model"] != "test-model" || config["subagentModel"] != "test-model" ||
		config["effort"] != "high" || config["planMode"] != true || config["ultraMode"] != false ||
		config["verifierMode"] != "on" || config["memoryMode"] != "on" ||
		config["targetAgent"] != "OPERON" || config["agentName"] != "OPERON" {
		t.Fatalf("runner session config=%#v", config)
	}

	messages := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+conversationID+"/messages?limit=50", nil, "")
	if messages.Code != http.StatusOK {
		t.Fatalf("messages status=%d body=%s", messages.Code, messages.Body.String())
	}
	messagePage := p3DecodeObject(t, messages)
	items, _ := messagePage["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("messages=%#v", messagePage)
	}
	var sentProjection map[string]any
	for _, item := range items {
		candidate, _ := item.(map[string]any)
		if webString(candidate["msg_id"]) == messageID {
			sentProjection = candidate
		}
	}
	if webString(sentProjection["id"]) != messageID+":0" || webString(sentProjection["type"]) != "text" {
		t.Fatalf("message projection=%#v", sentProjection)
	}

	message := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+conversationID+"/messages/"+messageID, nil, "")
	if message.Code != http.StatusOK || webString(p3DecodeObject(t, message)["id"]) != messageID+":0" {
		t.Fatalf("message status=%d body=%s", message.Code, message.Body.String())
	}

	ensure := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/runtime/ensure", map[string]any{}, "")
	if ensure.Code != http.StatusOK {
		t.Fatalf("ensure status=%d body=%s", ensure.Code, ensure.Body.String())
	}
	lease := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/active-lease", map[string]any{}, "")
	if lease.Code != http.StatusNoContent {
		t.Fatalf("lease status=%d body=%s", lease.Code, lease.Body.String())
	}
	if _, created, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: "frame:" + conversationID, OwnerID: "local", ClientMessageID: "p3-clone-canonical-user",
		PayloadJSON: []byte(`{"role":"user","text":"Summarize the evidence"}`),
	}); err != nil || !created {
		t.Fatalf("canonical clone seed created=%v err=%v", created, err)
	}
	app.transcriptStore = repository
	stream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", conversationID)
	if err != nil || !found {
		t.Fatalf("clone source stream found=%v err=%v", found, err)
	}
	claim, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "p3-clone-source-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("clone source claim=%#v err=%v", claim, err)
	}
	if _, _, created, err := repository.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "p3-clone-source-finished", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil || !created {
		t.Fatalf("clone source finish created=%v err=%v", created, err)
	}

	clone := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/clone", map[string]any{
		"conversation": created,
	}, "")
	if clone.Code != http.StatusCreated {
		t.Fatalf("clone status=%d body=%s", clone.Code, clone.Body.String())
	}
	cloneID := webString(p3DecodeObject(t, clone)["id"])
	cloneMessages := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+cloneID+"/messages", nil, "")
	clonePage := p3DecodeObject(t, cloneMessages)
	cloneItems, _ := clonePage["items"].([]any)
	if len(cloneItems) != 2 {
		t.Fatalf("clone messages=%#v", clonePage)
	}

	reset := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+conversationID+"/reset", map[string]any{}, "")
	if reset.Code != http.StatusNoContent {
		t.Fatalf("reset status=%d body=%s", reset.Code, reset.Body.String())
	}
	deleteResponse := p3JSONRequest(t, app, http.MethodDelete, "/api/conversations/"+conversationID, nil, "")
	if deleteResponse.Code != http.StatusOK || deleteResponse.Body.String() != "true\n" {
		t.Fatalf("delete status=%d body=%s", deleteResponse.Code, deleteResponse.Body.String())
	}
	startServerRealtimeOutbox(t, store, app)
	waitServerRealtimeOutbox(t, store)
	realtimeEvents, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: project.ID, Type: "conversation.listChanged", Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	deletedEvent := false
	for _, event := range realtimeEvents {
		if event.Payload["conversation_id"] == conversationID && event.Payload["action"] == "deleted" {
			deletedEvent = true
		}
	}
	if !deletedEvent {
		t.Fatalf("durable conversation delete event missing: %#v", realtimeEvents)
	}
	missing := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+conversationID, nil, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("deleted conversation status=%d body=%s", missing.Code, missing.Body.String())
	}
}

func TestWebConversationRuntimeMetadataFailureIsStageCodedAndRedacted(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	project := createP3Project(t, store, "project-runtime-error", "local")
	created, err := store.CreateFrame(workspace.CreateFrameInput{
		ID:               "frame-runtime-error",
		ProjectID:        project.ID,
		AgentName:        "OPERON",
		Status:           "processing",
		ConversationType: "agent",
		Name:             "Runtime error contract",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame, found, err := store.GetCompatibilityFrame(created.ID)
	if err != nil || !found {
		t.Fatalf("load frame: found=%t err=%v", found, err)
	}
	app := newV11TestServer(t, Options{Workspace: store, FileRoot: t.TempDir()})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/conversations/"+created.ID, nil)
	response := httptest.NewRecorder()
	app.handleWebConversationRecord(response, request, frame, project)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get(webErrorCodeHeader); got != webConversationRuntimeMetadataCode {
		t.Fatalf("error code header=%q", got)
	}
	payload := p3DecodeObject(t, response)
	if payload["code"] != webConversationRuntimeMetadataCode || payload["message"] != "unable to load conversation runtime" {
		t.Fatalf("payload=%#v", payload)
	}
	if strings.Contains(strings.ToLower(response.Body.String()), "database") ||
		strings.Contains(strings.ToLower(response.Body.String()), "closed") {
		t.Fatalf("response leaked storage details: %s", response.Body.String())
	}
}

func TestP3WebConversationDeleteReturnsSuccessAfterRuntimeCleanupWarnings(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "project-delete-web-runtime", "local")
	create := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name":      "Delete runtime conversation",
		"assistant": map[string]any{"id": "synonbiomed:OPERON"},
		"extra":     map[string]any{"project_id": project.ID, "project_name": project.Name},
	}, "")
	if create.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", create.Code, create.Body.String())
	}
	conversationID := webString(p3DecodeObject(t, create)["id"])
	if conversationID == "" {
		t.Fatal("created conversation has no id")
	}
	brokenRuntimeRoot := filepath.Join(t.TempDir(), "broken-runtime-root")
	if err := os.WriteFile(brokenRuntimeRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	app.sessionStore = sessionstore.NewStore(brokenRuntimeRoot)
	app.eventJournal = eventjournal.NewEventJournal(brokenRuntimeRoot)

	deleted := p3JSONRequest(t, app, http.MethodDelete, "/api/conversations/"+conversationID, nil, "")
	if deleted.Code != http.StatusOK || deleted.Body.String() != "true\n" {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if _, found, err := store.GetFrame(conversationID); err != nil || found {
		t.Fatalf("deleted conversation found=%t err=%v", found, err)
	}
}

func TestP3WebConversationProjectsHistoricalRunnerMessageAsText(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "runner-message-project", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "8a070fdc-f19e-4f49-a344-e89a7a18fe22", ProjectID: project.ID,
		AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Runner response",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: frame.ID, Type: "assistant_message",
		Payload: map[string]any{
			"type": "message", "role": "assistant", "text": "Durable runner output.",
			"clientMessageId": "runner-assistant-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	response := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("messages status=%d body=%s", response.Code, response.Body.String())
	}
	items, _ := p3DecodeObject(t, response)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("messages=%#v", items)
	}
	message, _ := items[0].(map[string]any)
	content, _ := message["content"].(map[string]any)
	if webString(message["type"]) != "text" || webString(message["position"]) != "left" ||
		webString(content["content"]) != "Durable runner output." {
		t.Fatalf("message projection=%#v", message)
	}
}

func TestP3WebConversationProjectsTypedBootstrapToolHistoryFromTranscriptOnly(t *testing.T) {
	largeToolOutput := strings.Repeat("tool-result-", 700)
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "typed-history-project", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "d77f6a2a-06f2-44a2-b455-32ec47e5e15e", ProjectID: project.ID,
		AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Typed tool history",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []workspace.FrameEventInput{
		{FrameID: frame.ID, Type: "assistant_message", Payload: map[string]any{
			"role": "assistant", "content": []any{
				map[string]any{"type": "text", "text": "Inspecting."},
				map[string]any{"type": "tool_use", "id": "call-1", "name": "read_file", "input": map[string]any{"path": "result.txt"}},
			},
		}},
		{FrameID: frame.ID, Type: "user_message", Payload: map[string]any{
			"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "call-1", "content": largeToolOutput},
			},
		}},
	} {
		if _, err := store.AppendFrameEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report, err := repo.ReconcileNoStreamFrameHistories(context.Background(), transcriptstore.ReconcileNoStreamFrameHistoriesInput{Limit: 10})
	if err != nil || report.PayloadCreated != 1 || report.Deferred != 0 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	app = New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if app.transcriptStore == nil || app.transcriptContractErr != nil {
		t.Fatalf("transcript runtime store=%p err=%v", app.transcriptStore, app.transcriptContractErr)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", frame.ID)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	if _, created, err := repo.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "future-user-1",
		PayloadJSON: []byte(`{"text":"Continue."}`),
	}); err != nil || !created {
		t.Fatalf("future user created=%t err=%v", created, err)
	}

	response := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages?content_mode=full", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("messages status=%d body=%s", response.Code, response.Body.String())
	}
	items, _ := p3DecodeObject(t, response)["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("messages=%#v", items)
	}
	textMessage, _ := items[0].(map[string]any)
	textContent, _ := textMessage["content"].(map[string]any)
	if webString(textMessage["type"]) != "text" || webString(textMessage["position"]) != "left" ||
		webString(textContent["content"]) != "Inspecting." {
		t.Fatalf("text message=%#v", textMessage)
	}
	toolMessage, _ := items[1].(map[string]any)
	toolContent, _ := toolMessage["content"].(map[string]any)
	if webString(toolMessage["type"]) != "tool_call" || webString(toolMessage["status"]) != "finish" ||
		webString(toolContent["call_id"]) != "call-1" || webString(toolContent["name"]) != "read_file" ||
		webString(toolContent["status"]) != "completed" || webString(toolContent["output"]) != largeToolOutput {
		t.Fatalf("tool message=%#v", toolMessage)
	}
	compactResponse := p3JSONRequest(
		t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages?content_mode=compact", nil, "",
	)
	if compactResponse.Code != http.StatusOK {
		t.Fatalf("compact messages status=%d body=%s", compactResponse.Code, compactResponse.Body.String())
	}
	compactItems, _ := p3DecodeObject(t, compactResponse)["items"].([]any)
	compactTool, _ := compactItems[1].(map[string]any)
	compactContent, _ := compactTool["content"].(map[string]any)
	compactMeta, _ := compactContent["_compact"].(map[string]any)
	if compactMeta["truncated"] != true || len(webString(compactContent["output"])) > compactWebToolOutputBytes+4 {
		t.Fatalf("compact tool message=%#v", compactTool)
	}
	futureMessage, _ := items[2].(map[string]any)
	futureContent, _ := futureMessage["content"].(map[string]any)
	if webString(futureMessage["position"]) != "right" || webString(futureContent["content"]) != "Continue." {
		t.Fatalf("future message=%#v", futureMessage)
	}
	messageID := webString(toolMessage["id"])
	single := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages/"+messageID, nil, "")
	singleMessage := p3DecodeObject(t, single)
	singleContent, _ := singleMessage["content"].(map[string]any)
	if single.Code != http.StatusOK || webString(singleMessage["id"]) != messageID ||
		webString(singleContent["output"]) != largeToolOutput {
		t.Fatalf("single status=%d body=%s", single.Code, single.Body.String())
	}
}

func TestP3WebConversationProjectsRunnerTextAlongsideRichUserMessage(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "mixed-runner-message-project", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "23c973ad-3d00-4d8f-b45f-e2fb414fc30c", ProjectID: project.ID,
		AgentName: "OPERON", Status: "completed", ConversationType: "agent", Name: "Mixed runner response",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: frame.ID, Type: "user_message",
		Payload: map[string]any{
			"_uuid": "rich-user-1", "role": "user", "text": "Run the model.",
			"content": []any{map[string]any{"type": "text", "text": "Run the model."}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: frame.ID, Type: "assistant_message",
		Payload: map[string]any{
			"type": "assistant_message", "role": "assistant", "text": "SYNON_DEEPSEEK_RUNTIME_OK",
			"clientMessageId": "runner-assistant-mixed-1",
		},
	}); err != nil {
		t.Fatal(err)
	}

	response := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages?content_mode=full", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("messages status=%d body=%s", response.Code, response.Body.String())
	}
	items, _ := p3DecodeObject(t, response)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("messages=%#v", items)
	}
	assistant, _ := items[1].(map[string]any)
	content, _ := assistant["content"].(map[string]any)
	if webString(assistant["type"]) != "text" || webString(assistant["position"]) != "left" ||
		webString(content["content"]) != "SYNON_DEEPSEEK_RUNTIME_OK" {
		t.Fatalf("assistant projection=%#v", assistant)
	}
}

func TestP3WebConversationOwnerIsolationAndDefaultProject(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "owned-project", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "4e66d623-ce45-4f66-9eef-c1aed295e0df", ProjectID: project.ID,
		AgentName: "GENERAL", Status: "completed", ConversationType: "agent", Name: "Private",
	})
	if err != nil {
		t.Fatal(err)
	}

	foreignGet := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID, nil, "foreign")
	if foreignGet.Code != http.StatusNotFound {
		t.Fatalf("foreign get status=%d body=%s", foreignGet.Code, foreignGet.Body.String())
	}
	foreignCreate := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Unauthorized", "extra": map[string]any{"project_id": project.ID},
	}, "foreign")
	if foreignCreate.Code != http.StatusNotFound {
		t.Fatalf("foreign create status=%d body=%s", foreignCreate.Code, foreignCreate.Body.String())
	}

	defaultProject := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name": "Personal task", "extra": map[string]any{},
	}, "")
	if defaultProject.Code != http.StatusCreated {
		t.Fatalf("default project create status=%d body=%s", defaultProject.Code, defaultProject.Body.String())
	}
	created := p3DecodeObject(t, defaultProject)
	extra, _ := created["extra"].(map[string]any)
	if webString(extra["project_id"]) == "" {
		t.Fatalf("default project was not assigned: %#v", created)
	}
}

func TestP3WebConversationMessageCursorBoundaries(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "cursor-project", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "55a02fd4-943a-4fc4-a287-e702d3b12129", ProjectID: project.ID,
		AgentName: "GENERAL", Status: "completed", ConversationType: "agent", Name: "Long history",
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 205; index++ {
		id := "message-" + strconv.Itoa(index)
		if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: frame.ID, Type: "user_message",
			Payload: map[string]any{"messageUuid": id, "role": "user", "text": fmt.Sprintf("message %03d", index)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	latest := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages?limit=25", nil, "")
	latestPage := p3DecodeObject(t, latest)
	latestItems, _ := latestPage["items"].([]any)
	if latest.Code != http.StatusOK || len(latestItems) != 25 || latestPage["has_more_before"] != true || latestPage["has_more_after"] != false {
		t.Fatalf("latest status=%d page=%#v", latest.Code, latestPage)
	}
	if webString(latestPage["oldest_cursor"]) != "idx:180" {
		t.Fatalf("latest cursor=%#v", latestPage)
	}

	previous := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages?limit=25&before=idx:180", nil, "")
	previousPage := p3DecodeObject(t, previous)
	if webString(previousPage["oldest_cursor"]) != "idx:155" || webString(previousPage["newest_cursor"]) != "idx:179" {
		t.Fatalf("previous page=%#v", previousPage)
	}
	stablePrevious := p3JSONRequest(t, app, http.MethodGet,
		"/api/conversations/"+frame.ID+"/messages?limit=25&before=message-180", nil, "")
	stablePreviousPage := p3DecodeObject(t, stablePrevious)
	if stablePrevious.Code != http.StatusOK || webString(stablePreviousPage["oldest_cursor"]) != "idx:155" ||
		webString(stablePreviousPage["newest_cursor"]) != "idx:179" {
		t.Fatalf("stable previous status=%d page=%#v", stablePrevious.Code, stablePreviousPage)
	}
	stableNext := p3JSONRequest(t, app, http.MethodGet,
		"/api/conversations/"+frame.ID+"/messages?limit=25&after=message-179", nil, "")
	stableNextPage := p3DecodeObject(t, stableNext)
	if stableNext.Code != http.StatusOK || webString(stableNextPage["oldest_cursor"]) != "idx:180" ||
		webString(stableNextPage["newest_cursor"]) != "idx:204" {
		t.Fatalf("stable next status=%d page=%#v", stableNext.Code, stableNextPage)
	}

	anchored := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages?limit=20&anchor_message_id=message-100", nil, "")
	anchoredPage := p3DecodeObject(t, anchored)
	if webString(anchoredPage["oldest_cursor"]) != "idx:90" {
		t.Fatalf("anchored page=%#v", anchoredPage)
	}

	invalid := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages?before=bad", nil, "")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	conflicting := p3JSONRequest(t, app, http.MethodGet, "/api/conversations/"+frame.ID+"/messages?before=idx:10&after=idx:2", nil, "")
	if conflicting.Code != http.StatusBadRequest {
		t.Fatalf("conflicting cursor status=%d body=%s", conflicting.Code, conflicting.Body.String())
	}
}

func TestP3WebConversationRuntimeRecoversPendingInputsAndScopesOwnedModel(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "runtime-project", "local")
	frame, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "65ed27b6-80a8-4ef5-9b9b-188b302632e6", ProjectID: project.ID,
		AgentName: "GENERAL", Status: "awaiting_user_response", ConversationType: "agent", Name: "Waiting",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata(frame.ID, workspace.FrameRuntimeMetadata{
		FrameID: frame.ID,
		ContextData: map[string]any{"_pending_input_requests": []any{
			map[string]any{"tool_id": "question-1", "kind": "ask"},
			map[string]any{"tool_id": "approval-1", "kind": "mcp_tool"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	for _, provider := range []workspace.ModelProviderInput{
		{ID: "provider-a", UserID: "local", Name: "Primary", Type: "openai-compatible", BaseURL: "https://models.example.test/v1", Model: "model-a", Enabled: &enabled},
		{ID: "provider-b", UserID: "local", Name: "Reasoning", Type: "openai-compatible", BaseURL: "https://models.example.test/v1", Model: "model-b", Enabled: &enabled},
	} {
		if _, err := store.RegisterModelProvider(provider); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := app.settingsStore.Set(webConversationActiveProviderSetting, "provider-a"); err != nil {
		t.Fatal(err)
	}

	ensure := p3JSONRequest(t, app, http.MethodPost, "/api/conversations/"+frame.ID+"/runtime/ensure", map[string]any{}, "")
	if ensure.Code != http.StatusOK {
		t.Fatalf("ensure status=%d body=%s", ensure.Code, ensure.Body.String())
	}
	payload := p3DecodeObject(t, ensure)
	if payload["recovered"] != true {
		t.Fatalf("runtime was not recovered: %#v", payload)
	}
	runtime, _ := payload["runtime"].(map[string]any)
	if runtime["state"] != "waiting_confirmation" || runtime["pending_confirmations"] != float64(2) || runtime["can_send_message"] != false {
		t.Fatalf("waiting runtime=%#v", runtime)
	}
	options, _ := payload["config_options"].([]any)
	if len(options) != 1 {
		t.Fatalf("config options=%#v", payload["config_options"])
	}
	modelOption, _ := options[0].(map[string]any)
	if modelOption["current_value"] != "model-a" {
		t.Fatalf("initial model option=%#v", modelOption)
	}

	setModel := p3JSONRequest(t, app, http.MethodPut, "/api/conversations/"+frame.ID+"/config-options/model", map[string]any{
		"value": "model-b",
	}, "")
	if setModel.Code != http.StatusOK {
		t.Fatalf("set model status=%d body=%s", setModel.Code, setModel.Body.String())
	}
	setPayload := p3DecodeObject(t, setModel)
	if setPayload["confirmation"] != "observed" {
		t.Fatalf("set model response=%#v", setPayload)
	}
	setting, found, err := app.settingsStore.Get(webConversationActiveProviderSetting)
	if err != nil || !found || setting.Value != "provider-a" {
		t.Fatalf("active provider found=%v value=%#v err=%v", found, setting.Value, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(frame.ID)
	if err != nil || !found || webConversationModelSelectionFromContext(metadata.ContextData) != "model-b" {
		t.Fatalf("conversation model metadata found=%v metadata=%#v err=%v", found, metadata, err)
	}

	foreignProvider, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "provider-foreign", UserID: "foreign", Name: "Foreign", Type: "openai-compatible",
		BaseURL: "https://models.example.test/v1", Model: "foreign-model", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign := p3JSONRequest(t, app, http.MethodPut, "/api/conversations/"+frame.ID+"/config-options/model", map[string]any{
		"value": foreignProvider.Model,
	}, "")
	if foreign.Code != http.StatusNotFound {
		t.Fatalf("foreign model status=%d body=%s", foreign.Code, foreign.Body.String())
	}
}

func TestP3WebConversationPermissionSelectorIsSoleRuntimeAuthority(t *testing.T) {
	app, store := newP3WebConversationServer(t)
	project := createP3Project(t, store, "permission-authority-project", "local")
	if _, err := app.settingsStore.Set(webAssistantConfigKey("local", "OPERON"), map[string]any{
		"defaults": map[string]any{
			"permission": map[string]any{"mode": "fixed", "value": "allow"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	created := p3JSONRequest(t, app, http.MethodPost, "/api/conversations", map[string]any{
		"name":      "Permission authority",
		"assistant": map[string]any{"id": "synonbiomed:OPERON"},
		"extra":     map[string]any{"project_id": project.ID, "project_name": project.Name},
	}, "local")
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	conversationID := webString(p3DecodeObject(t, created)["id"])
	if conversationID == "" {
		t.Fatal("created conversation has no id")
	}

	allow := p3JSONRequest(t, app, http.MethodPut, "/api/conversations/"+conversationID+"/config-options/mode", map[string]any{
		"value": "bypassPermissions",
	}, "local")
	if allow.Code != http.StatusOK {
		t.Fatalf("allow status=%d body=%s", allow.Code, allow.Body.String())
	}
	if got, err := app.webSessionApprovalMode(conversationID); err != nil || got != "allow" {
		t.Fatalf("allow runtime mode=%q err=%v", got, err)
	}

	defaultMode := p3JSONRequest(t, app, http.MethodPut, "/api/conversations/"+conversationID+"/config-options/mode", map[string]any{
		"value": "default",
	}, "local")
	if defaultMode.Code != http.StatusOK {
		t.Fatalf("default status=%d body=%s", defaultMode.Code, defaultMode.Body.String())
	}
	defaultPayload := p3DecodeObject(t, defaultMode)
	options, _ := defaultPayload["config_options"].([]any)
	permissionOption := map[string]any{}
	for _, raw := range options {
		option, _ := raw.(map[string]any)
		if option["id"] == "mode" {
			permissionOption = option
			break
		}
	}
	if webString(permissionOption["current_value"]) != "default" {
		t.Fatalf("permission option after default=%#v", permissionOption)
	}
	if got, err := app.webSessionApprovalMode(conversationID); err != nil || got != "ask" {
		t.Fatalf("default runtime mode=%q err=%v", got, err)
	}
	metadata, found, err := store.GetFrameRuntimeMetadata(conversationID)
	if err != nil || !found {
		t.Fatalf("metadata found=%v err=%v", found, err)
	}
	assistant, _ := metadata.ContextData["web_assistant"].(map[string]any)
	overrides, _ := assistant["conversation_overrides"].(map[string]any)
	if webString(overrides["permission"]) != "default" {
		t.Fatalf("default permission override was not persisted: %#v", overrides)
	}
}

func newP3WebConversationServer(t *testing.T) (*Server, *workspace.Store) {
	t.Helper()
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close conversation fixture workspace: %v", err)
		}
	})
	app := newV11TestServer(t, Options{Workspace: store, FileRoot: t.TempDir()})
	// Cleanup is LIFO: join server-owned MCP refreshes and other background
	// workers before the borrowed workspace and fixture runtime stores close.
	t.Cleanup(func() { closeTestServer(t, app) })
	return app, store
}

func createP3Project(t *testing.T, store *workspace.Store, projectID, userID string) workspace.CompatibilityProject {
	t.Helper()
	project, _, err := store.CreateCompatibilityProject(workspace.CreateCompatibilityProjectInput{
		ID: projectID, UserID: userID, Name: "P3 Project",
	})
	if err != nil {
		t.Fatal(err)
	}
	return project
}

func p3JSONRequest(
	t *testing.T,
	app *Server,
	method string,
	path string,
	body any,
	userID string,
) *httptest.ResponseRecorder {
	t.Helper()
	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = bytes.NewReader(raw)
	}
	request := httptest.NewRequest(method, path, requestBody)
	request.RemoteAddr = "127.0.0.1:12345"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if userID != "" {
		request.Header.Set("X-Synon-User-Id", userID)
	}
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	return response
}

func p3DecodeObject(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response status=%d body=%s: %v", response.Code, response.Body.String(), err)
	}
	return payload
}
