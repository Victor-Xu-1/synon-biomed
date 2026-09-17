package server

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceMCPHTTPAPIManagesAssignmentsGrantsAndOAuth(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateAgent(workspace.CreateAgentInput{
		ID: "agent-1", UserID: "user-1", Name: "research", DisplayName: "Research",
		Description: "Research", SystemPrompt: "Research",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store})
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
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/mcp/servers", map[string]any{
		"id": "mcp-1", "userId": "user-1", "name": "evidence",
		"url": "https://mcp.example.test", "transport": "streamable-http",
	}, http.StatusOK)
	list := httptest.NewRecorder()
	app.ServeHTTP(list, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/servers?user_id=user-1", nil))
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte("evidence")) {
		t.Fatalf("mcp servers = %d: %s", list.Code, list.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/mcp/servers/mcp-1/assignments", map[string]any{
		"id": "assignment-1", "userId": "user-1", "agentName": "research",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/mcp/servers/mcp-1/grants", map[string]any{
		"id": "grant-1", "userId": "user-1", "agentName": "research",
		"toolName": "search", "enabled": true,
	}, http.StatusOK)
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/mcp/servers/mcp-1/oauth", map[string]any{
		"userId": "user-1", "accessTokenRef": "secret://mcp/evidence",
		"tokenType": "Bearer", "expiresAt": expires, "scopes": []string{"tools.read"},
	}, http.StatusOK)
	status := httptest.NewRecorder()
	app.ServeHTTP(status, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/servers/mcp-1/oauth?user_id=user-1", nil))
	if status.Code != http.StatusOK || !bytes.Contains(status.Body.Bytes(), []byte("\"connected\":true")) || bytes.Contains(status.Body.Bytes(), []byte("secret://")) {
		t.Fatalf("oauth status = %d: %s", status.Code, status.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/mcp/servers/mcp-1/oauth?user_id=user-1", nil, http.StatusOK)
	disconnected := httptest.NewRecorder()
	app.ServeHTTP(disconnected, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/servers/mcp-1/oauth?user_id=user-1", nil))
	if disconnected.Code != http.StatusOK || !bytes.Contains(disconnected.Body.Bytes(), []byte("\"connected\":false")) {
		t.Fatalf("oauth disconnected = %d: %s", disconnected.Code, disconnected.Body.String())
	}
	realtime := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?limit=100", "user-1", nil, http.StatusOK)
	counts := map[string]int{}
	for _, raw := range realtime["events"].([]any) {
		counts[raw.(map[string]any)["type"].(string)]++
	}
	if counts["connector_update"] != 3 || counts["connector_status"] != 2 {
		t.Fatalf("custom MCP realtime event counts=%#v", counts)
	}
	target := map[string]int{"connector_update": 3, "connector_status": 2}
	liveEvents, err := readDomainWebSocketEvents(ctx, liveConnection, target, 5)
	if err != nil {
		t.Fatalf("read live MCP events: %v", err)
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
	replayedEvents, err := readDomainWebSocketEvents(ctx, replayConnection, target, 5)
	if err != nil {
		t.Fatalf("read replayed MCP events: %v", err)
	}
	if !reflect.DeepEqual(liveEvents, replayedEvents) {
		t.Fatalf("live and replayed MCP events differ: live=%#v replay=%#v", liveEvents, replayedEvents)
	}
	for _, event := range liveEvents {
		assertDomainInvalidation(t, event, "mcpConnectors", []any{"mcp-connectors"}, "debounced", "exact")
		assertDomainInvalidation(t, event, "mcpAttachmentCounts", []any{"mcp-attachment-counts"}, "debounced", "exact")
		connectorID := stringValue(event["connector_id"])
		if connectorID == "" {
			t.Fatalf("MCP event has no connector identity: %#v", event)
		}
		assertDomainInvalidation(t, event, "customMCPServer",
			[]any{"custom-mcp-server", connectorID}, "debounced", "exact")
		assertDomainInvalidation(t, event, "mcpToolGrants",
			[]any{"mcp-tool-grants", connectorID}, "debounced", "exact")
		if event["agent_name"] == "research" {
			assertDomainInvalidation(t, event, "agentCustomMCPServers",
				[]any{"agent-custom-mcp-servers", "research"}, "debounced", "exact")
		}
	}
}
