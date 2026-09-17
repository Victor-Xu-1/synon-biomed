package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	runtimecontrol "synon-go/internal/runtimecontrol"
)

func TestControllerStagesAppliesAndClearsMigration(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	controlPath := filepath.Join(root, "control", "data-dir.json")
	writeDataDirTestFile(t, filepath.Join(source, "settings.json"), []byte("settings"))
	writeDataDirTestFile(t, filepath.Join(source, "workspace", "state.db"), []byte("database"))
	controller := New(controlPath)

	move, err := controller.Stage(source, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if move.ID == "" || move.Source != source || move.Target != target || !move.Migrate {
		t.Fatalf("staged move = %#v", move)
	}
	state, err := controller.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending == nil || state.Current != "" || state.LastMove != nil {
		t.Fatalf("staged state = %#v", state)
	}

	current, sourceKind, err := controller.Resolve(source)
	if err != nil {
		t.Fatal(err)
	}
	if current != target || sourceKind != "pointer" {
		t.Fatalf("resolved current=%q source=%q", current, sourceKind)
	}
	for path, want := range map[string]string{
		filepath.Join(target, "settings.json"):         "settings",
		filepath.Join(target, "workspace", "state.db"): "database",
	} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("migrated %s = %q, err=%v", path, raw, err)
		}
	}
	if _, err := os.Stat(filepath.Join(source, "workspace", "state.db")); err != nil {
		t.Fatalf("source removed before confirmation: %v", err)
	}
	state, err = controller.Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.Pending != nil || state.LastMove == nil || !state.LastMove.Copied || state.Current != target {
		t.Fatalf("applied state = %#v", state)
	}
	writeDataDirTestFile(t, filepath.Join(source, "created-after-migration"), []byte("preserve"))

	if err := controller.ClearLastMove(true); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "created-after-migration" {
		t.Fatalf("source entries after confirmed cleanup = %#v", entries)
	}
	if raw, err := os.ReadFile(filepath.Join(target, "workspace", "state.db")); err != nil || string(raw) != "database" {
		t.Fatalf("target changed by source cleanup: %q, err=%v", raw, err)
	}
}

func TestControllerClearLastMoveRefusesInventoriedEntryReplacedBySymlink(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	controlPath := filepath.Join(root, "control", "data-dir.json")
	entry := filepath.Join(source, "state")
	writeDataDirTestFile(t, entry, []byte("state"))
	controller := New(controlPath)
	if _, err := controller.Stage(source, target, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := controller.Resolve(source); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(entry); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	writeDataDirTestFile(t, outside, []byte("outside"))
	if err := os.Symlink(outside, entry); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := controller.ClearLastMove(true); err == nil {
		t.Fatal("source cleanup accepted an inventoried entry replaced by a symlink")
	}
	if raw, err := os.ReadFile(outside); err != nil || string(raw) != "outside" {
		t.Fatalf("external file changed: %q err=%v", raw, err)
	}
	state, err := controller.Load()
	if err != nil || state.LastMove == nil {
		t.Fatalf("failed cleanup discarded recovery state: %#v err=%v", state, err)
	}
}

func TestControllerClearsMigratedEmptySourceWithCapturedInventory(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	controller := New(filepath.Join(root, "control", "data-dir.json"))
	move, err := controller.Stage(source, target, true)
	if err != nil {
		t.Fatal(err)
	}
	if !move.InventoryCaptured || len(move.Entries) != 0 {
		t.Fatalf("empty migration inventory = %#v", move)
	}
	if _, _, err := controller.Resolve(source); err != nil {
		t.Fatal(err)
	}
	if err := controller.ClearLastMove(true); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty source cleanup entries=%#v err=%v", entries, err)
	}
}

func TestControllerLegacyMoveWithoutInventoryFailsClosed(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	for _, directory := range []string{source, target} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	move := Move{ID: "legacy-move", Source: source, Target: target, Migrate: true, Copied: true}
	writeDataDirTestFile(t, sourceMarkerPath(source, move.ID), []byte(move.ID))
	controller := New(filepath.Join(root, "control", "data-dir.json"))
	if err := controller.saveLocked(State{Current: target, LastMove: &move}); err != nil {
		t.Fatal(err)
	}
	if err := controller.ClearLastMove(true); err == nil {
		t.Fatal("legacy move without a cleanup inventory was deleted")
	}
	state, err := controller.Load()
	if err != nil || state.LastMove == nil {
		t.Fatalf("legacy recovery state was discarded: %#v err=%v", state, err)
	}
}

func TestControllerRejectsUnsafeMigrationTargetsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	writeDataDirTestFile(t, filepath.Join(source, "state"), []byte("state"))

	for name, target := range map[string]string{
		"same":     source,
		"nested":   filepath.Join(source, "nested"),
		"contains": root,
	} {
		t.Run(name, func(t *testing.T) {
			controller := New(filepath.Join(t.TempDir(), "control.json"))
			if _, err := controller.Stage(source, target, true); err == nil {
				t.Fatalf("unsafe target %q accepted", target)
			}
		})
	}

	nonempty := filepath.Join(root, "nonempty")
	writeDataDirTestFile(t, filepath.Join(nonempty, "existing"), []byte("occupied"))
	if _, err := New(filepath.Join(t.TempDir(), "control.json")).Stage(source, nonempty, true); err == nil {
		t.Fatal("non-empty target accepted")
	}

	outside := t.TempDir()
	symlink := filepath.Join(root, "symlink-source")
	if err := os.Symlink(outside, symlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := New(filepath.Join(t.TempDir(), "control.json")).Stage(symlink, filepath.Join(root, "new-target"), true); err == nil {
		t.Fatal("symlink source accepted")
	}
}

