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

func TestCompatibilityArtifactControlsUseRealStateBlobsAndOutboxAcrossRestart(t *testing.T) {
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

	renameFirst := writeCompatibilityArtifactTestVersion(t, store, "rename-me", "owned", "root-owned", "original.txt", "first")
	renameCurrent := writeCompatibilityArtifactTestVersion(t, store, "rename-me", "owned", "root-owned", "original.txt", "second version")
	deleteFirst := writeCompatibilityArtifactTestVersion(t, store, "delete-me", "owned", "root-owned", "delete.txt", "delete first")
	deleteCurrent := writeCompatibilityArtifactTestVersion(t, store, "delete-me", "owned", "root-owned", "delete.txt", "delete second")
	_ = writeCompatibilityArtifactTestVersion(t, store, "foreign-artifact", "foreign", "root-foreign", "private.txt", "private")

	deleteBlobPaths := []string{
		filepath.Join(databasePath+".blobs", filepath.FromSlash(deleteFirst.StoragePath)),
		filepath.Join(databasePath+".blobs", filepath.FromSlash(deleteCurrent.StoragePath)),
	}
	for _, path := range deleteBlobPaths {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("real delete blob %q is unavailable before request: %v", path, err)
		}
	}

	app := New(Options{FileRoot: runtimeRoot, Workspace: store}).Handler()
	blank := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/rename-me/rename", "local",
		map[string]any{"filename": " \u0000\u202e "}, http.StatusBadRequest)
	if blank["detail"] != "filename is required" {
		t.Fatalf("blank rename = %#v", blank)
	}
	renamed := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/rename-me/rename", "local",
		map[string]any{"filename": "  \u0000report\u202e.txt\t"}, http.StatusOK)
	if len(renamed) != 3 || renamed["artifact_id"] != "rename-me" || renamed["old_filename"] != "original.txt" ||
		renamed["new_filename"] != "report.txt" {
		t.Fatalf("renamed artifact = %#v", renamed)
	}
	for _, request := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPatch, "/api/artifacts/missing/rename", map[string]any{"filename": "missing.txt"}},
		{http.MethodPatch, "/api/artifacts/foreign-artifact/rename", map[string]any{"filename": "leak.txt"}},
		{http.MethodPatch, "/api/artifacts/foreign-artifact/priority", map[string]any{"priority": "user_starred"}},
		{http.MethodDelete, "/api/artifacts/foreign-artifact", nil},
	} {
		response := compatJSONRequest(t, app, request.method, request.path, "local", request.body, http.StatusNotFound)
		artifactID := "foreign-artifact"
		if strings.Contains(request.path, "/missing/") {
			artifactID = "missing"
		}
		if response["detail"] != "Artifact "+artifactID+" not found" {
			t.Fatalf("private/missing artifact response for %s = %#v", request.path, response)
		}
	}
	invalidPriority := compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/rename-me/priority", "local",
		map[string]any{"priority": "urgent"}, http.StatusBadRequest)
	if !strings.Contains(asString(invalidPriority["detail"]), "Invalid enum value") {
		t.Fatalf("invalid priority = %#v", invalidPriority)
	}

	priorities := []string{"user_hidden", "user_no_priority", "unknown", "user_starred"}
	var priorityProjection map[string]any
	for _, priority := range priorities {
		priorityProjection = compatJSONRequest(t, app, http.MethodPatch, "/api/artifacts/rename-me/priority", "local",
			map[string]any{"priority": priority}, http.StatusOK)
		if priorityProjection["priority"] != priority {
			t.Fatalf("priority projection for %q = %#v", priority, priorityProjection)
		}
	}
	assertCompatibilityArtifactProjection(t, priorityProjection, renameFirst, renameCurrent)

	deleted := compatJSONRequest(t, app, http.MethodDelete, "/api/artifacts/delete-me", "local", nil, http.StatusOK)
	if len(deleted) != 2 || deleted["artifact_id"] != "delete-me" || numberValue(deleted["versions_deleted"]) != 2 {
		t.Fatalf("deleted artifact = %#v", deleted)
	}
	for _, path := range deleteBlobPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted blob %q still exists: %v", path, err)
		}
	}
	missingDelete := compatJSONRequest(t, app, http.MethodDelete, "/api/artifacts/delete-me", "local", nil, http.StatusNotFound)
	if missingDelete["detail"] != "Artifact delete-me not found" {
		t.Fatalf("second delete = %#v", missingDelete)
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
	reloaded := compatJSONArrayRequest(t, app, http.MethodGet, "/api/frames/root-owned/artifacts", "local", nil, http.StatusOK)
	if len(reloaded) != 1 || reloaded[0]["id"] != "rename-me" || reloaded[0]["filename"] != "report.txt" ||
		reloaded[0]["priority"] != "user_starred" || numberValue(reloaded[0]["version_number"]) != 2 {
		t.Fatalf("reloaded artifacts = %#v", reloaded)
	}
	if _, found, err := store.GetArtifact("delete-me"); err != nil || found {
		t.Fatalf("deleted artifact after restart found=%v err=%v", found, err)
	}
	if artifact, found, err := store.GetArtifact("foreign-artifact"); err != nil || !found || artifact.Name != "private.txt" {
		t.Fatalf("foreign artifact after restart = %#v found=%v err=%v", artifact, found, err)
	}

	waitServerRealtimeOutbox(t, store)
	assertCompatibilityArtifactControlEvents(t, store)
}

