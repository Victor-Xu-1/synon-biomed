package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWebMessageStreamContractAcrossHTTPSSERReplayAndLiveWebSocket(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "runner", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetFrameRuntimeMetadata("frame", workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{"web_extra": map[string]any{"backend": "synonbiomed"}},
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{FileRoot: root, Workspace: store})

	cases := []struct {
		sourceType string
		status     string
		want       string
		replace    bool
		blockType  string
	}{
		{sourceType: "user_message", want: "start"},
		{sourceType: "content_delta", want: "text"},
		{sourceType: "content_delta", want: "thinking", blockType: "thinking"},
		{sourceType: "content_reset", want: "text", replace: true},
		{sourceType: "assistant_message", want: "content", replace: true},
		{sourceType: "runner_finished", status: "completed", want: "finish"},
		{sourceType: "runner_finished", status: "failed", want: "error"},
		{sourceType: "runner_finished", status: "cancelled", want: "finish"},
		{sourceType: "runner_finished", status: "canceled", want: "finish"},
	}
	for index, item := range cases {
		payload := map[string]any{"text": item.want, "status": item.status, "block_type": item.blockType}
		event, err := store.AppendFrameEvent(workspace.FrameEventInput{
			ID:      "source-" + item.sourceType + "-" + item.status + string(rune('a'+index)),
			FrameID: "frame", Type: item.sourceType, Payload: payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := srv.publishWorkspaceEvent(event); err != nil {
			t.Fatalf("publish %s/%s: %v", item.sourceType, item.status, err)
		}
		if item.sourceType == "runner_finished" {
			wantStatus := item.status
			if wantStatus == "canceled" {
				wantStatus = "cancelled"
			}
			runtimeEvent, found, err := store.GetRealtimeEventByID("web-runtime-finish:" + event.ID)
			if err != nil || !found || runtimeEvent.Payload["terminal_status"] != wantStatus {
				t.Fatalf("runtime terminal %s found=%t event=%#v err=%v", wantStatus, found, runtimeEvent, err)
			}
			turnEvent, found, err := store.GetRealtimeEventByID("web-turn-completed:" + event.ID)
			if err != nil || !found || turnEvent.Payload["terminal_status"] != wantStatus {
				t.Fatalf("turn terminal %s found=%t event=%#v err=%v", wantStatus, found, turnEvent, err)
			}
			wantTurnStatus := "finished"
			if wantStatus == "failed" {
				wantTurnStatus = "error"
			} else if wantStatus == "cancelled" {
				wantTurnStatus = "cancelled"
			}
			if turnEvent.Payload["status"] != wantTurnStatus {
				t.Fatalf("turn status %s=%#v want=%q", wantStatus, turnEvent.Payload["status"], wantTurnStatus)
			}
		}
	}

	httpPayload := runtimeCompatJSON(t, srv.Handler(), http.MethodGet,
		"/api/events?frame_id=frame&type=message.stream&limit=100", "user-1", nil, http.StatusOK)
	events := httpPayload["events"].([]any)
	if len(events) != len(cases) {
		t.Fatalf("HTTP message.stream events=%d want=%d: %#v", len(events), len(cases), events)
	}
	for index, raw := range events {
		event := raw.(map[string]any)
		payload := event["payload"].(map[string]any)
		if event["type"] != "message.stream" || payload["type"] != cases[index].want || payload["stream_type"] != cases[index].want {
			t.Fatalf("HTTP stream event[%d]=%#v", index, event)
		}
		if got := payload["replace"] == true; got != cases[index].replace {
			t.Fatalf("HTTP stream replace[%d]=%t want=%t", index, got, cases[index].replace)
		}
		if cases[index].want == "thinking" {
			data, ok := payload["data"].(map[string]any)
			if !ok || data["content"] != "thinking" || data["status"] != "thinking" {
				t.Fatalf("HTTP thinking stream event[%d]=%#v", index, payload)
			}
		}
		if cases[index].sourceType == "runner_finished" {
			wantStatus := cases[index].status
			if wantStatus == "canceled" {
				wantStatus = "cancelled"
			}
			if payload["terminal_status"] != wantStatus {
				t.Fatalf("HTTP terminal status[%d]=%#v want=%q", index, payload["terminal_status"], wantStatus)
			}
		}
	}

	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	sse := readFirstWebMessageStreamSSE(t, httpServer.URL)
	if sse.Type != "message.stream" || sse.Payload["type"] != "start" || sse.Payload["stream_type"] != "start" {
		t.Fatalf("SSE stream event=%#v", sse)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/events/ws?userId=user-1&frame_id=frame&type=message.stream&after_sequence=0"
	replay, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if connected := readCompatWebSocketTest(t, ctx, replay); connected["type"] != "connected" {
		t.Fatalf("replay connected=%#v", connected)
	}
	for index, item := range cases {
		message := readCompatWebSocketTest(t, ctx, replay)
		if message["type"] != "message.stream" || message["stream_type"] != item.want {
			t.Fatalf("WS replay[%d]=%#v", index, message)
		}
	}
	_ = replay.Close(websocket.StatusNormalClosure, "replay complete")

	liveURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/events/ws?userId=user-1&frame_id=frame&type=message.stream&after_sequence=latest"
	live, _, err := websocket.Dial(ctx, liveURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close(websocket.StatusNormalClosure, "test complete")
	if connected := readCompatWebSocketTest(t, ctx, live); connected["type"] != "connected" {
		t.Fatalf("live connected=%#v", connected)
	}
	liveSource, err := store.AppendFrameEvent(workspace.FrameEventInput{
		ID: "source-live-reset", FrameID: "frame", Type: "content_reset", Payload: map[string]any{"text": "replacement"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.publishWorkspaceEvent(liveSource); err != nil {
		t.Fatal(err)
	}
	if message := readCompatWebSocketTest(t, ctx, live); message["type"] != "message.stream" || message["stream_type"] != "text" || message["replace"] != true {
		t.Fatalf("WS live=%#v", message)
	}
}

func TestWebRunnerTerminalProjectionRejectsUnknownStatusBeforePublishing(t *testing.T) {
	srv, store, frameContext := newWebMessageStreamTestServer(t)
	if _, err := store.SetFrameRuntimeMetadata(frameContext.Frame.ID, workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{"web_extra": map[string]any{"backend": "synonbiomed"}},
	}); err != nil {
		t.Fatal(err)
	}
	source, err := store.AppendFrameEvent(workspace.FrameEventInput{
		ID: "unknown-terminal", FrameID: frameContext.Frame.ID, Type: "runner_finished",
		Payload: map[string]any{"status": "done", "text": "must not publish"},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.LatestRealtimeEventSequence(workspace.RealtimeEventFilter{UserID: frameContext.UserID, FrameID: frameContext.Frame.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.publishWebFrameEventProjection(frameContext, source); err == nil || !strings.Contains(err.Error(), "unsupported runner terminal status") {
		t.Fatalf("unknown terminal projection error=%v", err)
	}
	after, err := store.LatestRealtimeEventSequence(workspace.RealtimeEventFilter{UserID: frameContext.UserID, FrameID: frameContext.Frame.ID})
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("unknown terminal projection mutated realtime sequence %d -> %d", before, after)
	}
}

func TestPublishWebMessageStreamRejectsUnknownSubtypeWithoutDurableEvent(t *testing.T) {
	srv, store, frameContext := newWebMessageStreamTestServer(t)
	before, err := store.LatestRealtimeEventSequence(workspace.RealtimeEventFilter{UserID: "user-1", FrameID: "frame"})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.publishWebMessageStream(frameContext, "invalid-stream", map[string]any{"type": "unknown", "data": "bad"}); err == nil {
		t.Fatal("unknown stream subtype should fail closed")
	}
	after, err := store.LatestRealtimeEventSequence(workspace.RealtimeEventFilter{UserID: "user-1", FrameID: "frame"})
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("invalid stream persisted an event: before=%d after=%d", before, after)
	}
}

func newWebMessageStreamTestServer(t *testing.T) (*Server, *workspace.Store, workspace.FrameRealtimeContext) {
	t.Helper()
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame", ProjectID: "project", AgentName: "runner", Status: "running", ConversationType: "agent"}); err != nil {
		t.Fatal(err)
	}
	frameContext, found, err := store.GetFrameRealtimeContext("frame")
	if err != nil || !found {
		t.Fatalf("frame context found=%t err=%v", found, err)
	}
	return New(Options{FileRoot: root, Workspace: store}), store, frameContext
}

func readFirstWebMessageStreamSSE(t *testing.T, serverURL string) workspace.RealtimeEvent {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet,
		serverURL+"/api/events/stream?frame_id=frame&type=message.stream&after_sequence=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Synon-User-Id", "user-1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event workspace.RealtimeEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal(err)
		}
		return event
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatal("SSE did not return a message.stream event")
	return workspace.RealtimeEvent{}
}
