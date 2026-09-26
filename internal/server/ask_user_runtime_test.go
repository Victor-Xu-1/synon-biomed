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

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAskUserRecoveryDoesNotParkAnUnsourcedOperationalChoice(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	run := &sessionRunnerChatRun{
		SessionID:        fixture.stream.SessionID,
		CorrectionReason: sessionRunnerToolRoundNoProgressReasonCode,
		Transcript:       &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}
	first := managedExecutionSafeAskUserOptionFields(
		"Create another environment", "Create a new execution environment.",
		"Keeps dependencies separate.", "Requires time and storage.",
	)
	first["recommended"] = true
	second := managedExecutionSafeAskUserOptionFields(
		"Retry the existing environment", "Retry installation in the current environment.",
		"Reuses the environment.", "May fail again.",
	)
	second["recommended"] = false
	result, err := fixture.server.executeAgentAskUserQuestion(
		withTranscriptRunnerChatRun(context.Background(), run), run.SessionID,
		"unsourced-recovery-choice", "ask_user", map[string]any{
			"question": "Which environment should be used after the failed tool call?",
			"header":   "Environment choice", "options": []any{first, second},
		},
	)
	if err != nil || stringValue(mapValue(result)["code"]) != "agent_owned_decision" ||
		mapValue(result)["executed"] != false {
		t.Fatalf("unsourced recovery reached the user: result=%#v err=%v", result, err)
	}
}

func TestAgentRuntimeGatewayCanonicalizesAskUserBeforePolicyAndAudit(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "allow"}); err != nil {
		t.Fatal(err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"AskUserQuestion"}, sessionID: "standalone-ask-session"}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "ask-alias-call", Name: "ask_user_question",
		Arguments: json.RawMessage(`{"questions":[{"question":"Continue?","header":"Choice","options":[{"label":"Yes","description":"Continue the current route.","pros":"Preserves progress.","cons":"Uses the current route.","readiness":"The request does not attest execution readiness.","readiness_status":"unverified","decision_evidence":["user-input:current-task"],"readiness_evidence":[],"selection_basis":"user_objective","expected_outcome":"Execution resumes from the current checkpoint.","selection_rationale":"Recommended when the user wants the current route to continue.","recommended":true},{"label":"No","description":"Stop before further execution.","pros":"Avoids further work.","cons":"Leaves the task incomplete.","readiness":"Stopping does not require execution readiness.","readiness_status":"not_applicable","decision_evidence":["user-input:current-task"],"readiness_evidence":[],"selection_basis":"user_objective","expected_outcome":"Execution remains stopped.","selection_rationale":"Choose when the task should not continue.","recommended":false}]}]}`),
	})
	if err != nil || result.Value == nil {
		t.Fatalf("gateway result=%#v err=%v", result, err)
	}
	entries, err := srv.runtimeStore.List(toolGatewayAuditNamespace)
	if err != nil {
		t.Fatal(err)
	}
	audit := findToolGatewayAuditValue(entries, "agent-runtime", "completed")
	if audit == nil || audit["tool"] != "ask_user" {
		t.Fatalf("canonical gateway audit = %#v", audit)
	}
	invalid, err := gateway.Execute(context.Background(), agentruntime.ToolCall{ID: "ask-invalid-call", Name: " ASK_USER", Arguments: json.RawMessage(`{}`)})
	if err != nil || invalid.Value.(map[string]any)["error"] != "tool name is invalid" {
		t.Fatalf("invalid gateway result=%#v err=%v", invalid, err)
	}
}

func TestAskUserRejectsIncompleteQuestionShapeWithReparableFeedback(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	for name, input := range map[string]map[string]any{
		"fragmentary_text": {
			"question": "本", "header": "指", "options": []any{
				map[string]any{"label": "全", "description": "全部范围"},
				map[string]any{"label": "仅", "description": "聚焦范围"},
			},
		},
		"missing_header": {
			"questions": []any{map[string]any{"question": "Continue?", "options": []any{
				map[string]any{"label": "Yes", "description": "Continue."}, map[string]any{"label": "No", "description": "Stop."},
			}}},
		},
		"options_not_array": {
			"questions": []any{map[string]any{"question": "Continue?", "header": "Choice", "options": "Yes"}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := srv.executeAskUserQuestionTool("ask_user", input)
			if err == nil || !strings.Contains(err.Error(), "ask_user.") {
				t.Fatalf("invalid ask_user input must identify the field to repair: %v", err)
			}
		})
	}
}

