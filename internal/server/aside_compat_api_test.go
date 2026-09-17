package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestTranscriptCompatibilityCreateAsideUsesSingleAuthority(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-aside", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "parent-aside", ProjectID: "project-aside", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Parent",
	}); err != nil {
		t.Fatal(err)
	}
	parentStream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:parent-aside", OwnerID: "local", ExternalID: "parent-aside", SessionID: "parent-aside",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-aside",
		RootFrameID: "parent-aside", FrameID: "parent-aside", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: parentStream.UID, OwnerID: "local", ClientMessageID: "parent-input",
		FrameEventID: "parent-input-event", MessageUUID: "parent-input-message", Text: "Parent request",
		RuntimeConfig: map[string]any{"model": "parent-transcript-model", "gpu_mode": "on"},
	}); err != nil || !created {
		t.Fatalf("parent input created=%t err=%v", created, err)
	}
	if err := store.SetFrameSubmissionMetadata("parent-aside", map[string]any{
		"request": "Parent request", "gpu_mode": "off",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("parent-aside", workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_model": "forged-legacy-model", "_original_input": map[string]any{"gpu_mode": "off"},
		"_pending_input_requests": []any{map[string]any{"tool_id": "parent-answer"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: parentStream.UID, OwnerID: "local", ClientMessageID: "parent-response",
		FrameEventID: "parent-response-event", MessageUUID: "parent-response-message", Text: "Parent response",
		MessageOrigin: "input_response",
		RuntimeConfig: map[string]any{"model": "parent-transcript-model", "gpu_mode": "on"},
	}); err != nil || !created {
		t.Fatalf("parent response created=%t err=%v", created, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: parentStream.UID, OwnerID: "local", RunnerID: "parent-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("parent claim=%#v err=%v", claim, err)
	}
	if _, _, created, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), transcriptstore.AppendEventInput{
		Claim: claim.Claim, ClientMessageID: "parent-answer", Type: "assistant_message",
		Source:      transcriptstore.EventSourcePayload,
		PayloadJSON: []byte(`{"role":"assistant","_uuid":"parent-answer","text":"Parent context","content":[{"type":"thinking","thinking":"private"},{"type":"text","text":"Parent context"},{"type":"tool_use","id":"parent-answer","name":"ask_user","input":{"question":"Continue?"}}]}`),
	}); err != nil || !created {
		t.Fatalf("parent assistant created=%t err=%v", created, err)
	}
	if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claim.Claim, ClientMessageID: "parent-finished", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil || !created {
		t.Fatalf("parent finish created=%t err=%v", created, err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "parent-aside", Type: "assistant_message",
		Payload: map[string]any{"role": "assistant", "_uuid": "forged-frame-answer", "content": "Forged frame-only context"},
	}); err != nil {
		t.Fatal(err)
	}
	server := newV11TestServer(t, Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	response := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/parent-aside/aside", "local", map[string]any{
		"request": "Investigate the transcript path.", "model": "aside-model",
		"intent_id": "22222222-2222-4222-8222-222222222222",
	}, http.StatusOK)
	asideID, _ := response["frame_id"].(string)
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", asideID)
	if err != nil || !found || stream.FrameID != asideID || stream.RootFrameID != asideID {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	projected, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", Limit: 20,
	})
	var firstMessage, secondMessage map[string]any
	if len(projected) >= 2 {
		_ = json.Unmarshal(projected[0].ResolvedPayloadJSON, &firstMessage)
		_ = json.Unmarshal(projected[1].ResolvedPayloadJSON, &secondMessage)
	}
	var thirdMessage, fourthMessage map[string]any
	if len(projected) >= 3 {
		_ = json.Unmarshal(projected[2].ResolvedPayloadJSON, &thirdMessage)
	}
	if len(projected) == 4 {
		_ = json.Unmarshal(projected[3].ResolvedPayloadJSON, &fourthMessage)
	}
	if err != nil || len(projected) != 4 || projected[0].Event.Type != "history_user_message" ||
		projected[1].Event.Type != "user_input_response" ||
		projected[2].Event.Type != "history_assistant_message" || projected[3].Event.Type != "user_message" ||
		projected[0].Event.Source != transcriptstore.EventSourcePayload ||
		projected[1].Event.Source != transcriptstore.EventSourcePayload ||
		projected[2].Event.Source != transcriptstore.EventSourcePayload ||
		projected[3].Event.Source != transcriptstore.EventSourcePayload ||
		runnerMessageText(firstMessage) != "Parent request" || runnerMessageText(secondMessage) != "Parent response" ||
		runnerMessageText(thirdMessage) != "Parent context" ||
		runnerMessageText(fourthMessage) != "Investigate the transcript path." {
		t.Fatalf("projected=%#v err=%v", projected, err)
	}
	blocks, _ := thirdMessage["content"].([]any)
	if len(blocks) != 2 || blocks[0].(map[string]any)["type"] != "text" || blocks[1].(map[string]any)["type"] != "tool_use" {
		t.Fatalf("canonical rich blocks=%#v", blocks)
	}
	var genesisCount, frameReferenceCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_payload_genesis_receipts WHERE stream_uid=?`, stream.UID).
		Scan(&genesisCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND source='frame_ref'`, stream.UID).
		Scan(&frameReferenceCount); err != nil {
		t.Fatal(err)
	}
	if genesisCount != 1 || frameReferenceCount != 0 {
		t.Fatalf("genesis receipts=%d frame references=%d", genesisCount, frameReferenceCount)
	}
	config, found, err := repo.LatestFrameRuntimeConfig(context.Background(), stream.UID, "local")
	if err != nil || !found || config["agentName"] != "OPERON" || config["model"] != "aside-model" || config["gpu_mode"] != "on" {
		t.Fatalf("config=%#v found=%t err=%v", config, found, err)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, "local")
	if err != nil || !found || intent.Text != "Investigate the transcript path." {
		t.Fatalf("intent=%#v found=%t err=%v", intent, found, err)
	}
	if entries, err := server.eventJournal.ReadAll(asideID); err != nil || len(entries) != 0 {
		t.Fatalf("legacy journal=%#v err=%v", entries, err)
	}
	if legacy, found, err := server.sessionStore.Get(asideID); err != nil || found {
		t.Fatalf("legacy session=%#v found=%t err=%v", legacy, found, err)
	}
	seedAnsweredTaskIntake(t, server, "local", asideID)
	runner, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: asideID, RunnerID: "transcript-aside-runner",
		Endpoint: "builtin://synon-go/deterministic-chat", Model: "synon-go-deterministic",
		LeaseTTL: time.Minute, ReplayLimit: 20, OutputLimitBytes: 64 * 1024,
	})
	if err != nil || !runner.Claimed || runner.Status != "completed" {
		terminal, _ := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
			StreamUID: stream.UID, OwnerID: "local", Limit: 50,
		})
		diagnostics := make([]map[string]any, 0, len(terminal))
		for _, event := range terminal {
			if event.Event.Type != "runner_checkpoint" && event.Event.Type != "runner_finished" {
				continue
			}
			payload := map[string]any{}
			_ = json.Unmarshal(event.ResolvedPayloadJSON, &payload)
			diagnostics = append(diagnostics, map[string]any{
				"eventId": event.Event.EventID, "type": event.Event.Type, "payload": payload,
			})
		}
		t.Fatalf("runner=%#v err=%v diagnostics=%#v", runner, err, diagnostics)
	}
	if entries, err := server.eventJournal.ReadAll(asideID); err != nil || len(entries) != 0 {
		t.Fatalf("post-run legacy journal=%#v err=%v", entries, err)
	}
	if legacy, found, err := server.sessionStore.Get(asideID); err != nil || found {
		t.Fatalf("post-run legacy session=%#v found=%t err=%v", legacy, found, err)
	}
	history, authoritative, err := server.loadTranscriptWebHistory(context.Background(), "local", asideID)
	var parentContent, contextContent, taskContent map[string]any
	if len(history) >= 3 {
		parentContent, _ = history[0]["content"].(map[string]any)
		contextContent, _ = history[1]["content"].(map[string]any)
		taskContent, _ = history[2]["content"].(map[string]any)
	}
	if err != nil || !authoritative || len(history) != 4 || stringValue(parentContent["content"]) != "Parent request" ||
		stringValue(contextContent["content"]) != "Parent context" ||
		stringValue(taskContent["content"]) != "Investigate the transcript path." || history[3]["position"] != "left" {
		t.Fatalf("history=%#v authoritative=%t err=%v", history, authoritative, err)
	}
}

