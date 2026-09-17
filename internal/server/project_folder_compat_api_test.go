package server

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestCompatibilityProjectFolderLifecycleUsesRealOwnedStateAcrossRestart(t *testing.T) {
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
	app := New(Options{FileRoot: runtimeRoot, Workspace: store}).Handler()

	initial := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/owned/folders", "local", nil, http.StatusOK)
	if len(initial) != 1 || initial[0]["name"] != "User Uploads" || initial[0]["is_user_uploads_folder"] != true ||
		initial[0]["is_conversation_folder"] != false || numberValue(initial[0]["sort_order"]) != -1000 ||
		initial[0]["parent_id"] != nil || initial[0]["root_frame_id"] != nil || numberValue(initial[0]["artifact_count"]) != 0 {
		t.Fatalf("initial system folder = %#v", initial)
	}
	uploadsID, _ := initial[0]["id"].(string)
	systemUpdate := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned/folders/"+uploadsID, "local",
		map[string]any{"name": "Renamed"}, http.StatusForbidden)
	if systemUpdate["detail"] != "Cannot rename or move system folders" {
		t.Fatalf("system update = %#v", systemUpdate)
	}

	root := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/folders", "local",
		map[string]any{"name": "Root folder"}, http.StatusCreated)
	rootID, _ := root["id"].(string)
	if rootID == "" || root["project_id"] != "owned" || root["parent_id"] != nil || root["name"] != "Root folder" ||
		numberValue(root["sort_order"]) != 0 || numberValue(root["artifact_count"]) != 0 {
		t.Fatalf("root folder = %#v", root)
	}
	child := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/folders", "local",
		map[string]any{"name": "Child folder", "parent_id": rootID}, http.StatusCreated)
	childID, _ := child["id"].(string)
	if childID == "" || child["parent_id"] != rootID || child["name"] != "Child folder" {
		t.Fatalf("child folder = %#v", child)
	}
	hierarchy := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/owned/folders", "local", nil, http.StatusOK)
	if len(hierarchy) != 3 || hierarchy[0]["id"] != uploadsID || hierarchy[1]["id"] != rootID || hierarchy[2]["id"] != childID {
		t.Fatalf("folder hierarchy order = %#v", hierarchy)
	}

	blank := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/folders", "local",
		map[string]any{"name": "   "}, http.StatusBadRequest)
	if blank["detail"] != "name is required" {
		t.Fatalf("blank create = %#v", blank)
	}
	missingParent := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/folders", "local",
		map[string]any{"name": "Orphan", "parent_id": "missing-folder"}, http.StatusNotFound)
	if missingParent["detail"] != "Parent folder missing-folder not found" {
		t.Fatalf("missing parent = %#v", missingParent)
	}
	missingProject := compatJSONRequest(t, app, http.MethodGet, "/api/projects/missing/folders", "local", nil, http.StatusNotFound)
	if missingProject["detail"] != "Project missing not found" {
		t.Fatalf("missing project = %#v", missingProject)
	}
	foreignProject := compatJSONRequest(t, app, http.MethodGet, "/api/projects/foreign/folders", "local", nil, http.StatusNotFound)
	if foreignProject["detail"] != "Project foreign not found" {
		t.Fatalf("foreign project = %#v", foreignProject)
	}

	selfParent := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned/folders/"+rootID, "local",
		map[string]any{"parent_id": rootID}, http.StatusBadRequest)
	if selfParent["detail"] != "Folder cannot be its own parent" {
		t.Fatalf("self parent = %#v", selfParent)
	}
	cycle := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned/folders/"+rootID, "local",
		map[string]any{"parent_id": childID}, http.StatusBadRequest)
	if cycle["detail"] != "Cannot move folder into itself" {
		t.Fatalf("cycle update = %#v", cycle)
	}

	if _, err := store.CreateArtifactFolder(workspace.CreateArtifactFolderInput{
		ID: "conversation", ProjectID: "owned", Name: "Conversation", IsConversationFolder: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateArtifactFolder(workspace.CreateArtifactFolderInput{
		ID: "conversation-child", ProjectID: "owned", ParentID: "conversation", Name: "Conversation child",
	}); err != nil {
		t.Fatal(err)
	}
	conversationParent := compatJSONRequest(t, app, http.MethodPost, "/api/projects/owned/folders", "local",
		map[string]any{"name": "Rejected", "parent_id": "conversation-child"}, http.StatusBadRequest)
	if conversationParent["detail"] != "Cannot nest folders under a conversation folder" {
		t.Fatalf("conversation parent = %#v", conversationParent)
	}
	conversationUpdate := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned/folders/conversation", "local",
		map[string]any{"name": "Rejected"}, http.StatusForbidden)
	if conversationUpdate["detail"] != "Cannot rename or move system folders" {
		t.Fatalf("conversation update = %#v", conversationUpdate)
	}

	foreignFolder := compatJSONRequest(t, app, http.MethodPost, "/api/projects/foreign/folders", "other",
		map[string]any{"name": "Private"}, http.StatusCreated)
	foreignFolderID, _ := foreignFolder["id"].(string)
	foreignUpdate := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/foreign/folders/"+foreignFolderID, "local",
		map[string]any{"name": "Leak"}, http.StatusForbidden)
	if foreignUpdate["detail"] != "Project foreign not found" {
		t.Fatalf("foreign update = %#v", foreignUpdate)
	}
	wrongProjectFolder := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned/folders/"+foreignFolderID, "local",
		map[string]any{"name": "Leak"}, http.StatusNotFound)
	if wrongProjectFolder["detail"] != "Folder "+foreignFolderID+" not found" {
		t.Fatalf("wrong-project folder update = %#v", wrongProjectFolder)
	}

	updated := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned/folders/"+childID, "local",
		map[string]any{"name": "Renamed child", "parent_id": nil, "sort_order": 7}, http.StatusOK)
	if updated["name"] != "Renamed child" || updated["parent_id"] != nil || numberValue(updated["sort_order"]) != 7 {
		t.Fatalf("updated child = %#v", updated)
	}
	emptyUpdate := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned/folders/"+childID, "local",
		map[string]any{}, http.StatusOK)
	if emptyUpdate["name"] != "Renamed child" || numberValue(emptyUpdate["sort_order"]) != 7 {
		t.Fatalf("empty update = %#v", emptyUpdate)
	}
	emptyName := compatJSONRequest(t, app, http.MethodPatch, "/api/projects/owned/folders/"+childID, "local",
		map[string]any{"name": ""}, http.StatusOK)
	if emptyName["name"] != "" {
		t.Fatalf("empty name update = %#v", emptyName)
	}

	if _, _, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-a", ProjectID: "owned", Name: "evidence.txt", Kind: "text/plain", Content: []byte("real evidence"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetArtifactFolder("artifact-a", childID); err != nil {
		t.Fatal(err)
	}
	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/owned/folders", "local", nil, http.StatusOK)
	childAfterCount, found := compatibilityFolderByID(listed, childID)
	if !found || numberValue(childAfterCount["artifact_count"]) != 1 || childAfterCount["name"] != "" ||
		childAfterCount["parent_id"] != nil || numberValue(childAfterCount["sort_order"]) != 7 {
		t.Fatalf("listed counted child = %#v found=%v", childAfterCount, found)
	}
	if count, err := store.CountUndeliveredRealtimeOutbox(context.Background()); err != nil || count != 6 {
		t.Fatalf("durable folder outbox count = %d, want 6: %v", count, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	restarted := New(Options{FileRoot: runtimeRoot, Workspace: store})
	startServerRealtimeOutbox(t, store, restarted)
	app = restarted.Handler()
	reloaded := compatJSONArrayRequest(t, app, http.MethodGet, "/api/projects/owned/folders", "local", nil, http.StatusOK)
	reloadedChild, found := compatibilityFolderByID(reloaded, childID)
	if !found || numberValue(reloadedChild["artifact_count"]) != 1 || reloadedChild["name"] != "" ||
		numberValue(reloadedChild["sort_order"]) != 7 || reloadedChild["parent_id"] != nil {
		t.Fatalf("reloaded child = %#v found=%v", reloadedChild, found)
	}
	waitServerRealtimeOutbox(t, store)
	createdEvents, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "owned", Type: "folder_created", Limit: 20,
	})
	if err != nil || len(createdEvents) != 2 {
		t.Fatalf("folder_created events = %#v err=%v", createdEvents, err)
	}
	updatedEvents, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "owned", Type: "folder_updated", Limit: 20,
	})
	if err != nil || len(updatedEvents) != 3 {
		t.Fatalf("folder_updated events = %#v err=%v", updatedEvents, err)
	}
	lastFolder, _ := updatedEvents[len(updatedEvents)-1].Payload["folder"].(map[string]any)
	if lastFolder["id"] != childID || lastFolder["name"] != "" || numberValue(lastFolder["sort_order"]) != 7 {
		t.Fatalf("last folder_updated payload = %#v", updatedEvents[len(updatedEvents)-1].Payload)
	}
}

func compatibilityFolderByID(folders []map[string]any, id string) (map[string]any, bool) {
	for _, folder := range folders {
		if folder["id"] == id {
			return folder, true
		}
	}
	return nil, false
}