func TestCompatibilityArtifactFilenameSanitizerMatchesV11Limits(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "ascii limit", input: strings.Repeat("a", 260), want: strings.Repeat("a", 255)},
		{name: "utf16 limit", input: strings.Repeat("\U0001f9ea", 127) + "ab", want: strings.Repeat("\U0001f9ea", 127) + "a"},
		{name: "controls", input: " \u0000report\u200e\u202e.txt\u2069\t", want: "report.txt"},
		{name: "path retained", input: " ../results/report.txt ", want: "../results/report.txt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sanitizeCompatibilityArtifactFilename(test.input); got != test.want {
				t.Fatalf("sanitizeCompatibilityArtifactFilename() = %q, want %q", got, test.want)
			}
		})
	}
}

func writeCompatibilityArtifactTestVersion(
	t *testing.T, store *workspace.Store, artifactID, projectID, rootFrameID, filename, content string,
) workspace.ArtifactVersion {
	t.Helper()
	_, version, err := store.WriteArtifactVersion(context.Background(), workspace.WriteArtifactVersionInput{
		ArtifactID: artifactID, ProjectID: projectID, Name: filename, ContentType: "text/plain",
		Content: strings.NewReader(content), RootFrameID: rootFrameID, FrameID: rootFrameID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return version
}

func assertCompatibilityArtifactProjection(
	t *testing.T, artifact map[string]any, first, current workspace.ArtifactVersion,
) {
	t.Helper()
	if len(artifact) != 22 || artifact["retention_mode"] != "snapshot" || artifact["id"] != "rename-me" || artifact["project_id"] != "owned" ||
		artifact["version_id"] != current.ID || numberValue(artifact["version_number"]) != 2 ||
		artifact["root_frame_id"] != "root-owned" || artifact["frame_id"] != "root-owned" ||
		artifact["creating_frame_id"] != "root-owned" || artifact["filename"] != "report.txt" ||
		artifact["content_type"] != "text/plain" || numberValue(artifact["size_bytes"]) != current.SizeBytes ||
		artifact["checksum"] != current.ContentSHA256 || artifact["is_user_upload"] != false ||
		artifact["agent_name"] != nil || artifact["language"] != nil || artifact["is_intermediate"] != false ||
		artifact["ref_host_path"] != nil || artifact["priority"] != "user_starred" ||
		artifact["creating_version_id"] != first.ID || artifact["superseded_by_artifact_id"] != nil {
		t.Fatalf("compatibility artifact projection = %#v", artifact)
	}
	filePath, _ := artifact["file_path"].(string)
	if info, err := os.Stat(filePath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("projection file_path %q is not a real blob: %v", filePath, err)
	}
	if asString(artifact["created_at"]) == "" {
		t.Fatalf("projection created_at is empty: %#v", artifact)
	}
}

func assertCompatibilityArtifactControlEvents(t *testing.T, store *workspace.Store) {
	t.Helper()
	tests := []struct {
		eventType string
		wantCount int
	}{
		{"artifact_renamed", 1},
		{"artifact_priority_update", 4},
		{"artifact_deleted", 1},
	}
	for _, test := range tests {
		events, err := store.ListRealtimeEvents(workspace.RealtimeEventFilter{
			UserID: "local", ProjectID: "owned", Type: test.eventType, Limit: 20,
		})
		if err != nil || len(events) != test.wantCount {
			t.Fatalf("%s events = %#v err=%v", test.eventType, events, err)
		}
		for _, event := range events {
			if event.Payload["artifact_id"] == "" || event.Payload["project_id"] != "owned" {
				t.Fatalf("%s payload = %#v", test.eventType, event.Payload)
			}
		}
		if test.eventType == "artifact_renamed" &&
			(events[0].Payload["old_filename"] != "original.txt" || events[0].Payload["new_filename"] != "report.txt") {
			t.Fatalf("artifact_renamed payload = %#v", events[0].Payload)
		}
	}
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}
