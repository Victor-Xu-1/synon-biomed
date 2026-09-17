package server

import (
	"net/http"
	"path/filepath"
	"testing"
)

func TestStorageRulesAPIRoundTripAndPathSafety(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	app := srv.Handler()

	get := func() map[string]any {
		t.Helper()
		return runtimeCompatJSON(t, app, http.MethodGet, "/api/settings/storage-rules", "owner", nil, http.StatusOK)
	}

	payload := get()
	if payload["root"] != root {
		t.Fatalf("default storage rules response = %#v", payload)
	}
	rules, ok := payload["rules"].(map[string]any)
	if !ok || rules["taskArtifacts"] != "task_runs/artifacts" || rules["logs"] != "shell_tasks" {
		t.Fatalf("default storage rules = %#v", payload["rules"])
	}

	runtimeCompatJSON(t, app, http.MethodPut, "/api/settings/storage-rules", "owner", map[string]any{
		"rules": map[string]any{
			"taskArtifacts": "generated/task-files",
			"logs":          "runtime/logs",
			"toolResults":   "cache/tool-results",
			"temp":          "scratch",
		},
	}, http.StatusOK)

	if got, err := srv.storageDirectory("taskArtifacts", "run-1"); err != nil || got != filepath.Join(root, "generated", "task-files", "run-1") {
		t.Fatalf("task artifact directory = %q, err=%v", got, err)
	}
	if got, err := srv.storageDirectory("logs", "task-1.log"); err != nil || got != filepath.Join(root, "runtime", "logs", "task-1.log") {
		t.Fatalf("log directory = %q, err=%v", got, err)
	}

	restarted := New(Options{FileRoot: root})
	payload = runtimeCompatJSON(t, restarted.Handler(), http.MethodGet, "/api/settings/storage-rules", "owner", nil, http.StatusOK)
	if payload["root"] != root {
		t.Fatalf("persisted storage rules response = %#v", payload)
	}
	persisted, ok := payload["rules"].(map[string]any)
	if !ok || persisted["taskArtifacts"] != "generated/task-files" || persisted["temp"] != "scratch" {
		t.Fatalf("persisted storage rules = %#v", payload["rules"])
	}

	for _, invalid := range []string{"../outside", "/absolute", `C:\outside`} {
		runtimeCompatJSON(t, app, http.MethodPut, "/api/settings/storage-rules", "owner", map[string]any{
			"rules": map[string]any{"taskArtifacts": invalid},
		}, http.StatusBadRequest)
	}

	legacy := runtimeCompatJSON(t, app, http.MethodGet, "/api/go/settings/storage-rules", "owner", nil, http.StatusOK)
	if legacy["ok"] != true || legacy["root"] != root {
		t.Fatalf("legacy storage rules response = %#v", legacy)
	}
}
