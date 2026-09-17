package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestKernelResultSpoolV63UpgradesExistingWorkspace(t *testing.T) {
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
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 62); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table'
		AND name='kernel_execution_result_spool_refs'`).Scan(&before); err != nil || before != 0 {
		t.Fatalf("pre-v63 spool table=%d err=%v", before, err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 63); err != nil {
		t.Fatal(err)
	}
	var after, version int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='table'
		AND name='kernel_execution_result_spool_refs'`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT MAX(version) FROM workspace_schema_migrations`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if after != 1 || version != 63 {
		t.Fatalf("post-v63 spool table=%d version=%d", after, version)
	}
}
