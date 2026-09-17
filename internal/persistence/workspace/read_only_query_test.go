package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

func TestReadOnlyQueryScopesProjectsAndRejectsMutationOrPrivateTables(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, input := range []CreateProjectInput{
		{ID: "project-a", UserID: "owner", Name: "A"},
		{ID: "project-b", UserID: "owner", Name: "B"},
		{ID: "project-foreign", UserID: "other", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(input); err != nil {
			t.Fatal(err)
		}
	}
	for _, input := range []CreateFrameInput{
		{ID: "frame-a", ProjectID: "project-a", AgentName: "OPERON", Status: "processing", ConversationType: "agent"},
		{ID: "frame-b", ProjectID: "project-b", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
		{ID: "frame-foreign", ProjectID: "project-foreign", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
	} {
		if _, err := store.CreateFrame(input); err != nil {
			t.Fatal(err)
		}
	}
	project, err := store.ReadOnlyQuery(context.Background(), ReadOnlyQueryInput{
		UserID: "owner", ProjectID: "project-a", SQL: "SELECT id, name FROM projects ORDER BY id", Scope: "project",
	})
	if err != nil || project.RowCount != 1 || project.Rows[0][0] != "project-a" {
		t.Fatalf("project query=%#v err=%v", project, err)
	}
	global, err := store.ReadOnlyQuery(context.Background(), ReadOnlyQueryInput{
		UserID: "owner", ProjectID: "project-a", SQL: "SELECT id FROM projects ORDER BY id", Scope: "global",
	})
	if err != nil || global.RowCount != 2 || global.Rows[0][0] != "project-a" || global.Rows[1][0] != "project-b" {
		t.Fatalf("global query=%#v err=%v", global, err)
	}
	frames, err := store.ReadOnlyQuery(context.Background(), ReadOnlyQueryInput{
		UserID: "owner", ProjectID: "project-a", SQL: "WITH chosen AS (SELECT id FROM frames) SELECT id FROM chosen", Scope: "project",
	})
	if err != nil || frames.RowCount != 1 || frames.Rows[0][0] != "frame-a" {
		t.Fatalf("frame query=%#v err=%v", frames, err)
	}
	for _, statement := range []string{
		"UPDATE projects SET name='x'",
		"SELECT * FROM main.projects",
		"SELECT * FROM frame_runtime_metadata",
		"SELECT * FROM sqlite_master",
	} {
		if _, err := store.ReadOnlyQuery(context.Background(), ReadOnlyQueryInput{UserID: "owner", ProjectID: "project-a", SQL: statement}); err == nil {
			t.Fatalf("unsafe query was admitted: %s", statement)
		}
	}
	schema, err := store.ReadOnlyQuerySchema(context.Background(), "owner", "project-a", "project")
	if err != nil || len(schema["projects"]) == 0 || len(schema["frames"]) == 0 {
		t.Fatalf("query schema=%#v err=%v", schema, err)
	}
}
