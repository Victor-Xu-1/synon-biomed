package server

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestRuntimePreferencesReportRealFrameAndDiskUsage(t *testing.T) {
	root := t.TempDir()
	condaHome := filepath.Join(t.TempDir(), "conda")
	condaEnvsPath := filepath.Join(t.TempDir(), "shared-envs")
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "p", Name: "project"}); err != nil {
		t.Fatal(err)
	}
	for _, frame := range []workspace.CreateFrameInput{
		{ID: "processing", ProjectID: "p", AgentName: "agent", Status: "processing", ConversationType: "agent"},
		{ID: "running", ProjectID: "p", AgentName: "agent", Status: "running", ConversationType: "agent"},
		{ID: "queued", ProjectID: "p", AgentName: "agent", Status: "queued", ConversationType: "agent"},
		{ID: "completed", ProjectID: "p", AgentName: "agent", Status: "completed", ConversationType: "agent"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}

	writeSizedRuntimeFile(t, filepath.Join(root, "task_runs", "artifacts", "artifact.bin"), 7)
	writeSizedRuntimeFile(t, filepath.Join(root, "workspace", "state.bin"), 11)
	writeSizedRuntimeFile(t, filepath.Join(root, "tool-results", "result.bin"), 13)
	writeSizedRuntimeFile(t, filepath.Join(condaEnvsPath, "env-a", "package.bin"), 17)
	writeSizedRuntimeFile(t, filepath.Join(condaHome, "pkgs", "cache.bin"), 19)
	outside := filepath.Join(t.TempDir(), "outside.bin")
	writeSizedRuntimeFile(t, outside, 23)
	if err := os.Symlink(outside, filepath.Join(root, "task_runs", "artifacts", "outside-link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	app := New(Options{
		FileRoot: root, Workspace: store, CondaHome: condaHome, CondaEnvsPath: condaEnvsPath,
	}).Handler()
	frames := getProjectControlJSON(t, app, "/api/preferences/running-frames", http.StatusOK)
	if frames["count"] != float64(3) {
		t.Fatalf("running frame count = %#v", frames)
	}

	disk := getProjectControlJSON(t, app, "/api/preferences/disk-usage", http.StatusOK)
	assertNestedTotalBytes(t, disk, "artifacts", 7)
	assertNestedTotalBytes(t, disk, "workspace", 11)
	assertNestedTotalBytes(t, disk, "toolResults", 13)
	assertNestedTotalBytes(t, disk, "conda", 19)
	if available, ok := disk["availableBytes"].(float64); !ok || available <= 0 {
		t.Fatalf("availableBytes = %#v", disk["availableBytes"])
	}

	conda := getProjectControlJSON(t, app, "/api/preferences/disk-usage/conda", http.StatusOK)
	if conda["pkgsBytes"] != float64(19) || conda["truncated"] != false {
		t.Fatalf("conda usage = %#v", conda)
	}
	envs := conda["envs"].([]any)
	if len(envs) != 1 || envs[0].(map[string]any)["name"] != "env-a" || envs[0].(map[string]any)["bytes"] != float64(17) {
		t.Fatalf("conda env usage = %#v", envs)
	}

	alias := getProjectControlJSON(t, app, "/api/go/preferences/running-frames", http.StatusOK)
	if alias["count"] != frames["count"] {
		t.Fatalf("Go route alias = %#v, baseline = %#v", alias, frames)
	}
	condaAlias := getProjectControlJSON(t, app, "/api/go/preferences/disk-usage/conda", http.StatusOK)
	if condaAlias["pkgsBytes"] != conda["pkgsBytes"] {
		t.Fatalf("Go Conda route alias = %#v, baseline = %#v", condaAlias, conda)
	}
}

func writeSizedRuntimeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertNestedTotalBytes(t *testing.T, payload map[string]any, key string, want float64) {
	t.Helper()
	value, ok := payload[key].(map[string]any)
	if !ok || value["totalBytes"] != want {
		t.Fatalf("%s usage = %#v, payload = %#v", key, value, payload)
	}
}
