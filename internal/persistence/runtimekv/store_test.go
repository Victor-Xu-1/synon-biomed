package runtimekv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorePersistsNamespaceEntriesAcrossReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-state.sqlite")
	store := New(path)

	first, err := store.Set("agent", "last_goal", map[string]any{"status": "running", "step": float64(2)})
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if first.Namespace != "agent" || first.Key != "last_goal" || first.Version != 1 {
		t.Fatalf("first entry = %#v", first)
	}
	second, err := store.Set("agent", "last_goal", map[string]any{"status": "done"})
	if err != nil {
		t.Fatalf("Set() second error = %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second version = %d", second.Version)
	}

	reloaded := New(path)
	got, ok, err := reloaded.Get("agent", "last_goal")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() did not find persisted entry")
	}
	value := got.Value.(map[string]any)
	if value["status"] != "done" || got.Version != 2 {
		t.Fatalf("got entry = %#v", got)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("runtime store file missing: %v", err)
	}
}

func TestStorePersistsOriginalGoalActiveKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-state.sqlite")
	store := New(path)
	key := "session:taskrun:run-1:taskrun:run-1"
	if _, err := store.Set("goal-runs-active", key, "run-1"); err != nil {
		t.Fatalf("Set(goal active key) error = %v", err)
	}
	reloaded := New(path)
	got, ok, err := reloaded.Get("goal-runs-active", key)
	if err != nil {
		t.Fatalf("Get(goal active key) error = %v", err)
	}
	if !ok || got.Value != "run-1" {
		t.Fatalf("goal active key entry = %#v found=%v", got, ok)
	}
}

func TestStoreListsAndDeletesEntriesDeterministically(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-state.sqlite")
	store := New(path)
	if _, err := store.Set("agent", "b", "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("agent", "a", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("bridge", "client", "online"); err != nil {
		t.Fatal(err)
	}

	agentEntries, err := store.List("agent")
	if err != nil {
		t.Fatalf("List(agent) error = %v", err)
	}
	if len(agentEntries) != 2 || agentEntries[0].Key != "a" || agentEntries[1].Key != "b" {
		t.Fatalf("agent entries = %#v", agentEntries)
	}
	allEntries, err := store.List("")
	if err != nil {
		t.Fatalf("List(all) error = %v", err)
	}
	if len(allEntries) != 3 || allEntries[0].Namespace != "agent" || allEntries[2].Namespace != "bridge" {
		t.Fatalf("all entries = %#v", allEntries)
	}

	deleted, err := store.Delete("agent", "a")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if !deleted {
		t.Fatal("Delete() returned false for existing key")
	}
	deleted, err = store.Delete("agent", "missing")
	if err != nil {
		t.Fatalf("Delete(missing) error = %v", err)
	}
	if deleted {
		t.Fatal("Delete() returned true for missing key")
	}
	agentEntries, err = store.List("agent")
	if err != nil {
		t.Fatalf("List(agent) after delete error = %v", err)
	}
	if len(agentEntries) != 1 || agentEntries[0].Key != "b" {
		t.Fatalf("agent entries after delete = %#v", agentEntries)
	}
}

func TestStoreListReadOnlyReturnsSortedNamespaceSnapshot(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "runtime-state.sqlite"))
	if _, err := store.Set("audit", "b", map[string]any{"sessionId": "frame-b"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("audit", "a", map[string]any{"sessionId": "frame-a"}); err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListReadOnly("audit")
	if err != nil {
		t.Fatalf("ListReadOnly(audit) error = %v", err)
	}
	if len(entries) != 2 || entries[0].Key != "a" || entries[1].Key != "b" {
		t.Fatalf("read-only entries = %#v", entries)
	}
	value, ok := entries[0].Value.(map[string]any)
	if !ok || value["sessionId"] != "frame-a" {
		t.Fatalf("read-only value = %#v", entries[0].Value)
	}
}

func TestStoreRejectsUnsafeNamespaceAndKey(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "runtime-state.sqlite"))
	for _, testCase := range []struct {
		namespace string
		key       string
	}{
		{"", "key"},
		{"../secret", "key"},
		{"agent", ""},
		{"agent", "../secret"},
		{"agent/slash", "key"},
		{"agent", "bad/key"},
	} {
		if _, err := store.Set(testCase.namespace, testCase.key, "value"); err == nil {
			t.Fatalf("Set(%q, %q) succeeded", testCase.namespace, testCase.key)
		}
	}
}

