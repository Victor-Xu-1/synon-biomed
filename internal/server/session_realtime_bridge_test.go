package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"synon-go/internal/compat/contracts"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRunnerSessionAppendMirrorsIntoFrameTraceAndRealtime(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: "project-1", AgentName: "runner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	if err := server.sessionStore.Upsert(sessionstore.Session{ID: "frame-1", Title: "Frame session"}); err != nil {
		t.Fatal(err)
	}
	entry, err := server.appendSessionToolEvent(map[string]any{
		"sessionId": "frame-1", "role": "assistant", "clientMessageId": "assistant-1",
		"message": map[string]any{"type": "message", "text": "Durable runner output."},
	})
	if err != nil || entry == nil {
		t.Fatalf("append runner event entry=%#v err=%v", entry, err)
	}
	if err := server.mirrorSessionEntryToWorkspaceFrame(entry); err != nil {
		t.Fatalf("idempotent mirror replay: %v", err)
	}
	frameEvents, err := store.ListFrameEvents("frame-1", 0, 10)
	if err != nil || len(frameEvents) != 1 || frameEvents[0].Type != "assistant_message" ||
		frameEvents[0].Payload["type"] != "assistant_message" || frameEvents[0].Payload["journal_event_id"] != float64(1) {
		t.Fatalf("mirrored frame events=%#v err=%v", frameEvents, err)
	}
	realtime := runtimeCompatJSON(t, server.Handler(), http.MethodGet, "/api/events?frame_id=frame-1", "user-1", nil, http.StatusOK)
	events := realtime["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["type"] != "frame_messages_delta" {
		t.Fatalf("runner realtime events=%#v", events)
	}
}

func TestRunnerFinishedFailureProjectsStatusDescription(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: "project-1", AgentName: "runner", Status: "failed", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	entry, err := server.eventJournal.Append("frame-1", eventjournal.Message{
		"type": "runner_finished", "role": "system", "status": "failed",
		"text": "runner chat exhausted its bounded tool budget",
	}, eventjournal.Metadata{})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.mirrorSessionEntryToWorkspaceFrame(entry); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.GetFrameTraceSnapshot("frame-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Root.StatusDescription != "runner chat exhausted its bounded tool budget" {
		t.Fatalf("status description = %q", snapshot.Root.StatusDescription)
	}
}

func TestWorkspaceSessionWebSocketEnforcesOwnershipAndMirrorsClientMessages(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: "project-1", AgentName: "runner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	if err := server.sessionStore.Upsert(sessionstore.Session{ID: "frame-1", Title: "Frame session"}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/ws/frame-1"
	foreign, response, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"X-Synon-User-Id": []string{"user-2"}}})
	if foreign != nil {
		_ = foreign.CloseNow()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign websocket response=%#v err=%v", response, err)
	}
	connection, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"X-Synon-User-Id": []string{"user-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "done")
	if connected := readWSMessage(t, ctx, connection); connected["type"] != "connected" {
		t.Fatalf("connected=%#v", connected)
	}
	writeWSMessage(t, ctx, connection, map[string]any{
		"type": "user_message", "clientMessageId": "client-ws-1", "messageUuid": "message-ws-1", "text": "Direct Web input.",
	})
	writeWSMessage(t, ctx, connection, map[string]any{"type": "ping"})
	if pong := readWSMessage(t, ctx, connection); pong["type"] != "pong" {
		t.Fatalf("pong=%#v", pong)
	}
	frameEvents, err := store.ListFrameEvents("frame-1", 0, 10)
	if err != nil || len(frameEvents) != 1 || frameEvents[0].Type != "user_message" {
		t.Fatalf("client websocket frame events=%#v err=%v", frameEvents, err)
	}
	realtime := runtimeCompatJSON(t, server.Handler(), http.MethodGet, "/api/events?frame_id=frame-1", "user-1", nil, http.StatusOK)
	if events := realtime["events"].([]any); len(events) != 1 || events[0].(map[string]any)["type"] != "frame_messages_delta" {
		t.Fatalf("client websocket realtime events=%#v", events)
	}
}

func TestFrameJournalTypesMapToBaselineStreamingEvents(t *testing.T) {
	cases := map[string]string{
		"content_delta": "text_chunk", "content_reset": "text_reset",
		"runner_checkpoint": "frame_activity", "runner_finished": "frame_activity",
		"session_compact": "compaction_status", "tool_stdout": "tool_stdout_chunk",
		"rate_limit": "rate_limit_notice", "execution_cell_update": "execution_cell_update",
		"unknown_internal_state": "frame_update",
	}
	for source, want := range cases {
		if got := baselineTypeForFrameEvent(source); got != want {
			t.Fatalf("baseline type for %s=%s want=%s", source, got, want)
		}
	}
}

