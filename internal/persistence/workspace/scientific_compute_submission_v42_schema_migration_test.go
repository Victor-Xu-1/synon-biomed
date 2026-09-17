package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestScientificComputeSubmissionV42CreatesStrictRecoveryAuthority(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	status, err := store.SchemaStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentVersion != workspaceSchemaVersion || status.TargetVersion != workspaceSchemaVersion || len(status.Migrations) != workspaceSchemaVersion {
		t.Fatalf("schema status=%#v", status)
	}
	latest := status.Migrations[41]
	if latest.Version != 42 || latest.Name != "scientific-compute-submission-recovery" {
		t.Fatalf("v42 migration=%#v", latest)
	}
	var ddl string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='table' AND name='scientific_compute_submissions'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"STRICT", "request_sha256", "handle_authority_seq", "recovery_generation",
		"archive_storage_path", "outbox_event_id", "scientific-submissions/*",
	} {
		if !strings.Contains(ddl, required) {
			t.Fatalf("submission ddl is missing %q: %s", required, ddl)
		}
	}
	var objects int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE name IN (
		'scientific_compute_submissions_recovery_idx','scientific_compute_submissions_identity_immutable'
	)`).Scan(&objects); err != nil || objects != 2 {
		t.Fatalf("submission recovery objects=%d err=%v", objects, err)
	}
}
