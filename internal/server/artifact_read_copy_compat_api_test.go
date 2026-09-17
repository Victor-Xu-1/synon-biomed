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

func TestCompatibilityArtifactReadAndCopyUseSharedRealBlobsAcrossRestart(t *testing.T) {
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
	for _, input := range []workspace.CreateFrameInput{
		{ID: "root-owned", ProjectID: "owned", AgentName: "OPERON", Status: "completed", ConversationType: "root"},
		{ID: "root-foreign", ProjectID: "foreign", AgentName: "OPERON", Status: "completed", ConversationType: "root"},
	} {
		if _, err := store.CreateFrame(input); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []workspace.CreateArtifactFolderInput{
		{ID: "source-folder", ProjectID: "owned", Name: "Source"},
		{ID: "target-folder", ProjectID: "owned", Name: "Target"},
		{ID: "foreign-folder", ProjectID: "foreign", Name: "Foreign"},
	} {
		if _, err := store.CreateArtifactFolder(input); err != nil {
			t.Fatal(err)
		}
	}

	first := writeCompatibilityArtifactReadCopyVersion(t, store, "source-artifact", "owned", "root-owned", "source.txt", "first", true)
	current := writeCompatibilityArtifactReadCopyVersion(t, store, "source-artifact", "owned", "root-owned", "source.txt", "current content", true)
	if _, err := store.SetArtifactFolder("source-artifact", "source-folder"); err != nil {
		t.Fatal(err)
	}
	_ = writeCompatibilityArtifactReadCopyVersion(t, store, "foreign-artifact", "foreign", "root-foreign", "private.txt", "private", false)

	app := New(Options{FileRoot: runtimeRoot, Workspace: store}).Handler()
	metadata := compatJSONRequest(t, app, http.MethodGet, "/api/artifacts/source-artifact/metadata", "local", nil, http.StatusOK)
	assertCompatibilityArtifactReadMetadata(t, metadata, "source-artifact", "source.txt", 2, current.ID, first.ID, "root-owned", "unknown")
	currentPath := asString(metadata["file_path"])
	if info, err := os.Stat(currentPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("metadata file path %q is not a real Blob: %v", currentPath, err)
	}

	versions := compatJSONArrayRequest(t, app, http.MethodGet, "/api/artifacts/source-artifact/versions", "local", nil, http.StatusOK)
	if len(versions) != 2 || versions[0]["version_id"] != first.ID || versions[1]["version_id"] != current.ID ||
		numberValue(versions[0]["version_number"]) != 1 || numberValue(versions[1]["version_number"]) != 2 ||
		versions[0]["parent_version_id"] != nil || versions[1]["parent_version_id"] != first.ID ||
		versions[0]["artifact_id"] != "source-artifact" || versions[1]["artifact_id"] != "source-artifact" ||
		versions[0]["frame_id"] != "root-owned" || versions[1]["frame_id"] != "root-owned" {
		t.Fatalf("version history = %#v", versions)
	}
	for _, version := range versions {
		if version["content_type"] != "text/plain" || asString(version["created_at"]) == "" {
			t.Fatalf("version projection = %#v", version)
		}
		if info, err := os.Stat(asString(version["file_path"])); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("version file path is not a real Blob: %#v err=%v", version, err)
		}
	}

	for _, endpoint := range []string{"metadata", "versions"} {
		missing := compatJSONRequest(t, app, http.MethodGet, "/api/artifacts/missing/"+endpoint, "local", nil, http.StatusNotFound)
		if missing["detail"] != "Artifact missing not found" {
			t.Fatalf("missing %s = %#v", endpoint, missing)
		}
		foreign := compatJSONRequest(t, app, http.MethodGet, "/api/artifacts/foreign-artifact/"+endpoint, "local", nil, http.StatusNotFound)
		if foreign["detail"] != "Artifact foreign-artifact not found" {
			t.Fatalf("foreign %s = %#v", endpoint, foreign)
		}
	}

	defaultCopy := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/source-artifact/copy", "local",
		nil, http.StatusCreated)
	defaultCopyID := asString(defaultCopy["new_artifact_id"])
	if len(defaultCopy) != 3 || defaultCopy["original_artifact_id"] != "source-artifact" ||
		defaultCopyID == "" || defaultCopy["filename"] != "source.txt" {
		t.Fatalf("default copy = %#v", defaultCopy)
	}
	namedCopy := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/source-artifact/copy", "local",
		map[string]any{"new_filename": " \u0000named\u202e-copy.txt ", "target_folder_id": "target-folder"}, http.StatusCreated)
	namedCopyID := asString(namedCopy["new_artifact_id"])
	if namedCopy["original_artifact_id"] != "source-artifact" || namedCopyID == "" || namedCopy["filename"] != "named-copy.txt" {
		t.Fatalf("named copy = %#v", namedCopy)
	}
	nullTargetCopy := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/source-artifact/copy", "local",
		map[string]any{"new_filename": nil, "target_folder_id": nil}, http.StatusCreated)
	nullTargetCopyID := asString(nullTargetCopy["new_artifact_id"])
	if nullTargetCopyID == "" || nullTargetCopy["filename"] != "source.txt" {
		t.Fatalf("null-target copy = %#v", nullTargetCopy)
	}

	blankCopy := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/source-artifact/copy", "local",
		map[string]any{"new_filename": " \u0000\u202e "}, http.StatusBadRequest)
	if blankCopy["detail"] != "filename is required" {
		t.Fatalf("blank copy = %#v", blankCopy)
	}
	foreignFolder := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/source-artifact/copy", "local",
		map[string]any{"target_folder_id": "foreign-folder"}, http.StatusNotFound)
	if foreignFolder["detail"] != "Folder foreign-folder not found" {
		t.Fatalf("foreign-folder copy = %#v", foreignFolder)
	}
	missingCopy := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/missing/copy", "local",
		map[string]any{}, http.StatusNotFound)
	if missingCopy["detail"] != "Artifact missing not found" {
		t.Fatalf("missing copy = %#v", missingCopy)
	}
	privateCopy := compatJSONRequest(t, app, http.MethodPost, "/api/artifacts/foreign-artifact/copy", "local",
		map[string]any{}, http.StatusNotFound)
	if privateCopy["detail"] != "Artifact foreign-artifact not found" {
		t.Fatalf("private copy = %#v", privateCopy)
	}

	for id, wantFolder := range map[string]string{
		defaultCopyID: "source-folder", namedCopyID: "target-folder", nullTargetCopyID: "source-folder",
	} {
		artifact, found, err := store.GetArtifact(id)
		if err != nil || !found || artifact.CurrentVersionNumber != 1 || artifact.FolderID != wantFolder {
			t.Fatalf("copied artifact %q = %#v found=%v err=%v", id, artifact, found, err)
		}
		copiedMetadata := compatJSONRequest(t, app, http.MethodGet, "/api/artifacts/"+id+"/metadata", "local", nil, http.StatusOK)
		if copiedMetadata["root_frame_id"] != "root-owned" || copiedMetadata["frame_id"] != nil ||
			copiedMetadata["creating_frame_id"] != nil || copiedMetadata["is_user_upload"] != true ||
			numberValue(copiedMetadata["version_number"]) != 1 || copiedMetadata["priority"] != "unknown" ||
			copiedMetadata["file_path"] != currentPath || copiedMetadata["checksum"] != current.ContentSHA256 {
			t.Fatalf("copied metadata %q = %#v", id, copiedMetadata)
		}
		copiedVersions := compatJSONArrayRequest(t, app, http.MethodGet, "/api/artifacts/"+id+"/versions", "local", nil, http.StatusOK)
		if len(copiedVersions) != 1 || numberValue(copiedVersions[0]["version_number"]) != 1 ||
			copiedVersions[0]["parent_version_id"] != nil || copiedVersions[0]["file_path"] != currentPath {
			t.Fatalf("copied versions %q = %#v", id, copiedVersions)
		}
	}

	deleted := compatJSONRequest(t, app, http.MethodDelete, "/api/artifacts/source-artifact", "local", nil, http.StatusOK)
	if numberValue(deleted["versions_deleted"]) != 2 {
		t.Fatalf("source delete = %#v", deleted)
	}
	if _, err := os.Stat(currentPath); err != nil {
		t.Fatalf("shared current Blob was removed with source: %v", err)
	}
	firstPath := filepath.Join(databasePath+".blobs", filepath.FromSlash(first.StoragePath))
	if _, err := os.Stat(firstPath); !os.IsNotExist(err) {
		t.Fatalf("unshared first-version Blob still exists: %v", err)
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
	for _, id := range []string{defaultCopyID, namedCopyID, nullTargetCopyID} {
		reloaded := compatJSONRequest(t, app, http.MethodGet, "/api/artifacts/"+id+"/metadata", "local", nil, http.StatusOK)
		if reloaded["file_path"] != currentPath || numberValue(reloaded["version_number"]) != 1 {
			t.Fatalf("reloaded copy %q = %#v", id, reloaded)
		}
	}
	waitServerRealtimeOutbox(t, store)
	assertCompatibilityArtifactCopyEvents(t, store, map[string]string{
		defaultCopyID: "source.txt", namedCopyID: "named-copy.txt", nullTargetCopyID: "source.txt",
	})
}