func TestAllBaselineRealtimeEventsHaveExecutableProducerPaths(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{ID: "frame-1", ProjectID: "project-1", AgentName: "runner", Status: "running", ConversationType: "task"}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	if err := server.sessionStore.Upsert(sessionstore.Session{ID: "frame-1", Title: "Frame session"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for index, baseline := range contracts.EventTypes {
		if baseline.Name == "pong" {
			continue
		}
		want[baseline.Name] = true
		entry, err := server.appendSessionToolEvent(map[string]any{
			"sessionId": "frame-1", "role": "assistant", "clientMessageId": "baseline-event-" + baseline.Name,
			"message": map[string]any{
				"type": baseline.Name, "project_id": "project-1", "root_frame_id": "frame-1",
				"frame_id": "frame-1", "artifact_id": "artifact-1",
				"version_ids": []string{"version-1"}, "text": "event", "index": index,
			},
		})
		if err != nil || entry == nil {
			t.Fatalf("produce %s entry=%#v err=%v", baseline.Name, entry, err)
		}
	}
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{UserID: "user-1", IncludeGlobal: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(contracts.EventTypes)-1 {
		t.Fatalf("durable baseline events=%d want=%d", len(events), len(contracts.EventTypes)-1)
	}
	for _, event := range events {
		delete(want, event.Type)
	}
	if len(want) != 0 {
		t.Fatalf("baseline events without durable producer=%#v", want)
	}
	client := newSessionWebSocketHub().Register("frame-1")
	if !server.handleSessionWebSocketMessage("frame-1", client, map[string]any{"type": "ping"}) {
		t.Fatal("ping producer closed the session")
	}
	select {
	case message := <-client.send:
		if message["type"] != "pong" {
			t.Fatalf("ping response=%#v", message)
		}
	case <-time.After(time.Second):
		t.Fatal("pong producer did not respond")
	}
}

func TestFrameStreamingEventsDeliverLiveReplayAndExactInvalidations(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-1", UserID: "user-1", Name: "Realtime",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-1", ProjectID: "project-1", AgentName: "runner",
		Status: "running", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{FileRoot: root, Workspace: store})
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	websocketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") +
		"/api/events/ws?userId=user-1&after_sequence=0"
	liveConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if connected := readCompatWebSocketTest(t, ctx, liveConnection); connected["type"] != "connected" {
		t.Fatalf("live websocket handshake=%#v", connected)
	}

	sources := []struct {
		source string
		want   string
	}{
		{"unknown_internal_state", "frame_update"},
		{"assistant_message", "frame_messages_delta"},
		{"content_delta", "text_chunk"},
		{"content_reset", "text_reset"},
		{"runner_checkpoint", "frame_activity"},
		{"session_compact", "compaction_status"},
		{"rolling_compact_status", "rolling_compact_status"},
		{"rate_limit", "rate_limit_notice"},
		{"tool_stdout", "tool_stdout_chunk"},
		{"transcript_annotations_update", "transcript_annotations_update"},
	}
	for index, item := range sources {
		event, err := store.AppendFrameEvent(workspace.FrameEventInput{
			ID: "stream-event-" + item.want, FrameID: "frame-1", Type: item.source,
			Payload: map[string]any{"text": item.want, "index": index},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := server.publishWorkspaceEvent(event); err != nil {
			t.Fatalf("publish %s: %v", item.want, err)
		}
	}

	targetCounts := map[string]int{
		"frame_update": 1, "frame_messages_delta": 1, "text_chunk": 1, "text_reset": 1,
		"frame_activity": 1, "compaction_status": 1, "rate_limit_notice": 1,
		"tool_stdout_chunk": 1, "transcript_annotations_update": 1,
	}
	liveEvents, err := readDomainWebSocketEvents(ctx, liveConnection, targetCounts, len(targetCounts))
	if err != nil {
		t.Fatalf("read live frame events: %v", err)
	}
	if err := liveConnection.Close(websocket.StatusNormalClosure, "reconnect for replay"); err != nil {
		t.Fatal(err)
	}
	replayConnection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer replayConnection.Close(websocket.StatusNormalClosure, "test complete")
	if connected := readCompatWebSocketTest(t, ctx, replayConnection); connected["type"] != "connected" {
		t.Fatalf("replay websocket handshake=%#v", connected)
	}
	replayedEvents, err := readDomainWebSocketEvents(ctx, replayConnection, targetCounts, len(targetCounts))
	if err != nil {
		t.Fatalf("read replayed frame events: %v", err)
	}
	if !reflect.DeepEqual(liveEvents, replayedEvents) {
		t.Fatalf("live and replayed frame events differ:\nlive=%#v\nreplayed=%#v", liveEvents, replayedEvents)
	}

	listed := runtimeCompatJSON(t, server.Handler(), http.MethodGet, "/api/events?frame_id=frame-1&limit=100", "user-1", nil, http.StatusOK)
	durable := listed["events"].([]any)
	if len(durable) != len(targetCounts) {
		t.Fatalf("durable frame events=%d want=%d: %#v", len(durable), len(targetCounts), durable)
	}
	for _, raw := range durable {
		event := raw.(map[string]any)
		switch event["type"] {
		case "frame_update":
			assertDomainInvalidation(t, event, "trace", []any{"trace", "frame-1"}, "immediate", "prefix")
			assertDomainInvalidation(t, event, "frame", []any{"frame", "frame-1"}, "immediate", "exact")
			assertDomainInvalidation(t, event, "benches", []any{"benches", "project-1"}, "debounced", "exact")
		case "frame_messages_delta":
			assertDomainInvalidation(t, event, "trace", []any{"trace", "frame-1"}, "immediate", "prefix")
			assertDomainInvalidation(t, event, "frame", []any{"frame", "frame-1"}, "immediate", "exact")
		case "transcript_annotations_update":
			assertDomainInvalidation(t, event, "transcriptAnnotations", []any{"transcript-annotations", "frame-1"}, "immediate", "exact")
		case "rolling_compact_status":
			if event["kind"] != "none" {
				t.Fatalf("rolling_compact_status kind=%#v", event)
			}
		case "text_chunk", "text_reset", "frame_activity", "compaction_status", "rate_limit_notice", "tool_stdout_chunk":
			if len(event["invalidations"].([]any)) != 0 {
				t.Fatalf("fanout event unexpectedly invalidates queries: %#v", event)
			}
		}
	}
}
