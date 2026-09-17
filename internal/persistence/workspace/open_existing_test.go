package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenExistingWorkspaceFailsClosedWithoutCreatingOrMigrating(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.sqlite")
	if store, err := OpenExisting(missingPath); err == nil {
		_ = store.Close()
		t.Fatal("OpenExisting created a missing workspace database")
	}
	if _, err := os.Lstat(missingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing workspace path err=%v", err)
	}

	legacyPath := filepath.Join(t.TempDir(), "legacy.sqlite")
	legacyDB, err := sql.Open(sqliteDriver, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacyDB.Exec(`CREATE TABLE legacy_sentinel(id INTEGER PRIMARY KEY)`); err != nil {
		_ = legacyDB.Close()
		t.Fatal(err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenExisting(legacyPath); err == nil {
		_ = store.Close()
		t.Fatal("OpenExisting accepted an unmigrated workspace database")
	}
	checkDB, err := sql.Open(sqliteDriver, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer checkDB.Close()
	var tableCount int
	if err := checkDB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 {
		t.Fatalf("OpenExisting mutated legacy schema: table count=%d", tableCount)
	}
}

func TestOpenExistingWorkspaceReusesValidatedTranscriptAuthorityWithoutDDL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	primary, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer primary.Close()

	var schemaVersionBefore, migrationCountBefore int
	if err := primary.db.QueryRow(`PRAGMA schema_version`).Scan(&schemaVersionBefore); err != nil {
		t.Fatal(err)
	}
	if err := primary.db.QueryRow(`SELECT COUNT(*) FROM workspace_schema_migrations`).Scan(&migrationCountBefore); err != nil {
		t.Fatal(err)
	}

	secondary, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer secondary.Close()
	first, err := secondary.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := secondary.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("TranscriptRepository did not reuse the store-scoped validated authority")
	}

	var schemaVersionAfter, migrationCountAfter int
	if err := primary.db.QueryRow(`PRAGMA schema_version`).Scan(&schemaVersionAfter); err != nil {
		t.Fatal(err)
	}
	if err := primary.db.QueryRow(`SELECT COUNT(*) FROM workspace_schema_migrations`).Scan(&migrationCountAfter); err != nil {
		t.Fatal(err)
	}
	if schemaVersionAfter != schemaVersionBefore || migrationCountAfter != migrationCountBefore {
		t.Fatalf("secondary runtime performed schema work: schema_version %d -> %d, migrations %d -> %d",
			schemaVersionBefore, schemaVersionAfter, migrationCountBefore, migrationCountAfter)
	}
}
