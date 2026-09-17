package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStoreScopesSecretsByUserAcrossRestart(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if _, err := store.Create(Secret{ID: "a", UserID: "user-a", Provider: "generic", Value: "a-secret"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(Secret{ID: "b", UserID: "user-b", Provider: "generic", Value: "b-secret"}); err != nil {
		t.Fatal(err)
	}
	restarted := New(root)
	items, err := restarted.ListForUser("user-a")
	if err != nil || len(items) != 1 || items[0].ID != "a" {
		t.Fatalf("user-a list = %#v, err = %v", items, err)
	}
	if _, found, err := restarted.ResolveForUser("a", "user-b"); err != nil || found {
		t.Fatalf("cross-user resolve found=%v err=%v", found, err)
	}
	if removed, err := restarted.DeleteForUser("a", "user-b"); err != nil || removed {
		t.Fatalf("cross-user delete removed=%v err=%v", removed, err)
	}
	if _, found, err := restarted.ResolveForUser("a", "user-a"); err != nil || !found {
		t.Fatalf("owner resolve found=%v err=%v", found, err)
	}
}

func TestStoreRejectsSymlinkedKeyAndVault(t *testing.T) {
	t.Run("key", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "secrets"), 0o700); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.key")
		if err := os.WriteFile(outside, make([]byte, 32), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "secrets", "master.key")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := New(root).List(); err == nil {
			t.Fatalf("symlinked key error = %v", err)
		}
	})
	t.Run("vault", func(t *testing.T) {
		root := t.TempDir()
		store := New(root)
		if _, err := store.Create(Secret{ID: "secret", Provider: "generic", Value: "value"}); err != nil {
			t.Fatal(err)
		}
		vault := filepath.Join(root, "secrets", "vault.enc")
		outside := filepath.Join(t.TempDir(), "outside.enc")
		if err := os.Rename(vault, outside); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, vault); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := New(root).List(); err == nil {
			t.Fatalf("symlinked vault error = %v", err)
		}
	})
}

func TestStoreRejectsSymlinkedVaultDirectoryAndParent(t *testing.T) {
	t.Run("vault directory", func(t *testing.T) {
		root := t.TempDir()
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(root, "secrets")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := New(root).List(); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("symlinked vault directory error = %v", err)
		}
	})
	t.Run("parent", func(t *testing.T) {
		container := t.TempDir()
		outside := t.TempDir()
		root := filepath.Join(container, "root-link")
		if err := os.Symlink(outside, root); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if _, err := New(root).List(); err == nil || !strings.Contains(err.Error(), "symbolic link") {
			t.Fatalf("symlinked parent error = %v", err)
		}
	})
}

func TestStoreRejectsSymlinkedLockFile(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secrets")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "outside.lock")
	if err := os.WriteFile(out, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(out, filepath.Join(dir, "vault.lock")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := New(root).List(); err == nil {
		t.Fatal("symlinked lock file was accepted")
	}
}
