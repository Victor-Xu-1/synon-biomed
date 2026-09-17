package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestOpenUpgradesTargetThreeThroughV24AfterOnlineBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	seedTargetThreeAuthorityFixture(t, path)
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := store.schemaBackupPath
	if backupPath == "" {
		t.Fatal("schema upgrade did not retain a backup")
	}
	if _, err := store.TranscriptRepository(context.Background()); err != nil {
		t.Fatalf("transcript repository: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	backup, err := sql.Open(sqliteDriver, backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var version int
	if err := backup.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&version); err != nil || version != 3 {
		t.Fatalf("backup version=%d err=%v", version, err)
	}
	var transcriptTables int
	if err := backup.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name='transcript_streams'`).Scan(&transcriptTables); err != nil || transcriptTables != 0 {
		t.Fatalf("backup transcript tables=%d err=%v", transcriptTables, err)
	}
}

func TestPruneWorkspaceSchemaBackupsKeepsCurrentAndNewestRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	root := path + ".schema-backups"
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	names := []string{
		"backup-" + strings.Repeat("1", 32),
		"backup-" + strings.Repeat("2", 32),
		"backup-" + strings.Repeat("3", 32),
		"backup-" + strings.Repeat("4", 32),
	}
	for index, name := range names {
		directory := filepath.Join(root, name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		snapshot := filepath.Join(directory, "snapshot.sqlite")
		if err := os.WriteFile(snapshot, []byte("snapshot"), 0o600); err != nil {
			t.Fatal(err)
		}
		modified := now.Add(time.Duration(index) * time.Hour)
		if err := os.Chtimes(snapshot, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	current := filepath.Join(root, names[0], "snapshot.sqlite")
	if err := pruneWorkspaceSchemaBackups(path, current); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name string
		keep bool
	}{
		{names[0], true}, {names[1], false}, {names[2], false}, {names[3], true},
	} {
		_, err := os.Stat(filepath.Join(root, testCase.name))
		if testCase.keep && err != nil {
			t.Fatalf("retained backup %q err=%v", testCase.name, err)
		}
		if !testCase.keep && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expired backup %q err=%v", testCase.name, err)
		}
	}
}

func TestOpenFreshV24DoesNotCreateUpgradeBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.schemaBackupPath != "" {
		t.Fatalf("fresh workspace backup=%q", store.schemaBackupPath)
	}
	if _, err := os.Stat(path + ".schema-backups"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fresh workspace backup root err=%v", err)
	}
	if _, err := store.TranscriptRepository(context.Background()); err != nil {
		t.Fatalf("transcript repository: %v", err)
	}
}

func TestOpenRejectsSymlinkDatabaseAndParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated Windows privileges")
	}
	root := t.TempDir()
	target := filepath.Join(root, "target.db")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "workspace.db")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil || err.Error() != "workspace database must be a regular file" {
		t.Fatalf("database symlink error=%v", err)
	}

	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(filepath.Join(linkedParent, "workspace.db")); err == nil || err.Error() != "workspace database parent must not contain symbolic links" {
		t.Fatalf("parent symlink error=%v", err)
	}
}
