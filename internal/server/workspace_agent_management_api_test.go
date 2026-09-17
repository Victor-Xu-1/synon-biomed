package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceAgentHTTPAPIUpdatesDeletesAndManagesSkills(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store}).Handler()
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/agents", map[string]any{
		"id": "agent-1", "userId": "user-1", "name": "research",
		"displayName": "Research", "description": "Find evidence", "systemPrompt": "Be precise",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/agents/research", map[string]any{
		"userId": "user-1", "displayName": "Senior Research",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/agents/research/skills/literature-review", map[string]any{
		"userId": "user-1",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/agents/research/skills/literature-review?user_id=user-1", nil, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPut, "/api/go/agents/research/skills", map[string]any{
		"userId": "user-1", "skillNames": []string{"citation-management", "patent-search"},
	}, http.StatusOK)

	response := httptest.NewRecorder()
	app.ServeHTTP(response, localWorkspaceRequest(http.MethodGet, "/api/go/agents?user_id=user-1", nil))
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("Senior Research")) || !bytes.Contains(response.Body.Bytes(), []byte("patent-search")) {
		t.Fatalf("updated agent = %d: %s", response.Code, response.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/agents/research?user_id=user-1", nil, http.StatusOK)
	deleted := httptest.NewRecorder()
	app.ServeHTTP(deleted, localWorkspaceRequest(http.MethodGet, "/api/go/agents?user_id=user-1", nil))
	if deleted.Code != http.StatusOK || bytes.Contains(deleted.Body.Bytes(), []byte("\"name\":\"research\"")) {
		t.Fatalf("deleted agent list = %d: %s", deleted.Code, deleted.Body.String())
	}
}
