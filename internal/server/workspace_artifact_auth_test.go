package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceArtifactRoutesEnforceProjectOwnership(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, input := range []workspace.CreateProjectInput{
		{ID: "project-1", UserID: "user-1", Name: "One"},
		{ID: "project-2", UserID: "user-2", Name: "Two"},
	} {
		if _, err := store.CreateProject(input); err != nil {
			t.Fatal(err)
		}
	}
	_, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "secret.txt",
		Kind: "text/plain", Content: []byte("tenant secret"), CreatedBy: "user-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-1", ProjectID: "project-1", AgentName: "agent",
		Status: "completed", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: "frame-1", Type: "artifact_created", Payload: map[string]any{"artifactId": "artifact-1"},
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(Options{Workspace: store}).Handler()

	unauthenticated := httptest.NewRecorder()
	handler.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/go/artifacts/artifact-1", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.Code, unauthenticated.Body.String())
	}

	cases := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/go/artifacts/artifact-1", nil},
		{http.MethodGet, "/api/go/artifacts/artifact-1/content", nil},
		{http.MethodGet, "/api/go/artifacts/artifact-1/text", nil},
		{http.MethodGet, "/api/go/artifacts/artifact-1/versions", nil},
		{http.MethodGet, "/api/go/artifacts/artifact-1/lineage", nil},
		{http.MethodGet, "/api/go/artifact-versions/" + version.ID + "/content", nil},
		{http.MethodPatch, "/api/go/artifacts/artifact-1", map[string]any{"name": "stolen.txt"}},
		{http.MethodDelete, "/api/go/artifacts/artifact-1", nil},
		{http.MethodPost, "/api/go/artifacts/artifact-1/versions", map[string]any{
			"projectId": "project-1", "name": "stolen.txt", "kind": "text/plain", "content": "changed",
		}},
		{http.MethodPost, "/api/go/artifacts/artifact-1/versions/binary", map[string]any{
			"projectId": "project-1", "name": "stolen.bin", "kind": "application/octet-stream", "contentBase64": "AA==",
		}},
		{http.MethodPost, "/api/go/artifacts/artifact-1/copy", map[string]any{
			"newArtifactId": "stolen-copy", "targetProjectId": "project-2", "name": "Copy",
		}},
		{http.MethodPatch, "/api/go/artifacts/artifact-1/priority", map[string]any{"priority": 10}},
		{http.MethodPatch, "/api/go/artifacts/artifact-1/folder", map[string]any{"folderId": ""}},
		{http.MethodPost, "/api/go/artifacts/bulk-move", map[string]any{"artifactIds": []string{"artifact-1"}, "folderId": ""}},
	}
	for _, testCase := range cases {
		t.Run(testCase.method+" "+testCase.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, workspaceArtifactAuthRequest(t, testCase.method, testCase.path, "user-2", testCase.body))
			if response.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}

	foreignCreate := httptest.NewRecorder()
	handler.ServeHTTP(foreignCreate, workspaceArtifactAuthRequest(t, http.MethodPost, "/api/go/artifacts/new-artifact/versions", "user-1", map[string]any{
		"projectId": "project-2", "name": "foreign.txt", "kind": "text/plain", "content": "data",
	}))
	if foreignCreate.Code != http.StatusNotFound {
		t.Fatalf("foreign create status=%d body=%s", foreignCreate.Code, foreignCreate.Body.String())
	}

	owner := httptest.NewRecorder()
	handler.ServeHTTP(owner, workspaceArtifactAuthRequest(t, http.MethodGet, "/api/go/artifacts/artifact-1/content", "user-1", nil))
	if owner.Code != http.StatusOK || owner.Body.String() != "tenant secret" {
		t.Fatalf("owner read status=%d body=%q", owner.Code, owner.Body.String())
	}
	artifact, found, err := store.GetArtifact("artifact-1")
	if err != nil || !found || artifact.Name != "secret.txt" || artifact.CurrentVersionNumber != 1 {
		t.Fatalf("foreign operations mutated artifact=%+v found=%v err=%v", artifact, found, err)
	}
	for _, testCase := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodGet, "/api/go/projects/project-1", nil},
		{http.MethodPatch, "/api/go/projects/project-1", map[string]any{"name": "Stolen"}},
		{http.MethodDelete, "/api/go/projects/project-1", nil},
		{http.MethodGet, "/api/go/projects/project-1/artifacts", nil},
		{http.MethodGet, "/api/go/projects/batch/artifacts?pids=project-1", nil},
		{http.MethodGet, "/api/go/frames/frame-1", nil},
		{http.MethodGet, "/api/go/frames/frame-1/artifacts", nil},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, workspaceArtifactAuthRequest(t, testCase.method, testCase.path, "user-2", testCase.body))
		if response.Code != http.StatusNotFound {
			t.Fatalf("foreign project %s %s status=%d body=%s", testCase.method, testCase.path, response.Code, response.Body.String())
		}
	}
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, workspaceArtifactAuthRequest(t, http.MethodGet, "/api/go/projects", "user-2", nil))
	if list.Code != http.StatusOK || bytes.Contains(list.Body.Bytes(), []byte("project-1")) || !bytes.Contains(list.Body.Bytes(), []byte("project-2")) {
		t.Fatalf("scoped project list status=%d body=%s", list.Code, list.Body.String())
	}
	dashboard := httptest.NewRecorder()
	handler.ServeHTTP(dashboard, workspaceArtifactAuthRequest(t, http.MethodGet, "/api/go/projects/dashboard", "user-2", nil))
	if dashboard.Code != http.StatusOK || bytes.Contains(dashboard.Body.Bytes(), []byte("project-1")) || !bytes.Contains(dashboard.Body.Bytes(), []byte("project-2")) {
		t.Fatalf("scoped dashboard status=%d body=%s", dashboard.Code, dashboard.Body.String())
	}
	forgedCreate := httptest.NewRecorder()
	handler.ServeHTTP(forgedCreate, workspaceArtifactAuthRequest(t, http.MethodPost, "/api/go/projects", "user-1", map[string]any{
		"id": "project-forged", "userId": "user-2", "name": "Forged",
	}))
	if forgedCreate.Code != http.StatusOK {
		t.Fatalf("create project status=%d body=%s", forgedCreate.Code, forgedCreate.Body.String())
	}
	if owned, err := store.ProjectOwnedBy("project-forged", "user-1"); err != nil || !owned {
		t.Fatalf("authenticated owner was not enforced: owned=%v err=%v", owned, err)
	}
}

func workspaceArtifactAuthRequest(t *testing.T, method, path, userID string, body any) *http.Request {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("X-Synon-User-Id", userID)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}
