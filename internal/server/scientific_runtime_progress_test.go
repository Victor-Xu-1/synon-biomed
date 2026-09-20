package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"synon-go/internal/kernel"
	"synon-go/internal/toolprogress"
	"testing"
)

func TestScientificWarmupProgressPublishesObservedFactsOnlyWhilePreparing(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	server.setScientificRuntimeWarmupStatus(commonStructureRuntimeID, scientificRuntimeWarmupStatus{State: "preparing"})
	percent := 23.5
	server.reportScientificRuntimeProgress(commonStructureRuntimeID, 0, toolprogress.Update{
		Phase: "download", PhasePercent: &percent, Message: "private installer output",
	})
	percent = 99
	server.scientificRuntimeWarmupMu.RLock()
	status := server.scientificRuntimeWarmups[commonStructureRuntimeID]
	server.scientificRuntimeWarmupMu.RUnlock()
	value := scientificRuntimeWarmupHealthValue(status)
	if value["phase"] != "download" || value["phase_percent"] != 23.5 || value["updated_at"] == nil {
		t.Fatalf("progress facts missing or aliased: %#v", value)
	}
	encoded, _ := json.Marshal(value)
	if strings.Contains(string(encoded), "private installer") {
		t.Fatal("raw output leaked")
	}
	server.setScientificRuntimeWarmupStatus(commonStructureRuntimeID, scientificRuntimeWarmupStatus{State: "ready"})
	server.reportScientificRuntimeProgress(commonStructureRuntimeID, 0, toolprogress.Update{Phase: "late-output"})
	server.scientificRuntimeWarmupMu.RLock()
	final := server.scientificRuntimeWarmups[commonStructureRuntimeID]
	server.scientificRuntimeWarmupMu.RUnlock()
	if scientificRuntimeWarmupHealthValue(final)["phase"] != nil {
		t.Fatal("late progress replaced terminal state")
	}
}

func TestScientificWarmupRetryRequiresSavedSelectionAndCoalescesDuplicates(t *testing.T) {
	root := t.TempDir()
	server := New(Options{FileRoot: root, KernelManager: kernel.NewManager(kernel.Config{
		Micromamba: "unused-installer", CondaHome: filepath.Join(root, "conda"), CondaEnvsPath: filepath.Join(root, "conda", "envs"),
	})})
	if _, err := server.settingsStore.Set(scientificRuntimeSelectionSettingKey, []string{commonStructureRuntimeID}); err != nil {
		t.Fatal(err)
	}
	app := server.Handler()
	runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/scientific-runtimes", "local",
		map[string]any{"id": "invented"}, http.StatusBadRequest)
	runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/scientific-runtimes", "local",
		map[string]any{"id": autoDockVinaRuntimeID}, http.StatusConflict)
	server.setScientificRuntimeWarmupStatus(commonStructureRuntimeID, scientificRuntimeWarmupStatus{State: "failed"})
	for range 2 {
		value := runtimeCompatJSON(t, app, http.MethodPost, "/api/preferences/scientific-runtimes", "local",
			map[string]any{"id": commonStructureRuntimeID}, http.StatusAccepted)
		if value["id"] != commonStructureRuntimeID {
			t.Fatalf("retry receipt %#v", value)
		}
	}
	if len(server.scientificRuntimeWarmupWake) != 1 {
		t.Fatal("duplicate retry queued another installer")
	}
	if !server.shouldRunScientificRuntimeWarmup(commonStructureRuntimeID) {
		t.Fatal("saved selection was not admitted")
	}
	if _, err := server.settingsStore.Set(scientificRuntimeSelectionSettingKey, []string{}); err != nil {
		t.Fatal(err)
	}
	if server.shouldRunScientificRuntimeWarmup(commonStructureRuntimeID) {
		t.Fatal("deselected queued runtime was installed")
	}
	if status := server.scientificRuntimeWarmupStatus(commonStructureRuntimeID); status.State != "waiting_for_selection" {
		t.Fatalf("stale queue status: %#v", status)
	}
}
