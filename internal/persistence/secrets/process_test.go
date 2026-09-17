package secrets

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const helperModeEnv = "SYNON_SECRETS_HELPER_MODE"
const helperRootEnv = "SYNON_SECRETS_HELPER_ROOT"
const helperIDEnv = "SYNON_SECRETS_HELPER_ID"

func TestSecretStoreHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		return
	}
	root := os.Getenv(helperRootEnv)
	switch mode {
	case "create":
		id := os.Getenv(helperIDEnv)
		if _, err := New(root).Create(Secret{ID: id, Provider: "process", Value: "value-" + id}); err != nil {
			t.Fatal(err)
		}
	case "crash-with-lock":
		store := New(root)
		store.mu.Lock()
		lock, err := store.lockProcess()
		if err != nil {
			t.Fatal(err)
		}
		_ = lock
		if err := os.WriteFile(filepath.Join(root, "locked"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Exit(23)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func TestConcurrentProcessesDoNotLoseSecrets(t *testing.T) {
	root := t.TempDir()
	const count = 12
	commands := make([]*exec.Cmd, 0, count)
	outputs := make([]bytes.Buffer, count)
	for i := 0; i < count; i++ {
		id := "process-" + strconv.Itoa(i)
		cmd := exec.Command(os.Args[0], "-test.run=^TestSecretStoreHelperProcess$")
		cmd.Env = append(os.Environ(), helperModeEnv+"=create", helperRootEnv+"="+root, helperIDEnv+"="+id)
		cmd.Stdout, cmd.Stderr = &outputs[i], &outputs[i]
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("process %d failed: %v\n%s", i, err, outputs[i].String())
		}
	}
	items, err := New(root).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != count {
		t.Fatalf("got %d secrets, want %d: %#v", len(items), count, items)
	}
}

func TestProcessLockIsReleasedAfterCrash(t *testing.T) {
	root := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSecretStoreHelperProcess$")
	cmd.Env = append(os.Environ(), helperModeEnv+"=crash-with-lock", helperRootEnv+"="+root)
	err := cmd.Run()
	if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 23 {
		t.Fatalf("helper exit = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "locked")); err != nil {
		t.Fatalf("helper did not acquire lock: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := New(root).Create(Secret{ID: "after-crash", Provider: "process"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("vault lock remained held after process exit")
	}
}

func TestStoreUsesUniqueTemporaryFilesAndCleansCrashRemnants(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secrets")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	fixed := filepath.Join(dir, "vault.enc.tmp")
	if err := os.WriteFile(fixed, []byte("do-not-touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, ".vault.enc.tmp-stale")
	if err := os.WriteFile(stale, []byte("partial-ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(root).Create(Secret{ID: "safe", Provider: "process"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(fixed)
	if err != nil || string(raw) != "do-not-touch" {
		t.Fatalf("fixed temp file changed: %q, err=%v", raw, err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp file was not recovered: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".vault.enc.tmp-") {
			t.Fatalf("temporary file remains after save: %s", entry.Name())
		}
	}
}
