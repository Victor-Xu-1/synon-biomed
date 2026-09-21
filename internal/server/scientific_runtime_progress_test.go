package server

import (
	"context"
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
	_, admissionCancel, admitted := server.beginScientificRuntimeWarmup(context.Background(), commonStructureRuntimeID)
	if !admitted {
		t.Fatal("saved selection was not admitted")
	}
	admissionCancel()
	server.setScientificRuntimeWarmupCancel(commonStructureRuntimeID, nil)
	if _, err := server.settingsStore.Set(scientificRuntimeSelectionSettingKey, []string{}); err != nil {
		t.Fatal(err)
	}
	if _, _, admitted := server.beginScientificRuntimeWarmup(context.Background(), commonStructureRuntimeID); admitted {
		t.Fatal("deselected queued runtime was installed")
	}
	if status := server.scientificRuntimeWarmupStatus(commonStructureRuntimeID); status.State != "waiting_for_selection" {
		t.Fatalf("stale queue status: %#v", status)
	}
}

func TestScientificRuntimeUninstallDoesNotRequireFutureSelection(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	server.setScientificRuntimeWarmupStatus(autoDockVinaRuntimeID, scientificRuntimeWarmupStatus{
		State: "ready", Environment: "autodock-vina", Generation: strings.Repeat("a", 64),
	})

	// The test server has no kernel manager, so the request must reach the
	// deactivation-availability guard rather than being rejected as an absent
	// future-download selection.
	runtimeCompatJSON(t, server.Handler(), http.MethodPost, "/api/preferences/scientific-runtimes", "local", map[string]any{
		"id": autoDockVinaRuntimeID, "action": "uninstall",
	}, http.StatusServiceUnavailable)
}

func TestScientificRuntimePauseRejectsReadyStateWithoutLosingIdentity(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set(scientificRuntimeSelectionSettingKey, []string{autoDockVinaRuntimeID}); err != nil {
		t.Fatal(err)
	}
	server.setScientificRuntimeWarmupStatus(autoDockVinaRuntimeID, scientificRuntimeWarmupStatus{
		State: "ready", Environment: "autodock-vina", Generation: strings.Repeat("b", 64),
	})
	runtimeCompatJSON(t, server.Handler(), http.MethodPost, "/api/preferences/scientific-runtimes", "local", map[string]any{
		"id": autoDockVinaRuntimeID, "action": "pause",
	}, http.StatusConflict)
	status := server.scientificRuntimeWarmupStatus(autoDockVinaRuntimeID)
	if status.State != "ready" || status.Environment != "autodock-vina" || status.Generation != strings.Repeat("b", 64) {
		t.Fatalf("pause changed ready identity: %#v", status)
	}
}

func TestScientificRuntimePauseFencesQueuedAdmission(t *testing.T) {
	server := New(Options{FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set(scientificRuntimeSelectionSettingKey, []string{autoDockVinaRuntimeID}); err != nil {
		t.Fatal(err)
	}
	server.setScientificRuntimeWarmupStatus(autoDockVinaRuntimeID, scientificRuntimeWarmupStatus{State: "scheduled"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server.scientificRuntimeWarmupMu.Lock()
	server.scientificRuntimeWarmupCancels[autoDockVinaRuntimeID] = cancel
	server.scientificRuntimeWarmupMu.Unlock()
	if !server.pauseScientificRuntimeWarmup(autoDockVinaRuntimeID) {
		t.Fatal("scheduled runtime was not pauseable")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("pause did not cancel the admitted operation")
	}
	if _, _, admitted := server.beginScientificRuntimeWarmup(context.Background(), autoDockVinaRuntimeID); admitted {
		t.Fatal("paused runtime crossed the admission fence")
	}
	server.setScientificRuntimeWarmupStatus(autoDockVinaRuntimeID, scientificRuntimeWarmupStatus{State: "ready"})
	if status := server.scientificRuntimeWarmupStatus(autoDockVinaRuntimeID); status.State != "stopped" {
		t.Fatalf("cancelled worker overwrote pause state: %#v", status)
	}
}
