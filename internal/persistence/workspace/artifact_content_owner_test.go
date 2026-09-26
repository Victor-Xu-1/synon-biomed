package workspace

import (
	"io"
	"path/filepath"
	"testing"
)

func TestOpenArtifactVersionContentForOwnerScopesProjectOwnership(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, project := range []CreateProjectInput{
		{ID: "project-owner-a", UserID: "owner-a", Name: "Owner A"},
		{ID: "project-owner-b", UserID: "owner-b", Name: "Owner B"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	artifact, version, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-owner-a", ProjectID: "project-owner-a", Name: "archive.bin",
		Kind: "application/octet-stream", Content: []byte("verified-content"), CreatedBy: "runner-a",
	})
	if err != nil {
		t.Fatal(err)
	}

	openedArtifact, openedVersion, content, found, err := store.OpenArtifactVersionContentForOwner(version.ID, "owner-a")
	if err != nil || !found {
		t.Fatalf("owner-scoped content found=%t err=%v", found, err)
	}
	defer content.Close()
	bytesRead, err := io.ReadAll(content)
	if err != nil || string(bytesRead) != "verified-content" || openedArtifact.ID != artifact.ID || openedVersion.ID != version.ID {
		t.Fatalf("artifact=%#v version=%#v content=%q err=%v", openedArtifact, openedVersion, bytesRead, err)
	}

	foreignArtifact, foreignVersion, foreignContent, found, err := store.OpenArtifactVersionContentForOwner(version.ID, "owner-b")
	if err != nil || found || foreignContent != nil || foreignArtifact.ID != "" || foreignVersion.ID != "" {
		t.Fatalf("foreign read artifact=%#v version=%#v content=%v found=%t err=%v", foreignArtifact, foreignVersion, foreignContent, found, err)
	}
}