func TestTranscriptCompatibilityCreateAsideRollsBackOnTranscriptFailure(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-aside-fail", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "parent-aside-fail", ProjectID: "project-aside-fail", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Parent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:parent-aside-fail", OwnerID: "local", ExternalID: "parent-aside-fail", SessionID: "parent-aside-fail",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-aside-fail",
		RootFrameID: "parent-aside-fail", FrameID: "parent-aside-fail", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER reject_aside_transcript BEFORE INSERT ON transcript_streams
		BEGIN SELECT RAISE(FAIL, 'forced transcript failure'); END`); err != nil {
		t.Fatal(err)
	}
	server := newV11TestServer(t, Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	failure := compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/parent-aside-fail/aside", "local", map[string]any{
		"request": "This must roll back.", "intent_id": "33333333-3333-4333-8333-333333333333",
	}, http.StatusInternalServerError)
	if strings.Contains(stringValue(failure["detail"]), "forced transcript failure") {
		t.Fatalf("failure leaked storage detail: %#v", failure)
	}
	var frames, intents, streams int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frames WHERE project_id='project-aside-fail'`).Scan(&frames); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM queued_user_messages WHERE intent_id='33333333-3333-4333-8333-333333333333'`).Scan(&intents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_streams
		WHERE project_id='project-aside-fail' AND frame_id!='parent-aside-fail'`).Scan(&streams); err != nil {
		t.Fatal(err)
	}
	if frames != 1 || intents != 0 || streams != 0 {
		t.Fatalf("rollback frames=%d intents=%d streams=%d", frames, intents, streams)
	}
}

