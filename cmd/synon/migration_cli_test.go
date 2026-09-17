package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	v11migration "synon-go/internal/migration/v11"

	_ "modernc.org/sqlite"
)

func TestV11MigrationCLIInspectsRealSQLiteAndValidatesActions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operon-cli.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE __drizzle_migrations (id INTEGER PRIMARY KEY, hash TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`CREATE TABLE projects (id TEXT PRIMARY KEY, user_id TEXT, name TEXT, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE frames (id TEXT PRIMARY KEY, agent_name TEXT NOT NULL, status TEXT NOT NULL, project_id TEXT, conversation_type TEXT NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
		`CREATE TABLE artifacts (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, filename TEXT NOT NULL, latest_version_id TEXT, created_at INTEGER NOT NULL)`,
		`CREATE TABLE artifact_versions (id TEXT PRIMARY KEY, artifact_id TEXT NOT NULL, version_number INTEGER NOT NULL, content_type TEXT NOT NULL, checksum TEXT NOT NULL, storage_path TEXT NOT NULL, created_at INTEGER NOT NULL)`,
		`INSERT INTO __drizzle_migrations VALUES (1, 'hash', 1782791100000)`,
		`INSERT INTO projects VALUES ('p', 'u', 'project', 1, 2)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := runV11MigrationCLI(context.Background(), []string{"inspect", "--source-db", path}, &output); err != nil {
		t.Fatal(err)
	}
	var inspection v11migration.Inspection
	if err := json.Unmarshal(output.Bytes(), &inspection); err != nil {
		t.Fatal(err)
	}
	if inspection.Schema != v11migration.SchemaName || inspection.TableCounts["projects"] != 1 || !inspection.QuickCheckOK {
		t.Fatalf("inspection = %#v", inspection)
	}

	for _, args := range [][]string{
		nil,
		{"unknown"},
		{"inspect"},
		{"migrate", "--source-db", path},
		{"verify"},
		{"activate", "--target-home", t.TempDir()},
		{"rollback-cutover", "--data-dir-control", filepath.Join(t.TempDir(), "control.json")},
		{"rollback", "--target-home", t.TempDir()},
	} {
		output.Reset()
		if err := runV11MigrationCLI(context.Background(), args, &output); err == nil {
			t.Fatalf("invalid args accepted: %#v", args)
		}
	}
}
