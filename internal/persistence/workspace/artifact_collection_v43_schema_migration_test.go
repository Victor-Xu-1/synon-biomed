package workspace

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactCollectionV43CreatesProjectCurrentCoveringIndex(t *testing.T) {
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
	latest := status.Migrations[42]
	if latest.Version != 43 || latest.Name != "artifact-project-collection-index" {
		t.Fatalf("v43 migration=%#v", latest)
	}

	var ddl string
	if err := store.db.QueryRow(`SELECT sql FROM sqlite_schema
		WHERE type='index' AND name='artifacts_project_current_idx'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"project_id", "updated_at DESC", "id DESC", "current_version_number"} {
		if !strings.Contains(ddl, required) {
			t.Fatalf("artifact collection index is missing %q: %s", required, ddl)
		}
	}

	rows, err := store.db.Query(`EXPLAIN QUERY PLAN
		SELECT COUNT(*),MAX(a.updated_at)
		FROM artifacts a
		WHERE a.project_id=?`, "project-plan")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(detail)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.String(), "artifacts_project_current_idx") {
		t.Fatalf("artifact project revision query does not use covering index:\n%s", plan.String())
	}
}
