package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/realtime"
)

func TestRuntimeAuxiliaryEventsUseRealProducersLiveDeliveryAndReplay(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-a", UserID: "owner-a", Name: "Runtime auxiliary events",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root-a", ProjectID: "project-a", AgentName: "OPERON",
		Status: "completed", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}

	prepared := filepath.Join(root, "synon-go-next")
	if err := os.WriteFile(prepared, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var updateMu sync.Mutex
	nextVersion := "4.0.3"
	restartFails := true
	app := New(Options{
		Workspace: store, FileRoot: root, VerifierToken: "verifier-test-token",
		RuntimeUpdate: RuntimeUpdateOptions{
			Channel: "test", Current: "4.0.2",
			Check: func(context.Context, string, string) (string, error) {
				updateMu.Lock()
				defer updateMu.Unlock()
				return nextVersion, nil
			},
			Stage: func(context.Context, string, string) (string, error) {
				return prepared, nil
			},
		},
		RestartRuntime: func(string) error {
			updateMu.Lock()
			defer updateMu.Unlock()
			if restartFails {
				return errors.New("successor exited early")
			}
			return nil
		},
	})
	startServerRealtimeOutbox(t, store, app)
	httpServer := newRuntimeAuxHTTPServer(t, app)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	socketURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/api/ws?user_id=owner-a"
	connection, _, err := websocket.Dial(ctx, socketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	connection.SetReadLimit(1 << 20)
	defer connection.Close(websocket.StatusNormalClosure, "test complete")
	if connected := readCompatWebSocketTest(t, ctx, connection); connected["type"] != "connected" {
		t.Fatalf("connected=%#v", connected)
	}
	messages := make(chan map[string]any, 128)
	readErr := make(chan error, 1)
	go func() {
		for {
			var message map[string]any
			if err := runtimeAuxWSRead(ctx, connection, &message); err != nil {
				readErr <- err
				return
			}
			messages <- message
		}
	}()

	watchPath := filepath.Join(root, "watched.txt")
	if err := os.WriteFile(watchPath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeAuxWSWrite(t, ctx, connection, map[string]any{"type": "file_watch", "path": watchPath})
	runtimeAuxWSWrite(t, ctx, connection, map[string]any{"type": "ping"})
	for {
		select {
		case message := <-messages:
			if message["type"] == "pong" {
				goto watchReady
			}
		case err := <-readErr:
			t.Fatalf("file watch registration: %v", err)
		case <-ctx.Done():
			t.Fatal("file watch registration timed out")
		}
	}
watchReady:
	if err := os.WriteFile(watchPath, []byte("second version"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case message := <-messages:
			if message["type"] == "file_changed" {
				goto fileChanged
			}
		case err := <-readErr:
			t.Fatalf("file change delivery: %v", err)
		case <-ctx.Done():
			t.Fatal("file change delivery timed out")
		}
	}
fileChanged:

	runtimeCompatJSON(t, app.Handler(), "POST", "/api/mcp/apps/tool-call", "owner-a", map[string]any{
		"root_frame_id": "root-a", "request_id": "request-a",
		"mount_id": "mount-a", "name": "render", "arguments": map[string]any{"mode": "full"},
	}, 202)
	runtimeAuxJSONWithHeaders(t, app.Handler(), "POST", "/api/mcp/apps/pin", "owner-a", map[string]any{
		"root_frame_id": "root-a", "filename": "app-state.json",
		"content_type": "application/json", "content": "{\"ready\":true}",
		"tool": "open_app", "arguments": map[string]any{"name": "Example"},
	}, map[string]string{"Idempotency-Key": "mcp-pin-a"}, 201)
	runtimeCompatJSON(t, app.Handler(), "POST", "/api/frames/root-a/verification", "owner-a", map[string]any{
		"status": "done", "n": 1, "verdict": "pass", "count": 0,
		"version_ids": []string{},
	}, 403)
	verification := runtimeAuxJSONWithHeaders(t, app.Handler(), "POST", "/api/frames/root-a/verification", "owner-a", map[string]any{
		"status": "done", "n": 1, "verdict": "pass", "count": 0,
		"version_ids": []string{},
	}, map[string]string{
		"X-Synon-Verification-Token": "verifier-test-token",
	}, 202)
	if verification["sequence"] == nil {
		t.Fatalf("verification=%#v", verification)
	}

	runtimeCompatJSON(t, app.Handler(), "POST", "/api/status/update/check", "owner-a", map[string]any{}, 200)
	updateMu.Lock()
	nextVersion = ""
	updateMu.Unlock()
	runtimeCompatJSON(t, app.Handler(), "POST", "/api/status/update/check", "owner-a", map[string]any{}, 200)
	runtimeCompatJSON(t, app.Handler(), "POST", "/api/status/update/required", "owner-a", map[string]any{
		"required": "9.0.0",
	}, 200)
	updateMu.Lock()
	nextVersion = "4.0.4"
	updateMu.Unlock()
	runtimeCompatJSON(t, app.Handler(), "POST", "/api/status/update/check", "owner-a", map[string]any{}, 200)
	processing := "processing"
	if _, err := store.UpdateFrame("root-a", workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	runtimeCompatJSON(t, app.Handler(), "POST", "/api/status/update/apply", "owner-a", map[string]any{}, 409)
	completed := "completed"
	if _, err := store.UpdateFrame("root-a", workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	runtimeCompatJSON(t, app.Handler(), "POST", "/api/status/update/apply", "owner-a", map[string]any{}, 500)
	updateMu.Lock()
	restartFails = false
	updateMu.Unlock()
	runtimeCompatJSON(t, app.Handler(), "POST", "/api/status/update/apply", "owner-a", map[string]any{}, 200)

	if err := os.Remove(watchPath); err != nil {
		t.Fatal(err)
	}
	wantLive := map[string]bool{
		"file_changed": true, "file_deleted": false, "verification_update": false,
		"update_available": false, "update_retracted": false, "update_required": false,
		"daemon_restarting": false, "daemon_restart_aborted": false,
	}
	live := collectRuntimeAuxEventTypes(t, ctx, messages, readErr, wantLive)
	for eventType, found := range live {
		if !found {
			t.Fatalf("live event %s was not delivered: %#v", eventType, live)
		}
	}

	if err := connection.Close(websocket.StatusNormalClosure, "replay"); err != nil {
		t.Fatal(err)
	}
	replay, _, err := websocket.Dial(ctx, socketURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	replay.SetReadLimit(1 << 20)
	defer replay.Close(websocket.StatusNormalClosure, "done")
	if connected := readCompatWebSocketTest(t, ctx, replay); connected["type"] != "connected" {
		t.Fatalf("replay connected=%#v", connected)
	}
	wantReplay := map[string]bool{
		"verification_update": false,
		"update_available":    false, "update_retracted": false, "update_required": false,
		"daemon_restarting": false, "daemon_restart_aborted": false,
	}
	deadline, deadlineCancel := context.WithTimeout(ctx, 10*time.Second)
	defer deadlineCancel()
	for {
		all := true
		for _, found := range wantReplay {
			all = all && found
		}
		if all {
			break
		}
		var message map[string]any
		if err := runtimeAuxWSRead(deadline, replay, &message); err != nil {
			t.Fatalf("replay=%#v err=%v", wantReplay, err)
		}
		if _, tracked := wantReplay[stringValue(message["type"])]; tracked {
			wantReplay[stringValue(message["type"])] = true
		}
	}
	for eventType, found := range wantReplay {
		if !found {
			t.Fatalf("replay event %s was not delivered", eventType)
		}
	}
	durable, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "owner-a", IncludeGlobal: true, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range durable {
		if event.Type == "file_changed" || event.Type == "file_deleted" {
			t.Fatalf("connection-scoped file event was persisted: %#v", event)
		}
		if event.Type == "verification_update" {
			assertRuntimeAuxInvalidation(t, event.Invalidations, "verificationChecks", []any{"verification-checks", "root-a"})
		}
	}
}

func TestCompatFileWatchRejectsEscapesAndStopsAfterUnwatch(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store, FileRoot: root})
	httpServer := newRuntimeAuxHTTPServer(t, app)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(httpServer.URL, "http")+"/api/ws?user_id=owner-a", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(websocket.StatusNormalClosure, "done")
	readCompatWebSocketTest(t, ctx, connection)
	runtimeAuxWSWrite(t, ctx, connection, map[string]any{"type": "file_watch", "path": filepath.Join(root, "..", "outside")})
	var rejected map[string]any
	if err := runtimeAuxWSRead(ctx, connection, &rejected); err != nil {
		t.Fatal(err)
	}
	if rejected["type"] != "error" || rejected["code"] != "FILE_WATCH_REJECTED" {
		t.Fatalf("rejected=%#v", rejected)
	}
}

func TestRuntimeUpdateCheckSerializesAndCommitsOnlyAfterPublication(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entered := make(chan struct{})
	release := make(chan struct{})
	var enteredOnce sync.Once
	app := New(Options{
		Workspace: store,
		RuntimeUpdate: RuntimeUpdateOptions{
			Check: func(ctx context.Context, _, _ string) (string, error) {
				enteredOnce.Do(func() { close(entered) })
				select {
				case <-release:
					return "4.0.3", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
		},
	})
	firstStatus := make(chan int, 1)
	go func() {
		firstStatus <- runtimeAuxRequestStatus(app.Handler(), http.MethodPost, "/api/status/update/check")
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first update check did not start")
	}
	if status := runtimeAuxRequestStatus(app.Handler(), http.MethodPost, "/api/status/update/check"); status != http.StatusConflict {
		t.Fatalf("concurrent update check status=%d", status)
	}
	close(release)
	if status := <-firstStatus; status != http.StatusOK {
		t.Fatalf("first update check status=%d", status)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	failing := New(Options{
		Workspace: store,
		RuntimeUpdate: RuntimeUpdateOptions{
			Check: func(context.Context, string, string) (string, error) { return "4.0.4", nil },
		},
	})
	if status := runtimeAuxRequestStatus(failing.Handler(), http.MethodPost, "/api/status/update/check"); status != http.StatusInternalServerError {
		t.Fatalf("publication failure status=%d", status)
	}
	response := runtimeCompatJSON(t, failing.Handler(), http.MethodGet, "/api/status/update", "owner-a", nil, http.StatusOK)
	if response["update_available"] != false {
		t.Fatalf("failed publication committed update state: %#v", response)
	}
}

func runtimeAuxRequestStatus(handler http.Handler, method, path string) int {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newLoopbackTestRequest(method, path, nil))
	return response.Code
}

func collectRuntimeAuxEventTypes(
	t *testing.T,
	ctx context.Context,
	messages <-chan map[string]any,
	readErr <-chan error,
	tracked map[string]bool,
) map[string]bool {
	t.Helper()
	deadline, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	for {
		all := true
		for _, found := range tracked {
			all = all && found
		}
		if all {
			return tracked
		}
		select {
		case message := <-messages:
			if _, found := tracked[stringValue(message["type"])]; found {
				tracked[stringValue(message["type"])] = true
			}
		case err := <-readErr:
			t.Fatalf("live websocket read: %v", err)
		case <-deadline.Done():
			t.Fatalf("live events timed out: %#v", tracked)
		}
	}
}

func assertRuntimeAuxInvalidation(
	t *testing.T,
	invalidations []realtime.QueryInvalidation,
	name string,
	key []any,
) {
	t.Helper()
	for _, invalidation := range invalidations {
		if invalidation.Query == name && reflect.DeepEqual(invalidation.Key, key) {
			return
		}
	}
	t.Fatalf("missing invalidation %s %#v in %#v", name, key, invalidations)
}

func newRuntimeAuxHTTPServer(t *testing.T, app *Server) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	return server
}

func runtimeAuxWSRead(ctx context.Context, connection *websocket.Conn, target any) error {
	messageType, raw, err := connection.Read(ctx)
	if err != nil {
		return err
	}
	if messageType != websocket.MessageText {
		return errors.New("websocket message is not text")
	}
	return json.Unmarshal(raw, target)
}

func runtimeAuxWSWrite(t *testing.T, ctx context.Context, connection *websocket.Conn, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Write(ctx, websocket.MessageText, raw); err != nil {
		t.Fatal(err)
	}
}

func runtimeAuxJSONWithHeaders(
	t *testing.T,
	handler http.Handler,
	method, path, userID string,
	body any,
	headers map[string]string,
	wantStatus int,
) map[string]any {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	request := newLoopbackTestRequest(method, path, reader)
	request.Header.Set("X-Synon-User-ID", userID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, wantStatus, response.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("%s %s decode: %v body=%s", method, path, err, response.Body.String())
	}
	return decoded
}
