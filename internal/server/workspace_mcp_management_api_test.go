package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceMCPManagementHTTPAPI(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store}).Handler()
	for index, name := range []string{"research", "writer"} {
		serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/agents", map[string]any{
			"id": "agent-" + name, "userId": "user-1", "name": name,
			"displayName": name, "description": "agent", "systemPrompt": "work",
			"skillNames": []string{}, "enabled": true, "index": index,
		}, http.StatusBadRequest)
		serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/agents", map[string]any{
			"id": "agent-" + name, "userId": "user-1", "name": name,
			"displayName": name, "description": "agent", "systemPrompt": "work",
		}, http.StatusOK)
	}
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/mcp/servers", map[string]any{
		"id": "mcp-1", "userId": "user-1", "name": "literature",
		"url": "https://mcp.example.test", "transport": "streamable-http",
	}, http.StatusOK)

	server := httptest.NewRecorder()
	app.ServeHTTP(server, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/servers/mcp-1?user_id=user-1", nil))
	if server.Code != http.StatusOK || !bytes.Contains(server.Body.Bytes(), []byte("\"name\":\"literature\"")) {
		t.Fatalf("get mcp server = %d: %s", server.Code, server.Body.String())
	}
	foreignRead := httptest.NewRecorder()
	app.ServeHTTP(foreignRead, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/servers/mcp-1?user_id=user-2", nil))
	if foreignRead.Code != http.StatusNotFound {
		t.Fatalf("cross-user mcp read = %d: %s", foreignRead.Code, foreignRead.Body.String())
	}
	foreignDelete := httptest.NewRecorder()
	app.ServeHTTP(foreignDelete, authenticatedWorkspaceURLRequest(http.MethodDelete, "/api/go/mcp/servers/mcp-1?user_id=user-2", nil))
	if foreignDelete.Code != http.StatusBadRequest {
		t.Fatalf("cross-user mcp delete = %d: %s", foreignDelete.Code, foreignDelete.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/mcp/servers/mcp-1", map[string]any{
		"userId": "user-1", "name": "evidence", "enabled": true,
	}, http.StatusOK)

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/mcp/servers/mcp-1/assignments/all", map[string]any{
		"userId": "user-1",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/mcp/servers/mcp-1/assignments/all", map[string]any{
		"userId": "user-1",
	}, http.StatusOK)
	assignments := httptest.NewRecorder()
	app.ServeHTTP(assignments, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/servers/mcp-1/assignments?user_id=user-1", nil))
	if assignments.Code != http.StatusOK || bytes.Count(assignments.Body.Bytes(), []byte("\"agentName\"")) != 2 {
		t.Fatalf("mcp assignments = %d: %s", assignments.Code, assignments.Body.String())
	}
	counts := httptest.NewRecorder()
	app.ServeHTTP(counts, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/attachments/counts?user_id=user-1", nil))
	if counts.Code != http.StatusOK || !bytes.Contains(counts.Body.Bytes(), []byte("\"research\":1")) ||
		!bytes.Contains(counts.Body.Bytes(), []byte("\"writer\":1")) {
		t.Fatalf("mcp attachment counts = %d: %s", counts.Code, counts.Body.String())
	}
	agentConnectors := httptest.NewRecorder()
	app.ServeHTTP(agentConnectors, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/agents/research/connectors?user_id=user-1", nil))
	if agentConnectors.Code != http.StatusOK || !bytes.Contains(agentConnectors.Body.Bytes(), []byte("\"id\":\"mcp-1\"")) {
		t.Fatalf("agent mcp servers = %d: %s", agentConnectors.Code, agentConnectors.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/mcp/servers/mcp-1/grants", map[string]any{
		"id": "grant-1", "userId": "user-1", "agentName": "research",
		"toolName": "search", "enabled": true,
	}, http.StatusOK)
	grants := httptest.NewRecorder()
	app.ServeHTTP(grants, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/servers/mcp-1/grants?user_id=user-1", nil))
	if grants.Code != http.StatusOK || !bytes.Contains(grants.Body.Bytes(), []byte("\"toolName\":\"search\"")) {
		t.Fatalf("mcp grants = %d: %s", grants.Code, grants.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/mcp/servers/mcp-1/assignments/research?user_id=user-1", nil, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/mcp/servers/mcp-1/assignments/all?user_id=user-1", nil, http.StatusOK)
	connectors := httptest.NewRecorder()
	app.ServeHTTP(connectors, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/connectors?user_id=user-1", nil))
	if connectors.Code != http.StatusOK || !bytes.Contains(connectors.Body.Bytes(), []byte("\"name\":\"evidence\"")) {
		t.Fatalf("mcp connectors = %d: %s", connectors.Code, connectors.Body.String())
	}

	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/mcp/servers/mcp-1?user_id=user-1", nil, http.StatusOK)
	deleted := httptest.NewRecorder()
	app.ServeHTTP(deleted, authenticatedWorkspaceURLRequest(http.MethodGet, "/api/go/mcp/servers/mcp-1?user_id=user-1", nil))
	if deleted.Code != http.StatusNotFound {
		t.Fatalf("deleted mcp server = %d: %s", deleted.Code, deleted.Body.String())
	}
	realtime := runtimeCompatJSON(t, app, http.MethodGet, "/api/events?type=connector_update&limit=100", "user-1", nil, http.StatusOK)
	if events := realtime["events"].([]any); len(events) != 8 {
		t.Fatalf("custom MCP connector update events=%#v", events)
	}
}