func TestAskUserRejectsMultiQuestionBatch(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	question := func(text, header, labelA, labelB string) map[string]any {
		return map[string]any{
			"question": text,
			"header":   header,
			"options": []any{
				map[string]any{"label": labelA, "description": "选择该项。"},
				map[string]any{"label": labelB, "description": "选择另一项。"},
			},
		}
	}
	_, err := srv.executeAskUserQuestionTool("ask_user", map[string]any{
		"questions": []any{
			question("第一问？", "第一问", "甲", "乙"),
			question("第二问？", "第二问", "丙", "丁"),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("ask_user admitted a multi-question round: %v", err)
	}
}

func TestFrameAskUserWithoutTranscriptAuthorityFailsBeforeLegacyMutation(t *testing.T) {
	store, _, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-ask-authority", "frame-ask-authority")
	srv := New(Options{FileRoot: t.TempDir(), Workspace: store})
	questions := []any{map[string]any{
		"question": "Continue?", "header": "Choice",
		"options": []any{
			askUserDecisionOption(
				"Continue", "Continue the verified route.", "Preserves progress.", "Uses the current route.",
				"Ready from the current checkpoint.", []any{"checkpoint:ask-authority"}, "No additional resources.",
				"Execution resumes from the current checkpoint.", "Recommended when the current route remains valid.", true,
			),
			askUserDecisionOption(
				"Stop", "Stop before further execution.", "Avoids further work.", "Leaves the task incomplete.",
				"Ready immediately.", []any{"checkpoint:ask-authority"}, "No additional resources.",
				"Execution remains stopped.", "Choose when the task should not continue.", false,
			),
		},
	}}
	for _, alias := range []string{"ask_user", "AskUserQuestion", "ask_user_question"} {
		_, err := srv.executeAgentAskUserQuestion(context.Background(), "frame-ask-authority", "ask-1", alias, map[string]any{
			"questions": questions,
		})
		if err == nil || !strings.Contains(err.Error(), "transcript AskUser authority is required") {
			t.Fatalf("alias=%q err=%v", alias, err)
		}
	}
	frame, found, err := store.GetFrame("frame-ask-authority")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	messages, err := store.CompatibilityFrameMessages("frame-ask-authority", 0, 20)
	if err != nil || len(messages.Messages) != 0 {
		t.Fatalf("messages=%#v err=%v", messages, err)
	}
	result, err := srv.executeAgentAskUserQuestion(context.Background(), "standalone-session", "ask-2", "AskUserQuestion", map[string]any{
		"questions": questions,
	})
	if err != nil || result == nil {
		t.Fatalf("standalone result=%#v err=%v", result, err)
	}
}

func TestTranscriptRunnerAskUserPausesUntilNewInputThenResumesCheckpoint(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-ask", "frame-ask")
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if sequence == 1 {
			tools, _ := payload["tools"].([]any)
			encodedTools, _ := json.Marshal(tools)
			if !strings.Contains(string(encodedTools), `"name":"ask_user"`) ||
				strings.Contains(string(encodedTools), `"name":"AskUserQuestion"`) ||
				strings.Contains(string(encodedTools), `"name":"ask_user_question"`) {
				t.Fatalf("model AskUser tool schema=%s", encodedTools)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"I will ask before selecting the structure.","tool_calls":[{"id":"ask-structure","type":"function","function":{"name":"ask_user","arguments":"{\"questions\":[{\"question\":\"Which structure?\",\"header\":\"Structure\",\"options\":[{\"label\":\"Structure A\",\"description\":\"Use the primary structure.\",\"pros\":\"Best aligned with the stated objective.\",\"cons\":\"Does not test the alternate conformation.\",\"readiness\":\"Structure availability has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Analysis using the primary structure if verified.\",\"selection_rationale\":\"Recommended only for alignment with the stated objective.\",\"recommended\":true},{\"label\":\"Structure B\",\"description\":\"Use the alternate structure.\",\"pros\":\"Tests an alternate conformation.\",\"cons\":\"Availability is not established by the task statement.\",\"readiness\":\"Structure availability has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Analysis using the alternate structure if verified.\",\"selection_rationale\":\"Choose when conformational diversity is the user-owned priority.\",\"recommended\":false}]}]}"}}]}}]}`))
			return
		}
		rawMessages, _ := json.Marshal(payload["messages"])
		toolResultFound := false
		messages, _ := payload["messages"].([]any)
		for _, item := range messages {
			message, _ := item.(map[string]any)
			if message["role"] != "tool" || message["tool_call_id"] != "ask-structure" {
				continue
			}
			content, _ := message["content"].(string)
			var result map[string]any
			if json.Unmarshal([]byte(content), &result) == nil && result["status"] == "answered" {
				answers, _ := result["answers"].(map[string]any)
				toolResultFound = answers["Which structure?"] == "Structure A"
			}
		}
		if sequence != 2 || !toolResultFound {
			t.Fatalf("resumed request %d messages=%s", sequence, rawMessages)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Using the selected structure."}}]}`))
	}))
	defer modelAPI.Close()

	srv := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	if _, _, err := srv.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-ask", MessageUUID: "message-1", ClientMessageID: "client-1", Text: "Analyze CRBN after asking me for the structure.",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{
		SessionID: "frame-ask", RunnerID: "ask-runner", Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "test-model",
		AllowedTools: []string{"ask_user"}, LeaseTTL: time.Minute, ReplayLimit: 100,
	}
	paused, err := srv.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || !paused.Claimed || paused.Status != "awaiting_user_response" || requests.Load() != 1 {
		t.Fatalf("paused=%#v requests=%d err=%v", paused, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-ask")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	pendingHistory, authoritative, err := srv.loadTranscriptWebHistory(context.Background(), "local", "frame-ask")
	if err != nil || !authoritative || len(pendingHistory) != 3 {
		t.Fatalf("pending history=%#v authoritative=%t err=%v", pendingHistory, authoritative, err)
	}
	progress, _ := pendingHistory[1]["content"].(map[string]any)
	if pendingHistory[1]["type"] != "text" ||
		progress["content"] != "I will ask before selecting the structure." {
		t.Fatalf("AskUser progress explanation=%#v", pendingHistory[1])
	}
	pendingAsk := requireAskUserHistoryMessage(t, pendingHistory, "ask-structure")
	pendingContent, _ := pendingAsk["content"].(map[string]any)
	pendingCompatibility, _ := pendingContent["_frame_compat"].(map[string]any)
	if pendingAsk["status"] != "work" || pendingContent["status"] != "running" ||
		pendingCompatibility["result_is_error"] != false {
		t.Fatalf("pending AskUser=%#v", pendingAsk)
	}
	pendingHistoryJSON, _ := json.Marshal(pendingHistory)
	audit, auditCreated, err := repo.AuditAskUserHistory(context.Background(), transcriptstore.AuditAskUserHistoryInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID,
		MaxEvents: 64, MaxCandidates: 16, MaxShadowRows: 64,
	})
	if err != nil || !auditCreated || audit.Status != transcriptstore.AskUserHistoryNativeV1 || len(audit.Comparisons) != 5 {
		t.Fatalf("history audit=%#v created=%t err=%v", audit, auditCreated, err)
	}
	pendingHistoryAfterAudit, authoritativeAfterAudit, err := srv.loadTranscriptWebHistory(context.Background(), "local", "frame-ask")
	pendingHistoryAfterAuditJSON, _ := json.Marshal(pendingHistoryAfterAudit)
	if err != nil || !authoritativeAfterAudit || string(pendingHistoryAfterAuditJSON) != string(pendingHistoryJSON) {
		t.Fatalf("shadow audit changed served history: before=%s after=%s authoritative=%t err=%v",
			pendingHistoryJSON, pendingHistoryAfterAuditJSON, authoritativeAfterAudit, err)
	}
	assertNoEmptyTranscriptTextMessages(t, pendingHistory)
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, 1)
	if err != nil || state.Status != "running" || state.Phase != transcriptstore.RunnerPhaseWaitingUser || state.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("paused runtime=%#v err=%v", state, err)
	}
	poll := options
	poll.SessionID = ""
	poll.RunnerID = "ask-poller"
	if idle, err := srv.RunSessionRunnerChatOnce(context.Background(), poll); err != nil || idle.Claimed || requests.Load() != 1 {
		t.Fatalf("pre-answer poll=%#v requests=%d err=%v", idle, requests.Load(), err)
	}
	resolved := compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/frame-ask/resolve-input", "local", map[string]any{
		"ultra_mode": true,
		"responses": []any{map[string]any{
			"tool_id": "ask-structure", "action": "answer",
			"answers": map[string]any{"Which structure?": "Structure A"},
		}},
	}, http.StatusOK)
	if resolved["status"] != "accepted" {
		t.Fatalf("resolve response=%#v", resolved)
	}
	if entries, err := srv.eventJournal.ReadAll("frame-ask"); err != nil || len(entries) != 0 {
		t.Fatalf("legacy resolution journal=%#v err=%v", entries, err)
	}
	runtimeConfig, found, err := repo.LatestFrameRuntimeConfig(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || runtimeConfig["agentName"] != "synon" || runtimeConfig["ultra_mode"] != true {
		t.Fatalf("runtime config=%#v found=%t err=%v", runtimeConfig, found, err)
	}
	if legacy, found, err := srv.sessionStore.Get("frame-ask"); err != nil || found {
		t.Fatalf("legacy runtime session=%#v found=%t err=%v", legacy, found, err)
	}
	resumingFrame, found, err := store.GetFrame("frame-ask")
	if err != nil || !found || resumingFrame.Status != "processing" {
		t.Fatalf("resuming frame=%#v found=%t err=%v", resumingFrame, found, err)
	}
	intent, found, err := repo.GetActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found || intent.Text != "Analyze CRBN after asking me for the structure." || intent.Revision != 1 {
		t.Fatalf("canonical task intent=%#v found=%t err=%v", intent, found, err)
	}
	if generic, err := srv.RunSessionRunnerChatOnce(context.Background(), poll); err != nil || generic.Claimed || requests.Load() != 1 {
		t.Fatalf("generic runner claimed dedicated resume dispatch: result=%#v requests=%d err=%v", generic, requests.Load(), err)
	}
	resumed, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "ask-resume-dispatch", ClaimTTL: time.Second, Chat: options,
	})
	if err != nil || !resumed.Claimed || !resumed.Runner.Claimed || resumed.Runner.Attempt != 1 ||
		resumed.Status != "completed" || requests.Load() != 2 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}
	resumedState, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, 1)
	if err != nil || resumedState.Status != "completed" || resumedState.ResumeSource != transcriptstore.ResumeSourceCheckpoint || resumedState.ResumeCheckpoint <= 0 {
		t.Fatalf("resumed runtime=%#v err=%v", resumedState, err)
	}
	if err := srv.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.transcriptWebReadModel = transcriptstore.NewWebReadModelRepository(db, db)
	if err := srv.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	branch, err := repo.GetBranchState(context.Background(), stream.UID, stream.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	projection := requireTranscriptWebReadModelFence(t, srv.transcriptWebReadModel, stream, branch.ActiveBranchID)
	if projection.StateStatus != "ready" || projection.StateThroughPublicationSequence != projection.ThroughPublicationSequence ||
		projection.StateSourceRevision != projection.SourceRevision {
		t.Fatalf("AskUser resume projection=%#v", projection)
	}
	stableProjection := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID)
	if err := srv.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := srv.runTranscriptWebReadModelCycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if after := readTranscriptWebProjectionClock(t, db, stream.UID, branch.ActiveBranchID); after != stableProjection {
		t.Fatalf("AskUser resume projection replay changed state before=%#v after=%#v", stableProjection, after)
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
	historyRequest := httptest.NewRequest(http.MethodGet, "/api/conversations/frame-ask/messages?limit=100", nil)
	historyRequest.Header.Set("X-Synon-User-Id", "local")
	historyResponse := httptest.NewRecorder()
	srv.Handler().ServeHTTP(historyResponse, historyRequest)
	if historyResponse.Code != http.StatusOK || !strings.Contains(historyResponse.Body.String(), "Analyze CRBN") ||
		!strings.Contains(historyResponse.Body.String(), "Structure A") || !strings.Contains(historyResponse.Body.String(), "Using the selected structure.") ||
		!strings.Contains(historyResponse.Body.String(), `"terminal_status":"completed"`) {
		t.Fatalf("history status=%d body=%s", historyResponse.Code, historyResponse.Body.String())
	}
	var historyPage struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(historyResponse.Body.Bytes(), &historyPage); err != nil {
		t.Fatal(err)
	}
	answeredAsk := requireAskUserHistoryMessage(t, historyPage.Items, "ask-structure")
	answeredContent, _ := answeredAsk["content"].(map[string]any)
	answeredCompatibility, _ := answeredContent["_frame_compat"].(map[string]any)
	if answeredAsk["status"] != "finish" || answeredContent["status"] != "completed" ||
		!strings.Contains(webString(answeredContent["output"]), `"status":"answered"`) ||
		answeredCompatibility["result_is_error"] != false {
		t.Fatalf("answered AskUser=%#v", answeredAsk)
	}
	leftTextIDs := make([]string, 0, 2)
	for _, message := range historyPage.Items {
		if message["type"] == "text" && message["position"] == "left" {
			leftTextIDs = append(leftTextIDs, webString(message["msg_id"]))
		}
	}
	wantBefore := transcriptAssistantMessageID("frame-ask", 1, 1)
	wantAfter := transcriptAssistantMessageID("frame-ask", 1, 2)
	if len(leftTextIDs) != 2 || leftTextIDs[0] != wantBefore || leftTextIDs[1] != wantAfter {
		snapshot, snapshotErr := repo.GetProjectionSnapshot(context.Background(), stream.UID, stream.OwnerID)
		projected, projectionErr := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Limit: 1000,
		})
		trace := make([]string, 0, len(projected))
		for _, event := range projected {
			switch event.Event.Type {
			case "content_delta", "assistant_message", "runner_checkpoint",
				transcriptstore.AskUserPromptEventType, transcriptstore.AskUserResultEventType:
				trace = append(trace, event.Event.Type+":"+string(event.ResolvedPayloadJSON))
			}
		}
		t.Fatalf("AskUser assistant segment ids=%#v want=[%q,%q] snapshot_err=%v projection_err=%v trace=%#v",
			leftTextIDs, wantBefore, wantAfter, snapshotErr, projectionErr, trace)
	}
	assertNoEmptyTranscriptTextMessages(t, historyPage.Items)
}

