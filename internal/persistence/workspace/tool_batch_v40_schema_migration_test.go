package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestToolCallBatchV40MigratesWithStrictAuthority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	status, err := store.SchemaStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentVersion != workspaceSchemaVersion || status.TargetVersion != workspaceSchemaVersion || len(status.Migrations) != workspaceSchemaVersion {
		t.Fatalf("schema status=%#v", status)
	}
	if migration := status.Migrations[39]; migration.Version != 40 || migration.Name != "transcript-tool-call-batch-authority" {
		t.Fatalf("v40 migration=%#v", migration)
	}

	for _, table := range []string{"transcript_tool_call_batches", "transcript_tool_call_items"} {
		var ddl string
		if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&ddl); err != nil {
			t.Fatalf("read %s ddl: %v", table, err)
		}
		if !strings.Contains(ddl, "STRICT") {
			t.Fatalf("%s is not strict: %s", table, ddl)
		}
	}

	rows, err := store.db.Query(`PRAGMA table_info(transcript_tool_call_items)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !columns["arguments_json"] || !columns["arguments_sha256"] || !columns["terminal_result_sha256"] ||
		!columns["result_ref"] || !columns["terminal_event_id"] || columns["result_json"] {
		t.Fatalf("unexpected item columns=%#v", columns)
	}
}

func TestToolCallBatchV40PreflightRejectsPollutedCohort(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{"transcript_streams", "transcript_events", "transcript_branch_state", "transcript_branch_events"} {
		if _, err := db.Exec(`CREATE TABLE ` + table + `(id TEXT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE transcript_tool_call_batches(batch_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := preflightToolCallBatchV40(context.Background(), db); err == nil ||
		!strings.Contains(err.Error(), "cohort is polluted") {
		t.Fatalf("preflight error=%v", err)
	}
}
