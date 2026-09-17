package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStoreEncryptsSecretsAndPersistsUpdates(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	created, err := store.Create(Secret{
		ID: "secret-1", Provider: "github", Name: "token",
		Value: "super-secret-value", Credentials: map[string]string{"token": "credential-secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "secret-1" {
		t.Fatalf("created = %#v", created)
	}
	raw, err := os.ReadFile(filepath.Join(root, "secrets", "vault.enc"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("super-secret-value")) || bytes.Contains(raw, []byte("credential-secret")) {
		t.Fatal("vault contains plaintext secret material")
	}
	info, err := os.Stat(filepath.Join(root, "secrets", "master.key"))
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("key mode = %v err=%v", info.Mode().Perm(), err)
	}
	updated, err := store.Update("secret-1", func(secret *Secret) error {
		secret.Description = "updated"
		return nil
	})
	if err != nil || updated.Description != "updated" {
		t.Fatalf("updated = %#v err=%v", updated, err)
	}
	restarted := New(root)
	resolved, found, err := restarted.Resolve("secret-1")
	if err != nil || !found || resolved.Value != "super-secret-value" ||
		resolved.Credentials["token"] != "credential-secret" {
		t.Fatalf("resolved = %#v found=%v err=%v", resolved, found, err)
	}
	if removed, err := restarted.Delete("secret-1"); err != nil || !removed {
		t.Fatalf("delete removed=%v err=%v", removed, err)
	}
}
