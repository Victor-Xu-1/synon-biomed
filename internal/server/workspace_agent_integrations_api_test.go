package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceAgentPromptAndConnectorHTTPAPI(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store}).Handler()

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/agents", map[string]any{
		"id": "agent-1", "userId": "user-1", "name": "research",
		"displayName": "Research", "description": "Find evidence", "systemPrompt": "Base prompt",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/mcp/servers", map[string]any{
		"id": "mcp-1", "userId": "user-1", "name": "literature",
		"url": "https://mcp.example.test", "transport": "streamable-http",
	}, http.StatusOK)

	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/agents/research/prompt", map[string]any{
		"userId": "user-1", "promptText": "Always cite primary sources.",
	}, http.StatusOK)
	prompt := httptest.NewRecorder()
	app.ServeHTTP(prompt, newLoopbackTestRequest(http.MethodGet, "/api/go/agents/research/prompt?user_id=user-1", nil))
	if prompt.Code != http.StatusOK || !bytes.Contains(prompt.Body.Bytes(), []byte("Always cite primary sources.")) {
		t.Fatalf("custom prompt = %d: %s", prompt.Code, prompt.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/agents/research/connectors/mcp-1", map[string]any{
		"id": "assignment-1", "userId": "user-1",
	}, http.StatusOK)
	connectors := httptest.NewRecorder()
	app.ServeHTTP(connectors, newLoopbackTestRequest(http.MethodGet, "/api/go/agents/research/connectors?user_id=user-1", nil))
	if connectors.Code != http.StatusOK || !bytes.Contains(connectors.Body.Bytes(), []byte("\"id\":\"mcp-1\"")) {
		t.Fatalf("agent connectors = %d: %s", connectors.Code, connectors.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/agents/research/connector-exclusions", map[string]any{
		"userId": "user-1", "connectorIds": []string{"bundled:bio", "mcp-blocked", "bundled:bio", ""},
	}, http.StatusOK)
	exclusions := httptest.NewRecorder()
	app.ServeHTTP(exclusions, newLoopbackTestRequest(http.MethodGet, "/api/go/agents/research/connector-exclusions?user_id=user-1", nil))
	if exclusions.Code != http.StatusOK ||
		bytes.Count(exclusions.Body.Bytes(), []byte("bundled:bio")) != 1 ||
		!bytes.Contains(exclusions.Body.Bytes(), []byte("mcp-blocked")) {
		t.Fatalf("connector exclusions = %d: %s", exclusions.Code, exclusions.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/agents/research/connectors/mcp-1?user_id=user-1", nil, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/agents/research/prompt?user_id=user-1", nil, http.StatusOK)
	deletedPrompt := httptest.NewRecorder()
	app.ServeHTTP(deletedPrompt, newLoopbackTestRequest(http.MethodGet, "/api/go/agents/research/prompt?user_id=user-1", nil))
	if deletedPrompt.Code != http.StatusOK || !bytes.Contains(deletedPrompt.Body.Bytes(), []byte("\"prompt\":null")) {
		t.Fatalf("deleted prompt = %d: %s", deletedPrompt.Code, deletedPrompt.Body.String())
	}
	realtime := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?type=connector_update&limit=100", "user-1", nil, http.StatusOK)
	if events := realtime["events"].([]any); len(events) != 4 {
		t.Fatalf("agent connector realtime events=%#v", events)
	}
}
