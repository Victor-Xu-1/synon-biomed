package workspace

import (
	"path/filepath"
	"testing"
)

func TestArtifactListRenameMetadataAndDelete(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	artifact, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "draft.md",
		Kind: "markdown", Content: []byte("draft"), CreatedBy: "planner",
	})
	if err != nil {
		t.Fatalf("save artifact: %v", err)
	}
	if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-2", ProjectID: "project-1", Name: "data.csv",
		Kind: "csv", Content: []byte("a,b"), CreatedBy: "planner",
	}); err != nil {
		t.Fatalf("save second artifact: %v", err)
	}
	artifacts, err := store.ListArtifacts("project-1", 10, 0)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("list artifacts = %#v, %v", artifacts, err)
	}
	renamed, err := store.RenameArtifact(artifact.ID, "final.md")
	if err != nil || renamed.Name != "final.md" {
		t.Fatalf("rename artifact = %#v, %v", renamed, err)
	}
	metadata, ok, err := store.GetArtifact(artifact.ID)
	if err != nil || !ok || metadata.CurrentVersionNumber != 1 {
		t.Fatalf("artifact metadata = %#v, ok=%v, err=%v", metadata, ok, err)
	}
	if err := store.DeleteArtifact(artifact.ID); err != nil {
		t.Fatalf("delete artifact: %v", err)
	}
	if _, ok, err := store.GetArtifact(artifact.ID); err != nil || ok {
		t.Fatalf("artifact after delete = ok:%v err:%v", ok, err)
	}
}
