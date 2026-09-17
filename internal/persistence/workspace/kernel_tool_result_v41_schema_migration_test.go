package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestKernelToolResultV41BackfillsDeliveredLegacyCheckpointExactly(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`CREATE TABLE kernel_local_operations(operation_id TEXT PRIMARY KEY,state TEXT NOT NULL,result_sha256 TEXT)`,
		`CREATE TABLE kernel_local_operation_protocol_receipts(
			operation_id TEXT PRIMARY KEY,stream_uid TEXT,event_id INTEGER,result_sha256 TEXT,result_ref TEXT)`,
		`CREATE TABLE transcript_events(stream_uid TEXT,event_id INTEGER,payload_json TEXT,created_at TEXT,
			PRIMARY KEY(stream_uid,event_id))`,
		kernelToolResultMaterializationsDDL,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	resultJSON := json.RawMessage(`{"exec_id":"exec-legacy","ok":true,"stdout":"done\n"}`)
	digest := sha256.Sum256(resultJSON)
	resultSHA := hex.EncodeToString(digest[:])
	createdAt := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	payload, err := json.Marshal(map[string]any{"toolResult": resultJSON})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kernel_local_operations VALUES(?,?,?)`,
		"operation-legacy", KernelLocalOperationStateCompleted, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_events VALUES(?,?,?,?)`,
		"stream-legacy", 17, string(payload), createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kernel_local_operation_protocol_receipts VALUES(?,?,?,?,NULL)`,
		"operation-legacy", "stream-legacy", 17, resultSHA); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := backfillKernelToolResultV41(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var storedJSON, storedSHA, source string
	var resultRef sql.NullString
	if err := db.QueryRow(`SELECT terminal_result_json,terminal_result_sha256,result_ref,source
		FROM kernel_local_operation_materializations WHERE operation_id=?`, "operation-legacy").Scan(
		&storedJSON, &storedSHA, &resultRef, &source,
	); err != nil {
		t.Fatal(err)
	}
	if storedJSON != string(resultJSON) || storedSHA != resultSHA || resultRef.Valid || source != "legacy_checkpoint" {
		t.Fatalf("json=%s sha=%s ref=%#v source=%s", storedJSON, storedSHA, resultRef, source)
	}
	if _, err := db.Exec(`UPDATE kernel_local_operation_protocol_receipts SET result_sha256=?
		WHERE operation_id=?`, strings.Repeat("b", 64), "operation-legacy"); err == nil ||
		!strings.Contains(err.Error(), "protocol receipt is immutable") {
		t.Fatalf("receipt update error=%v", err)
	}
}

func TestKernelToolResultV41MigratesWithStrictMaterializationAuthority(t *testing.T) {
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
	if migration := status.Migrations[40]; migration.Version != 41 ||
		migration.Name != "kernel-tool-result-materialization-authority" {
		t.Fatalf("v41 migration=%#v", migration)
	}

	var ddl string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='kernel_local_operation_materializations'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "STRICT") || !strings.Contains(ddl, "terminal_result_json") ||
		!strings.Contains(ddl, "execution_log_sha256") {
		t.Fatalf("materialization ddl=%s", ddl)
	}
	rows, err := store.db.Query(`PRAGMA table_info(kernel_local_operation_protocol_receipts)`)
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
	if !columns["result_ref"] {
		t.Fatalf("receipt columns=%#v", columns)
	}
}

func TestKernelToolResultV41PreflightRejectsPollutedCohort(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, table := range []string{
		"kernel_local_operations", "kernel_local_operation_protocol_receipts",
		"transcript_tool_call_batches", "transcript_tool_call_items",
	} {
		if _, err := db.Exec(`CREATE TABLE ` + table + `(id TEXT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE kernel_local_operation_materializations(operation_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := preflightKernelToolResultV41(context.Background(), db); err == nil ||
		!strings.Contains(err.Error(), "cohort is polluted") {
		t.Fatalf("preflight error=%v", err)
	}
}