func TestTranscriptCompatibilityCreateAsideFailsClosedWithoutParentStream(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-aside-missing", UserID: "local", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "parent-aside-missing", ProjectID: "project-aside-missing", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent", Name: "Parent",
	}); err != nil {
		t.Fatal(err)
	}
	server := newV11TestServer(t, Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/parent-aside-missing/aside", "local", map[string]any{
		"request": "Must not use legacy metadata.",
	}, http.StatusInternalServerError)
	var frames int
	if err := db.QueryRow(`SELECT COUNT(*) FROM frames WHERE project_id='project-aside-missing'`).Scan(&frames); err != nil {
		t.Fatal(err)
	}
	if frames != 1 {
		t.Fatalf("missing parent stream created child frames=%d", frames-1)
	}
}

func TestCompatibilityCreateAsideCreatesRunnableRootAndConsumesItWithRealRunner(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project", UserID: "local", Name: "Project",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "parent", ProjectID: "project", AgentName: "OPERON",
		Status: "awaiting_user_response", ConversationType: "agent", Name: "Parent",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFrameSubmissionMetadata("parent", map[string]any{
		"request": "Parent request", "gpu_mode": "off",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("parent", workspace.FrameRuntimeMetadata{ContextData: map[string]any{
		"_model":                  "parent-model",
		"_pending_input_requests": []any{map[string]any{"tool_id": "ask-1"}},
		"_original_input":         map[string]any{"gpu_mode": "on"},
	}}); err != nil {
		t.Fatal(err)
	}
	for _, message := range []map[string]any{
		{
			"role": "assistant", "_uuid": "answer",
			"content": []any{map[string]any{"type": "text", "text": "Parent context"}},
		},
		{
			"role": "assistant", "_uuid": "tool",
			"content": []any{map[string]any{"type": "tool_use", "id": "ask-1", "name": "ask_user"}},
		},
		{
			"role": "user", "_uuid": "pending",
			"content": []any{map[string]any{
				"type": "tool_result", "tool_use_id": "ask-1",
				"content": `{"status":"awaiting_user_response"}`,
			}},
		},
	} {
		if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: "parent", Type: "user_message", Payload: message,
		}); err != nil {
			t.Fatal(err)
		}
	}
	server := newV11TestServer(t, Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	intentID := "11111111-1111-4111-8111-111111111111"
	response := compatJSONRequest(t, app, http.MethodPost, "/api/frames/parent/aside", "local", map[string]any{
		"request": "Investigate the independent path.",
		"model":   "new-model", "intent_id": intentID,
	}, http.StatusOK)
	if len(response) != 2 || response["prefix_len"] != float64(1) {
		t.Fatalf("aside response = %#v", response)
	}
	asideID, _ := response["frame_id"].(string)
	if asideID == "" {
		t.Fatalf("aside id = %#v", response)
	}
	aside, found, err := store.GetCompatibilityFrame(asideID)
	if err != nil || !found || aside.RootFrameID != asideID || aside.ParentFrameID != "" ||
		!aside.IsHidden || !strings.HasPrefix(aside.Name, "Aside ") {
		t.Fatalf("aside frame = %#v, found=%t, err=%v", aside, found, err)
	}
	parentChildren, err := store.ListFramesForRoot("parent")
	if err != nil || len(parentChildren) != 1 {
		t.Fatalf("aside must not be a parent child: %#v, err=%v", parentChildren, err)
	}
	page, err := store.CompatibilityFrameMessages(asideID, 0, 20)
	if err != nil || len(page.Messages) != 2 ||
		runnerMessageText(page.Messages[0]) != "Parent context" ||
		runnerMessageText(page.Messages[1]) != "Investigate the independent path." {
		t.Fatalf("aside messages = %#v, err=%v", page, err)
	}
	entries, err := server.eventJournal.ReadAll(asideID)
	if err != nil || len(entries) != 2 ||
		runnerMessageText(entries[0].Message) != "Parent context" ||
		runnerMessageText(entries[1].Message) != "Investigate the independent path." {
		t.Fatalf("aside journal = %#v, err=%v", entries, err)
	}
	session, found, err := server.sessionStore.Get(asideID)
	if err != nil || !found || session.Runner != nil || session.LastRole != "user" ||
		session.MessageCount != 2 {
		t.Fatalf("aside session = %#v, found=%t, err=%v", session, found, err)
	}
	config := session.Orchestration["sessionConfig"].(map[string]any)
	if config["agentName"] != "OPERON" || config["model"] != "new-model" || config["gpu_mode"] != "on" {
		t.Fatalf("aside config = %#v", config)
	}
	backlog, err := server.sessionStore.RunnerBacklog(sessionstore.RunnerBacklogOptions{
		ProjectID: "project", State: "pending",
	})
	if err != nil || backlog.Returned != 1 || backlog.Items[0].SessionID != asideID {
		t.Fatalf("aside runner backlog = %#v, err=%v", backlog, err)
	}
	if _, err := server.eventJournal.Append(asideID, eventjournal.Message{
		"type": "message", "role": "user", "text": "Use the recommended scope.",
		"messageOrigin": "input_response",
	}, eventjournal.Metadata{ClientMessageID: "aside-intake-answer"}); err != nil {
		t.Fatal(err)
	}
	runner, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: asideID, RunnerID: "aside-runner",
		Endpoint: "builtin://synon-go/deterministic-chat",
		Model:    "synon-go-deterministic", LeaseTTL: time.Minute,
		ReplayLimit: 20, OutputLimitBytes: 64 * 1024,
	})
	if err != nil || !runner.Claimed || runner.SessionID != asideID ||
		runner.Status != "completed" || runner.AssistantEventID == 0 {
		t.Fatalf("aside runner = %#v, err=%v", runner, err)
	}
	restarted := newV11TestServer(t, Options{FileRoot: root, Workspace: store})
	replayed, err := restarted.eventJournal.ReadAll(asideID)
	if err != nil || len(replayed) < 4 ||
		replayed[len(replayed)-1].Message["type"] != "runner_finished" {
		t.Fatalf("restarted aside journal = %#v, err=%v", replayed, err)
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/parent/aside", "local", map[string]any{
		"request": "Duplicate.", "intent_id": intentID,
	}, http.StatusConflict)
}

