package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

func TestReadAccountOverviewScopesOwnerAndVisibleRootRecords(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	for _, project := range []CreateProjectInput{
		{ID: "project-a", UserID: "owner-a", Name: "Alpha"},
		{ID: "project-b", UserID: "owner-a", Name: "Beta"},
		{ID: "project-foreign", UserID: "owner-b", Name: "Foreign"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatalf("create project %s: %v", project.ID, err)
		}
	}

	for _, frame := range []CreateFrameInput{
		{ID: "root-visible", ProjectID: "project-a", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
		{ID: "child-visible", ProjectID: "project-a", ParentFrameID: "root-visible", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
		{ID: "root-hidden", ProjectID: "project-b", AgentName: "OPERON", Status: "failed", ConversationType: "agent"},
		{ID: "root-foreign", ProjectID: "project-foreign", AgentName: "OPERON", Status: "completed", ConversationType: "agent"},
	} {
		if _, err := store.CreateFrame(frame); err != nil {
			t.Fatalf("create frame %s: %v", frame.ID, err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO frame_runtime_metadata(frame_id, is_hidden) VALUES(?, 1)`, "root-hidden"); err != nil {
		t.Fatalf("hide frame: %v", err)
	}
	for _, artifact := range []SaveArtifactVersionInput{
		{ArtifactID: "artifact-a", ProjectID: "project-a", Name: "a.csv", Kind: "table", Content: []byte("a")},
		{ArtifactID: "artifact-b", ProjectID: "project-b", Name: "b.md", Kind: "document", Content: []byte("b")},
		{ArtifactID: "large-tool-result-hidden", ProjectID: "project-a", Name: "internal.txt", Kind: "internal", Content: []byte("internal")},
		{ArtifactID: "artifact-foreign", ProjectID: "project-foreign", Name: "foreign.md", Kind: "document", Content: []byte("foreign")},
	} {
		if _, _, err := store.SaveArtifactVersion(artifact); err != nil {
			t.Fatalf("save artifact %s: %v", artifact.ArtifactID, err)
		}
	}

	snapshot, err := store.ReadAccountOverview(context.Background(), "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ProjectCount != 2 || snapshot.ArtifactCount != 2 {
		t.Fatalf("totals = projects %d, artifacts %d, want 2 and 2", snapshot.ProjectCount, snapshot.ArtifactCount)
	}
	if len(snapshot.Tasks) != 1 || snapshot.Tasks[0].ID != "root-visible" || snapshot.Tasks[0].Status != FrameStatusCompleted {
		t.Fatalf("visible root tasks = %#v, want one completed task", snapshot.Tasks)
	}
}

func TestReadAccountOverviewRejectsMissingReadBoundary(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	if _, err := store.ReadAccountOverview(nil, "owner"); err == nil {
		t.Fatal("ReadAccountOverview(nil) error = nil, want context error")
	}
	if _, err := store.ReadAccountOverview(context.Background(), "   "); err == nil {
		t.Fatal("ReadAccountOverview(empty user) error = nil, want user error")
	}
}