func TestControllerStagesPointerChangeUntilResolveAndCanCancel(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	writeDataDirTestFile(t, filepath.Join(source, "state"), []byte("state"))
	controller := New(filepath.Join(root, "control", "data-dir.json"))

	move, err := controller.Stage(source, target, false)
	if err != nil {
		t.Fatal(err)
	}
	state, err := controller.Load()
	if err != nil || state.Pending == nil || state.Current != "" {
		t.Fatalf("staged pointer state = %#v err=%v", state, err)
	}
	if err := controller.CancelPending(move.ID); err != nil {
		t.Fatal(err)
	}
	state, err = controller.Load()
	if err != nil || state.Pending != nil || state.Current != "" {
		t.Fatalf("cancelled pointer state = %#v err=%v", state, err)
	}

	move, err = controller.Stage(source, target, false)
	if err != nil {
		t.Fatal(err)
	}
	current, sourceKind, err := controller.Resolve(source)
	if err != nil || current != target || sourceKind != "pointer" {
		t.Fatalf("resolved pointer current=%q source=%q move=%#v err=%v", current, sourceKind, move, err)
	}
}

func TestControllerRejectsProtectedTargetsAndChangedTargetParent(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	protected := filepath.Join(root, "protected")
	writeDataDirTestFile(t, filepath.Join(source, "state"), []byte("state"))
	if err := os.MkdirAll(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	controller := New(filepath.Join(root, "control", "data-dir.json"))
	protectedTarget := filepath.Join(protected, "runtime")
	if _, err := controller.StageWithPolicy(source, protectedTarget, true, StagePolicy{
		ProtectedDirectories: []string{protected},
	}); err == nil {
		t.Fatal("target inside protected directory was accepted")
	}
	if _, err := os.Stat(protectedTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected target was not cleaned up: %v", err)
	}

	parent := filepath.Join(root, "mount")
	target := filepath.Join(parent, "runtime")
	if _, err := controller.Stage(source, target, true); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, parent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, _, err := controller.Resolve(source); err == nil {
		t.Fatal("migration accepted a target parent replaced with a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration wrote through replaced target parent: %v", err)
	}
}

func TestControllerPreflightsRealTargetCapacity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	_, available, err := runtimecontrol.PathUsage(target)
	if err != nil || available == nil || *available > uint64(^uint64(0)>>1) {
		t.Skipf("target capacity unavailable: available=%v err=%v", available, err)
	}
	sparse := filepath.Join(source, "capacity-probe.bin")
	file, err := os.OpenFile(sparse, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(int64(*available)); err != nil {
		_ = file.Close()
		t.Skipf("sparse files unavailable: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = New(filepath.Join(root, "control", "data-dir.json")).Stage(source, target, true)
	var insufficient *InsufficientSpaceError
	if !errors.As(err, &insufficient) || insufficient.AvailableBytes == 0 ||
		insufficient.RequiredBytes <= insufficient.AvailableBytes {
		t.Fatalf("capacity error = %#v (%v)", insufficient, err)
	}
}

func TestControllerActivatesVerifiedExistingDirectoryAndRollsBackPointer(t *testing.T) {
	root := t.TempDir()
	defaultHome := filepath.Join(root, "default")
	migratedHome := filepath.Join(root, "migrated")
	writeDataDirTestFile(t, filepath.Join(defaultHome, "old-state"), []byte("old"))
	writeDataDirTestFile(t, filepath.Join(migratedHome, "workspace", "state.db"), []byte("new"))
	controller := New(filepath.Join(root, "control", "data-dir.json"))

	activation, err := controller.ActivateExisting(migratedHome, "migration-1")
	if err != nil {
		t.Fatal(err)
	}
	if activation.ID == "" || activation.ActivationKey != "migration-1" || activation.Target != migratedHome || activation.PreviousCurrent != "" {
		t.Fatalf("activation = %#v", activation)
	}
	current, source, err := controller.Resolve(defaultHome)
	if err != nil || current != migratedHome || source != "pointer" {
		t.Fatalf("activated current=%q source=%q err=%v", current, source, err)
	}
	repeated, err := controller.ActivateExisting(migratedHome, "migration-1")
	if err != nil || repeated.ID != activation.ID {
		t.Fatalf("idempotent activation=%#v err=%v", repeated, err)
	}
	if _, err := controller.ActivateExisting(defaultHome, "migration-2"); err == nil {
		t.Fatal("second activation accepted before rollback")
	}

	rolledBack, err := controller.RollbackActivation(activation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.ID != activation.ID {
		t.Fatalf("rolled back activation = %#v", rolledBack)
	}
	current, source, err = controller.Resolve(defaultHome)
	if err != nil || current != defaultHome || source != "default" {
		t.Fatalf("rolled back current=%q source=%q err=%v", current, source, err)
	}
	if raw, err := os.ReadFile(filepath.Join(migratedHome, "workspace", "state.db")); err != nil || string(raw) != "new" {
		t.Fatalf("rollback changed migrated data: %q err=%v", raw, err)
	}
}

func writeDataDirTestFile(t *testing.T, path string, value []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, value, 0o600); err != nil {
		t.Fatal(err)
	}
}
