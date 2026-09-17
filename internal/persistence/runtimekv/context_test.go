package runtimekv

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSetContextHonorsDeadlineWhenRuntimeKVIsBusy(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "runtime-state.sqlite"))
	store.mu.Lock()
	defer store.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := store.SetContext(ctx, "skill-invocations", "busy", map[string]any{"ok": true})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SetContext error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("SetContext remained blocked for %s", elapsed)
	}
}
