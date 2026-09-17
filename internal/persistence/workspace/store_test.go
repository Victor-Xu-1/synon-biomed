package workspace

import (
	"path/filepath"
	"testing"
)

func TestArtifactVersionsAreTransactionalAndTraceable(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	project, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Migration"})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	artifact, versionOne, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1",
		ProjectID:  project.ID,
		Name:       "plan.md",
		Kind:       "markdown",
		Content:    []byte("first"),
		CreatedBy:  "agent-1",
	})
	if err != nil {
		t.Fatalf("save first version: %v", err)
	}
	_, versionTwo, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: artifact.ID,
		ProjectID:  project.ID,
		Name:       artifact.Name,
		Kind:       artifact.Kind,
		Content:    []byte("second"),
		CreatedBy:  "agent-1",
	})
	if err != nil {
		t.Fatalf("save second version: %v", err)
	}
	if versionOne.VersionNumber != 1 || versionTwo.VersionNumber != 2 {
		t.Fatalf("version numbers = %d, %d; want 1, 2", versionOne.VersionNumber, versionTwo.VersionNumber)
	}

	lineage, err := store.ArtifactLineage(artifact.ID)
	if err != nil {
		t.Fatalf("artifact lineage: %v", err)
	}
	if len(lineage) != 2 || lineage[0].ID != versionTwo.ID || lineage[1].ID != versionOne.ID {
		t.Fatalf("unexpected lineage: %#v", lineage)
	}
}
