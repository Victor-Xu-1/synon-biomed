package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestWebFSSnapshotTracksAndRestoresOrdinaryWorkspace(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	writeWebFSTestFile(t, filepath.Join(projectRoot, "note.txt"), "base-note")
	writeWebFSTestFile(t, filepath.Join(projectRoot, "deleted.txt"), "base-deleted")
	srv := newWebFSProjectTestServer(t, stateRoot, projectRoot, "owner")
	handler := http.HandlerFunc(srv.handleWebFSSnapshot)

	initResponse := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/init", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, initResponse, http.StatusOK)
	var info struct {
		Mode   string  `json:"mode"`
		Branch *string `json:"branch"`
	}
	decodeWebFSTestResponse(t, initResponse, &info)
	if info.Mode != webFSSnapshotModeFiles || info.Branch != nil {
		t.Fatalf("snapshot info=%#v", info)
	}

	localRead := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/info", "other", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, localRead, http.StatusOK)
	unauthorizedWrite := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/reset", "other", map[string]any{
		"workspace": projectRoot, "file_path": "note.txt", "operation": "modify",
	})
	requireWebFSStatus(t, unauthorizedWrite, http.StatusForbidden)

	snapshotKey := webFSSnapshotKey("owner", projectRoot)
	srv.webSnapshotMu.Lock()
	baselineRoot := srv.webSnapshots[snapshotKey].BaselineRoot
	srv.webSnapshotMu.Unlock()
	if baselineRoot == "" {
		t.Fatal("baseline root is empty")
	}
	if raw, err := os.ReadFile(filepath.Join(baselineRoot, "note.txt")); err != nil || string(raw) != "base-note" {
		t.Fatalf("disk baseline=%q err=%v", raw, err)
	}

	writeWebFSTestFile(t, filepath.Join(projectRoot, "note.txt"), "changed-note")
	writeWebFSTestFile(t, filepath.Join(projectRoot, "created.txt"), "new-file")
	if err := os.Remove(filepath.Join(projectRoot, "deleted.txt")); err != nil {
		t.Fatal(err)
	}

	compare := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/compare", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, compare, http.StatusOK)
	var changes webFSSnapshotCompare
	decodeWebFSTestResponse(t, compare, &changes)
	if len(changes.Staged) != 0 {
		t.Fatalf("ordinary snapshot staged=%#v", changes.Staged)
	}
	assertWebFSSnapshotChanges(t, changes.Unstaged, map[string]string{
		"created.txt": "create", "deleted.txt": "delete", "note.txt": "modify",
	})

	baseline := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/baseline", "owner", map[string]any{
		"workspace": projectRoot, "file_path": "note.txt",
	})
	requireWebFSStatus(t, baseline, http.StatusOK)
	var baselineContent *string
	decodeWebFSTestResponse(t, baseline, &baselineContent)
	if baselineContent == nil || *baselineContent != "base-note" {
		t.Fatalf("baseline content=%v", baselineContent)
	}
	createdBaseline := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/baseline", "owner", map[string]any{
		"workspace": projectRoot, "file_path": "created.txt",
	})
	requireWebFSStatus(t, createdBaseline, http.StatusOK)
	baselineContent = nil
	decodeWebFSTestResponse(t, createdBaseline, &baselineContent)
	if baselineContent != nil {
		t.Fatalf("created baseline=%q", *baselineContent)
	}

	stage := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/stage", "owner", map[string]any{
		"workspace": projectRoot, "file_path": "note.txt",
	})
	requireWebFSStatus(t, stage, http.StatusBadRequest)

	for _, reset := range []struct {
		path      string
		operation string
	}{
		{path: "note.txt", operation: "modify"},
		{path: "deleted.txt", operation: "delete"},
		{path: "created.txt", operation: "create"},
	} {
		response := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/reset", "owner", map[string]any{
			"workspace": projectRoot, "file_path": reset.path, "operation": reset.operation,
		})
		requireWebFSStatus(t, response, http.StatusOK)
	}
	if raw, err := os.ReadFile(filepath.Join(projectRoot, "note.txt")); err != nil || string(raw) != "base-note" {
		t.Fatalf("restored note=%q err=%v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(projectRoot, "deleted.txt")); err != nil || string(raw) != "base-deleted" {
		t.Fatalf("restored deleted=%q err=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("created file stat error=%v", err)
	}
	compare = webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/compare", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, compare, http.StatusOK)
	decodeWebFSTestResponse(t, compare, &changes)
	assertWebFSSnapshotChanges(t, changes.Unstaged, nil)

	branches := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/branches", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, branches, http.StatusOK)
	var branchNames []string
	decodeWebFSTestResponse(t, branches, &branchNames)
	if len(branchNames) != 0 {
		t.Fatalf("ordinary branches=%v", branchNames)
	}

	dispose := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/dispose", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, dispose, http.StatusOK)
	if _, err := os.Stat(baselineRoot); !os.IsNotExist(err) {
		t.Fatalf("disposed baseline stat error=%v", err)
	}
	srv.webSnapshotMu.Lock()
	_, found := srv.webSnapshots[snapshotKey]
	srv.webSnapshotMu.Unlock()
	if found {
		t.Fatal("disposed snapshot remained registered")
	}
}

func assertWebFSSnapshotChanges(
	t *testing.T,
	changes []webFSSnapshotChange,
	want map[string]string,
) {
	t.Helper()
	if len(changes) != len(want) {
		t.Fatalf("changes=%#v want=%v", changes, want)
	}
	for _, change := range changes {
		operation, found := want[change.RelativePath]
		if !found || operation != change.Operation || change.FilePath == "" {
			t.Fatalf("change=%#v want=%v", change, want)
		}
	}
}
