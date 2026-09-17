package server

import (
	"path/filepath"
	"testing"

	runtimekv "synon-go/internal/persistence/runtimekv"
)

func TestCompatibilityRuntimeCleanupRemovesOnlyDeletedTaskAndProjectScopes(t *testing.T) {
	store := runtimekv.New(filepath.Join(t.TempDir(), "runtime-state.sqlite"))
	t.Cleanup(func() { _ = store.Close() })
	server := &Server{runtimeStore: store}
	for _, entry := range []struct {
		key   string
		value map[string]any
	}{
		{"frame", map[string]any{"projectId": "project-a", "rootFrameId": "root-a"}},
		{"project", map[string]any{"projectId": "project-b"}},
		{"keep", map[string]any{"projectId": "project-c", "rootFrameId": "root-c"}},
	} {
		if _, err := store.Set("session-runner-model-audit", entry.key, entry.value); err != nil {
			t.Fatal(err)
		}
	}
	if warnings := server.removeCompatibilityFrameRuntime("root-a"); len(warnings) != 0 {
		t.Fatalf("frame cleanup warnings=%v", warnings)
	}
	if _, found, err := store.Get("session-runner-model-audit", "frame"); err != nil || found {
		t.Fatalf("frame-scoped state found=%t err=%v", found, err)
	}
	if err := server.removeCompatibilityProjectRuntime("project-b"); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get("session-runner-model-audit", "project"); err != nil || found {
		t.Fatalf("project-scoped state found=%t err=%v", found, err)
	}
	if _, found, err := store.Get("session-runner-model-audit", "keep"); err != nil || !found {
		t.Fatalf("unrelated state found=%t err=%v", found, err)
	}
}
