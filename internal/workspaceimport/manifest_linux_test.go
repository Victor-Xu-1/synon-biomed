//go:build linux

package workspaceimport

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestInspectRejectsSpecialFilesWithoutOpeningThem(t *testing.T) {
	sourceRoot := t.TempDir()
	database := filepath.Join(sourceRoot, filepath.FromSlash(WorkspaceDatabaseRelative))
	if err := os.MkdirAll(filepath.Dir(database), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	t.Run("source external", func(t *testing.T) {
		fifo := filepath.Join(sourceRoot, "artifacts", "runtime.fifo")
		if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		plan, err := Inspect(context.Background(), Options{SourcePath: sourceRoot})
		if err != nil {
			t.Fatal(err)
		}
		if !hasIssue(plan.Issues, "source_external_tree_invalid") {
			t.Fatalf("issues=%#v", plan.Issues)
		}
		if err := os.Remove(fifo); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("target", func(t *testing.T) {
		targetRoot := t.TempDir()
		fifo := filepath.Join(targetRoot, "runtime.fifo")
		if err := syscall.Mkfifo(fifo, 0o600); err != nil {
			t.Fatal(err)
		}
		plan, err := Inspect(context.Background(), Options{SourcePath: sourceRoot, TargetHome: targetRoot})
		if err != nil {
			t.Fatal(err)
		}
		if !hasIssue(plan.Issues, "target_snapshot_unavailable") {
			t.Fatalf("issues=%#v", plan.Issues)
		}
	})
}
