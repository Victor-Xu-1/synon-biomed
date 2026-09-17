package server

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestTranscriptRealtimeRebaseSuppressesOnlyPreActivationFrameEvents(t *testing.T) {
	plan := compatTranscriptRebasePlan{byFrame: map[string]transcriptstore.FrameRealtimeRebase{
		"frame-a": {OwnerID: "owner", SessionID: "frame-a", RootFrameID: "root-a", RealtimeHighWater: 7},
	}}
	for _, test := range []struct {
		event workspace.RealtimeEvent
		want  bool
	}{
		{event: workspace.RealtimeEvent{Sequence: 7, UserID: "owner", RootFrameID: "root-a", FrameID: "frame-a"}, want: true},
		{event: workspace.RealtimeEvent{Sequence: 8, UserID: "owner", RootFrameID: "root-a", FrameID: "frame-a"}, want: false},
		{event: workspace.RealtimeEvent{Sequence: 6, UserID: "foreign", RootFrameID: "root-a", FrameID: "frame-a"}, want: false},
		{event: workspace.RealtimeEvent{Sequence: 6, UserID: "owner", RootFrameID: "root-child", FrameID: "frame-a"}, want: false},
		{event: workspace.RealtimeEvent{Sequence: 6, UserID: "owner", RootFrameID: "root-a", FrameID: "frame-b"}, want: false},
		{event: workspace.RealtimeEvent{Sequence: 6}, want: false},
	} {
		if got := plan.suppresses(test.event); got != test.want {
			t.Fatalf("event=%#v suppress=%t want=%t", test.event, got, test.want)
		}
	}
}

func TestCompatWebSocketReceivesDurableEventAfterOutboxFanout(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{Workspace: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx,
		"ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/events/ws?userId=owner&after_sequence=latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if connected := readCompatWebSocketTest(t, ctx, connection); connected["type"] != "connected" {
		t.Fatalf("connected=%#v", connected)
	}
	event, err := store.AppendRealtimeEvent(workspace.RealtimeEventInput{
		ID: "transcript-history-rebase:" + strings.Repeat("a", 64), UserID: "owner",
		ProjectID: "project", RootFrameID: "frame", FrameID: "frame", Type: "conversation.historyRebased",
		Payload: map[string]any{
			"conversation_id": "frame", "project_id": "project", "root_frame_id": "frame",
			"activation_id": strings.Repeat("a", 64), "authority_generation": 2, "target_epoch": 2,
			"active_branch_id": "br_00000001", "branch_generation": 1, "realtime_high_water": 0,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.FanoutRealtimeOutbox(event, nil); err != nil {
		t.Fatal(err)
	}
	message := readCompatWebSocketTest(t, ctx, connection)
	if message["type"] != "conversation.historyRebased" || numberValue(message["_sequence"]) != event.Sequence ||
		message["activation_id"] != strings.Repeat("a", 64) || message["_kind"] != "invalidate" {
		t.Fatalf("rebase=%#v event=%#v", message, event)
	}
}

func TestCompatWebSocketLiveCatchUpDoesNotDependOnGlobalTranscriptDrain(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Workspace: store, Transcript: repository})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx,
		"ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/events/ws?userId=owner&after_sequence=latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if connected := readCompatWebSocketTest(t, ctx, connection); connected["type"] != "connected" {
		t.Fatalf("connected=%#v", connected)
	}

	// A transcript projection fault or active owner drain must not block
	// unrelated durable events on an already-established connection.
	srv.transcriptContractErr = errors.New("transcript projection unavailable")
	event, err := srv.publishCompatEvent(workspace.RealtimeEventInput{
		ID: "ordinary-live-event", UserID: "owner", Type: "conversation.listChanged",
		Payload: map[string]any{"conversation_id": "conversation", "action": "updated"},
	})
	if err != nil {
		t.Fatal(err)
	}
	message := readCompatWebSocketTest(t, ctx, connection)
	if message["type"] != event.Type || message["conversation_id"] != "conversation" ||
		message["action"] != "updated" || numberValue(message["_sequence"]) != event.Sequence {
		t.Fatalf("message=%#v event=%#v", message, event)
	}
}

