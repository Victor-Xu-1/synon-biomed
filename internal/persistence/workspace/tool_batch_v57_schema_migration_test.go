package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestToolBatchV57PublishedIdentity(t *testing.T) {
	checksum, err := toolBatchV57Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const published = "88dbe69d4f6b3c24f43313b3fd5a66fc7a40da1afc5f254b4e48d0b627ffc39e"
	if checksum != published {
		t.Fatalf("published v57 checksum changed: got %s want %s", checksum, published)
	}
}

func TestToolBatchV57UpgradesPrestartFailureTransitions(t *testing.T) {
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
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 56); err != nil {
		t.Fatal(err)
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 57); err != nil {
		t.Fatal(err)
	}
	var batchTrigger, itemTrigger string
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='transcript_tool_call_batches_transition_valid'`).Scan(&batchTrigger); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='transcript_tool_call_batch_items_transition_valid'`).Scan(&itemTrigger); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(batchTrigger, "NEW.state IN ('ready','running','settled'") ||
		!strings.Contains(itemTrigger, "NEW.state IN ('running','failed'") {
		t.Fatalf("batch trigger=%q item trigger=%q", batchTrigger, itemTrigger)
	}
}
