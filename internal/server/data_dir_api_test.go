package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/datadir"
	workspace "synon-go/internal/persistence/workspace"
)

func TestDataDirectoryHTTPStagesRestartSafeMigration(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	controlPath := filepath.Join(root, "control", "data-dir.json")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload.bin"), make([]byte, 37), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "p", Name: "project"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "p", AgentName: "agent", Status: "running", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	restartReasons := make(chan string, 1)
	app := New(Options{
		FileRoot: source, Workspace: store,
		DataDirSource: "default", DataDirControlPath: controlPath, DefaultDataDir: source,
		RestartRuntime: func(reason string) error {
			restartReasons <- reason
			return nil
		},
	}).Handler()

	current := getProjectControlJSON(t, app, "/api/settings/data-dir", http.StatusOK)
	if current["current"] != source || current["source"] != "default" ||
		current["activeFrames"] != float64(1) || current["usageBytes"].(float64) < 37 {
		t.Fatalf("data directory status = %#v", current)
	}
	postProjectControlJSON(t, app, http.MethodPost, "/api/settings/data-dir", map[string]any{
		"path": target, "migrate": true,
	}, http.StatusConflict)

	completed := "completed"
	if _, err := store.UpdateFrame("frame", workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	staged := postProjectControlJSON(t, app, http.MethodPost, "/api/settings/data-dir", map[string]any{
		"path": target, "migrate": true,
	}, http.StatusOK)
	if staged["ok"] != true || staged["restarting"] != true || staged["restartRequired"] != true {
		t.Fatalf("staged migration = %#v", staged)
	}
	select {
	case reason := <-restartReasons:
		if reason != "data_dir_move" {
			t.Fatalf("restart reason = %q", reason)
		}
	default:
		t.Fatal("restart was not requested")
	}
	state, err := datadir.New(controlPath).Load()
	if err != nil || state.Pending == nil {
		t.Fatalf("pending migration state = %#v, err=%v", state, err)
	}

	resolved, _, err := datadir.New(controlPath).Resolve(source)
	if err != nil || resolved != target {
		t.Fatalf("resolved migrated directory = %q, err=%v", resolved, err)
	}
	restarted := New(Options{
		FileRoot: target, Workspace: store,
		DataDirSource: "pointer", DataDirControlPath: controlPath, DefaultDataDir: source,
	}).Handler()
	response := httptest.NewRecorder()
	restarted.ServeHTTP(response, newLoopbackTestRequest(http.MethodDelete, "/api/settings/data-dir/last-move?deleteSource=0", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("clear last move status=%d body=%s", response.Code, response.Body.String())
	}
	state, err = datadir.New(controlPath).Load()
	if err != nil || state.LastMove != nil || state.Current != target {
		t.Fatalf("cleared migration state = %#v, err=%v", state, err)
	}
	if _, err := os.Stat(filepath.Join(source, "payload.bin")); err != nil {
		t.Fatalf("source deleted without confirmation: %v", err)
	}
}

func TestDataDirectoryHTTPAllowsPointerChangeWhenHomeIsExplicit(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	controlPath := filepath.Join(root, "control", "data-dir.json")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	app := New(Options{
		FileRoot: source, DataDirSource: "env",
		DataDirControlPath: controlPath, DefaultDataDir: source,
	}).Handler()
	result := postProjectControlJSON(t, app, http.MethodPost, "/api/settings/data-dir", map[string]any{
		"path": target, "migrate": false, "noRestart": true,
	}, http.StatusOK)
	if result["ok"] != true || result["restartRequired"] != true {
		t.Fatalf("explicit-home pointer change = %#v", result)
	}
	resolved, sourceKind, err := datadir.New(controlPath).Resolve(source)
	if err != nil || resolved != target || sourceKind != "pointer" {
		t.Fatalf("resolved explicit-home pointer = path %q source %q err=%v", resolved, sourceKind, err)
	}
}

func TestDataDirectoryHTTPRollsBackStageWhenRestartRequestFails(t *testing.T) {
	for _, migrate := range []bool{false, true} {
		t.Run(map[bool]string{false: "pointer", true: "migration"}[migrate], func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source")
			target := filepath.Join(root, "target")
			controlPath := filepath.Join(root, "control", "data-dir.json")
			if err := os.MkdirAll(source, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "state"), []byte("state"), 0o600); err != nil {
				t.Fatal(err)
			}
			app := New(Options{
				FileRoot: source, DataDirSource: "default",
				DataDirControlPath: controlPath, DefaultDataDir: source,
				RestartRuntime: func(string) error { return errors.New("restart queue unavailable") },
			}).Handler()
			response := postProjectControlJSON(t, app, http.MethodPost, "/api/settings/data-dir", map[string]any{
				"path": target, "migrate": migrate,
			}, http.StatusInternalServerError)
			if response["rolledBack"] != true {
				t.Fatalf("restart failure response = %#v", response)
			}
			state, err := datadir.New(controlPath).Load()
			if err != nil || state.Pending != nil || state.Current != "" {
				t.Fatalf("rolled back state = %#v err=%v", state, err)
			}
			markers, err := filepath.Glob(filepath.Join(source, ".synon-data-move-source-*"))
			if err != nil || len(markers) != 0 {
				t.Fatalf("cancelled migration markers = %#v err=%v", markers, err)
			}
		})
	}
}

