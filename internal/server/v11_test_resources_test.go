package server

import (
	"path/filepath"
	"testing"

	"synon-go/internal/persistence/runtimekv"
)

func TestV11FixtureClosesOwnedRuntimeAfterSubtest(t *testing.T) {
	var owned *runtimekv.Store
	t.Run("fixture", func(t *testing.T) {
		server := newV11TestServer(t, Options{FileRoot: t.TempDir()})
		owned = server.runtimeStore
		if _, err := owned.Set("fixture", "entry", "value"); err != nil {
			t.Fatal(err)
		}
	})
	if _, _, err := owned.Get("fixture", "entry"); err == nil {
		t.Fatal("fixture left its owned runtime database open")
	}
}

func TestV11FixtureDoesNotCloseBorrowedRuntime(t *testing.T) {
	borrowed := runtimekv.New(filepath.Join(t.TempDir(), "borrowed.sqlite"))
	defer borrowed.Close()
	t.Run("fixture", func(t *testing.T) {
		server := newV11TestServer(t, Options{FileRoot: t.TempDir(), RuntimeStore: borrowed})
		if _, err := server.runtimeStore.Set("fixture", "entry", "value"); err != nil {
			t.Fatal(err)
		}
	})
	if _, found, err := borrowed.Get("fixture", "entry"); err != nil || !found {
		t.Fatalf("fixture closed its caller-owned runtime: found=%t err=%v", found, err)
	}
}
