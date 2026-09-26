package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestArtifactVersionStreamPersistsOutsideSQLiteAndCleansUpOnDelete(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("streamed-artifact\n"), 8192)
	artifact, version, err := store.SaveArtifactVersionFromReader(context.Background(), SaveArtifactVersionReaderInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "cloud.txt",
		Kind: "text/plain", Content: bytes.NewReader(payload), CreatedBy: "cloud-import",
	})
	if err != nil {
		t.Fatal(err)
	}
	if version.StoragePath == "" || version.SizeBytes != int64(len(payload)) || len(version.Content) != 0 {
		t.Fatalf("unexpected streamed version: %+v", version)
	}
	absolute, err := store.blobAbsolute(version.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(absolute); err != nil || info.Size() != int64(len(payload)) {
		t.Fatalf("streamed blob not installed: info=%v err=%v", info, err)
	}

	gotArtifact, gotVersion, reader, found, err := store.OpenCurrentArtifactContent(artifact.ID)
	if err != nil || !found {
		t.Fatalf("open current artifact: found=%v err=%v", found, err)
	}
	defer reader.Close()
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if gotArtifact.ID != artifact.ID || gotVersion.ID != version.ID || !bytes.Equal(got, payload) {
		t.Fatal("streamed artifact round-trip mismatch")
	}

	if err := store.DeleteArtifact(artifact.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absolute); !os.IsNotExist(err) {
		t.Fatalf("external artifact blob survived delete: %v", err)
	}
}

func TestArtifactVersionStreamFailureDoesNotAdvanceArtifact(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	failing := &errorAfterReader{remaining: 8}
	if _, _, err := store.SaveArtifactVersionFromReader(context.Background(), SaveArtifactVersionReaderInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "failed.bin",
		Kind: "application/octet-stream", Content: failing,
	}); err == nil {
		t.Fatal("expected stream failure")
	}
	if _, found, err := store.GetArtifact("artifact-1"); err != nil || found {
		t.Fatalf("failed stream created artifact: found=%v err=%v", found, err)
	}
}

func TestArtifactVersionStreamEnforcesTypedByteLimit(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.SaveArtifactVersionFromReader(context.Background(), SaveArtifactVersionReaderInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "oversized.bin",
		Kind: "application/octet-stream", Content: strings.NewReader("seventeen bytes!!"), MaxBytes: 16,
	})
	var tooLarge *ArtifactContentTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 16 {
		t.Fatalf("error=%T %v", err, err)
	}
	if _, found, err := store.GetArtifact("artifact-1"); err != nil || found {
		t.Fatalf("oversized stream created artifact: found=%v err=%v", found, err)
	}
	staged, err := os.ReadDir(filepath.Join(store.blobRoot, "staging"))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Fatalf("oversized stream left staging files=%#v", staged)
	}
}

func TestArtifactBlobRecoveryRemovesCrashOrphansAndKeepsReferences(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "workspace.db")
	store, err := Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.SaveArtifactVersionFromReader(context.Background(), SaveArtifactVersionReaderInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "kept.bin",
		Kind: "application/octet-stream", Content: strings.NewReader("kept"),
	})
	if err != nil {
		t.Fatal(err)
	}
	referenced, err := store.blobAbsolute(version.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	orphanRelative := artifactVersionBlobPath("orphan-version")
	orphan, err := store.blobAbsolute(orphanRelative)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(orphan), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("orphaned secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	staging := filepath.Join(store.blobRoot, "staging", ".artifact-part-crash")
	if err := os.WriteFile(staging, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := os.Stat(referenced); err != nil {
		t.Fatalf("referenced blob was removed: %v", err)
	}
	for _, path := range []string{orphan, staging} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("crash orphan survived recovery: %s err=%v", path, err)
		}
	}
}

func TestArtifactBlobRecoveryMarksMissingReferencesUnavailable(t *testing.T) {
	root := t.TempDir()
	database := filepath.Join(root, "workspace.db")
	store, err := Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.SaveArtifactVersionFromReader(context.Background(), SaveArtifactVersionReaderInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "missing.bin",
		Kind: "application/octet-stream", Content: strings.NewReader("recoverable metadata"),
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := store.blobAbsolute(version.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blob); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(database)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	available, found, err := reopened.ArtifactVersionContentAvailable(version.ID)
	if err != nil || !found || available {
		t.Fatalf("missing blob availability=%t found=%t err=%v", available, found, err)
	}
	_, metadata, found, err := reopened.GetArtifactVersionMetadata(version.ID)
	if err != nil || !found || metadata.ContentSHA256 != version.ContentSHA256 || metadata.SizeBytes != version.SizeBytes || metadata.StoragePath != version.StoragePath {
		t.Fatalf("missing blob metadata was not preserved: found=%t metadata=%+v err=%v", found, metadata, err)
	}
	if _, _, _, found, err := reopened.OpenArtifactVersionContent(version.ID); !errors.Is(err, ErrArtifactContentPruned) || found {
		t.Fatalf("missing blob did not fail closed: found=%t err=%v", found, err)
	}
}

func TestDeleteProjectCleansExternalArtifactBlobs(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.SaveArtifactVersionFromReader(context.Background(), SaveArtifactVersionReaderInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "blob.bin",
		Kind: "application/octet-stream", Content: strings.NewReader("payload"),
	})
	if err != nil {
		t.Fatal(err)
	}
	absolute, err := store.blobAbsolute(version.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteProject("project-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(absolute); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("project delete left external artifact blob: %v", err)
	}
}

type errorAfterReader struct {
	remaining int
}

func (r *errorAfterReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	if len(p) > r.remaining {
		p = p[:r.remaining]
	}
	for index := range p {
		p[index] = 'x'
	}
	r.remaining -= len(p)
	return len(p), nil
}
