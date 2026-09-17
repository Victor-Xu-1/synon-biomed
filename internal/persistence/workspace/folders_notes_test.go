package workspace

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenMigratesLegacyArtifactTableWithFolderColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE artifacts (
			id TEXT PRIMARY KEY,
			project_id TEXT NOT NULL,
			name TEXT NOT NULL,
			kind TEXT NOT NULL,
			current_version_number INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP NOT NULL,
			updated_at TIMESTAMP NOT NULL
		)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := legacy.Exec(`
		INSERT INTO artifacts (
			id, project_id, name, kind, current_version_number, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"legacy-artifact", "legacy-project", "Legacy", "text/plain", 0, now, now,
	); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	artifact, found, err := store.GetArtifact("legacy-artifact")
	if err != nil || !found || artifact.FolderID != "" || artifact.Name != "Legacy" {
		t.Fatalf("migrated artifact = %#v, found=%v, err=%v", artifact, found, err)
	}
	if _, err := store.db.ExecContext(t.Context(),
		`UPDATE artifacts SET folder_id = NULL WHERE id = ?`, artifact.ID,
	); err != nil {
		t.Fatalf("folder_id column unavailable after migration: %v", err)
	}
}
