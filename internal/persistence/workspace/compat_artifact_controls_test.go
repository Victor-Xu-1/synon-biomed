package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompatibilityArtifactBlobCleanupPreservesSharedReferences(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.CreateCompatibilityProject(CreateCompatibilityProjectInput{
		ID: "owned", UserID: "local", Name: "Owned",
	}); err != nil {
		t.Fatal(err)
	}
	write := func(id, content string) ArtifactVersion {
		t.Helper()
		_, version, err := store.WriteArtifactVersion(context.Background(), WriteArtifactVersionInput{
			ArtifactID: id, ProjectID: "owned", Name: id + ".txt", ContentType: "text/plain", Content: strings.NewReader(content),
		})
		if err != nil {
			t.Fatal(err)
		}
		return version
	}
	first := write("artifact-first", "shared content")
	second := write("artifact-second", "discarded private content")
	sharedAbsolute, err := store.blobAbsolute(first.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	secondAbsolute, err := store.blobAbsolute(second.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(context.Background(), `UPDATE artifact_versions SET storage_path = ? WHERE id = ?`, first.StoragePath, second.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(secondAbsolute); err != nil {
		t.Fatal(err)
	}

	firstDelete, err := store.DeleteCompatibilityArtifactRealtime(context.Background(), "local", "artifact-first")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveArtifactBlobs(firstDelete.BlobPaths); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sharedAbsolute); err != nil {
		t.Fatalf("shared blob was removed while still referenced: %v", err)
	}

	secondDelete, err := store.DeleteCompatibilityArtifactRealtime(context.Background(), "local", "artifact-second")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveArtifactBlobs(secondDelete.BlobPaths); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sharedAbsolute); !os.IsNotExist(err) {
		t.Fatalf("unreferenced shared blob still exists: %v", err)
	}
}
