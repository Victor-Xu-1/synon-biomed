package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceSchemaJournalIsVersionedAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.SchemaStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.CurrentVersion != workspaceSchemaVersion || status.TargetVersion != workspaceSchemaVersion || len(status.Migrations) != workspaceSchemaVersion {
		t.Fatalf("schema status=%#v", status)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedStatus, err := reopened.SchemaStatus(context.Background())
	if err != nil || len(reopenedStatus.Migrations) != len(status.Migrations) {
		t.Fatalf("reopened schema status=%#v err=%v", reopenedStatus, err)
	}
}

func TestWorkspaceSchemaJournalRejectsTamperingAndForwardVersions(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate string
		want   string
	}{
		{name: "checksum", mutate: `UPDATE workspace_schema_migrations SET checksum='tampered' WHERE version=2`, want: "identity mismatch"},
		{name: "forward version", mutate: `INSERT INTO workspace_schema_migrations(version,name,checksum,applied_at) VALUES(999,'future','future',CURRENT_TIMESTAMP)`, want: "newer than supported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workspace.db")
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.mutate); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(path); err == nil {
				_ = reopened.Close()
				t.Fatalf("tampered schema journal was accepted")
			} else if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Open() error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestExpectedSchemaStatusMatchesFreshWorkspace(t *testing.T) {
	expected, err := ExpectedSchemaStatus()
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actual, err := store.SchemaStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if actual.CurrentVersion != expected.CurrentVersion || actual.TargetVersion != expected.TargetVersion || len(actual.Migrations) != len(expected.Migrations) {
		t.Fatalf("schema status actual=%#v expected=%#v", actual, expected)
	}
	for index := range expected.Migrations {
		left, right := actual.Migrations[index], expected.Migrations[index]
		if left.Version != right.Version || left.Name != right.Name || left.Checksum != right.Checksum {
			t.Fatalf("migration[%d] actual=%#v expected=%#v", index, left, right)
		}
	}
}