func requireAskUserHistoryMessage(t *testing.T, messages []map[string]any, callID string) map[string]any {
	t.Helper()
	var result map[string]any
	for _, message := range messages {
		content, _ := message["content"].(map[string]any)
		if message["type"] != "tool_call" || webString(content["name"]) != "ask_user" ||
			webString(content["call_id"]) != callID {
			continue
		}
		if result != nil {
			t.Fatalf("duplicate AskUser history messages: %#v", messages)
		}
		result = message
	}
	if result == nil {
		t.Fatalf("AskUser %q missing from history: %#v", callID, messages)
	}
	return result
}

func assertNoEmptyTranscriptTextMessages(t *testing.T, messages []map[string]any) {
	t.Helper()
	for _, message := range messages {
		if message["type"] != "text" {
			continue
		}
		content, _ := message["content"].(map[string]any)
		if strings.TrimSpace(transcriptPayloadText(content)) == "" {
			t.Fatalf("empty transcript text message: %#v", message)
		}
	}
}

func TestTranscriptAskUserRealtimeOutboxRecoversWithoutDirectPublish(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-ask-outbox", "frame-ask-outbox")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-ask-outbox", OwnerID: "local", ExternalID: "frame-ask-outbox",
		SessionID: "frame-ask-outbox", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-ask-outbox", RootFrameID: "frame-ask-outbox", FrameID: "frame-ask-outbox", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "ask-outbox-user",
		FrameEventID: "ask-outbox-user-frame", MessageUUID: "ask-outbox-message",
		MessageOrigin: "task_intent", Text: "Ask before continuing.", Destinations: []string{"ws"},
	}); err != nil || !created {
		t.Fatalf("user created=%t err=%v", created, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "ask-outbox-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	parked, _, err := store.ParkAskUserWithTranscript(context.Background(), workspace.ParkAskUserInput{
		FrameID: stream.FrameID, ToolID: "ask-outbox", ToolName: "AskUserQuestion",
		Questions: []any{map[string]any{
			"question": "Continue?", "header": "Choice",
			"options": []any{
				map[string]any{"label": "Continue", "description": "Continue the analysis."},
				map[string]any{"label": "Stop", "description": "Stop the analysis."},
			},
		}},
	}, transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "pause-ask-outbox",
		Phase: transcriptstore.RunnerPhaseWaitingUser, Resumable: true,
		PayloadJSON: []byte(`{"status":"awaiting_user_response"}`), Destinations: []string{"ws"},
	})
	if err != nil || len(parked.Events) != 3 {
		t.Fatalf("parked=%#v err=%v", parked, err)
	}
	if pending, err := store.CountUndeliveredRealtimeOutbox(context.Background()); err != nil || pending != 3 {
		t.Fatalf("pending outbox=%d err=%v", pending, err)
	}
	for _, event := range parked.Events {
		if durable, found, err := store.GetRealtimeEventByID("frame-event:" + event.ID); err != nil || found {
			t.Fatalf("event %q was directly published: %#v found=%t err=%v", event.ID, durable, found, err)
		}
	}
	srv := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	startServerRealtimeOutbox(t, store, srv)
	waitServerRealtimeOutbox(t, store)
	for _, source := range parked.Events {
		realtimeID := "frame-event:" + source.ID
		durable, found, err := store.GetRealtimeEventByID(realtimeID)
		if err != nil || !found || webString(durable.Payload["source_event_id"]) != source.ID ||
			durable.Type != baselineTypeForFrameEvent(source.Type) {
			t.Fatalf("recovered realtime=%#v found=%t err=%v", durable, found, err)
		}
		outboxEvent, err := store.GetOutboxEvent(context.Background(), workspace.DeriveOutboxEventID(
			workspace.RealtimeOutboxTopic, realtimeID,
		))
		if err != nil || outboxEvent.Status != workspace.OutboxStatusDelivered || outboxEvent.AttemptCount != 1 {
			t.Fatalf("outbox event=%#v err=%v", outboxEvent, err)
		}
	}
	confirmationID := "web-confirmation-add:" + stream.FrameID + ":" + webEventComponent("ask-outbox")
	confirmation, found, err := store.GetRealtimeEventByID(confirmationID)
	if err != nil || !found || confirmation.Type != "confirmation.add" || confirmation.Payload["id"] != "ask-outbox" {
		t.Fatalf("recovered confirmation=%#v found=%t err=%v", confirmation, found, err)
	}
	confirmationAdds := 0
	for _, event := range transcriptWebEvents(t, store, stream.OwnerID) {
		if event.Type == "confirmation.add" && event.ID == confirmationID {
			confirmationAdds++
		}
		if event.Type == "message.userCreated" && strings.TrimSpace(webString(event.Payload["content"])) == "" {
			t.Fatalf("recovery emitted an empty user message: %#v", event)
		}
		if event.Type == "message.stream" {
			streamType := strings.TrimSpace(webString(event.Payload["stream_type"]))
			if (streamType == "text" || streamType == "content") && strings.TrimSpace(webString(event.Payload["data"])) == "" {
				t.Fatalf("recovery emitted an empty stream message: %#v", event)
			}
		}
	}
	if confirmationAdds != 1 {
		t.Fatalf("confirmation.add count=%d", confirmationAdds)
	}
}

