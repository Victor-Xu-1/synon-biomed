package workspace

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestBYOCSettingsPersistAndRemainOwnerScoped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := store.SetBYOCEnabled("modal", "owner-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.AppName != DefaultModalAppName {
		t.Fatalf("enabled=%#v", enabled)
	}
	maxJobs, maxTimeout := 3, 7200
	updated, err := store.UpdateBYOCSettings("modal", "owner-a", BYOCSettings{
		Provider: "modal", Enabled: true, DetailsMD: "operator notes",
		MaxConcurrentJobs: &maxJobs, MaxTimeoutSec: &maxTimeout,
		AppName: "research-compute", EnvironmentName: "research-1",
		EgressPolicy: map[string]any{"mode": "blocked"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AppName != "research-compute" || updated.EnvironmentName != "research-1" ||
		updated.MaxConcurrentJobs == nil || *updated.MaxConcurrentJobs != 3 ||
		updated.EgressPolicy["mode"] != "blocked" || updated.DetailsRev != 1 ||
		len(updated.PriorAppNames) != 1 || updated.PriorAppNames[0] != DefaultModalAppName {
		t.Fatalf("updated=%#v", updated)
	}
	stale := updated
	stale.DetailsRev = 0
	stale.DetailsMD = "stale overwrite"
	if _, err := store.UpdateBYOCSettings("modal", "owner-a", stale); !errors.Is(err, ErrBYOCRevisionConflict) {
		t.Fatalf("stale update err=%v", err)
	}
	foreign, found, err := store.GetBYOCSettings("modal", "owner-b")
	if err != nil || found || foreign.Enabled {
		t.Fatalf("foreign=%#v found=%v err=%v", foreign, found, err)
	}
	if _, err := store.SetBYOCEnabled("modal", "owner-b", true); err == nil {
		t.Fatal("foreign owner unexpectedly replaced BYOC provider")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	persisted, found, err := reopened.GetBYOCSettings("modal", "owner-a")
	if err != nil || !found || !persisted.Enabled || persisted.DetailsMD != "operator notes" ||
		persisted.MaxTimeoutSec == nil || *persisted.MaxTimeoutSec != 7200 {
		t.Fatalf("persisted=%#v found=%v err=%v", persisted, found, err)
	}
	disabled, err := reopened.SetBYOCEnabled("modal", "owner-a", false)
	if err != nil || disabled.Enabled {
		t.Fatalf("disabled=%#v err=%v", disabled, err)
	}
}
