package server

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityArtifactBulkMoveIsAtomicDurableAndPublishesExactEvents(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range []workspace.CreateProjectInput{
		{ID: "owned", UserID: "local", Name: "Owned"},
		{ID: "foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CreateArtifactFolder(workspace.CreateArtifactFolderInput{
		ID: "owned-folder", ProjectID: "owned", Name: "Results",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateArtifactFolder(workspace.CreateArtifactFolderInput{
		ID: "foreign-folder", ProjectID: "foreign", Name: "Private",
	}); err != nil {
		t.Fatal(err)
	}
	for _, artifact := range []struct{ id, project string }{
		{id: "artifact-a", project: "owned"},
		{id: "artifact-b", project: "owned"},
		{id: "artifact-foreign", project: "foreign"},
	} {
		if _, _, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
			ArtifactID: artifact.id, ProjectID: artifact.project, Name: artifact.id + ".txt",
			ContentType: "text/plain", Content: strings.NewReader(artifact.id),
		}); err != nil {
			t.Fatal(err)
		}
	}
	app := New(Options{Workspace: store}).Handler()

	moved := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/bulk-move", "", map[string]any{
		"artifact_ids": []string{"artifact-a", "artifact-b", "artifact-a"}, "folder_id": "owned-folder",
	}, http.StatusOK)
	if len(moved) != 2 || moved["status"] != "ok" || numberValue(moved["count"]) != 3 {
		t.Fatalf("bulk move response = %#v", moved)
	}

	failed := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/bulk-move", "local", map[string]any{
		"artifact_ids": []string{"artifact-a", "artifact-foreign"}, "folder_id": nil,
	}, http.StatusNotFound)
	if failed["detail"] != "Artifact not found" {
		t.Fatalf("private bulk move = %#v", failed)
	}
	for _, id := range []string{"artifact-a", "artifact-b"} {
		artifact, found, err := store.GetArtifact(id)
		if err != nil || !found || artifact.FolderID != "owned-folder" {
			t.Fatalf("atomic bulk move %s = %#v found=%v err=%v", id, artifact, found, err)
		}
	}

	wrongFolder := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/bulk-move", "local", map[string]any{
		"artifact_ids": []string{"artifact-a"}, "folder_id": "foreign-folder",
	}, http.StatusNotFound)
	if wrongFolder["detail"] != "Folder foreign-folder not found" {
		t.Fatalf("foreign folder bulk move = %#v", wrongFolder)
	}

	cleared := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/bulk-move", "local", map[string]any{
		"artifact_ids": []string{"artifact-a", "artifact-b"}, "folder_id": nil,
	}, http.StatusOK)
	if cleared["status"] != "ok" || numberValue(cleared["count"]) != 2 {
		t.Fatalf("bulk clear response = %#v", cleared)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, id := range []string{"artifact-a", "artifact-b"} {
		artifact, found, err := store.GetArtifact(id)
		if err != nil || !found || artifact.FolderID != "" {
			t.Fatalf("restarted bulk move %s = %#v found=%v err=%v", id, artifact, found, err)
		}
	}
	restarted := New(Options{Workspace: store})
	startServerRealtimeOutbox(t, store, restarted)
	waitServerRealtimeOutbox(t, store)
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "owned", Type: "artifact_moved", Limit: 20,
	})
	if err != nil || len(events) != 4 {
		t.Fatalf("artifact_moved events = %#v err=%v", events, err)
	}
	toFolder, toRoot := 0, 0
	for _, event := range events {
		if event.Payload["project_id"] != "owned" || event.Payload["artifact_id"] == "" {
			t.Fatalf("artifact_moved payload = %#v", event.Payload)
		}
		switch {
		case event.Payload["old_folder_id"] == nil && event.Payload["new_folder_id"] == "owned-folder":
			toFolder++
		case event.Payload["old_folder_id"] == "owned-folder" && event.Payload["new_folder_id"] == nil:
			toRoot++
		default:
			t.Fatalf("unexpected artifact_moved transition = %#v", event.Payload)
		}
	}
	if toFolder != 2 || toRoot != 2 {
		t.Fatalf("artifact_moved transitions toFolder=%d toRoot=%d", toFolder, toRoot)
	}
}
