package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestTranscriptWebProjectorV67WidensVersionConstraint(t *testing.T) {
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
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 66); err != nil {
		t.Fatal(err)
	}
	assertTranscriptWebProjectorVersionConstraint(t, db, "CHECK(projector_versionIN(1,2,3,4,5,6,7,8,9,10))")
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 67); err != nil {
		t.Fatal(err)
	}
	assertTranscriptWebProjectorVersionConstraint(t, db, "CHECK(projector_versionIN(1,2,3,4,5,6,7,8,9,10,11))")
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 67); err != nil {
		t.Fatalf("repeat v67 migration: %v", err)
	}
}
