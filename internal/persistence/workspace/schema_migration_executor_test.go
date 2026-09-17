package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

func TestWorkspaceMigrationUsesPinnedConnectionExclusively(t *testing.T) {
	// This context covers two complete migrations, not a connection-acquisition
	// latency assertion. The closed pool below detects accidental pool use
	// immediately, independently of race-instrumented migration throughput.
	deadline := time.Now().Add(2 * time.Minute)
	if testDeadline, ok := t.Deadline(); ok && testDeadline.Before(deadline) {
		deadline = testDeadline.Add(-time.Second)
	}
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	root := t.TempDir()
	pinnedPath := filepath.Join(root, "pinned.sqlite")
	db, err := sql.Open(sqliteDriver, pinnedPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	// sql.DB.Close leaves an already borrowed Conn valid but rejects any new
	// pool operation. Keep Store.db pointing at this pool so a migration that
	// bypasses its supplied executor fails instead of waiting for a timeout.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.PingContext(ctx); err != nil {
		t.Fatalf("borrowed connection did not survive pool close: %v", err)
	}
	if err := db.PingContext(ctx); err == nil {
		t.Fatal("closed pool unexpectedly accepted a new operation")
	}
	fixedNow := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	store := &Store{db: db, now: func() time.Time { return fixedNow }}
	if err := store.migrateWithExecutor(ctx, conn); err != nil {
		t.Fatalf("migrate through pinned connection: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}

	ordinaryPath := filepath.Join(root, "ordinary.sqlite")
	ordinaryDB, err := sql.Open(sqliteDriver, ordinaryPath)
	if err != nil {
		t.Fatal(err)
	}
	ordinaryDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = ordinaryDB.Close() })
	ordinaryStore := &Store{db: ordinaryDB, now: func() time.Time { return fixedNow }}
	if err := ordinaryStore.migrateWithExecutor(ctx, ordinaryDB); err != nil {
		t.Fatalf("migrate through database pool: %v", err)
	}
	if err := ordinaryDB.Close(); err != nil {
		t.Fatal(err)
	}

	pinnedSchema := readMigrationSchema(t, pinnedPath)
	ordinarySchema := readMigrationSchema(t, ordinaryPath)
	if differences := migrationSchemaDifferences(pinnedSchema, ordinarySchema); len(differences) != 0 {
		t.Fatalf("pinned schema differs from ordinary schema: %v", differences)
	}
	pinnedJournal := readMigrationJournal(t, pinnedPath)
	ordinaryJournal := readMigrationJournal(t, ordinaryPath)
	if !reflect.DeepEqual(pinnedJournal, ordinaryJournal) {
		t.Fatalf("pinned journal differs from ordinary journal\npinned=%#v\nordinary=%#v", pinnedJournal, ordinaryJournal)
	}

	reopened, err := Open(pinnedPath)
	if err != nil {
		t.Fatalf("reopen pinned database: %v", err)
	}
	defer reopened.Close()
	status, err := reopened.SchemaStatus(ctx)
	if err != nil || status.CurrentVersion != workspaceSchemaVersion {
		t.Fatalf("reopened status=%#v err=%v", status, err)
	}
}

func readMigrationSchema(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT type || ':' || name, COALESCE(sql, '')
		FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func migrationSchemaDifferences(left, right map[string]string) []string {
	keys := map[string]bool{}
	for key := range left {
		keys[key] = true
	}
	for key := range right {
		keys[key] = true
	}
	differences := []string{}
	for key := range keys {
		switch {
		case left[key] == "":
			differences = append(differences, "missing pinned "+key)
		case right[key] == "":
			differences = append(differences, "missing ordinary "+key)
		case left[key] != right[key]:
			differences = append(differences, "definition "+key)
		}
	}
	sort.Strings(differences)
	return differences
}

func readMigrationJournal(t *testing.T, path string) []string {
	t.Helper()
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT printf('%d:%s:%s', version, name, checksum)
		FROM workspace_schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}
