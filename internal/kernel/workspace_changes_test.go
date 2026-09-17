package kernel

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorkspaceChangesTracksWorkspaceAndExternalWorkingDirectory(t *testing.T) {
	if maxKernelTrackedFiles != 10000 || maxKernelWorkspaceScanDuration != time.Second {
		t.Fatalf("workspace scan contract files=%d duration=%s", maxKernelTrackedFiles, maxKernelWorkspaceScanDuration)
	}
	workspace := t.TempDir()
	external := t.TempDir()
	before := snapshotWorkspaceFiles(workspace, external)
	if err := os.WriteFile(filepath.Join(workspace, "workspace.txt"), []byte("workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	externalPath := filepath.Join(external, "external.txt")
	if err := os.WriteFile(externalPath, []byte("external"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := snapshotWorkspaceFiles(workspace, external)
	changed, droppedRoots := changedWorkspaceFiles(before, after)
	if len(changed) != 2 || changed[0].Path != externalPath || changed[0].Dropped ||
		changed[1].Path != "workspace.txt" || changed[1].Dropped {
		t.Fatalf("changed files=%#v", changed)
	}
	if len(droppedRoots) != 0 {
		t.Fatalf("dropped roots=%#v", droppedRoots)
	}
	inside := filepath.Join(workspace, "nested")
	if err := os.MkdirAll(inside, 0o700); err != nil {
		t.Fatal(err)
	}
	if roots := snapshotWorkspaceFiles(workspace, inside); len(roots.roots) != 1 {
		t.Fatalf("nested working directory roots=%#v", roots.roots)
	}
}

func TestWorkspaceChangesDropsWholeRootWhenBudgetExhausts(t *testing.T) {
	workspace := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before := workspaceFilesSnapshot{roots: map[string]workspaceRootSnapshot{
		workspace: {root: workspace, files: map[string]fileFingerprint{}},
	}}
	after := snapshotWorkspaceFilesWithLimits(workspace, "", 1, time.Second)
	changed, droppedRoots := changedWorkspaceFiles(before, after)
	if len(changed) != 0 || len(droppedRoots) != 1 || droppedRoots[0] != "." {
		t.Fatalf("truncated workspace changes=%#v dropped=%#v", changed, droppedRoots)
	}
	if root := after.roots[workspace]; !root.dropped || len(root.files) != 1 {
		t.Fatalf("truncated root=%#v", root)
	}
}

func TestWorkspaceChangesExcludesDependencyAndRepositoryTrees(t *testing.T) {
	workspace := t.TempDir()
	before := snapshotWorkspaceFiles(workspace, "")
	for _, directory := range []string{".git", ".r-libs", "node_modules", "site-packages", "conda-meta"} {
		path := filepath.Join(workspace, directory)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "ignored.txt"), []byte("ignored"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workspace, "reported.txt"), []byte("reported"), 0o600); err != nil {
		t.Fatal(err)
	}
	after := snapshotWorkspaceFiles(workspace, "")
	changed, droppedRoots := changedWorkspaceFiles(before, after)
	if len(changed) != 1 || changed[0].Path != "reported.txt" || len(droppedRoots) != 0 {
		t.Fatalf("changes=%#v dropped=%#v", changed, droppedRoots)
	}
}

func TestWorkspaceChangesPreservesMachineReadableJSONOutputs(t *testing.T) {
	workspace := t.TempDir()
	before := snapshotWorkspaceFiles(workspace, "")
	for _, name := range []string{"binding_comparison_data.json", "tool-input.JSONL", "report.csv"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	after := snapshotWorkspaceFiles(workspace, "")
	changed, droppedRoots := changedWorkspaceFiles(before, after)
	if len(changed) != 3 || changed[0].Path != "binding_comparison_data.json" ||
		changed[1].Path != "report.csv" || changed[2].Path != "tool-input.JSONL" || len(droppedRoots) != 0 {
		t.Fatalf("changed=%#v dropped=%#v", changed, droppedRoots)
	}
	for _, write := range changed {
		if _, err := os.Stat(filepath.Join(workspace, filepath.FromSlash(write.Path))); err != nil {
			t.Fatalf("machine-readable output %q was not preserved: %v", write.Path, err)
		}
	}
}

func TestWorkspaceChangesIgnoresTimestampOnlyRewriteWhenCompleteHashesMatch(t *testing.T) {
	const hash = "f2ca1bb6c7e907d06dafe4687e579fce2b64b3f5b5bfbef4d9fcf8fcb4559ed9"
	root := t.TempDir()
	before := workspaceFilesSnapshot{roots: map[string]workspaceRootSnapshot{
		root: {root: root, files: map[string]fileFingerprint{
			"evidence.csv": {size: 4, mtime: 1, sha256: hash},
		}},
	}}
	after := workspaceFilesSnapshot{roots: map[string]workspaceRootSnapshot{
		root: {root: root, files: map[string]fileFingerprint{
			"evidence.csv": {size: 4, mtime: 2, sha256: hash},
		}},
	}}
	changed, dropped := changedWorkspaceFiles(before, after)
	if len(changed) != 0 || len(dropped) != 0 {
		t.Fatalf("timestamp-only rewrite was reported as content progress: changed=%#v dropped=%#v", changed, dropped)
	}

	after.roots[root].files["evidence.csv"] = fileFingerprint{size: 4, mtime: 3, sha256: "different"}
	changed, dropped = changedWorkspaceFiles(before, after)
	if len(changed) != 1 || changed[0].Path != "evidence.csv" || len(dropped) != 0 {
		t.Fatalf("real content rewrite was not reported: changed=%#v dropped=%#v", changed, dropped)
	}

	before.roots[root].files["large.bin"] = fileFingerprint{size: maxKernelTrackedFileBytes + 1, mtime: 1, oversize: true}
	after.roots[root].files["large.bin"] = fileFingerprint{size: maxKernelTrackedFileBytes + 1, mtime: 2, oversize: true}
	changed, dropped = changedWorkspaceFiles(before, after)
	if len(changed) != 2 || changed[1].Path != "large.bin" || !changed[1].Oversize || len(dropped) != 0 {
		t.Fatalf("unhashed oversize rewrite was hidden: changed=%#v dropped=%#v", changed, dropped)
	}
}