func TestTranscriptRunnerAskUserSurvivesProcessRestartBeforeResolve(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebFrame(t, store, "local", "project-ask-restart", "frame-ask-restart")

	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if sequence == 1 {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"ask-restart","type":"function","function":{"name":"AskUserQuestion","arguments":"{\"questions\":[{\"question\":\"Which branch?\",\"header\":\"Branch\",\"options\":[{\"label\":\"Alpha\",\"description\":\"Use the alpha branch.\",\"pros\":\"Preserves the requested direction.\",\"cons\":\"Does not test beta.\",\"readiness\":\"Branch state has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Execution attempts to resume with alpha.\",\"selection_rationale\":\"Recommended for continuity with the stated task.\",\"recommended\":true},{\"label\":\"Beta\",\"description\":\"Use the beta branch.\",\"pros\":\"Tests the alternate route.\",\"cons\":\"Requires switching branches.\",\"readiness\":\"Branch state has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Execution attempts to resume with beta.\",\"selection_rationale\":\"Choose when the alternate route is required.\",\"recommended\":false}]}]}"}}]}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Restarted with Alpha."}}]}`))
	}))
	t.Cleanup(modelAPI.Close)

	first := New(Options{FileRoot: t.TempDir(), Workspace: store, Transcript: repo})
	if _, _, err := first.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-ask-restart", MessageUUID: "message-ask-restart",
		ClientMessageID: "client-ask-restart", Text: "Ask before restart.",
	}); err != nil {
		t.Fatal(err)
	}
	chat := SessionRunnerChatOptions{
		SessionID: "frame-ask-restart", RunnerID: "ask-restart-runner",
		Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "test-model",
		AllowedTools: []string{"AskUserQuestion"}, LeaseTTL: time.Minute, ReplayLimit: 100,
	}
	paused, err := first.RunSessionRunnerChatOnce(context.Background(), chat)
	if err != nil || paused.Status != "awaiting_user_response" || requests.Load() != 1 {
		t.Fatalf("paused=%#v requests=%d err=%v", paused, requests.Load(), err)
	}
	closeContext, cancelClose := context.WithTimeout(context.Background(), time.Second)
	if err := first.Close(closeContext); err != nil {
		cancelClose()
		t.Fatal(err)
	}
	cancelClose()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restartedStore, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restartedStore.Close() })
	restartedRepo, err := restartedStore.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(Options{FileRoot: t.TempDir(), Workspace: restartedStore, Transcript: restartedRepo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = restarted.Close(ctx)
	})

	frame, found, err := restartedStore.GetCompatibilityFrame("frame-ask-restart")
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("restarted frame=%#v found=%t err=%v", frame, found, err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/frames/frame-ask-restart/resolve-input", nil)
	request.Header.Set("X-Synon-User-Id", "local")
	resolved, err := restarted.resolveCompatibilityInput(request, frame, compatibilityResolveInputRequest{Responses: []compatibilityInputResponse{{
		ToolID: "ask-restart", Action: "answer", Answers: map[string]string{"Which branch?": "Alpha"},
	}}})
	if err != nil || resolved.Status != "accepted" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	generic := chat
	generic.SessionID = ""
	generic.RunnerID = "generic-ask-restart-poller"
	claimedByGeneric, err := restarted.RunSessionRunnerChatOnce(context.Background(), generic)
	if err != nil || claimedByGeneric.Claimed {
		t.Fatalf("generic runner bypassed pending resume dispatch: result=%#v err=%v", claimedByGeneric, err)
	}
	chat.SessionID = ""
	chat.RunnerID = "ask-restart-poller"
	resumed, err := restarted.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "ask-restart-dispatch", ClaimTTL: time.Second, Chat: chat,
	})
	if err != nil || !resumed.Claimed || resumed.Status != "completed" || requests.Load() != 2 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}
}

