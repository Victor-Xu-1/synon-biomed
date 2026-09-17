package server

import (
	"net/http"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestWorkspaceArtifactControlHTTPAPI(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "root-frame", ProjectID: "project-1", AgentName: "writer",
		Status: "running", ConversationType: "task",
	}); err != nil {
		t.Fatal(err)
	}
	folder, err := store.CreateArtifactFolder(workspace.CreateArtifactFolderInput{
		ID: "folder-1", ProjectID: "project-1", Name: "Selected",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, artifactID := range []string{"artifact-1", "artifact-2"} {
		if _, _, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
			ArtifactID: artifactID, ProjectID: "project-1", Name: artifactID + ".md",
			Kind: "markdown", Content: []byte(artifactID),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: "root-frame", Type: "artifact_created",
			Payload: map[string]any{"artifactId": artifactID},
		}); err != nil {
			t.Fatal(err)
		}
	}
	app := New(Options{Workspace: store}).Handler()

	list := getProjectControlJSON(t, app, "/api/go/frames/root-frame/artifacts", http.StatusOK)
	artifacts := list["artifacts"].([]any)
	if len(artifacts) != 2 {
		t.Fatalf("conversation artifacts = %#v", artifacts)
	}

	priority := postProjectControlJSON(t, app, http.MethodPatch, "/api/go/artifacts/artifact-1/priority", map[string]any{
		"priority": "user_starred",
	}, http.StatusOK)
	if priority["artifact"].(map[string]any)["priority"] != "user_starred" {
		t.Fatalf("priority response = %#v", priority)
	}

	postProjectControlJSON(t, app, http.MethodPost, "/api/go/artifacts/bulk-move", map[string]any{
		"artifact_ids": []any{"artifact-1", "missing-artifact"}, "folder_id": folder.ID,
	}, http.StatusNotFound)
	for _, artifactID := range []string{"artifact-1", "artifact-2"} {
		artifact, found, err := store.GetArtifact(artifactID)
		if err != nil || !found || artifact.FolderID != "" {
			t.Fatalf("bulk rollback artifact %s = %#v, found=%v, err=%v", artifactID, artifact, found, err)
		}
	}

	moved := postProjectControlJSON(t, app, http.MethodPost, "/api/go/artifacts/bulk-move", map[string]any{
		"artifact_ids": []any{"artifact-1", "artifact-2"}, "folder_id": folder.ID,
	}, http.StatusOK)
	movedArtifacts := moved["artifacts"].([]any)
	if len(movedArtifacts) != 2 {
		t.Fatalf("bulk moved artifacts = %#v", movedArtifacts)
	}
	for _, artifactID := range []string{"artifact-1", "artifact-2"} {
		artifact, found, err := store.GetArtifact(artifactID)
		if err != nil || !found || artifact.FolderID != folder.ID {
			t.Fatalf("moved artifact %s = %#v, found=%v, err=%v", artifactID, artifact, found, err)
		}
	}
	artifact, _, _ := store.GetArtifact("artifact-1")
	if artifact.Priority != workspace.ArtifactPriorityUserStarred {
		t.Fatalf("priority was not preserved after move: %#v", artifact)
	}
}
