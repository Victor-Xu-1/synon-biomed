package workspace

import (
	"path/filepath"
	"testing"
)

func TestScientificComputeAuthorityV37FreshSchemaIsRegisteredAndStrict(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	assertSchemaJournalVersion(t, store.db, workspaceSchemaVersion)
	var v37Name string
	if err := store.db.QueryRow(`SELECT name FROM workspace_schema_migrations WHERE version=37`).Scan(&v37Name); err != nil || v37Name != "scientific-compute-authority" {
		t.Fatalf("v37 migration name=%q err=%v", v37Name, err)
	}
	for _, table := range []string{
		"scientific_compute_admissions",
		"scientific_compute_admission_inputs",
		"scientific_compute_admission_outputs",
		"scientific_compute_receipts",
		"scientific_compute_receipt_outputs",
		"scientific_compute_handle_authorities",
		"scientific_compute_handles",
	} {
		var strict int
		if err := store.db.QueryRow(`SELECT strict FROM pragma_table_list WHERE name=?`, table).Scan(&strict); err != nil || strict != 1 {
			t.Fatalf("table=%s strict=%d err=%v", table, strict, err)
		}
	}
	rows, err := store.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("scientific compute schema has a foreign-key violation")
	}
}
