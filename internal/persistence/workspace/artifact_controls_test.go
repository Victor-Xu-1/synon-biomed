package workspace

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestArtifactPriorityMigrationUpgradesLegacyTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-workspace.db")
	db, err := sql.Open(sqliteDriver, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE artifacts (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		name TEXT NOT NULL,
		kind TEXT NOT NULL,
		current_version_number INTEGER NOT NULL DEFAULT 0,
		folder_id TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.md",
		Kind: "markdown", Content: []byte("report"),
	}); err != nil {
		t.Fatal(err)
	}
	artifact, found, err := store.GetArtifact("artifact-1")
	if err != nil || !found || artifact.Priority != ArtifactPriorityUnknown {
		t.Fatalf("migrated artifact = %#v, found=%v, err=%v", artifact, found, err)
	}
	artifact, err = store.UpdateArtifactPriority("artifact-1", ArtifactPriorityUserStarred)
	if err != nil || artifact.Priority != ArtifactPriorityUserStarred {
		t.Fatalf("updated migrated priority = %#v, err=%v", artifact, err)
	}
}
