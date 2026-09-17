package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	workspace "synon-go/internal/persistence/workspace"
)

func TestSystemStateEventsDeliverLiveReplayAndExactInvalidations(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := New(Options{FileRoot: root, Workspace: store})
	app := server.Handler()
	httpServer := httptest.NewServer(app)
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

	runtimeCompatJSON(t, app, http.MethodPost, "/api/environments/retry", "user-1", map[string]any{}, http.StatusOK)
	runtimeCompatJSON(t, app, http.MethodPost, "/api/auth/logout", "user-1", map[string]any{}, http.StatusOK)
	granted := filepath.Join(t.TempDir(), "granted")
	if err := os.MkdirAll(granted, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeCompatJSON(t, app, http.MethodPost, "/api/go/preferences/host-grants", "user-1", map[string]any{
		"path": granted, "mode": "read",
	}, http.StatusOK)
	runtimeCompatJSON(t, app, http.MethodPut, "/api/go/settings/allowed-domains", "user-1", map[string]any{
		"domains": []string{"example.com"},
	}, http.StatusOK)

	target := map[string]int{
		"environment_status":     1,
		"auth_status_changed":    1,
		"host_access_granted":    1,
		"network_access_granted": 1,
	}
	liveEvents, err := readDomainWebSocketEvents(ctx, liveConnection, target, len(target))
	if err != nil {
		t.Fatalf("read live system events: %v", err)
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
	replayedEvents, err := readDomainWebSocketEvents(ctx, replayConnection, target, len(target))
	if err != nil {
		t.Fatalf("read replayed system events: %v", err)
	}
	if !reflect.DeepEqual(liveEvents, replayedEvents) {
		t.Fatalf("live and replayed system events differ: live=%#v replay=%#v", liveEvents, replayedEvents)
	}
	for _, event := range liveEvents {
		switch event["type"] {
		case "environment_status":
			assertDomainInvalidation(t, event, "environmentStatus",
				[]any{"environments", "status"}, "immediate", "exact")
		case "auth_status_changed":
			assertDomainInvalidation(t, event, "me", []any{"me"}, "immediate", "exact")
			assertDomainInvalidation(t, event, "auth.status", []any{"auth", "status"}, "immediate", "exact")
		case "host_access_granted":
			assertDomainInvalidation(t, event, "hostGrants", []any{"host-grants"}, "debounced", "exact")
		case "network_access_granted":
			assertDomainInvalidation(t, event, "remoteImageAllowlist",
				[]any{"remote-image-allowlist"}, "debounced", "exact")
		}
	}
}
