package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestWebPreviewHistoryPersistsAndIsolatesUsers(t *testing.T) {
	root := t.TempDir()
	target := map[string]any{
		"content_type": "markdown",
		"artifact_id":  "artifact-1",
		"file_name":    "report.md",
	}
	first := New(Options{FileRoot: root})
	savedResponse := webFSTestRequest(t, first.Handler(), http.MethodPost,
		"/api/preview-history/save", "alice", map[string]any{
			"target": target, "content": "# durable",
		})
	requireWebFSStatus(t, savedResponse, http.StatusOK)
	var saved webPreviewSnapshot
	decodeWebFSTestResponse(t, savedResponse, &saved)
	if saved.ID == "" || saved.Size != int64(len("# durable")) ||
		saved.ContentType != "markdown" {
		t.Fatalf("saved snapshot = %#v", saved)
	}

	restarted := New(Options{FileRoot: root})
	listResponse := webFSTestRequest(t, restarted.Handler(), http.MethodPost,
		"/api/preview-history/list", "alice", map[string]any{"target": target})
	requireWebFSStatus(t, listResponse, http.StatusOK)
	var snapshots []webPreviewSnapshot
	decodeWebFSTestResponse(t, listResponse, &snapshots)
	if len(snapshots) != 1 || snapshots[0].ID != saved.ID {
		t.Fatalf("snapshots after restart = %#v", snapshots)
	}

	contentResponse := webFSTestRequest(t, restarted.Handler(), http.MethodPost,
		"/api/preview-history/get-content", "alice", map[string]any{
			"target": target, "snapshot_id": saved.ID,
		})
	requireWebFSStatus(t, contentResponse, http.StatusOK)
	var content struct {
		Snapshot webPreviewSnapshot `json:"snapshot"`
		Content  string             `json:"content"`
	}
	decodeWebFSTestResponse(t, contentResponse, &content)
	if content.Snapshot.ID != saved.ID || content.Content != "# durable" {
		t.Fatalf("snapshot content = %#v", content)
	}

	otherUser := webFSTestRequest(t, restarted.Handler(), http.MethodPost,
		"/api/preview-history/list", "bob", map[string]any{"target": target})
	requireWebFSStatus(t, otherUser, http.StatusOK)
	var isolated []webPreviewSnapshot
	decodeWebFSTestResponse(t, otherUser, &isolated)
	if len(isolated) != 0 {
		t.Fatalf("other user snapshots = %#v", isolated)
	}

	invalidID := webFSTestRequest(t, restarted.Handler(), http.MethodPost,
		"/api/preview-history/get-content", "alice", map[string]any{
			"target": target, "snapshot_id": "../escape",
		})
	requireWebFSStatus(t, invalidID, http.StatusBadRequest)
}

func TestWebPreviewHistoryDetectsContentCorruption(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	target := webPreviewHistoryTarget{
		ContentType: "markdown", ArtifactID: "artifact-2", FileName: "report.md",
	}
	response := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/preview-history/save", "alice", map[string]any{
			"target": target, "content": "original",
		})
	requireWebFSStatus(t, response, http.StatusOK)
	var saved webPreviewSnapshot
	decodeWebFSTestResponse(t, response, &saved)
	directory, err := srv.webPreviewTargetDirectory("alice", target)
	if err != nil {
		t.Fatal(err)
	}
	contentPath := filepath.Join(directory, saved.ID+".content")
	if err := os.WriteFile(contentPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := webFSTestRequest(t, srv.Handler(), http.MethodPost,
		"/api/preview-history/get-content", "alice", map[string]any{
			"target": target, "snapshot_id": saved.ID,
		})
	requireWebFSStatus(t, corrupt, http.StatusInternalServerError)
}