func TestCompatibilityCreateAsideSessionAndRootOnlyErrors(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project", UserID: "local", Name: "Project",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "parent", ProjectID: "project", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "child", ProjectID: "project", ParentFrameID: "parent",
		AgentName: "OPERON", Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	server := newV11TestServer(t, Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	response := compatJSONRequest(t, app, http.MethodPost, "/api/frames/parent/aside", "local", map[string]any{
		"request": "Visible fork", "as_session": true,
	}, http.StatusOK)
	sessionID, _ := response["frame_id"].(string)
	session, found, err := store.GetCompatibilityFrame(sessionID)
	if err != nil || !found || session.IsHidden || session.Name != "Visible fork" ||
		session.TaskSummary != "Visible fork" {
		t.Fatalf("visible aside session = %#v, found=%t, err=%v", session, found, err)
	}
	page, err := store.CompatibilityFrameMessages(sessionID, 0, 20)
	if err != nil || len(page.Messages) != 2 ||
		page.Messages[0]["_harness_notice"] != true ||
		runnerMessageText(page.Messages[1]) != "Visible fork" {
		t.Fatalf("visible aside messages = %#v, err=%v", page, err)
	}
	asideError := compatJSONRequest(t, app, http.MethodPost, "/api/frames/child/aside", "local", map[string]any{
		"request": "Invalid aside",
	}, http.StatusBadRequest)
	if asideError["detail"] != "Aside parent must be a root frame" {
		t.Fatalf("child aside error = %#v", asideError)
	}
	forkError := compatJSONRequest(t, app, http.MethodPost, "/api/frames/child/aside", "local", map[string]any{
		"request": "Invalid session", "as_session": true,
	}, http.StatusBadRequest)
	if forkError["detail"] != "Fork source must be a root frame" {
		t.Fatalf("child session error = %#v", forkError)
	}
	intentError := compatJSONRequest(t, app, http.MethodPost, "/api/frames/parent/aside", "local", map[string]any{
		"request": "Bad intent", "intent_id": "not-a-uuid",
	}, http.StatusBadRequest)
	if !strings.Contains(stringValue(intentError["detail"]), "intent_id must be a UUID") {
		t.Fatalf("intent error = %#v", intentError)
	}
}
