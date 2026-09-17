package workspace

import (
	"path/filepath"
	"testing"
)

func TestSkillPreferencePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetSkillEnabled("_runtime", "demo", false); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	preferences, err := reopened.ListSkillPreferences("_runtime")
	if err != nil {
		t.Fatal(err)
	}
	enabled, found := preferences["demo"]
	if !found || enabled {
		t.Fatalf("persisted preference = %#v", preferences)
	}
}
