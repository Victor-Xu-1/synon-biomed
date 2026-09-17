package workspace

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
)

func TestWorkingDataRetentionKeepsLatestContentAndPreservesPrunedLineage(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project", UserID: "owner", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, first, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "working", ProjectID: "project", Name: "working.csv", Kind: "file", Content: []byte("v1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, second, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "working", ProjectID: "project", Name: "working.csv", Kind: "file", Content: []byte("v2"), ParentVersionID: first.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetArtifactRetentionMode(context.Background(), "working", "project", "owner", "working_data"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PruneArtifactVersionContentExcept(context.Background(), "working", second.ID, "project", "owner"); err != nil {
		t.Fatal(err)
	}
	if _, _, reader, found, err := store.OpenArtifactVersionContent(first.ID); !errors.Is(err, ErrArtifactContentPruned) || found || reader != nil {
		t.Fatalf("pruned content found=%t reader=%#v err=%v", found, reader, err)
	}
	_, _, reader, found, err := store.OpenArtifactVersionContent(second.ID)
	if err != nil || !found {
		t.Fatalf("latest content found=%t err=%v", found, err)
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || string(content) != "v2" {
		t.Fatalf("latest content=%q read=%v close=%v", content, readErr, closeErr)
	}
	lineage, err := store.ArtifactLineage("working")
	if err != nil || len(lineage) != 2 || lineage[1].ID != first.ID {
		t.Fatalf("lineage=%#v err=%v", lineage, err)
	}
	mode, found, err := store.ArtifactRetentionMode("working")
	if err != nil || !found || mode != "working_data" {
		t.Fatalf("retention=%q found=%t err=%v", mode, found, err)
	}
}
