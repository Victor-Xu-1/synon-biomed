package operationlog

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDailyWriterRotatesAndRemovesLogsOlderThanSevenDays(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	oldPath := filepath.Join(root, "server-20260820.log")
	recentPath := filepath.Join(root, "health-recent.json")
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(oldPath, now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recentPath, []byte("recent"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(recentPath, now.Add(-6*24*time.Hour), now.Add(-6*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	clock := now
	writer, err := openWithClock(root, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired log still exists: %v", err)
	}
	if _, err := os.Stat(recentPath); err != nil {
		t.Fatalf("recent log was removed: %v", err)
	}
	if _, err := writer.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(root, "server-20260901.log")
	assertPrivateLog(t, firstPath, "first\n")
	clock = clock.Add(24 * time.Hour)
	if _, err := writer.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	assertPrivateLog(t, filepath.Join(root, "server-20260902.log"), "second\n")
}

func assertPrivateLog(t *testing.T, path, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != want {
		t.Fatalf("log content=%q want=%q", raw, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("log mode=%v", info.Mode().Perm())
	}
}
