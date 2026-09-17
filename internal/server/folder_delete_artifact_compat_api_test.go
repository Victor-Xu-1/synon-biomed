package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityFolderDeleteArtifactMoveAndConversationListUseRealStateAcrossRestart(t *testing.T) {
	runtimeRoot := t.TempDir()
	databasePath := filepath.Join(runtimeRoot, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []workspace.CreateCompatibilityProjectInput{
		{ID: "owned", UserID: "local", Name: "Owned"},
		{ID: "foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, _, err := store.CreateCompatibilityProject(input); err != nil {
			t.Fatal(err)
		}
	}
	server := New(Options{FileRoot: runtimeRoot, Workspace: store})
	app := server.Handler()
	submitted := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/request", "local", map[string]any{
		"input_data": map[string]any{"request": "Create the real artifact fixture."},
		"intent_id":  "00000000-0000-4000-8000-000000000141",
	}, http.StatusOK)
	rootFrameID, _ := submitted["root_frame_id"].(string)
	if rootFrameID == "" {
		t.Fatalf("submitted root frame = %#v", submitted)
	}

	folders := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/owned/folders", "local", nil, http.StatusOK)
	if len(folders) != 1 {
		t.Fatalf("initial folders = %#v", folders)
	}
	uploadsID, _ := folders[0]["id"].(string)
	createFolder := func(name string, parentID string) string {
		t.Helper()
		body := map[string]any{"name": name}
		if parentID != "" {
			body["parent_id"] = parentID
		}
		created := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/folders", "local", body, http.StatusCreated)
		id, _ := created["id"].(string)
		if id == "" {
			t.Fatalf("created folder = %#v", created)
		}
		return id
	}
	targetID := createFolder("Target", "")
	deleteRootID := createFolder("Delete root", "")
	deleteChildID := createFolder("Delete child", deleteRootID)
	moveRootID := createFolder("Move root", "")
	moveChildID := createFolder("Move child", moveRootID)
	uploadsChildID := createFolder("Uploads child", uploadsID)
	uploadsDeleteChildID := createFolder("Uploads guarded delete", uploadsID)

	writeArtifact := func(id, name, folderID, projectID, rootID string) workspace.ArtifactVersion {
		t.Helper()
		_, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
			ArtifactID: id, ProjectID: projectID, Name: name, ContentType: "text/plain",
			Content: strings.NewReader("real content for " + id), RootFrameID: rootID, FrameID: rootID,
		})
		if err != nil {
			t.Fatal(err)
		}
		if folderID != "" {
			if _, err := store.SetArtifactFolder(id, folderID); err != nil {
				t.Fatal(err)
			}
		}
		return version
	}
	listVersion := writeArtifact("artifact-list", "list.txt", targetID, "owned", rootFrameID)
	deleteVersion := writeArtifact("artifact-delete", "delete.txt", deleteChildID, "owned", rootFrameID)
	writeArtifact("artifact-move", "move.txt", moveChildID, "owned", rootFrameID)
	writeArtifact("artifact-upload", "upload.txt", uploadsChildID, "owned", rootFrameID)
	writeArtifact("artifact-upload-delete", "upload-delete.txt", uploadsDeleteChildID, "owned", rootFrameID)
	writeArtifact("artifact-foreign", "foreign.txt", "", "foreign", "")

	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames/"+rootFrameID+"/artifacts", "local", nil, http.StatusOK)
	if len(listed) != 5 {
		t.Fatalf("conversation artifacts = %#v", listed)
	}
	listedArtifact, found := compatibilityFolderByID(listed, "artifact-list")
	if !found || listedArtifact["version_id"] != listVersion.ID || listedArtifact["creating_version_id"] != listVersion.ID ||
		listedArtifact["project_id"] != "owned" || listedArtifact["root_frame_id"] != rootFrameID ||
		listedArtifact["frame_id"] != rootFrameID || listedArtifact["creating_frame_id"] != rootFrameID ||
		listedArtifact["filename"] != "list.txt" || listedArtifact["content_type"] != "text/plain" ||
		numberValue(listedArtifact["size_bytes"]) == 0 || listedArtifact["checksum"] == nil ||
		listedArtifact["is_user_upload"] != false || listedArtifact["is_intermediate"] != false ||
		listedArtifact["priority"] != "unknown" || listedArtifact["ref_host_path"] != nil {
		t.Fatalf("listed artifact projection = %#v found=%v", listedArtifact, found)
	}
	filePath, _ := listedArtifact["file_path"].(string)
	if filePath == "" {
		t.Fatalf("listed artifact file path = %#v", listedArtifact)
	}
	if _, err := os.Stat(filePath); err != nil {
		t.Fatalf("stat listed artifact path: %v", err)
	}
	includingIntermediate := compatJSONArrayRequest(t, app, http.MethodGet,
		"/api/frames/"+rootFrameID+"/artifacts?exclude_intermediate=false", "local", nil, http.StatusOK)
	if len(includingIntermediate) != len(listed) {
		t.Fatalf("including-intermediate artifacts = %#v", includingIntermediate)
	}
	missingConversation := compatJSONRequest(t, app, http.MethodGet, "/api/frames/missing-conversation/artifacts", "local", nil, http.StatusNotFound)
	if missingConversation["detail"] != "Conversation missing-conversation not found" {
		t.Fatalf("missing conversation list = %#v", missingConversation)
	}

	emptyUpdate := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/artifact-list/folder", "local",
		map[string]any{}, http.StatusOK)
	if emptyUpdate["status"] != "updated" || emptyUpdate["folder_id"] != targetID || emptyUpdate["sort_order"] != nil {
		t.Fatalf("empty artifact folder update = %#v", emptyUpdate)
	}
	sortOnly := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/artifact-list/folder", "local",
		map[string]any{"sort_order": 9}, http.StatusOK)
	if sortOnly["folder_id"] != nil || numberValue(sortOnly["sort_order"]) != 9 {
		t.Fatalf("sort-only artifact update = %#v", sortOnly)
	}
	if artifact, found, err := store.GetArtifact("artifact-list"); err != nil || !found || artifact.FolderID != targetID {
		t.Fatalf("sort-only artifact state = %#v found=%v err=%v", artifact, found, err)
	}
	movedRoot := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/artifact-list/folder", "local",
		map[string]any{"folder_id": nil}, http.StatusOK)
	if movedRoot["folder_id"] != nil || movedRoot["sort_order"] != nil {
		t.Fatalf("root artifact move = %#v", movedRoot)
	}
	movedBack := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/artifact-list/folder", "local",
		map[string]any{"folder_id": targetID, "sort_order": 11}, http.StatusOK)
	if movedBack["folder_id"] != targetID || numberValue(movedBack["sort_order"]) != 11 {
		t.Fatalf("artifact move with sort = %#v", movedBack)
	}
	missingTarget := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/artifact-list/folder", "local",
		map[string]any{"folder_id": "missing-folder"}, http.StatusNotFound)
	if missingTarget["detail"] != "Folder missing-folder not found" {
		t.Fatalf("missing artifact move target = %#v", missingTarget)
	}
	blankTarget := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/artifact-list/folder", "local",
		map[string]any{"folder_id": ""}, http.StatusNotFound)
	if blankTarget["detail"] != "Folder not found" {
		t.Fatalf("blank artifact move target = %#v", blankTarget)
	}
	missingArtifact := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/missing-artifact/folder", "local",
		map[string]any{}, http.StatusNotFound)
	if missingArtifact["detail"] != "Artifact missing-artifact not found" {
		t.Fatalf("missing artifact move = %#v", missingArtifact)
	}
	foreignArtifact := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/artifact-foreign/folder", "local",
		map[string]any{"folder_id": nil}, http.StatusNotFound)
	if foreignArtifact["detail"] != "Artifact artifact-foreign not found" {
		t.Fatalf("foreign artifact move = %#v", foreignArtifact)
	}

	uploadMoveOutside := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/artifact-upload/folder", "local",
		map[string]any{"folder_id": targetID}, http.StatusBadRequest)
	if uploadMoveOutside["detail"] != "User-uploaded files can only be moved within the User Uploads folder" {
		t.Fatalf("user upload outside move = %#v", uploadMoveOutside)
	}
	uploadMoveInside := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/artifact-upload/folder", "local",
		map[string]any{"folder_id": uploadsID}, http.StatusOK)
	if uploadMoveInside["folder_id"] != uploadsID {
		t.Fatalf("user upload inside move = %#v", uploadMoveInside)
	}
	uploadDeleteBypass := compatJSONRequest(t, app, http.MethodDelete,
		"/api/projects/owned/folders/"+uploadsDeleteChildID+"?delete_artifacts=false&move_artifacts_to="+targetID,
		"local", nil, http.StatusBadRequest)
	if uploadDeleteBypass["detail"] != "User-uploaded files can only be moved within the User Uploads folder" {
		t.Fatalf("user upload folder-delete bypass = %#v", uploadDeleteBypass)
	}

	insideTree := compatJSONRequest(t, app, http.MethodDelete,
		"/api/projects/owned/folders/"+deleteRootID+"?delete_artifacts=false&move_artifacts_to="+deleteChildID,
		"local", nil, http.StatusBadRequest)
	if insideTree["detail"] != "Cannot move artifacts into a folder being deleted" {
		t.Fatalf("inside-tree move target = %#v", insideTree)
	}
	deleteBlobPath := filepath.Join(databasePath+".blobs", filepath.FromSlash(deleteVersion.StoragePath))
	if _, err := os.Stat(deleteBlobPath); err != nil {
		t.Fatalf("stat delete fixture blob: %v", err)
	}
	deleted := compatJSONRequest(t, app, http.MethodDelete, "/api/projects/owned/folders/"+deleteRootID,
		"local", nil, http.StatusOK)
	if deleted["status"] != "deleted" || numberValue(deleted["folders_deleted"]) != 2 ||
		numberValue(deleted["artifacts_deleted"]) != 1 || numberValue(deleted["artifacts_moved"]) != 0 {
		t.Fatalf("deleted folder tree = %#v", deleted)
	}
	if _, found, err := store.GetArtifact("artifact-delete"); err != nil || found {
		t.Fatalf("deleted artifact found=%v err=%v", found, err)
	}
	if _, err := os.Stat(deleteBlobPath); !os.IsNotExist(err) {
		t.Fatalf("deleted artifact blob remains: %v", err)
	}

	moved := compatJSONRequest(t, app, http.MethodDelete,
		"/api/projects/owned/folders/"+moveRootID+"?delete_artifacts=false&move_artifacts_to="+targetID,
		"local", nil, http.StatusOK)
	if numberValue(moved["folders_deleted"]) != 2 || numberValue(moved["artifacts_deleted"]) != 0 ||
		numberValue(moved["artifacts_moved"]) != 1 {
		t.Fatalf("moved folder tree artifacts = %#v", moved)
	}
	if artifact, found, err := store.GetArtifact("artifact-move"); err != nil || !found || artifact.FolderID != targetID {
		t.Fatalf("moved artifact = %#v found=%v err=%v", artifact, found, err)
	}
	systemDelete := compatJSONRequest(t, app, http.MethodDelete, "/api/projects/owned/folders/"+uploadsID,
		"local", nil, http.StatusForbidden)
	if systemDelete["detail"] != "Cannot delete system folders" {
		t.Fatalf("system folder delete = %#v", systemDelete)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server = New(Options{FileRoot: runtimeRoot, Workspace: store})
	startServerRealtimeOutbox(t, store, server)
	app = server.Handler()
	reloaded := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames/"+rootFrameID+"/artifacts", "local", nil, http.StatusOK)
	if len(reloaded) != 4 {
		t.Fatalf("reloaded conversation artifacts = %#v", reloaded)
	}
	if artifact, found, err := store.GetArtifact("artifact-move"); err != nil || !found || artifact.FolderID != targetID {
		t.Fatalf("reloaded moved artifact = %#v found=%v err=%v", artifact, found, err)
	}
	if _, found, err := store.GetArtifactFolder(deleteRootID); err != nil || found {
		t.Fatalf("reloaded deleted root folder found=%v err=%v", found, err)
	}
	if _, found, err := store.GetArtifactFolder(moveChildID); err != nil || found {
		t.Fatalf("reloaded deleted move child found=%v err=%v", found, err)
	}
	waitServerRealtimeOutbox(t, store)
	folderEvents, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "owned", Type: "folder_deleted", Limit: 20,
	})
	if err != nil || len(folderEvents) != 4 {
		t.Fatalf("folder_deleted events = %#v err=%v", folderEvents, err)
	}
	artifactEvents, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "owned", Type: "artifact_moved", Limit: 20,
	})
	if err != nil || len(artifactEvents) != 4 {
		t.Fatalf("artifact_moved events = %#v err=%v", artifactEvents, err)
	}
}