func TestCompatWebSocketConnectsWhenUnrelatedTranscriptDeliveryIsPoisoned(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seedTranscriptWebFrame(t, store, "owner-poison", "project-poison", "frame-poison")
	stream, err := repository.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "stream-poison", OwnerID: "owner-poison", ExternalID: "frame-poison", SessionID: "frame-poison",
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "project-poison", RootFrameID: "frame-poison",
		FrameID: "frame-poison", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repository.AppendUserEvent(context.Background(), transcriptstore.AppendUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "poison-user",
		PayloadJSON: []byte(`{"text":"work"}`), Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`UPDATE transcript_delivery_intents
		SET status='failed',attempt_count=100,last_max_attempts=100,last_error_code='projection_failed'
		WHERE stream_uid=? AND destination=?`, stream.UID, transcriptWebDestination); err != nil {
		t.Fatal(err)
	}

	srv := New(Options{Workspace: store, Transcript: repository})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	connection, response, err := websocket.Dial(ctx,
		"ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/events/ws?userId=owner-poison&after_sequence=latest", nil)
	if err != nil {
		var body []byte
		if response != nil && response.Body != nil {
			body, _ = io.ReadAll(response.Body)
		}
		t.Fatalf("realtime response=%#v body=%s error=%v", response, body, err)
	}
	defer connection.CloseNow()
	if connected := readCompatWebSocketTest(t, ctx, connection); connected["type"] != "connected" {
		t.Fatalf("connected=%#v", connected)
	}
}

