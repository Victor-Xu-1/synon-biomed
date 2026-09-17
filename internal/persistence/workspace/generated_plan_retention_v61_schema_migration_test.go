package workspace

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGeneratedPlanRetentionV61PublishedIdentity(t *testing.T) {
	checksum, err := generatedPlanRetentionV61Migration.validatedChecksum()
	if err != nil {
		t.Fatal(err)
	}
	const published = "ac55242959f19cc97fec965306300ea00b99dbaab735655f3a55a02b352bdf25"
	if checksum != published {
		t.Fatalf("published v61 checksum changed: got %s want %s", checksum, published)
	}
}

func TestGeneratedPlanRetentionV61HidesOnlyRuntimeGeneratedPlans(t *testing.T) {
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
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 60); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-v61", UserID: "owner-v61", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rows := []struct{ id, name string }{
		{"plan-" + strings.Repeat("a", 32), "plan_aaaaaaaaaaaaaaaa.json"},
		{"user-plan", "plan.json"},
		{"plan-" + strings.Repeat("b", 32), "requested_plan.json"},
	}
	for _, row := range rows {
		if _, err := db.Exec(`INSERT INTO artifacts(
			id,project_id,name,kind,current_version_number,retention_mode,created_at,updated_at
		) VALUES(?,?,?,?,0,'snapshot',?,?)`, row.id, "project-v61", row.name, "application/json", now, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := applyVersionedSchemaMigrationsThrough(ctx, db, time.Now, 61); err != nil {
		t.Fatal(err)
	}
	for index, row := range rows {
		var mode string
		if err := db.QueryRow(`SELECT retention_mode FROM artifacts WHERE id=?`, row.id).Scan(&mode); err != nil {
			t.Fatal(err)
		}
		want := "snapshot"
		if index == 0 {
			want = "working_data"
		}
		if mode != want {
			t.Fatalf("artifact %q retention=%q want=%q", row.id, mode, want)
		}
	}
}
