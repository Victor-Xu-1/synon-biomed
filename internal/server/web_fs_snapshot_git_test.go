package server

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWebFSSnapshotUsesRealGitIndexAndWorktree(t *testing.T) {
	stateRoot := t.TempDir()
	projectRoot := filepath.Join(stateRoot, "project")
	writeWebFSTestFile(t, filepath.Join(projectRoot, "tracked.txt"), "base-tracked")
	writeWebFSTestFile(t, filepath.Join(projectRoot, "deleted.txt"), "base-deleted")
	runWebFSTestGit(t, projectRoot, "init", "-b", "main")
	runWebFSTestGit(t, projectRoot, "config", "user.name", "Synon Test")
	runWebFSTestGit(t, projectRoot, "config", "user.email", "synon@example.invalid")
	runWebFSTestGit(t, projectRoot, "add", ".")
	runWebFSTestGit(t, projectRoot, "commit", "-m", "baseline")
	runWebFSTestGit(t, projectRoot, "branch", "feature")

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
	if info.Mode != webFSSnapshotModeGit || info.Branch == nil || *info.Branch != "main" {
		t.Fatalf("git snapshot info=%#v", info)
	}

	writeWebFSTestFile(t, filepath.Join(projectRoot, "tracked.txt"), "changed-tracked")
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
		t.Fatalf("initial staged changes=%#v", changes.Staged)
	}
	assertWebFSSnapshotChanges(t, changes.Unstaged, map[string]string{
		"created.txt": "create", "deleted.txt": "delete", "tracked.txt": "modify",
	})

	baseline := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/baseline", "owner", map[string]any{
		"workspace": projectRoot, "file_path": "tracked.txt",
	})
	requireWebFSStatus(t, baseline, http.StatusOK)
	var baselineContent *string
	decodeWebFSTestResponse(t, baseline, &baselineContent)
	if baselineContent == nil || *baselineContent != "base-tracked" {
		t.Fatalf("git baseline=%v", baselineContent)
	}
	branches := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/branches", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, branches, http.StatusOK)
	var branchNames []string
	decodeWebFSTestResponse(t, branches, &branchNames)
	if strings.Join(branchNames, ",") != "feature,main" {
		t.Fatalf("git branches=%v", branchNames)
	}

	stage := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/stage", "owner", map[string]any{
		"workspace": projectRoot, "file_path": "created.txt",
	})
	requireWebFSStatus(t, stage, http.StatusOK)
	compare = webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/compare", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, compare, http.StatusOK)
	decodeWebFSTestResponse(t, compare, &changes)
	assertWebFSSnapshotChanges(t, changes.Staged, map[string]string{"created.txt": "create"})
	assertWebFSSnapshotChanges(t, changes.Unstaged, map[string]string{
		"deleted.txt": "delete", "tracked.txt": "modify",
	})

	unstage := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/unstage", "owner", map[string]any{
		"workspace": projectRoot, "file_path": "created.txt",
	})
	requireWebFSStatus(t, unstage, http.StatusOK)
	stageAll := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/stage-all", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, stageAll, http.StatusOK)
	compare = webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/compare", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, compare, http.StatusOK)
	decodeWebFSTestResponse(t, compare, &changes)
	assertWebFSSnapshotChanges(t, changes.Staged, map[string]string{
		"created.txt": "create", "deleted.txt": "delete", "tracked.txt": "modify",
	})
	assertWebFSSnapshotChanges(t, changes.Unstaged, nil)

	unstageAll := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/unstage-all", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, unstageAll, http.StatusOK)
	compare = webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/compare", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, compare, http.StatusOK)
	decodeWebFSTestResponse(t, compare, &changes)
	assertWebFSSnapshotChanges(t, changes.Staged, nil)
	assertWebFSSnapshotChanges(t, changes.Unstaged, map[string]string{
		"created.txt": "create", "deleted.txt": "delete", "tracked.txt": "modify",
	})

	for _, discard := range []struct {
		path      string
		operation string
		route     string
	}{
		{path: "tracked.txt", operation: "modify", route: "/api/fs/snapshot/discard"},
		{path: "created.txt", operation: "create", route: "/api/fs/snapshot/discard"},
		{path: "deleted.txt", operation: "delete", route: "/api/fs/snapshot/reset"},
	} {
		response := webFSTestRequest(t, handler, http.MethodPost, discard.route, "owner", map[string]any{
			"workspace": projectRoot, "file_path": discard.path, "operation": discard.operation,
		})
		requireWebFSStatus(t, response, http.StatusOK)
	}
	if raw, err := os.ReadFile(filepath.Join(projectRoot, "tracked.txt")); err != nil || string(raw) != "base-tracked" {
		t.Fatalf("discarded tracked=%q err=%v", raw, err)
	}
	if raw, err := os.ReadFile(filepath.Join(projectRoot, "deleted.txt")); err != nil || string(raw) != "base-deleted" {
		t.Fatalf("reset deleted=%q err=%v", raw, err)
	}
	if _, err := os.Stat(filepath.Join(projectRoot, "created.txt")); !os.IsNotExist(err) {
		t.Fatalf("discarded created stat error=%v", err)
	}
	compare = webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/compare", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, compare, http.StatusOK)
	decodeWebFSTestResponse(t, compare, &changes)
	assertWebFSSnapshotChanges(t, changes.Staged, nil)
	assertWebFSSnapshotChanges(t, changes.Unstaged, nil)

	writeWebFSTestFile(t, filepath.Join(projectRoot, "tracked.txt"), "staged-change")
	stage = webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/stage", "owner", map[string]any{
		"workspace": projectRoot, "file_path": "tracked.txt",
	})
	requireWebFSStatus(t, stage, http.StatusOK)
	reset := webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/reset", "owner", map[string]any{
		"workspace": projectRoot, "file_path": "tracked.txt", "operation": "modify",
	})
	requireWebFSStatus(t, reset, http.StatusOK)
	if raw, err := os.ReadFile(filepath.Join(projectRoot, "tracked.txt")); err != nil || string(raw) != "base-tracked" {
		t.Fatalf("reset staged tracked=%q err=%v", raw, err)
	}
	compare = webFSTestRequest(t, handler, http.MethodPost, "/api/fs/snapshot/compare", "owner", map[string]any{
		"workspace": projectRoot,
	})
	requireWebFSStatus(t, compare, http.StatusOK)
	decodeWebFSTestResponse(t, compare, &changes)
	assertWebFSSnapshotChanges(t, changes.Staged, nil)
	assertWebFSSnapshotChanges(t, changes.Unstaged, nil)
}

func runWebFSTestGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