func TestCompatRealtimeHubWakeDrainsDurableEventsInSequence(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{Workspace: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx,
		"ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/events/ws?userId=owner&after_sequence=latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if connected := readCompatWebSocketTest(t, ctx, connection); connected["type"] != "connected" {
		t.Fatalf("connected=%#v", connected)
	}

	sseRequest, err := http.NewRequestWithContext(ctx, http.MethodGet,
		httpServer.URL+"/api/events/stream?after_sequence=latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	sseRequest.Header.Set("X-Synon-User-Id", "owner")
	sseResponse, err := httpServer.Client().Do(sseRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResponse.Body.Close()
	sseReader := bufio.NewReader(sseResponse.Body)

	rebase, err := store.AppendRealtimeEvent(workspace.RealtimeEventInput{
		ID: "durable-rebase", UserID: "owner", Type: "conversation.historyRebased",
		Payload: map[string]any{"conversation_id": "frame", "activation_id": strings.Repeat("b", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	live, err := srv.publishCompatEvent(workspace.RealtimeEventInput{
		ID: "hub-live", UserID: "owner", Type: "frame_update", Payload: map[string]any{"status": "running"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if live.Sequence != rebase.Sequence+1 {
		t.Fatalf("sequences rebase=%d live=%d", rebase.Sequence, live.Sequence)
	}

	for index, want := range []workspace.RealtimeEvent{rebase, live} {
		message := readCompatWebSocketTest(t, ctx, connection)
		if numberValue(message["_sequence"]) != want.Sequence || message["type"] != want.Type {
			t.Fatalf("websocket[%d]=%#v want=%#v", index, message, want)
		}
		sseEvent := readCompatSSEEventTest(t, sseReader)
		if sseEvent.id != want.Sequence || sseEvent.eventType != want.Type {
			t.Fatalf("sse[%d]=%#v want=%#v", index, sseEvent, want)
		}
	}
}

type compatSSETestEvent struct {
	id        int64
	eventType string
	data      map[string]any
}

func readCompatSSEEventTest(t *testing.T, reader *bufio.Reader) compatSSETestEvent {
	t.Helper()
	var result compatSSETestEvent
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			return result
		}
		switch {
		case strings.HasPrefix(line, "id: "):
			result.id, err = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
		case strings.HasPrefix(line, "event: "):
			result.eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &result.data); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestCompatSSECursorResetUpdatesReconnectLastEventID(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{Workspace: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	history, err := srv.publishCompatEvent(workspace.RealtimeEventInput{
		ID: "history", UserID: "owner", Type: "frame_update", Payload: map[string]any{"status": "running"},
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)

	resetContext, cancelReset := context.WithTimeout(context.Background(), 5*time.Second)
	resetRequest, err := http.NewRequestWithContext(resetContext, http.MethodGet,
		httpServer.URL+"/api/events/stream?after_sequence=999999", nil)
	if err != nil {
		t.Fatal(err)
	}
	resetRequest.Header.Set("X-Synon-User-Id", "owner")
	resetResponse, err := httpServer.Client().Do(resetRequest)
	if err != nil {
		t.Fatal(err)
	}
	reset := readCompatSSEEventTest(t, bufio.NewReader(resetResponse.Body))
	if reset.eventType != "realtime.cursorReset" || reset.id != history.Sequence || numberValue(reset.data["cursor"]) != history.Sequence {
		t.Fatalf("reset=%#v history=%#v", reset, history)
	}
	cancelReset()
	_ = resetResponse.Body.Close()

	reconnectContext, cancelReconnect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelReconnect()
	reconnectRequest, err := http.NewRequestWithContext(reconnectContext, http.MethodGet,
		httpServer.URL+"/api/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	reconnectRequest.Header.Set("X-Synon-User-Id", "owner")
	reconnectRequest.Header.Set("Last-Event-ID", strconv.FormatInt(reset.id, 10))
	reconnectResponse, err := httpServer.Client().Do(reconnectRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer reconnectResponse.Body.Close()
	live, err := srv.publishCompatEvent(workspace.RealtimeEventInput{
		ID: "after-reset", UserID: "owner", Type: "frame_update", Payload: map[string]any{"status": "completed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	replayed := readCompatSSEEventTest(t, bufio.NewReader(reconnectResponse.Body))
	if replayed.id != live.Sequence || replayed.eventType != live.Type {
		t.Fatalf("replayed=%#v live=%#v", replayed, live)
	}
}

func TestCompatReplayScansPastSuppressedFullPage(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for index := 0; index < 1000; index++ {
		if _, err := store.AppendRealtimeEvent(workspace.RealtimeEventInput{
			ID: fmt.Sprintf("legacy-%04d", index), UserID: "owner", RootFrameID: "root", FrameID: "frame",
			Type: "frame_update", Payload: map[string]any{"index": index},
		}); err != nil {
			t.Fatal(err)
		}
	}
	latest, err := store.AppendRealtimeEvent(workspace.RealtimeEventInput{
		ID: "new-event", UserID: "owner", RootFrameID: "other", FrameID: "other",
		Type: "frame_update", Payload: map[string]any{"current": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	seen := make([]string, 0, 1)
	cursor, complete, err := replayCompatEvents(
		store, workspace.RealtimeEventFilter{UserID: "owner"}, latest.Sequence,
		func(event workspace.RealtimeEvent) bool { seen = append(seen, event.ID); return true },
		func(event workspace.RealtimeEvent) bool { return event.FrameID == "frame" },
	)
	if err != nil || !complete || cursor != latest.Sequence || !reflect.DeepEqual(seen, []string{"new-event"}) {
		t.Fatalf("cursor=%d complete=%t seen=%#v err=%v", cursor, complete, seen, err)
	}
}

func TestCompatEventsReplayOverHTTPSSENAndWebSocket(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{Workspace: store})
	first, err := srv.publishCompatEvent(workspace.RealtimeEventInput{
		UserID: "user-1", ProjectID: "project-1", RootFrameID: "root", FrameID: "frame",
		Type: "frame_update", Payload: map[string]any{
			"project_id": "project-1", "root_frame_id": "root", "frame_id": "frame", "status": "running",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.publishCompatEvent(workspace.RealtimeEventInput{
		UserID: "user-2", Type: "update_available", Payload: map[string]any{"version": "private"},
	}); err != nil {
		t.Fatal(err)
	}
	testServer := httptest.NewServer(srv.Handler())
	t.Cleanup(testServer.Close)

	listed := runtimeCompatJSON(t, srv.Handler(), http.MethodGet, "/api/events", "user-1", nil, http.StatusOK)
	events := listed["events"].([]any)
	if len(events) != 1 || events[0].(map[string]any)["type"] != "frame_update" {
		t.Fatalf("listed events=%#v", listed)
	}
	foreign := runtimeCompatJSON(t, srv.Handler(), http.MethodGet, "/api/events", "user-2", nil, http.StatusOK)
	if len(foreign["events"].([]any)) != 1 || foreign["events"].([]any)[0].(map[string]any)["userId"] != "user-2" {
		t.Fatalf("foreign events=%#v", foreign)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sseRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, testServer.URL+"/api/events/stream?after_sequence=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	sseRequest.Header.Set("X-Synon-User-Id", "user-1")
	sseResponse, err := testServer.Client().Do(sseRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer sseResponse.Body.Close()
	reader := bufio.NewReader(sseResponse.Body)
	lines := make([]string, 0, 3)
	for len(lines) < 3 {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "id: "+formatTestInt(first.Sequence)) || !strings.Contains(joined, "event: frame_update") || !strings.Contains(joined, "invalidations") {
		t.Fatalf("SSE replay=%q", joined)
	}

	websocketURL := "ws" + strings.TrimPrefix(testServer.URL, "http") + "/api/events/ws?userId=user-1&after_sequence=0"
	connection, _, err := websocket.Dial(ctx, websocketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "test complete")
	connected := readCompatWebSocketTest(t, ctx, connection)
	if connected["type"] != "connected" {
		t.Fatalf("connected=%#v", connected)
	}
	replayed := readCompatWebSocketTest(t, ctx, connection)
	if replayed["type"] != "frame_update" || replayed["frame_id"] != "frame" {
		t.Fatalf("replayed=%#v", replayed)
	}
	live, err := srv.publishCompatEvent(workspace.RealtimeEventInput{
		UserID: "user-1", ProjectID: "project-1", RootFrameID: "root", FrameID: "frame",
		Type: "text_chunk", Payload: map[string]any{"text": "live"},
	})
	if err != nil {
		t.Fatal(err)
	}
	liveMessage := readCompatWebSocketTest(t, ctx, connection)
	if liveMessage["type"] != "text_chunk" || liveMessage["text"] != "live" || liveMessage["_sequence"] != float64(live.Sequence) {
		t.Fatalf("live=%#v", liveMessage)
	}
	srv.compatEvents.PublishDirect(compatDirectEvent{
		UserID: "user-1", ProjectID: "project-1", RootFrameID: "root", FrameID: "frame",
		Message: map[string]any{"type": "file_changed", "path": "/tmp/result.csv"},
	})
	direct := readCompatWebSocketTest(t, ctx, connection)
	if direct["type"] != "file_changed" || direct["path"] != "/tmp/result.csv" || direct["_sequence"] != nil {
		t.Fatalf("direct=%#v", direct)
	}
	if err := writeWebSocketJSON(ctx, connection, map[string]any{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
	if pong := readCompatWebSocketTest(t, ctx, connection); pong["type"] != "pong" {
		t.Fatalf("pong=%#v", pong)
	}
}

func TestCompatEventHubCoalescesDurableWakeWithoutClosingSubscriber(t *testing.T) {
	hub := newCompatEventHub()
	channel, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	disconnected := 0
	for sequence := int64(1); sequence <= compatEventSubscriberBuffer*4; sequence++ {
		disconnected += hub.Publish(workspace.RealtimeEvent{Sequence: sequence, Type: "text_chunk"})
	}
	if disconnected != 0 {
		t.Fatalf("disconnected=%d", disconnected)
	}
	first, open := <-channel
	if !open || first.Sequence != 1 {
		t.Fatalf("first=%#v open=%t", first, open)
	}
	select {
	case duplicate := <-channel:
		t.Fatalf("duplicate wake=%#v", duplicate)
	default:
	}
	if disconnected := hub.Publish(workspace.RealtimeEvent{Sequence: 999, Type: "text_chunk"}); disconnected != 0 {
		t.Fatalf("second disconnected=%d", disconnected)
	}
	if next, open := <-channel; !open || next.Sequence != 999 {
		t.Fatalf("next=%#v open=%t", next, open)
	}
}

func TestCompatEventHubKeepsDirectEventOutsideDurableCursor(t *testing.T) {
	hub := newCompatEventHub()
	durable, unsubscribeDurable := hub.Subscribe()
	defer unsubscribeDurable()
	direct, unsubscribeDirect := hub.SubscribeDirect()
	defer unsubscribeDirect()

	event := compatDirectEvent{
		UserID: "user-1", ProjectID: "project-1", RootFrameID: "root-1", FrameID: "frame-1",
		Message: map[string]any{"type": "file_changed", "path": "/tmp/result.csv"},
	}
	if disconnected := hub.PublishDirect(event); disconnected != 0 {
		t.Fatalf("disconnected=%d", disconnected)
	}
	if got := <-direct; got.UserID != event.UserID || got.FrameID != event.FrameID || got.Message["path"] != "/tmp/result.csv" {
		t.Fatalf("direct=%#v", got)
	}
	select {
	case got := <-durable:
		t.Fatalf("direct event leaked into durable hub: %#v", got)
	default:
	}
	filter := workspace.RealtimeEventFilter{ProjectID: "project-1", RootFrameID: "root-1", FrameID: "frame-1"}
	if !compatDirectEventVisible(event, "user-1", filter) || compatDirectEventVisible(event, "user-2", filter) {
		t.Fatal("direct preview owner visibility mismatch")
	}
}

func TestCompatEventCursorParsing(t *testing.T) {
	tests := []struct {
		name, query, header string
		wantLatest          bool
		wantSequence        int64
		wantError           bool
	}{
		{name: "omitted"},
		{name: "query", query: "42", header: "7", wantSequence: 42},
		{name: "latest query", query: "latest", header: "7", wantLatest: true},
		{name: "header", header: "9", wantSequence: 9},
		{name: "latest header", header: "latest", wantLatest: true},
		{name: "negative", query: "-1", wantError: true},
		{name: "overflow", query: "9223372036854775808", wantError: true},
		{name: "invalid", query: "newest", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/events?after_sequence="+test.query, nil)
			request.Header.Set("Last-Event-ID", test.header)
			cursor, err := compatEventCursor(request)
			if (err != nil) != test.wantError {
				t.Fatalf("cursor=%#v err=%v", cursor, err)
			}
			if err == nil && (cursor.Latest != test.wantLatest || cursor.Sequence != test.wantSequence) {
				t.Fatalf("cursor=%#v", cursor)
			}
		})
	}
}

func TestCompatEventReplayIsBoundedAndAdvancesAcrossDeliveryNone(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	appendEvent := func(eventType string) workspace.RealtimeEvent {
		t.Helper()
		event, err := store.AppendRealtimeEvent(workspace.RealtimeEventInput{UserID: "owner", Type: eventType})
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	for index := 0; index < 1001; index++ {
		appendEvent("text_chunk")
	}
	none := appendEvent("rolling_compact_status")
	high, err := store.LatestRealtimeEventSequence(workspace.RealtimeEventFilter{UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	visited := 0
	cursor, ok, err := replayCompatEvents(store, workspace.RealtimeEventFilter{UserID: "owner"}, high, func(workspace.RealtimeEvent) bool {
		visited++
		if visited == 1 {
			for index := 0; index < 25; index++ {
				appendEvent("text_chunk")
			}
		}
		return true
	})
	if err != nil || !ok || cursor != none.Sequence || visited != 1001 {
		t.Fatalf("cursor=%d high=%d visited=%d ok=%v", cursor, high, visited, ok)
	}
	origin, err := store.LatestRealtimeEventSequence(workspace.RealtimeEventFilter{UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	betweenSnapshotAndSubscribe := appendEvent("text_chunk")
	hub := newCompatEventHub()
	channel, unsubscribe := hub.Subscribe()
	defer unsubscribe()
	betweenSubscribeAndHigh := appendEvent("text_chunk")
	hub.Publish(betweenSubscribeAndHigh)
	catchupHigh, err := store.LatestRealtimeEventSequence(workspace.RealtimeEventFilter{UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	seen := make([]int64, 0, 2)
	cursor, ok, err = replayCompatEvents(store, workspace.RealtimeEventFilter{UserID: "owner", AfterSequence: origin}, catchupHigh, func(event workspace.RealtimeEvent) bool {
		seen = append(seen, event.Sequence)
		return true
	})
	duplicate := <-channel
	if err != nil || !ok || cursor != catchupHigh || duplicate.Sequence > cursor || len(seen) != 2 || seen[0] != betweenSnapshotAndSubscribe.Sequence || seen[1] != betweenSubscribeAndHigh.Sequence {
		t.Fatalf("race replay cursor=%d high=%d duplicate=%d seen=%v", cursor, catchupHigh, duplicate.Sequence, seen)
	}
}

func TestCompatWebSocketServicesControlsBetweenDurableCatchUpPages(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{Workspace: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	httpServer := httptest.NewServer(srv.Handler())
	t.Cleanup(httpServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx,
		"ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/events/ws?userId=owner&after_sequence=latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	if connected := readCompatWebSocketTest(t, ctx, connection); connected["type"] != "connected" {
		t.Fatalf("connected=%#v", connected)
	}

	var last workspace.RealtimeEvent
	for index := 0; index < compatDurableCatchUpPageSize*4; index++ {
		last, err = store.AppendRealtimeEvent(workspace.RealtimeEventInput{
			ID: fmt.Sprintf("backlog-%04d", index), UserID: "owner", Type: "frame_update",
			Payload: map[string]any{"status": "running", "index": index},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	srv.compatEvents.Publish(last)
	for range 2 {
		if err := writeWebSocketJSON(ctx, connection, map[string]any{"type": "ping"}); err != nil {
			t.Fatal(err)
		}
	}

	pongs := 0
	eventsSincePong := 0
	lastSequence := int64(0)
	for pongs < 2 {
		message := readCompatWebSocketTest(t, ctx, connection)
		if message["type"] == "pong" {
			pongs++
			eventsSincePong = 0
			continue
		}
		sequence := int64(numberValue(message["_sequence"]))
		if sequence <= lastSequence {
			t.Fatalf("non-monotonic message=%#v last=%d", message, lastSequence)
		}
		lastSequence = sequence
		eventsSincePong++
		if eventsSincePong > compatDurableCatchUpPageSize {
			t.Fatalf("control starved behind %d durable events", eventsSincePong)
		}
	}
}

func TestCompatEventDeletedScopeRequiresExactOwnedHistory(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "deleted", UserID: "owner", Name: "Deleted"}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.CreateFrameInput{
		{ID: "root", ProjectID: "deleted", AgentName: "agent", Status: "running", ConversationType: "task"},
		{ID: "child", ProjectID: "deleted", ParentFrameID: "root", AgentName: "agent", Status: "running", ConversationType: "delegate"},
	} {
		if _, err := store.CreateFrame(input); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(Options{Workspace: store})
	history, err := srv.publishCompatEvent(workspace.RealtimeEventInput{UserID: "owner", ProjectID: "deleted", RootFrameID: "root", FrameID: "child", Type: "text_chunk"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.publishCompatEvent(workspace.RealtimeEventInput{ProjectID: "global-only", Type: "update_available"}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProject("deleted"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/events?project_id=deleted", "/api/events?root_frame_id=root", "/api/events?frame_id=child",
		"/api/events?project_id=deleted&root_frame_id=root&frame_id=child",
	} {
		result := runtimeCompatJSON(t, srv.Handler(), http.MethodGet, path, "owner", nil, http.StatusOK)
		if len(result["events"].([]any)) != 1 {
			t.Fatalf("path=%s result=%#v", path, result)
		}
	}
	for _, test := range []struct {
		path, userID string
		status       int
	}{
		{path: "/api/events?project_id=deleted", userID: "foreign", status: http.StatusNotFound},
		{path: "/api/events?project_id=missing", userID: "owner", status: http.StatusNotFound},
		{path: "/api/events?project_id=deleted&root_frame_id=wrong", userID: "owner", status: http.StatusNotFound},
		{path: "/api/events?project_id=global-only", userID: "owner", status: http.StatusNotFound},
		{path: "/api/events?frame_id=child&type=frame_update", userID: "owner", status: http.StatusOK},
	} {
		result := runtimeCompatJSON(t, srv.Handler(), http.MethodGet, test.path, test.userID, nil, test.status)
		if test.status == http.StatusOK && len(result["events"].([]any)) != 0 {
			t.Fatalf("type-filtered result=%#v", result)
		}
	}
	testServer := httptest.NewServer(srv.Handler())
	t.Cleanup(testServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(testServer.URL, "http")+"/api/events/ws?userId=owner&project_id=deleted&root_frame_id=root&frame_id=child", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	_ = readCompatWebSocketTest(t, ctx, connection)
	if message := readCompatWebSocketTest(t, ctx, connection); numberValue(message["_sequence"]) != history.Sequence {
		t.Fatalf("history message=%#v", message)
	}
}

func TestCompatEventReplayStorageFailureIsDiagnostic(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{Workspace: store})
	event, err := srv.publishCompatEvent(workspace.RealtimeEventInput{UserID: "owner", Type: "text_chunk"})
	if err != nil {
		t.Fatal(err)
	}
	if _, complete, err := replayCompatEvents(store, workspace.RealtimeEventFilter{UserID: "owner"}, event.Sequence, func(workspace.RealtimeEvent) bool { return false }); err != nil || complete {
		t.Fatalf("transport stop complete=%v err=%v", complete, err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE realtime_events SET payload = '{' WHERE sequence = ?`, event.Sequence); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/events/stream", nil)
	request.Header.Set("X-Synon-User-Id", "owner")
	srv.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "realtime event replay failed") || strings.Contains(recorder.Body.String(), "unexpected end") {
		t.Fatalf("SSE status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	testServer := httptest.NewServer(srv.Handler())
	t.Cleanup(testServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(testServer.URL, "http")+"/api/events/ws?userId=owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = readCompatWebSocketTest(t, ctx, connection)
	if _, _, err := connection.Read(ctx); websocket.CloseStatus(err) != websocket.StatusInternalError {
		t.Fatalf("WebSocket close status=%v err=%v", websocket.CloseStatus(err), err)
	}
}

func TestCompatEventLatestFutureAndScopeAcrossTransports(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []workspace.CreateProjectInput{
		{ID: "project-a", UserID: "owner-a", Name: "A"}, {ID: "project-a2", UserID: "owner-a", Name: "A2"}, {ID: "project-b", UserID: "owner-b", Name: "B"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	for _, frame := range []workspace.CreateFrameInput{
		{ID: "root-a", ProjectID: "project-a", AgentName: "agent", Status: "running", ConversationType: "task"},
		{ID: "child-a", ProjectID: "project-a", ParentFrameID: "root-a", AgentName: "agent", Status: "running", ConversationType: "delegate"},
		{ID: "root-a2", ProjectID: "project-a2", AgentName: "agent", Status: "running", ConversationType: "task"},
		{ID: "root-b", ProjectID: "project-b", AgentName: "agent", Status: "running", ConversationType: "task"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(Options{Workspace: store})
	history, err := srv.publishCompatEvent(workspace.RealtimeEventInput{UserID: "owner-a", ProjectID: "project-a", RootFrameID: "root-a", FrameID: "child-a", Type: "text_chunk", Payload: map[string]any{"text": "history"}})
	if err != nil {
		t.Fatal(err)
	}
	latest := runtimeCompatJSON(t, srv.Handler(), http.MethodGet, "/api/events?after_sequence=latest", "owner-a", nil, http.StatusOK)
	if len(latest["events"].([]any)) != 0 || numberValue(latest["next_sequence"]) != history.Sequence {
		t.Fatalf("latest=%#v", latest)
	}
	future := runtimeCompatJSON(t, srv.Handler(), http.MethodGet, "/api/events?after_sequence=999999", "owner-a", nil, http.StatusOK)
	if numberValue(future["next_sequence"]) != history.Sequence || future["cursor_reset"] != true {
		t.Fatalf("future=%#v", future)
	}
	for _, path := range []string{
		"/api/events?project_id=missing", "/api/events?project_id=project-b", "/api/events?root_frame_id=child-a",
		"/api/events?frame_id=root-b", "/api/events?project_id=project-a2&frame_id=child-a", "/api/events?root_frame_id=root-a&frame_id=root-a2",
	} {
		runtimeCompatJSON(t, srv.Handler(), http.MethodGet, path, "owner-a", nil, http.StatusNotFound)
	}
	runtimeCompatJSON(t, srv.Handler(), http.MethodGet, "/api/events?type=unknown", "owner-a", nil, http.StatusBadRequest)

	testServer := httptest.NewServer(srv.Handler())
	t.Cleanup(testServer.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(testServer.URL, "http")+"/api/events/ws?userId=owner-a&after_sequence=latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	connected := readCompatWebSocketTest(t, ctx, connection)
	if numberValue(connected["cursor"]) != history.Sequence {
		t.Fatalf("connected=%#v", connected)
	}
	futureConnection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(testServer.URL, "http")+"/api/events/ws?userId=owner-a&after_sequence=999999", nil)
	if err != nil {
		t.Fatal(err)
	}
	if futureConnected := readCompatWebSocketTest(t, ctx, futureConnection); numberValue(futureConnected["cursor"]) != history.Sequence {
		t.Fatalf("future connected=%#v", futureConnected)
	}
	if reset := readCompatWebSocketTest(t, ctx, futureConnection); reset["type"] != "realtime.cursorReset" || numberValue(reset["cursor"]) != history.Sequence {
		t.Fatalf("future reset=%#v", reset)
	}
	live, err := srv.publishCompatEvent(workspace.RealtimeEventInput{UserID: "owner-a", Type: "text_chunk", Payload: map[string]any{"text": "ws-live"}})
	if err != nil {
		t.Fatal(err)
	}
	if message := readCompatWebSocketTest(t, ctx, connection); numberValue(message["_sequence"]) != live.Sequence {
		t.Fatalf("live message=%#v", message)
	}
	if message := readCompatWebSocketTest(t, ctx, futureConnection); numberValue(message["_sequence"]) != live.Sequence {
		t.Fatalf("future live message=%#v", message)
	}
	_ = connection.Close(websocket.StatusNormalClosure, "done")
	_ = futureConnection.Close(websocket.StatusNormalClosure, "done")

	sseContext, stopSSE := context.WithCancel(context.Background())
	sseRequest, _ := http.NewRequestWithContext(sseContext, http.MethodGet, testServer.URL+"/api/events/stream?after_sequence=latest", nil)
	sseRequest.Header.Set("X-Synon-User-Id", "owner-a")
	sseResponse, err := testServer.Client().Do(sseRequest)
	if err != nil {
		t.Fatal(err)
	}
	sseLive, err := srv.publishCompatEvent(workspace.RealtimeEventInput{UserID: "owner-a", Type: "text_chunk", Payload: map[string]any{"text": "sse-live"}})
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(sseResponse.Body)
	var lines strings.Builder
	for index := 0; index < 3; index++ {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		lines.WriteString(line)
	}
	stopSSE()
	_ = sseResponse.Body.Close()
	if !strings.Contains(lines.String(), "id: "+formatTestInt(sseLive.Sequence)) {
		t.Fatalf("SSE=%q", lines.String())
	}
	deadline := time.Now().Add(time.Second)
	for {
		srv.compatEvents.mu.Lock()
		subscribers := len(srv.compatEvents.subscribers)
		srv.compatEvents.mu.Unlock()
		if subscribers == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscribers after cancellation=%d", subscribers)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWorkspaceMutationsPublishDurableBaselineFrameEvents(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{FileRoot: t.TempDir(), Workspace: store})
	startServerRealtimeOutbox(t, store, srv)
	app := srv.Handler()

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects", map[string]any{
		"id": "project-1", "name": "Realtime",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/project-1/frames", map[string]any{
		"id": "frame-1", "agentName": "planner", "status": "running", "conversationType": "task",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/frames/frame-1/messages", map[string]any{
		"messageUuid": "message-1", "clientMessageId": "client-1", "text": "Ship the runtime.",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/benches/frame-1", map[string]any{
		"name": "Release", "task_summary": "Verify realtime parity.",
	}, http.StatusOK)
	waitServerRealtimeOutbox(t, store)

	var events []any
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		listed := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?project_id=project-1", "local", nil, http.StatusOK)
		events = listed["events"].([]any)
		if len(events) == 4 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(events) != 4 {
		t.Fatalf("baseline workspace events=%#v", events)
	}
	wantTypes := map[string]string{
		"project_created": "frame_update",
		"frame_created":   "frame_update",
		"user_message":    "frame_messages_delta",
		"bench_updated":   "frame_update",
	}
	seen := make(map[string]bool, len(wantTypes))
	lastSequence := float64(0)
	for index, raw := range events {
		event := raw.(map[string]any)
		payload := event["payload"].(map[string]any)
		source, _ := payload["source_event_type"].(string)
		if source == "" {
			source, _ = payload["action"].(string)
		}
		wantType, known := wantTypes[source]
		if !known || seen[source] || event["type"] != wantType || event["userId"] != "local" ||
			event["projectId"] != "project-1" {
			t.Fatalf("event[%d] source=%q event=%#v", index, source, event)
		}
		seen[source] = true
		if source != "project_created" && (event["rootFrameId"] != "frame-1" || event["frameId"] != "frame-1" ||
			payload["source_event_id"] == "" || payload["source_event_sequence"].(float64) <= 0) {
			t.Fatalf("event[%d] source metadata=%#v", index, event)
		}
		sequence := event["sequence"].(float64)
		if sequence <= lastSequence {
			t.Fatalf("event sequence is not monotonic: previous=%v current=%v", lastSequence, sequence)
		}
		lastSequence = sequence
		if len(event["invalidations"].([]any)) == 0 {
			t.Fatalf("event[%d] has no query invalidations: %#v", index, event)
		}
	}
	if len(seen) != len(wantTypes) {
		t.Fatalf("event sources=%v, want=%v", seen, wantTypes)
	}
}

func readCompatWebSocketTest(t *testing.T, ctx context.Context, connection *websocket.Conn) map[string]any {
	t.Helper()
	_, raw, err := connection.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	if err := json.Unmarshal(raw, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func formatTestInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
