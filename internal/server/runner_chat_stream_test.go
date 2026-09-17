package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	eventjournal "synon-go/internal/persistence/journal"
	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type runnerStreamRoundTripFunc func(*http.Request) (*http.Response, error)

func (function runnerStreamRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestTranscriptRunnerResumesAfterProviderTransportEOFExhaustsRequestRetries(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-transport-eof", "frame-transport-eof")
	var requests atomic.Int64
	httpClient := &http.Client{Transport: runnerStreamRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		sequence := requests.Add(1)
		if sequence <= 4 {
			return nil, io.EOF
		}
		body := "data: {\"id\":\"transport-recovered\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"recovered after transport interruption\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir(), HTTPClient: httpClient})
	if _, err := server.settingsStore.Set("model.activeProviderId", "transport-eof-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "transport-eof-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "transport-eof-provider", UserID: "local", Name: "Transport EOF provider", Type: "openai",
		BaseURL: "https://provider.example.test/v1", Model: "test-model",
		SecretRef: "secret://transport-eof-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-transport-eof", MessageUUID: "message-transport-eof",
		ClientMessageID: "client-transport-eof", Text: "continue after a temporary provider transport interruption",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{
		SessionID: "frame-transport-eof", RunnerID: "transport-eof-runner-1", LeaseTTL: time.Minute,
		RequestTimeout: time.Second, MaxAttempts: 4, RequireSavedModel: true, DisableSkillDiscovery: true,
	}
	interrupted, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || interrupted.Status != "interrupted" ||
		interrupted.InterruptionReasonCode != sessionRunnerProviderTransportTemporaryReasonCode ||
		interrupted.FinishEventID != 0 || requests.Load() != 4 {
		t.Fatalf("interrupted=%#v requests=%d err=%v", interrupted, requests.Load(), err)
	}
	options.SessionID = ""
	options.RunnerID = "transport-eof-runner-2"
	completed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || completed.Status != "completed" || completed.FinishEventID == 0 || requests.Load() != 5 {
		t.Fatalf("completed=%#v requests=%d err=%v", completed, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-transport-eof")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	temporaryInterruptions, terminals := 0, 0
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		switch event.Event.Type {
		case "runner_checkpoint":
			if payload["reason_code"] == sessionRunnerProviderTransportTemporaryReasonCode {
				temporaryInterruptions++
			}
		case "runner_finished":
			terminals++
		}
	}
	if temporaryInterruptions != 1 || terminals != 1 {
		t.Fatalf("temporary interruptions=%d terminals=%d events=%#v", temporaryInterruptions, terminals, events)
	}
}

func TestParseProviderContinuationV1RejectsFractionalFence(t *testing.T) {
	payload := map[string]any{
		"provider_continuation": providerContinuationPayloadV1(sessionRunnerProviderContinuationV1{
			ContractVersion:                    sessionRunnerProviderContinuationContractVersion,
			StreamUID:                          "stream-1",
			OwnerID:                            "owner-1",
			BranchID:                           "branch-1",
			BranchGeneration:                   1,
			RootAttempt:                        1,
			RootSegmentOrdinal:                 1,
			RootStartedEventID:                 10,
			RootStartedPublicationSequence:     10,
			PreviousAttempt:                    1,
			CurrentSegmentOrdinal:              1,
			SegmentIndex:                       1,
			AcceptedThroughEventID:             11,
			AcceptedThroughPublicationSequence: 11,
			AcceptedSemanticBytes:              7,
			AcceptedSHA256:                     strings.Repeat("a", sha256.Size*2),
		}),
	}
	payload["provider_continuation"].(map[string]any)["segment_index"] = 1.5
	if _, present, err := parseProviderContinuationV1(payload); !present || err == nil {
		t.Fatalf("fractional continuation fence present=%v err=%v", present, err)
	}
}

func TestSessionRunnerChatPublishesConclusionOnlyAtTerminalSettlement(t *testing.T) {
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if payload.Model != "stream-model" || !payload.Stream {
			http.Error(w, "stream request required", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"id\":\"chat-stream-session\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"private reasoning must not be shown\"}}]}\n\n"))
		flusher.Flush()
		for index := range 16 {
			event := `{"id":"chat-stream-session","choices":[{"delta":{"content":"x"}}]}`
			if index == 0 {
				event = `{"id":"chat-stream-session","choices":[{"delta":{"role":"assistant","content":"x"}}]}`
			}
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: {\"id\":\"chat-stream-session\",\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":16,\"total_tokens\":24}}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	srv := New(Options{FileRoot: root, Workspace: workspaceStore})
	if _, err := workspaceStore.CreateProject(workspace.CreateProjectInput{
		ID: "project-stream", UserID: "user-1", Name: "Stream project", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.settingsStore.Set("model.activeProviderId", "stream-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.secretStore.Create(secretstore.Secret{
		ID: "stream-key", UserID: "user-1", Provider: "openai", Value: "stream-secret",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "stream-provider", UserID: "user-1", Name: "Stream provider", Type: "openai",
		BaseURL: modelAPI.URL + "/v1", Model: "stream-model",
		SecretRef: "secret://stream-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "session-stream", Title: "Streaming", WorkDir: root,
		CreatedAt: now, UpdatedAt: now, LastUserMessageAt: now,
		MessageCount: 1, LastRole: "user",
		Project: &sessionstore.Project{ID: "project-stream", Name: "Stream project", Path: root, BoundAt: now},
	}
	if err := srv.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.eventJournal.Append(session.ID, eventjournal.Message{
		"type": "message", "role": "user", "text": "stream this response",
	}, eventjournal.Metadata{ClientMessageID: "stream-user"}); err != nil {
		t.Fatal(err)
	}
	app := httptest.NewServer(srv.Handler())
	defer app.Close()
	wsContext, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	wsURL := strings.Replace(app.URL, "http://", "ws://", 1) +
		"/ws/" + url.PathEscape(session.ID) + "?lastEventId=1"
	connection, _, err := websocket.Dial(wsContext, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Synon-User-Id": []string{"user-1"}},
	})
	if err != nil {
		t.Fatalf("dial streaming session websocket: %v", err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "done")
	if connected := readWSMessage(t, wsContext, connection); connected["type"] != "connected" {
		t.Fatalf("connected = %#v", connected)
	}

	result, err := srv.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: session.ID, RunnerID: "stream-runner",
		Endpoint: "http://127.0.0.1:1/v1/chat/completions",
		Model:    "wrong-model", LeaseTTL: time.Minute, ReplayLimit: 50,
		OutputLimitBytes: 64 * 1024, RequestTimeout: time.Minute,
		MaxAttempts: 1, MaxToolRounds: 1, DisableSkillDiscovery: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || result.AssistantEventID == 0 || result.FinishEventID == 0 {
		failedEntries, _ := srv.eventJournal.ReadAfter(session.ID, 0, 100)
		t.Fatalf("result = %+v entries=%#v", result, failedEntries)
	}
	entries, err := srv.eventJournal.ReadAfter(session.ID, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	deltas := make([]eventjournal.Entry, 0)
	contentResets := 0
	var assistantEventID int64
	var finishEventID int64
	for _, entry := range entries {
		if entry.Message["type"] == "content_delta" {
			deltas = append(deltas, entry)
		}
		if entry.Message["type"] == "content_reset" {
			contentResets++
		}
		if entry.Message["type"] == "message" && entry.Message["role"] == "assistant" {
			assistantEventID = entry.EventID
		}
		if entry.Message["type"] == "runner_finished" {
			finishEventID = entry.EventID
		}
	}
	if len(deltas) != 0 || contentResets != 0 || assistantEventID <= 0 || finishEventID != assistantEventID+1 {
		t.Fatalf("final candidate escaped before terminal settlement: deltas=%#v resets=%d assistant=%d finish=%d entries=%#v",
			deltas, contentResets, assistantEventID, finishEventID, entries)
	}
	audits, err := srv.runtimeStore.List(sessionRunnerModelAuditRuntimeNamespace)
	if err != nil {
		t.Fatal(err)
	}
	audit := findSessionRunnerModelAuditValue(audits, session.ID)
	if audit == nil || audit["requestId"] != "chat-stream-session" || audit["totalTokens"] != float64(24) {
		t.Fatalf("audit = %#v", audit)
	}
	liveTypes := make([]string, 0, 3)
	for len(liveTypes) < 10 {
		readContext, cancelRead := context.WithTimeout(context.Background(), 2*time.Second)
		_, raw, readErr := connection.Read(readContext)
		cancelRead()
		if readErr != nil {
			t.Fatalf("stream websocket read after %#v: %v", liveTypes, readErr)
		}
		message := map[string]any{}
		if err := json.Unmarshal(raw, &message); err != nil {
			t.Fatalf("decode stream websocket message after %#v: %v raw=%s", liveTypes, err, raw)
		}
		messageType, _ := message["type"].(string)
		liveTypes = append(liveTypes, messageType)
		if messageType == "runner_finished" {
			break
		}
	}
	if len(liveTypes) != 3 || liveTypes[0] != "runner_checkpoint" ||
		liveTypes[len(liveTypes)-2] != "message" || liveTypes[len(liveTypes)-1] != "runner_finished" ||
		strings.Contains(strings.ToLower(strings.Join(liveTypes, " ")), "private reasoning") {
		t.Fatalf("live types=%#v", liveTypes)
	}
}

func TestSessionRunnerChatPublishesPublicProgressBeforeToolAndFinalOnlyAtSettlement(t *testing.T) {
	testSessionRunnerChatProgressBeforeTool(t, false, false)
}

func TestSessionRunnerChatPublishesStructuredProgressBeforeToolWithoutExtraRequest(t *testing.T) {
	testSessionRunnerChatProgressBeforeTool(t, true, false)
}

func TestSessionRunnerChatPublishesLocalizedProgressThroughHTTPAndJournal(t *testing.T) {
	for _, structured := range []bool{false, true} {
		t.Run(fmt.Sprintf("structured=%t", structured), func(t *testing.T) {
			testSessionRunnerChatProgressBeforeTool(t, structured, true)
		})
	}
}

func testSessionRunnerChatProgressBeforeTool(t *testing.T, structured, chinese bool) {
	expectedProgress := "I found an earlier result; I’ll verify it with the runtime now."
	expectedFinal := "final answer"
	userText := "run a tool before the final answer"
	expectedRequests := int64(2)
	if chinese {
		expectedProgress = "我找到了之前的结果，现在将通过运行环境核实。"
		expectedFinal = "已完成结果核实。"
		userText = "请先调用工具核实，再用中文回答。"
		expectedRequests = 3
	}
	var requests atomic.Int64
	toolBoundaryWritten := make(chan struct{})
	releaseToolBoundary := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseToolBoundary) }) })
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sequence := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if chinese && sequence == 2 {
			if request["tools"] != nil || request["tool_choice"] != nil {
				t.Errorf("translation request must not execute tools")
			}
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"localized-progress\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":%q}}]}\n\ndata: [DONE]\n\n", expectedProgress)
			flusher.Flush()
			return
		}
		if chinese && sequence > 2 {
			sequence--
		}
		switch sequence {
		case 1:
			messages, ok := request["messages"].([]any)
			if !ok || !hasChatSystemMessage(messages, "Assistant communication has two explicit roles") ||
				hasChatSystemMessage(messages, "User-visible progress contract:") {
				t.Fatalf("public communication contract mismatch: %#v", request["messages"])
			}
			args := map[string]any{"namespace": "test", "key": "tasks"}
			if structured {
				args[runnerPublicProgressField] = "I found an earlier result; I’ll verify it with the runtime now."
			} else {
				_, _ = w.Write([]byte("data: {\"id\":\"candidate-before-tool\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"I found an earlier result; I’ll verify it with the runtime now.\"}}]}\n\n"))
			}
			encodedArgs, _ := json.Marshal(args)
			encodedDelta, _ := json.Marshal(map[string]any{"id": "candidate-before-tool", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "call_stream_list", "type": "function", "function": map[string]any{"name": "runtime_get", "arguments": string(encodedArgs)}}}}}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encodedDelta)
			flusher.Flush()
			close(toolBoundaryWritten)
			<-releaseToolBoundary
			_, _ = w.Write([]byte("data: {\"id\":\"candidate-before-tool\",\"choices\":[{\"index\":0,\"finish_reason\":\"tool_calls\",\"delta\":{}}]}\n\n"))
		case 2:
			messages, ok := request["messages"].([]any)
			if !ok || !hasToolResultMessage(messages, "call_stream_list", "") {
				t.Fatalf("tool result was not returned to the provider: %#v", request["messages"])
			}
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"final-after-tool\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":%q}}]}\n\n", expectedFinal)
		default:
			http.Error(w, "unexpected provider request", http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	workspaceStore, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspaceStore.Close() })
	server := New(Options{FileRoot: root, Workspace: workspaceStore})
	if _, err := workspaceStore.CreateProject(workspace.CreateProjectInput{
		ID: "project-stream-tool-boundary", UserID: "user-1", Name: "Stream tool boundary", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := server.settingsStore.Set("model.activeProviderId", "stream-tool-boundary-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "stream-tool-boundary-key", UserID: "user-1", Provider: "openai", Value: "stream-secret",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := workspaceStore.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "stream-tool-boundary-provider", UserID: "user-1", Name: "Stream tool boundary provider", Type: "openai",
		BaseURL: modelAPI.URL + "/v1", Model: "stream-model", SecretRef: "secret://stream-tool-boundary-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	session := sessionstore.Session{
		ID: "session-stream-tool-boundary", Title: "Streaming tool boundary", WorkDir: root,
		CreatedAt: now, UpdatedAt: now, LastUserMessageAt: now, MessageCount: 1, LastRole: "user",
		Project: &sessionstore.Project{ID: "project-stream-tool-boundary", Name: "Stream tool boundary", Path: root, BoundAt: now},
	}
	if err := server.sessionStore.Save(session); err != nil {
		t.Fatal(err)
	}
	if _, err := server.eventJournal.Append(session.ID, eventjournal.Message{
		"type": "message", "role": "user", "text": userText,
	}, eventjournal.Metadata{ClientMessageID: "stream-tool-boundary-user"}); err != nil {
		t.Fatal(err)
	}

	type cycleOutcome struct {
		result SessionRunnerCycleResult
		err    error
	}
	finished := make(chan cycleOutcome, 1)
	go func() {
		result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: session.ID, RunnerID: "stream-tool-boundary-runner", LeaseTTL: time.Minute,
			ReplayLimit: 50, OutputLimitBytes: 64 * 1024, RequestTimeout: time.Minute,
			MaxAttempts: 1, MaxToolRounds: 2, RequireSavedModel: true,
			DisableSkillDiscovery: true, AllowedTools: []string{"runtime_get"},
		})
		finished <- cycleOutcome{result: result, err: err}
	}()
	select {
	case <-toolBoundaryWritten:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not reach its tool-call boundary")
	}
	progressBeforeResponseEnd := false
	deadline := time.Now().Add(2 * time.Second)
	for !chinese && !structured && !progressBeforeResponseEnd && time.Now().Before(deadline) {
		entries, readErr := server.eventJournal.ReadAfter(session.ID, 0, 100)
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, entry := range entries {
			if stringValue(entry.Message["type"]) == "content_delta" &&
				stringValue(entry.Message["text"]) == "I found an earlier result; I’ll verify it with the runtime now." {
				progressBeforeResponseEnd = true
				break
			}
		}
		if !progressBeforeResponseEnd {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !chinese && !structured && !progressBeforeResponseEnd {
		t.Fatal("public progress was not durable while the provider response remained open")
	}
	releaseOnce.Do(func() { close(releaseToolBoundary) })
	outcome := <-finished
	result, err := outcome.result, outcome.err
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || requests.Load() != expectedRequests {
		t.Fatalf("result=%+v requests=%d", result, requests.Load())
	}
	entries, err := server.eventJournal.ReadAfter(session.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var streamedText strings.Builder
	finalText := ""
	resetIndex := -1
	for index, entry := range entries {
		switch stringValue(entry.Message["type"]) {
		case "content_delta":
			streamedText.WriteString(stringValue(entry.Message["text"]))
		case "content_reset":
			resetIndex = index
		case "message":
			if stringValue(entry.Message["role"]) == "assistant" {
				finalText = stringValue(entry.Message["text"])
			}
		}
	}
	if streamedText.String() != expectedProgress || finalText != expectedFinal {
		t.Fatalf("streamed text=%q final=%q entries=%#v", streamedText.String(), finalText, entries)
	}
	if resetIndex >= 0 {
		t.Fatalf("accepted public progress must never require a reset: reset=%d entries=%#v", resetIndex, entries)
	}
}

func TestTranscriptRunnerProviderInterruptionContinuesOneStableTaskAttempt(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-stream-recovery", "frame-stream-recovery")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&chatCompletionRequest{}); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requestNumber := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if requestNumber == 1 {
			_, _ = w.Write([]byte("data: {\"id\":\"interrupted-generation\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"partial output\"}}]}\n\n"))
			flusher.Flush()
			return
		}
		if requestNumber != 2 {
			http.Error(w, "unexpected extra provider segment", http.StatusConflict)
			return
		}
		_, _ = w.Write([]byte("data: {\"id\":\"resumed-generation\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\" resumed\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "stream-recovery-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "stream-recovery-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "stream-recovery-provider", UserID: "local", Name: "Stream recovery provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://stream-recovery-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-stream-recovery", MessageUUID: "message-stream-recovery", ClientMessageID: "client-stream-recovery",
		Text: "recover this provider generation exactly once",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: "frame-stream-recovery", RunnerID: "stream-recovery-runner", LeaseTTL: time.Minute,
		RequestTimeout: time.Second, MaxAttempts: 3, RequireSavedModel: true,
		DisableSkillDiscovery: true, AllowedTools: []string{"runtime_get"},
	}
	interrupted, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || interrupted.Status != "interrupted" || interrupted.Attempt != 1 || interrupted.FinishEventID != 0 {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	options.SessionID = ""
	options.RunnerID = "stream-recovery-runner-2"
	restarted := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := restarted.settingsStore.Set("model.activeProviderId", "stream-recovery-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.secretStore.Create(secretstore.Secret{
		ID: "stream-recovery-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = restarted.Close(ctx)
	})
	resumed, err := restarted.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || !resumed.Claimed || resumed.Status != "completed" || resumed.Attempt != 1 || requests.Load() != 2 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-stream-recovery")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltaText, assistantText string
	interruptions, continuations, resets, terminals := 0, 0, 0, 0
	for _, projected := range events {
		switch projected.Event.Type {
		case "content_delta":
			var payload map[string]any
			if err := json.Unmarshal(projected.ResolvedPayloadJSON, &payload); err != nil {
				t.Fatal(err)
			}
			deltaText += stringValue(payload["text"])
		case "assistant_message":
			var payload map[string]any
			if err := json.Unmarshal(projected.ResolvedPayloadJSON, &payload); err != nil {
				t.Fatal(err)
			}
			assistantText += stringValue(payload["text"])
		case "content_reset":
			resets++
		case "runner_checkpoint":
			var payload map[string]any
			if err := json.Unmarshal(projected.ResolvedPayloadJSON, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["reason_code"] == "provider_stream_interrupted" {
				interruptions++
			}
			if _, ok := payload["provider_continuation"].(map[string]any); ok {
				continuations++
			}
		case "runner_finished":
			terminals++
		}
	}
	if deltaText+assistantText != "partial output resumed" || interruptions != 1 || continuations != 1 || resets != 0 || terminals != 1 {
		t.Fatalf("delta=%q assistant=%q interruptions=%d continuations=%d resets=%d terminals=%d events=%#v",
			deltaText, assistantText, interruptions, continuations, resets, terminals, events)
	}
	options.RunnerID = "stream-recovery-terminal-fence"
	fenced, err := restarted.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || fenced.Claimed || requests.Load() != 2 {
		t.Fatalf("terminal fence result=%#v requests=%d err=%v", fenced, requests.Load(), err)
	}
}

func TestTranscriptRunnerEmptyProviderTurnRemainsResumable(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-empty-turn-recovery", "frame-empty-turn-recovery")
	var requests atomic.Int64
	var recoveryEnabled atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&chatCompletionRequest{}); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if !recoveryEnabled.Load() {
			// A successful HTTP/SSE exchange may still contain no semantic model
			// output. Provider retries are bounded transport attempts; exhaustion
			// must remain a resumable boundary for the logical task.
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		} else {
			_, _ = w.Write([]byte("data: {\"id\":\"recovered-after-empty-turn\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"recovered final\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
		flusher.Flush()
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "empty-turn-recovery-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "empty-turn-recovery-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "empty-turn-recovery-provider", UserID: "local", Name: "Empty turn recovery provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://empty-turn-recovery-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-empty-turn-recovery", MessageUUID: "message-empty-turn-recovery",
		ClientMessageID: "client-empty-turn-recovery", Text: "recover an empty provider turn without recreating this task",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{
		SessionID: "frame-empty-turn-recovery", RunnerID: "empty-turn-recovery-runner-1",
		LeaseTTL: time.Minute, RequestTimeout: time.Second, MaxAttempts: 1,
		RequireSavedModel: true, DisableSkillDiscovery: true,
	}
	interrupted, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || interrupted.Status != "interrupted" ||
		interrupted.InterruptionReasonCode != "provider_stream_no_progress" || interrupted.FinishEventID != 0 {
		t.Fatalf("interrupted=%#v requests=%d err=%v", interrupted, requests.Load(), err)
	}
	requestsAfterInterruption := requests.Load()
	if requestsAfterInterruption < 1 {
		t.Fatalf("empty provider turn was not attempted: requests=%d", requestsAfterInterruption)
	}

	recoveryEnabled.Store(true)
	options.SessionID = ""
	options.RunnerID = "empty-turn-recovery-runner-2"
	completed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || !completed.Claimed || completed.Status != "completed" ||
		completed.Attempt != 1 || completed.FinishEventID == 0 || requests.Load() != requestsAfterInterruption+1 {
		t.Fatalf("completed=%#v requests=%d err=%v", completed, requests.Load(), err)
	}

	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-empty-turn-recovery")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	noProgressBoundaries, terminals := 0, 0
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		switch event.Event.Type {
		case "runner_checkpoint":
			if payload["reason_code"] == "provider_stream_no_progress" {
				noProgressBoundaries++
			}
		case "runner_finished":
			terminals++
		}
	}
	if noProgressBoundaries != 1 || terminals != 1 {
		t.Fatalf("no_progress=%d terminals=%d events=%v",
			noProgressBoundaries, terminals, projectedEventSummaries(events))
	}
}

func TestTranscriptRunnerMalformedToolCallProtocolRecoversWithinExecutionUnit(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-protocol-recovery", "frame-protocol-recovery")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&chatCompletionRequest{}); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requestNumber := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		switch requestNumber {
		case 1:
			chunk, err := json.Marshal(map[string]any{
				"id": "malformed-tool-call",
				"choices": []any{map[string]any{
					"index": 0, "finish_reason": "tool_calls",
					"delta": map[string]any{
						"role": "assistant", "content": "evidence collected ",
						"tool_calls": []any{map[string]any{
							"index": 0, "id": "call-malformed", "type": "function",
							"function": map[string]any{"name": "runtime_get", "arguments": `{"key":`},
						}},
					},
				}},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case 2:
			_, _ = w.Write([]byte("data: {\"id\":\"recovered-generation\",\"choices\":[{\"index\":0,\"finish_reason\":\"stop\",\"delta\":{\"role\":\"assistant\",\"content\":\"evidence collected recovered final\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.Error(w, "unexpected extra provider generation", http.StatusConflict)
			return
		}
		flusher.Flush()
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "protocol-recovery-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "protocol-recovery-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "protocol-recovery-provider", UserID: "local", Name: "Protocol recovery provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://protocol-recovery-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-protocol-recovery", MessageUUID: "message-protocol-recovery",
		ClientMessageID: "client-protocol-recovery", Text: "recover malformed provider tool-call protocol",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: "frame-protocol-recovery", RunnerID: "protocol-recovery-runner-1", LeaseTTL: time.Minute,
		RequestTimeout: time.Second, MaxAttempts: 1, RequireSavedModel: true,
		DisableSkillDiscovery: true, AllowedTools: []string{"runtime_get"},
	}
	initial, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || initial.Status != "completed" || initial.Attempt != 1 || initial.FinishEventID == 0 || requests.Load() != 2 {
		t.Fatalf("initial=%#v requests=%d err=%v", initial, requests.Load(), err)
	}
	options.SessionID = ""
	options.RunnerID = "protocol-recovery-runner-2"
	resumed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || resumed.Claimed || requests.Load() != 2 {
		stream, _, _ := repo.GetFrameStreamBySession(context.Background(), "local", "frame-protocol-recovery")
		events, _ := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
			StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
		})
		t.Fatalf("resumed=%#v requests=%d err=%v events=%v",
			resumed, requests.Load(), err, projectedEventSummaries(events))
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-protocol-recovery")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltaText, assistantText strings.Builder
	interruptions, continuations, terminals := 0, 0, 0
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		switch event.Event.Type {
		case "content_delta":
			deltaText.WriteString(stringValue(payload["text"]))
		case "assistant_message":
			assistantText.WriteString(stringValue(payload["text"]))
		case "runner_checkpoint":
			if payload["reason_code"] == sessionRunnerModelProtocolErrorReasonCode {
				interruptions++
			}
			if _, ok := payload["provider_continuation"].(map[string]any); ok {
				continuations++
			}
		case "runner_finished":
			terminals++
		}
	}
	// The terminal message is the authoritative full segment, while deltas are
	// incremental presentation. Concatenating both would double-count progress.
	if deltaText.String() != "evidence collected" || assistantText.String() != "evidence collected recovered final" || interruptions != 0 || continuations != 0 || terminals != 1 {
		t.Fatalf("delta=%q assistant=%q interruptions=%d continuations=%d terminals=%d events=%v",
			deltaText.String(), assistantText.String(), interruptions, continuations, terminals, projectedEventSummaries(events))
	}
}

func projectedEventSummaries(events []transcriptstore.ProjectedEvent) []string {
	summaries := make([]string, 0, len(events))
	for _, event := range events {
		var payload map[string]any
		_ = json.Unmarshal(event.ResolvedPayloadJSON, &payload)
		summary := fmt.Sprintf("%d:%s", event.Event.EventID, event.Event.Type)
		if reason := strings.TrimSpace(stringValue(payload["reason_code"])); reason != "" {
			summary += ":" + reason
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func TestTranscriptRunnerContentOnlyTokenLimitContinuesFromDurablePrefix(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-content-truncation", "frame-content-truncation")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&chatCompletionRequest{}); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requestNumber := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		switch requestNumber {
		case 1:
			_, _ = w.Write([]byte("data: {\"id\":\"content-limit-1\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"bounded prefix\"},\"finish_reason\":\"length\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case 2:
			_, _ = w.Write([]byte("data: {\"id\":\"content-limit-2\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\" resumed\"},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.Error(w, "unexpected extra provider segment", http.StatusConflict)
			return
		}
		flusher.Flush()
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "content-truncation-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "content-truncation-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "content-truncation-provider", UserID: "local", Name: "Content truncation provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://content-truncation-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-content-truncation", MessageUUID: "message-content-truncation",
		ClientMessageID: "client-content-truncation", Text: "resume content-only token truncation",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: "frame-content-truncation", RunnerID: "content-truncation-runner-1", LeaseTTL: time.Minute,
		RequestTimeout: time.Second, MaxAttempts: 1, RequireSavedModel: true,
		DisableSkillDiscovery: true,
	}
	interrupted, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || interrupted.Status != "interrupted" || interrupted.Attempt != 1 || interrupted.FinishEventID != 0 {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	options.SessionID = ""
	options.RunnerID = "content-truncation-runner-2"
	history, historyFound, historyErr := server.loadTranscriptWebHistory(context.Background(), "local", "frame-content-truncation")
	if historyErr != nil || !historyFound {
		t.Fatalf("interrupted history found=%t err=%v", historyFound, historyErr)
	}
	historyJSON, historyErr := json.Marshal(history)
	if historyErr != nil || strings.Contains(string(historyJSON), "bounded prefix") {
		t.Fatalf("unfinished candidate leaked into reload history: %s err=%v", historyJSON, historyErr)
	}
	resumed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || !resumed.Claimed || resumed.Status != "completed" || resumed.Attempt != 1 || requests.Load() != 2 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-content-truncation")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltaText, assistantText strings.Builder
	interruptions, continuations, terminals := 0, 0, 0
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		switch event.Event.Type {
		case "content_delta":
			deltaText.WriteString(stringValue(payload["text"]))
		case "assistant_message":
			assistantText.WriteString(stringValue(payload["text"]))
		case "runner_checkpoint":
			if payload["reason_code"] == "provider_stream_interrupted" {
				interruptions++
			}
			if _, ok := payload["provider_continuation"].(map[string]any); ok {
				continuations++
			}
		case "runner_finished":
			terminals++
		}
	}
	if deltaText.Len() != 0 || assistantText.String() != "bounded prefix resumed" || interruptions != 1 || continuations != 1 || terminals != 1 {
		t.Fatalf("delta=%q assistant=%q interruptions=%d continuations=%d terminals=%d events=%#v",
			deltaText.String(), assistantText.String(), interruptions, continuations, terminals, projectedEventSummaries(events))
	}
}

func TestTranscriptRunnerPartialToolCallTokenLimitResumesFromCompletedToolCheckpoint(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-tool-truncation", "frame-tool-truncation")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&chatCompletionRequest{}); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requestNumber := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		switch requestNumber {
		case 1:
			_, _ = w.Write([]byte("data: {\"id\":\"tool-limit-1\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"incomplete-call\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"file_path\\\":\"}}]},\"finish_reason\":\"length\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		case 2:
			_, _ = w.Write([]byte("data: {\"id\":\"tool-limit-2\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"recovered without executing a partial call\"},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			http.Error(w, "unexpected extra provider segment", http.StatusConflict)
		}
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "tool-truncation-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{ID: "tool-truncation-key", UserID: "local", Provider: "openai", Value: "test-key"}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{ID: "tool-truncation-provider", UserID: "local", Name: "Tool truncation provider", Type: "openai", BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://tool-truncation-key", Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{FrameID: "frame-tool-truncation", MessageUUID: "message-tool-truncation", ClientMessageID: "client-tool-truncation", Text: "resume after a partial tool call token limit"}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: "frame-tool-truncation", RunnerID: "tool-truncation-runner-1", LeaseTTL: time.Minute, RequestTimeout: time.Second, MaxAttempts: 1, RequireSavedModel: true, DisableSkillDiscovery: true}
	interrupted, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || interrupted.Status != "interrupted" || interrupted.InterruptionReasonCode != "provider_output_token_limit" || interrupted.FinishEventID != 0 {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	options.SessionID = ""
	options.RunnerID = "tool-truncation-runner-2"
	resumed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || resumed.Status != "completed" || requests.Load() != 2 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}
}

func TestTranscriptRunnerProviderSegmentsHaveNoTaskLifetimeCeilingOrAttemptInflation(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-stream-endurance", "frame-stream-endurance")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sequence := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if sequence <= 3 {
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"interrupted-%d\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"partial-%d\"}}]}\n\n", sequence, sequence)
			flusher.Flush()
			return
		}
		_, _ = w.Write([]byte("data: {\"id\":\"completed-4\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"durable final\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "stream-endurance-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "stream-endurance-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "stream-endurance-provider", UserID: "local", Name: "Stream endurance provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://stream-endurance-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-stream-endurance", MessageUUID: "message-stream-endurance",
		ClientMessageID: "client-stream-endurance", Text: "continue through every recoverable provider interruption",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: "frame-stream-endurance", LeaseTTL: time.Minute, RequestTimeout: time.Second,
		MaxAttempts: 1, RequireSavedModel: true, DisableSkillDiscovery: true,
	}
	options.RunnerID = "stream-endurance-runner-1"
	result, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || result.Status != "interrupted" || result.Attempt != 1 || result.FinishEventID != 0 {
		t.Fatalf("initial result=%#v err=%v", result, err)
	}
	options.SessionID = ""
	for segment := 2; segment <= 3; segment++ {
		options.RunnerID = fmt.Sprintf("stream-endurance-runner-%d", segment)
		continued, err := server.RunSessionRunnerChatOnce(context.Background(), options)
		if err != nil || !continued.Claimed || continued.Status != "interrupted" || continued.Attempt != 1 || requests.Load() != int64(segment) {
			stream, _, _ := repo.GetFrameStreamBySession(context.Background(), "local", "frame-stream-endurance")
			events, _ := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
				StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
			})
			terminalPayloads := make([]string, 0, 1)
			for _, event := range events {
				if event.Event.Type == "runner_finished" {
					terminalPayloads = append(terminalPayloads, string(event.ResolvedPayloadJSON))
				}
			}
			t.Fatalf("segment=%d result=%#v requests=%d err=%v terminals=%v", segment, continued, requests.Load(), err, terminalPayloads)
		}
	}
	options.RunnerID = "stream-endurance-runner-4"
	completed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || !completed.Claimed || completed.Status != "completed" || completed.Attempt != 1 || requests.Load() != 4 {
		t.Fatalf("completed=%#v requests=%d err=%v", completed, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-stream-endurance")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	interruptions, continuations, terminals := 0, 0, 0
	for _, event := range events {
		switch event.Event.Type {
		case "runner_checkpoint":
			var payload map[string]any
			if json.Unmarshal(event.ResolvedPayloadJSON, &payload) == nil && payload["reason_code"] == "provider_stream_interrupted" {
				interruptions++
			}
			if _, ok := payload["provider_continuation"].(map[string]any); ok {
				continuations++
			}
		case "runner_finished":
			terminals++
		}
	}
	if interruptions != 3 || continuations != 3 || terminals != 1 {
		t.Fatalf("interruptions=%d continuations=%d terminals=%d events=%#v", interruptions, continuations, terminals, events)
	}
}

func TestTranscriptRunnerProviderContinuationRecoversTimeoutBeforeNewSemanticBytes(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-stream-timeout-resume", "frame-stream-timeout-resume")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		switch sequence {
		case 1:
			_, _ = w.Write([]byte("data: {\"id\":\"timeout-prefix\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"durable prefix\"}}]}\n\n"))
			flusher.Flush()
		case 2:
			// A provider may accept a continuation request and return response
			// headers, then time out before producing the next semantic byte.
			// The durable prefix from segment one remains the recovery fence.
			flusher.Flush()
			<-r.Context().Done()
		case 3:
			_, _ = w.Write([]byte("data: {\"id\":\"timeout-finish\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\" resumed\"},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
		default:
			http.Error(w, "unexpected extra provider segment", http.StatusConflict)
		}
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "stream-timeout-resume-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "stream-timeout-resume-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "stream-timeout-resume-provider", UserID: "local", Name: "Stream timeout resume provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://stream-timeout-resume-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-stream-timeout-resume", MessageUUID: "message-stream-timeout-resume",
		ClientMessageID: "client-stream-timeout-resume", Text: "continue after a bounded provider timeout",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: "frame-stream-timeout-resume", LeaseTTL: time.Minute, RequestTimeout: 80 * time.Millisecond,
		MaxAttempts: 1, RequireSavedModel: true, DisableSkillDiscovery: true,
	}
	options.RunnerID = "stream-timeout-resume-runner-1"
	first, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || first.Status != "interrupted" || first.InterruptionReasonCode != "provider_stream_interrupted" {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	options.SessionID = ""
	options.RunnerID = "stream-timeout-resume-runner-2"
	second, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || second.Status != "interrupted" || second.InterruptionReasonCode != "provider_stream_no_progress" {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	options.RunnerID = "stream-timeout-resume-runner-3"
	completed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || completed.Status != "completed" || requests.Load() != 3 {
		t.Fatalf("completed=%#v requests=%d err=%v", completed, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-stream-timeout-resume")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas, assistantText strings.Builder
	interruptions, noProgress, terminals := 0, 0, 0
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		switch event.Event.Type {
		case "content_delta":
			deltas.WriteString(stringValue(payload["text"]))
		case "assistant_message":
			assistantText.WriteString(stringValue(payload["text"]))
		case "runner_checkpoint":
			if payload["reason_code"] == "provider_stream_interrupted" {
				interruptions++
			}
			if payload["reason_code"] == "provider_stream_no_progress" {
				noProgress++
			}
		case "runner_finished":
			terminals++
		}
	}
	if deltas.String()+assistantText.String() != "durable prefix resumed" || interruptions != 1 || noProgress != 1 || terminals != 1 {
		t.Fatalf("deltas=%q assistant=%q interruptions=%d noProgress=%d terminals=%d", deltas.String(), assistantText.String(), interruptions, noProgress, terminals)
	}
}

func TestTranscriptRunnerProviderContinuationRemainsResumableAcrossRepeatedNoProgressSegments(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-stream-no-progress", "frame-stream-no-progress")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sequence := requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if sequence <= 6 {
			_, _ = w.Write([]byte("data: {\"id\":\"no-progress\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"stable prefix\"}}]}\n\n"))
		} else {
			_, _ = w.Write([]byte("data: {\"id\":\"progress-after-recovery\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"stable prefix recovered\"},\"finish_reason\":\"stop\"}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
		w.(http.Flusher).Flush()
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "stream-no-progress-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "stream-no-progress-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "stream-no-progress-provider", UserID: "local", Name: "Stream no progress provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://stream-no-progress-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-stream-no-progress", MessageUUID: "message-stream-no-progress",
		ClientMessageID: "client-stream-no-progress", Text: "continue without replaying accepted output",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: "frame-stream-no-progress", LeaseTTL: time.Minute, RequestTimeout: time.Second,
		MaxAttempts: 1, RequireSavedModel: true, DisableSkillDiscovery: true,
	}
	for segment := 1; segment <= 6; segment++ {
		options.RunnerID = fmt.Sprintf("stream-no-progress-runner-%d", segment)
		result, err := server.RunSessionRunnerChatOnce(context.Background(), options)
		if err != nil || result.Status != "interrupted" || result.Attempt != 1 {
			t.Fatalf("segment=%d result=%#v err=%v", segment, result, err)
		}
		options.SessionID = ""
	}
	options.RunnerID = "stream-no-progress-runner-7"
	completed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || completed.Status != "completed" || completed.Attempt != 1 || completed.FinishEventID == 0 || requests.Load() != 7 {
		t.Fatalf("completed=%#v requests=%d err=%v", completed, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-stream-no-progress")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas, assistantText strings.Builder
	noProgressBoundaries, terminals := 0, 0
	for _, event := range events {
		var payload map[string]any
		if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		switch event.Event.Type {
		case "content_delta":
			deltas.WriteString(stringValue(payload["text"]))
		case "assistant_message":
			assistantText.WriteString(stringValue(payload["text"]))
		case "runner_checkpoint":
			if payload["reason_code"] == "provider_stream_no_progress" {
				noProgressBoundaries++
			}
		case "runner_finished":
			terminals++
		}
	}
	if deltas.String()+assistantText.String() != "stable prefix recovered" || noProgressBoundaries != 5 || terminals != 1 {
		t.Fatalf("deltas=%q assistant=%q noProgressBoundaries=%d terminals=%d", deltas.String(), assistantText.String(), noProgressBoundaries, terminals)
	}
}

func TestTranscriptRunnerArtifactValidationFailureDoesNotStartReplacementCandidate(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-artifact-correction", "frame-artifact-correction")
	var requests atomic.Int64
	badOutput := true
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&chatCompletionRequest{}); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		sequence := requests.Add(1)
		content := "Published report: {{artifact:" + unresolvedArtifactVersionID + "}}"
		if !badOutput {
			content = "Corrected report without references."
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"artifact-correction-%d\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":\"stop\"}]}\n\n", sequence, content)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "artifact-correction-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "artifact-correction-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "artifact-correction-provider", UserID: "local", Name: "Artifact correction provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://artifact-correction-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-artifact-correction", MessageUUID: "message-artifact-correction",
		ClientMessageID: "client-artifact-correction", Text: "Create the exact research deliverable.",
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: "frame-artifact-correction", RunnerID: "artifact-correction-runner", LeaseTTL: time.Minute,
		RequestTimeout: time.Second, MaxAttempts: 1, RequireSavedModel: true,
		DisableSkillDiscovery: true, MaxToolRounds: 0,
		RuntimeSessionConfig: map[string]any{"verifier_mode": "off"},
	}
	failed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	// Artifact reference failures are resumable interruptions: the runner
	// records a correction-required checkpoint instead of a terminal failure.
	// A later run with a corrected model output resumes the same task (the
	// self-healing path for 24h unattended operation) rather than silently
	// abandoning it.
	if err != nil || failed.Status != "interrupted" || failed.InterruptionReasonCode != "artifact_reference_correction_required" ||
		failed.Attempt != 1 || failed.CheckpointEventID == 0 {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	options.SessionID = ""
	options.RunnerID = "artifact-correction-runner-2"
	badOutput = false
	// The correction checkpoint is a rate-limited quality bounce, so an
	// unattended runner may repair it without waiting for a user message. The
	// dispatcher backoff prevents a model that cannot repair itself from hot
	// looping without imposing a logical-task continuation ceiling.
	resumed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || !resumed.Claimed || resumed.Status != "completed" || requests.Load() != 2 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-artifact-correction")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	interruptions, terminals := 0, 0
	for _, event := range events {
		switch event.Event.Type {
		case "runner_checkpoint":
			var payload map[string]any
			if json.Unmarshal(event.ResolvedPayloadJSON, &payload) == nil &&
				payload["reason_code"] == "artifact_reference_correction_required" {
				interruptions++
				if !strings.Contains(stringValue(payload["resume_detail"]), "unresolved_artifacts=1") {
					t.Fatalf("correction payload=%#v", payload)
				}
			}
		case "runner_finished":
			terminals++
		}
	}
	if interruptions != 1 || terminals != 1 {
		t.Fatalf("interruptions=%d terminals=%d events=%#v", interruptions, terminals, events)
	}
}