func TestDataDirectoryHTTPRejectsCondaAndControlDirectories(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	condaHome := filepath.Join(root, "conda")
	condaEnvs := filepath.Join(root, "shared-envs")
	controlPath := filepath.Join(root, "control", "data-dir.json")
	for _, directory := range []string{source, condaHome, condaEnvs} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	app := New(Options{
		FileRoot: source, DataDirSource: "default", DataDirControlPath: controlPath,
		DefaultDataDir: source, CondaHome: condaHome, CondaEnvsPath: condaEnvs,
	}).Handler()
	for name, target := range map[string]string{
		"conda-home": filepath.Join(condaHome, "runtime"),
		"conda-envs": filepath.Join(condaEnvs, "runtime"),
		"control":    filepath.Join(filepath.Dir(controlPath), "runtime"),
	} {
		t.Run(name, func(t *testing.T) {
			postProjectControlJSON(t, app, http.MethodPost, "/api/settings/data-dir", map[string]any{
				"path": target, "migrate": false,
			}, http.StatusBadRequest)
		})
	}
}

func TestDataDirectoryHTTPKeepsStatusAvailableWhenUsageCannotBeFullyRead(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	blocked := filepath.Join(source, "blocked")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocked, "state"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	app := New(Options{
		FileRoot: source, DataDirSource: "default",
		DataDirControlPath: filepath.Join(root, "control", "data-dir.json"), DefaultDataDir: source,
	}).Handler()
	status := getProjectControlJSON(t, app, "/api/settings/data-dir", http.StatusOK)
	if status["usageBytes"] != nil || status["current"] != source {
		t.Fatalf("partial data directory status = %#v", status)
	}
}

func TestDataDirectoryHTTPMetadataReadDoesNotScanUsage(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "retained.dat"), []byte("retained data"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := New(Options{
		FileRoot: source, DataDirSource: "default", DefaultDataDir: source,
		DataDirControlPath: filepath.Join(root, "control", "data-dir.json"),
	}).Handler()
	status := getProjectControlJSON(t, app, "/api/settings/data-dir?includeUsage=false", http.StatusOK)
	if status["current"] != source || status["usageBytes"] != nil || status["usageIncluded"] != false {
		t.Fatalf("metadata must be independent of recursive size scans: %#v", status)
	}
	full := getProjectControlJSON(t, app, "/api/settings/data-dir", http.StatusOK)
	if full["usageBytes"].(float64) < float64(len("retained data")) || full["usageIncluded"] != true {
		t.Fatalf("default read must retain the full migration estimate: %#v", full)
	}
}

func TestDataDirectoryHTTPRejectsSecondPendingChangeAsConflict(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	controlPath := filepath.Join(root, "control", "data-dir.json")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	app := New(Options{
		FileRoot: source, DataDirSource: "default",
		DataDirControlPath: controlPath, DefaultDataDir: source,
	}).Handler()
	postProjectControlJSON(t, app, http.MethodPost, "/api/settings/data-dir", map[string]any{
		"path": filepath.Join(root, "first"), "migrate": false, "noRestart": true,
	}, http.StatusOK)
	postProjectControlJSON(t, app, http.MethodPost, "/api/settings/data-dir", map[string]any{
		"path": filepath.Join(root, "second"), "migrate": false, "noRestart": true,
	}, http.StatusConflict)
}