func TestStoreReusesUnchangedFileWithoutReadingItAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-state.sqlite")
	writer := New(path)
	if _, err := writer.Set("agent", "state", map[string]any{"status": "ready"}); err != nil {
		t.Fatal(err)
	}
	reader := New(path)
	if _, found, err := reader.Get("agent", "state"); err != nil || !found {
		t.Fatalf("prime cache found=%t err=%v", found, err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, found, err := reader.Get("agent", "state"); err != nil || !found {
		t.Fatalf("cached read found=%t err=%v", found, err)
	}
}

func TestStoreRefreshesCacheAfterExternalWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-state.sqlite")
	reader := New(path)
	writer := New(path)
	if _, err := writer.Set("agent", "state", "one"); err != nil {
		t.Fatal(err)
	}
	if entry, found, err := reader.Get("agent", "state"); err != nil || !found || entry.Value != "one" {
		t.Fatalf("initial read entry=%#v found=%t err=%v", entry, found, err)
	}
	updated := strings.Repeat("two", 20)
	if _, err := writer.Set("agent", "state", updated); err != nil {
		t.Fatal(err)
	}
	if entry, found, err := reader.Get("agent", "state"); err != nil || !found || entry.Value != updated {
		t.Fatalf("refreshed read entry=%#v found=%t err=%v", entry, found, err)
	}
}

func TestStoreEditNamespacePersistsOneAtomicRewrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-state.sqlite")
	store := New(path)
	if _, err := store.Set("audit", "large", strings.Repeat("x", 4096)); err != nil {
		t.Fatal(err)
	}
	if err := store.EditNamespace("audit", func(entries map[string]Entry) (bool, error) {
		entry := entries["large"]
		entry.Value = map[string]any{"truncated": true}
		entries["large"] = entry
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	reloaded := New(path)
	entry, found, err := reloaded.Get("audit", "large")
	if err != nil || !found {
		t.Fatalf("rewritten entry found=%t err=%v", found, err)
	}
	value, ok := entry.Value.(map[string]any)
	if !ok || value["truncated"] != true {
		t.Fatalf("rewritten value=%#v", entry.Value)
	}
}

func TestStoreEditNamespaceRollsBackNestedValueMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-state.sqlite")
	store := New(path)
	if _, err := store.Set("audit", "one", map[string]any{"nested": map[string]any{"value": "original"}}); err != nil {
		t.Fatal(err)
	}

	if err := store.EditNamespace("audit", func(entries map[string]Entry) (bool, error) {
		value := entries["one"].Value.(map[string]any)
		value["nested"].(map[string]any)["value"] = "mutated"
		return false, nil
	}); err != nil {
		t.Fatal(err)
	}
	entry, found, err := store.Get("audit", "one")
	if err != nil || !found {
		t.Fatalf("cached entry found=%t err=%v", found, err)
	}
	if got := entry.Value.(map[string]any)["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("cached nested value=%v", got)
	}
	reopened, found, err := New(path).Get("audit", "one")
	if err != nil || !found {
		t.Fatalf("reopened entry found=%t err=%v", found, err)
	}
	if got := reopened.Value.(map[string]any)["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("durable nested value=%v", got)
	}
}

func TestStoreUsesSQLiteAndSweepsOnlyClassifiedOrScopedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-state.sqlite")
	store := New(path)
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := old.Add(10 * 24 * time.Hour)
	store.now = func() time.Time { return old }
	if _, err := store.Set("tool-gateway-audit", "old", map[string]any{
		"projectId": "project-a", "rootFrameId": "root-a", "frameId": "frame-a",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Set("scheduled-tasks", "active", map[string]any{"projectId": "project-b"}); err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return recent }
	if _, err := store.Set("tool-gateway-audit", "recent", map[string]any{"projectId": "project-b"}); err != nil {
		t.Fatal(err)
	}
	deleted, err := store.DeleteOlderThan(context.Background(), []string{"tool-gateway-audit"}, recent.Add(-24*time.Hour))
	if err != nil || deleted != 1 {
		t.Fatalf("audit sweep deleted=%d err=%v", deleted, err)
	}
	if _, found, err := store.Get("scheduled-tasks", "active"); err != nil || !found {
		t.Fatalf("active state found=%t err=%v", found, err)
	}
	deleted, err = store.DeleteScope(context.Background(), Scope{ProjectID: "project-b"})
	if err != nil || deleted != 2 {
		t.Fatalf("scope delete deleted=%d err=%v", deleted, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 16 || string(raw[:16]) != "SQLite format 3\x00" {
		t.Fatalf("runtime store header=%q", raw[:min(len(raw), 16)])
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime store mode=%v", info.Mode().Perm())
	}
}
