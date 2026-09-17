package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceFoldersAndNotesHTTPAPI(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-1", ProjectID: "project-1", AgentName: "research",
		Status: "running", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "Report",
		Kind: "text/markdown", Content: []byte("report"),
	}); err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/project-1/folders", map[string]any{
		"id": "folder-root", "name": "Evidence", "sortOrder": 10,
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/project-1/folders", map[string]any{
		"id": "folder-child", "parentId": "folder-root", "name": "Primary", "sortOrder": 20,
		"rootFrameId": "frame-1", "isConversationFolder": true,
	}, http.StatusOK)
	folders := httptest.NewRecorder()
	app.ServeHTTP(folders, localWorkspaceRequest(http.MethodGet, "/api/go/projects/project-1/folders", nil))
	if folders.Code != http.StatusOK || !bytes.Contains(folders.Body.Bytes(), []byte("\"name\":\"Evidence\"")) ||
		!bytes.Contains(folders.Body.Bytes(), []byte("\"name\":\"Primary\"")) {
		t.Fatalf("folders = %d: %s", folders.Code, folders.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/folders/folder-child", map[string]any{
		"name": "Primary Sources", "sortOrder": 30,
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/artifacts/artifact-1/folder", map[string]any{
		"folderId": "folder-child",
	}, http.StatusOK)
	artifact, found, err := store.GetArtifact("artifact-1")
	if err != nil || !found || artifact.FolderID != "folder-child" {
		t.Fatalf("artifact folder = %#v, found=%v, err=%v", artifact, found, err)
	}

	serveWorkspaceJSON(t, app, http.MethodPost, "/api/go/projects/project-1/notes", map[string]any{
		"id": "note-1", "userId": "local", "targetType": "message",
		"targetFrameId": "frame-1", "targetMessageIndex": 2,
		"targetArtifactId": "artifact-1", "content": "Verify this claim.",
	}, http.StatusOK)
	notes := httptest.NewRecorder()
	app.ServeHTTP(notes, localWorkspaceRequest(http.MethodGet, "/api/go/projects/project-1/notes", nil))
	if notes.Code != http.StatusOK || !bytes.Contains(notes.Body.Bytes(), []byte("Verify this claim.")) {
		t.Fatalf("notes = %d: %s", notes.Code, notes.Body.String())
	}
	serveWorkspaceJSON(t, app, http.MethodPatch, "/api/go/notes/note-1", map[string]any{
		"userId": "local", "content": "Verified against source.",
	}, http.StatusOK)
	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/notes/note-1?user_id=local", nil, http.StatusOK)

	serveWorkspaceJSON(t, app, http.MethodDelete, "/api/go/folders/folder-root", nil, http.StatusOK)
	artifact, found, err = store.GetArtifact("artifact-1")
	if err != nil || !found || artifact.FolderID != "" {
		t.Fatalf("artifact folder after cascade = %#v, found=%v, err=%v", artifact, found, err)
	}
}
