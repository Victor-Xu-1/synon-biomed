package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTranscriptWebProjectorV64WidensVersionConstraint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace.sqlite")
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	store := &Store{db: db, now: time.Now, blobRoot: path + ".blobs"}
	if err := prepareSchemaJournal(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := store.prepareLegacyWorkspaceSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 63); err != nil {
		t.Fatal(err)
	}
	assertTranscriptWebProjectorVersionConstraint(t, db, "CHECK(projector_versionIN(1,2,3,4,5,6,7))")
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 64); err != nil {
		t.Fatal(err)
	}
	assertTranscriptWebProjectorVersionConstraint(t, db, "CHECK(projector_versionIN(1,2,3,4,5,6,7,8))")
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 64); err != nil {
		t.Fatalf("repeat v64 migration: %v", err)
	}
}

func assertTranscriptWebProjectorVersionConstraint(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	var tableSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='transcript_web_projection_state'`).Scan(&tableSQL); err != nil {
		t.Fatal(err)
	}
	compact := strings.NewReplacer(" ", "", "\n", "", "\r", "", "\t", "").Replace(tableSQL)
	if !strings.Contains(compact, want) {
		t.Fatalf("projector constraint=%s want %s", tableSQL, want)
	}
}
