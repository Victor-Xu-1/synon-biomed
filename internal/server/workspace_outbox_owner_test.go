package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestOutboxProjectFrameAndRoutineMutationsPreserveOwnerScope(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(Options{Workspace: store}).Handler()
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects", map[string]any{"id": "owned-project", "name": "Owned"}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/owned-project/frames", map[string]any{
		"id": "owned-frame", "agentName": "planner", "status": "running", "conversationType": "task",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/routines", map[string]any{
		"id": "owned-routine", "rootFrameId": "owned-frame", "ownerUserId": "local",
		"onTick": "continue", "everyMinutes": 5, "enabled": true, "nextDue": time.Now().UTC().Add(time.Hour),
	}, http.StatusOK)

	assertOwnerRequestStatus(t, app, http.MethodPatch, "/api/go/projects/owned-project", map[string]any{"name": "stolen"}, "intruder", http.StatusNotFound)
	assertOwnerRequestStatus(t, app, http.MethodPost, "/api/go/projects/owned-project/frames", map[string]any{
		"id": "intruder-frame", "agentName": "planner", "status": "running", "conversationType": "task",
	}, "intruder", http.StatusNotFound)
	assertOwnerRequestStatus(t, app, http.MethodGet, "/api/go/routines/owned-routine", nil, "intruder", http.StatusNotFound)
	assertOwnerRequestStatus(t, app, http.MethodPost, "/api/go/routines", map[string]any{
		"id": "intruder-routine", "rootFrameId": "owned-frame", "ownerUserId": "intruder",
		"onTick": "steal", "everyMinutes": 5, "enabled": true,
	}, "local", http.StatusForbidden)

	project, found, err := store.GetProject("owned-project")
	if err != nil || !found || project.Name != "Owned" {
		t.Fatalf("owner project=%#v found=%v err=%v", project, found, err)
	}
	if _, found, err := store.GetFrame("intruder-frame"); err != nil || found {
		t.Fatalf("intruder frame found=%v err=%v", found, err)
	}
}

func assertOwnerRequestStatus(t *testing.T, app http.Handler, method, target string, body any, userID string, want int) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, target, bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Synon-User-Id", userID)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("%s %s user=%s status=%d want=%d body=%s", method, target, userID, response.Code, want, response.Body.String())
	}
}
