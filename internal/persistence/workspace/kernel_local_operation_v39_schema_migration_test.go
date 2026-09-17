package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestKernelLocalOperationV39MigratesWithStrictImmutableAuthority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	status, err := store.SchemaStatus(context.Background())
	if err != nil || status.CurrentVersion < 39 || status.TargetVersion < 39 || len(status.Migrations) < 39 {
		t.Fatalf("schema status=%#v err=%v", status, err)
	}
	var migrationFound bool
	for _, migration := range status.Migrations {
		if migration.Version == 39 {
			migrationFound = migration.Name == "kernel-local-operation-authority"
			break
		}
	}
	if !migrationFound {
		t.Fatalf("v39 migration is unavailable: %#v", status.Migrations)
	}
	for _, name := range []string{"kernel_local_operations", "kernel_local_operation_transitions"} {
		var ddl string
		if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, name).Scan(&ddl); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(strings.TrimSpace(ddl), "STRICT") {
			t.Fatalf("%s is not strict: %s", name, ddl)
		}
	}
	for _, name := range []string{
		"kernel_local_operations_identity_immutable",
		"kernel_local_operations_state_transition_valid",
		"kernel_local_operations_confinement_assignment",
		"kernel_local_operation_transition_matches_head",
		"kernel_local_operation_transition_initial",
		"kernel_local_operation_transition_append",
		"kernel_local_operations_delete_forbidden",
		"kernel_local_operation_transitions_immutable",
		"kernel_local_operation_transitions_append_only",
	} {
		var found int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND name=?`, name).Scan(&found); err != nil || found != 1 {
			t.Fatalf("trigger %s count=%d err=%v", name, found, err)
		}
	}
	assertKernelLocalOperationV39RejectsPollutedUpgrade(t)
}

func assertKernelLocalOperationV39RejectsPollutedUpgrade(t *testing.T) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, name := range []string{"projects", "frames", "execution_log", "transcript_streams", "transcript_events"} {
		if _, err := db.Exec(`CREATE TABLE ` + name + `(id TEXT)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE kernel_local_operations(operation_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := preflightKernelLocalOperationV39(context.Background(), db); err == nil ||
		!strings.Contains(err.Error(), "cohort is polluted") {
		t.Fatalf("polluted v39 error=%v", err)
	}
}