func writeCompatibilityArtifactReadCopyVersion(
	t *testing.T, store *workspace.Store, artifactID, projectID, rootFrameID, filename, content string, userUpload bool,
) workspace.ArtifactVersion {
	t.Helper()
	_, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: projectID, Name: filename, ContentType: "text/plain",
		Content: strings.NewReader(content), RootFrameID: rootFrameID, FrameID: rootFrameID, IsUserUpload: userUpload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func assertCompatibilityArtifactReadMetadata(
	t *testing.T, artifact map[string]any, id, filename string, versionNumber int64,
	versionID, creatingVersionID, rootFrameID, priority string,
) {
	t.Helper()
	if len(artifact) != 22 || artifact["retention_mode"] != "snapshot" || artifact["id"] != id || artifact["filename"] != filename ||
		numberValue(artifact["version_number"]) != versionNumber || artifact["version_id"] != versionID ||
		artifact["creating_version_id"] != creatingVersionID || artifact["root_frame_id"] != rootFrameID ||
		artifact["frame_id"] != rootFrameID || artifact["creating_frame_id"] != rootFrameID ||
		artifact["priority"] != priority || artifact["content_type"] != "text/plain" {
		t.Fatalf("artifact metadata = %#v", artifact)
	}
}

func assertCompatibilityArtifactCopyEvents(t *testing.T, store *workspace.Store, copies map[string]string) {
	t.Helper()
	events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "owned", Type: "artifact_created", Limit: 20,
	})
	if err != nil || len(events) != len(copies) {
		t.Fatalf("artifact_created events = %#v err=%v", events, err)
	}
	seen := make(map[string]bool, len(events))
	for _, event := range events {
		artifact, _ := event.Payload["artifact"].(map[string]any)
		id := asString(artifact["id"])
		filename, found := copies[id]
		if !found || event.Payload["project_id"] != "owned" || event.Payload["root_frame_id"] != "root-owned" ||
			event.Payload["frame_id"] != nil || event.Payload["version_root_frame_id"] != "root-owned" ||
			artifact["filename"] != filename || numberValue(artifact["version_number"]) != 1 ||
			artifact["content_type"] != "text/plain" || artifact["file_path"] != nil ||
			artifact["is_user_upload"] != true || artifact["creating_frame_id"] != nil || artifact["is_intermediate"] != false {
			t.Fatalf("artifact_created payload = %#v", event.Payload)
		}
		seen[id] = true
	}
	if len(seen) != len(copies) {
		t.Fatalf("seen copy events = %#v want %#v", seen, copies)
	}
	lineageEvents, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
		UserID: "local", ProjectID: "owned", Type: "lineage_ready", Limit: 20,
	})
	if err != nil || len(lineageEvents) != 0 {
		t.Fatalf("copy emitted unexpected lineage_ready events = %#v err=%v", lineageEvents, err)
	}
}