func TestRunnerAskUserPausesPersistsAnswerAndResumes(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if sequence == 1 {
			_, _ = w.Write([]byte(`{
				"choices":[{"message":{"role":"assistant","tool_calls":[{
					"id":"ask-structure","type":"function","function":{
						"name":"AskUserQuestion",
							"arguments":"{\"questions\":[{\"question\":\"Which CRBN structure should be used?\",\"header\":\"Structure\",\"options\":[{\"label\":\"5FQD\",\"description\":\"Use the human CRBN complex.\",\"pros\":\"Matches the requested human-complex objective.\",\"cons\":\"Represents one conformation.\",\"readiness\":\"Structure availability has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Analysis using 5FQD if verified.\",\"selection_rationale\":\"Recommended for alignment with the requested species.\",\"recommended\":true},{\"label\":\"4TZ4\",\"description\":\"Use the alternate complex.\",\"pros\":\"Tests an alternate conformation.\",\"cons\":\"Does not match the primary species criterion as closely.\",\"readiness\":\"Structure availability has not been checked in this task.\",\"readiness_status\":\"unverified\",\"decision_evidence\":[\"user-input:current-task\"],\"readiness_evidence\":[],\"selection_basis\":\"user_objective\",\"expected_outcome\":\"Analysis using 4TZ4 if verified.\",\"selection_rationale\":\"Choose when conformational comparison is more important.\",\"recommended\":false}]}]}"
					}
				}]}}]
			}`))
			return
		}
		if sequence != 2 {
			t.Fatalf("unexpected model request %d", sequence)
		}
		rawMessages, _ := json.Marshal(payload["messages"])
		if !strings.Contains(string(rawMessages), "5FQD") || !strings.Contains(string(rawMessages), "answered") {
			t.Fatalf("resumed messages do not contain the durable answer: %s", rawMessages)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Using 5FQD as requested."}}]}`))
	}))
	t.Cleanup(modelAPI.Close)

	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "local", Name: "Project", Path: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing", ConversationType: "agent", Name: "CRBN analysis"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame", workspace.FrameRuntimeMetadata{ContextData: map[string]any{"web_extra": map[string]any{"backend": "synonbiomed"}}}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	if err := srv.sessionStore.Upsert(sessionstore.Session{
		ID: "frame", Title: "CRBN analysis", WorkDir: root, LastRole: "user", MessageCount: 1,
		Project: &sessionstore.Project{ID: "project", Name: "Project", Path: root, BoundAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame", MessageUUID: "user-1", ClientMessageID: "user-1", Text: "Analyze a CRBN structure.",
	}); err != nil {
		t.Fatal(err)
	}

	options := SessionRunnerChatOptions{
		SessionID: "frame", RunnerID: "ask-runner", Endpoint: modelAPI.URL + "/v1/chat/completions", Model: "test-model",
		AllowedTools: []string{"AskUserQuestion"}, LeaseTTL: time.Minute, ReplayLimit: 100,
	}
	paused, err := srv.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !paused.Claimed || paused.Status != "awaiting_user_response" {
		t.Fatalf("paused result = %+v", paused)
	}
	frame, found, err := store.GetCompatibilityFrame("frame")
	if err != nil || !found || frame.Status != "awaiting_user_response" {
		t.Fatalf("parked frame=%#v found=%v err=%v", frame, found, err)
	}
	confirmations, err := srv.webConversationPendingConfirmations("frame")
	if err != nil || len(confirmations) != 1 || confirmations[0]["id"] != "ask-structure" {
		t.Fatalf("confirmations=%#v err=%v", confirmations, err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/frames/frame/resolve-input", nil)
	request.Header.Set("X-Synon-User-Id", "local")
	resolved, err := srv.resolveCompatibilityInput(request, frame, compatibilityResolveInputRequest{Responses: []compatibilityInputResponse{{
		ToolID: "ask-structure", Action: "answer", Answers: map[string]string{"Which CRBN structure should be used?": "5FQD"},
	}}})
	if err != nil || resolved.Status != "accepted" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	resumed, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "ask-resume-worker", Chat: options,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Claimed || resumed.Status != "completed" || !resumed.Runner.Claimed || requests.Load() != 2 {
		t.Fatalf("resumed=%+v requests=%d", resumed, requests.Load())
	}
	history, found, err := srv.loadTranscriptWebHistory(context.Background(), "local", "frame")
	encodedHistory, _ := json.Marshal(history)
	if err != nil || !found || !strings.Contains(string(encodedHistory), "Using 5FQD as requested.") {
		t.Fatalf("history=%s found=%t err=%v", encodedHistory, found, err)
	}
}
