package workspace

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestKernelArtifactRenameUsesOwnerProjectAndAgentProvenance(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, project := range []CreateProjectInput{
		{ID: "project-a", UserID: "owner-a", Name: "A"},
		{ID: "project-b", UserID: "owner-a", Name: "B"},
	} {
		if _, err := store.CreateProject(project); err != nil {
			t.Fatal(err)
		}
	}
	write := func(id, project string) {
		t.Helper()
		if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
			ArtifactID: id, ProjectID: project, Name: id + ".txt", Kind: "text", Content: []byte(id),
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("agent", "project-a")
	write("upload", "project-a")
	write("branch", "project-a")
	write("other-project", "project-b")
	if _, err := store.db.Exec(`INSERT INTO artifact_runtime_metadata(artifact_id,is_user_upload) VALUES('upload',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifact_runtime_metadata(artifact_id,is_branch_mint) VALUES('branch',1)`); err != nil {
		t.Fatal(err)
	}

	result, err := store.RenameKernelArtifact(context.Background(), "owner-a", "project-a", "agent", "renamed.txt")
	if err != nil || result.OldFilename != "agent.txt" || result.NewFilename != "renamed.txt" {
		t.Fatalf("rename=%#v err=%v", result, err)
	}
	for _, id := range []string{"upload", "branch"} {
		if _, err := store.RenameKernelArtifact(context.Background(), "owner-a", "project-a", id, "blocked.txt"); !errors.Is(err, ErrKernelArtifactNotAgentOwned) {
			t.Fatalf("%s rename error=%v", id, err)
		}
	}
	if _, err := store.RenameKernelArtifact(context.Background(), "owner-a", "project-a", "other-project", "stolen.txt"); !errors.Is(err, ErrKernelArtifactNotFound) {
		t.Fatalf("cross-project rename error=%v", err)
	}
}

func TestKernelArtifactDeleteSnapshotsAndRevalidatesEachArtifact(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	write := func(id string) {
		t.Helper()
		if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
			ArtifactID: id, ProjectID: "project-a", Name: id + ".txt", Kind: "text", Content: []byte(id),
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("grows")
	write("deletes")
	write("vanishes")
	snapshots, _, err := store.InspectKernelArtifactsForDelete(context.Background(), "owner-a", "project-a", []string{"grows", "deletes", "vanishes"})
	if err != nil || len(snapshots) != 3 || snapshots[0].VersionCount != 1 {
		t.Fatalf("snapshots=%#v err=%v", snapshots, err)
	}
	if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "grows", ProjectID: "project-a", Name: "grows.txt", Kind: "text", Content: []byte("new"),
	}); err != nil {
		t.Fatal(err)
	}
	paths, err := store.DeleteArtifactRealtime(context.Background(), "vanishes", "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveArtifactBlobs(paths); err != nil {
		t.Fatal(err)
	}
	results := store.DeleteKernelArtifactsAfterApproval(context.Background(), "owner-a", "project-a", snapshots)
	if len(results) != 3 || results[0].Status != "skipped" || results[1].Status != "deleted" || results[1].VersionsDeleted != 1 || results[2].Status != "not_found" {
		t.Fatalf("delete results=%#v", results)
	}
	if _, found, err := store.GetArtifact("grows"); err != nil || !found {
		t.Fatalf("growing artifact found=%t err=%v", found, err)
	}
	if _, found, err := store.GetArtifact("deletes"); err != nil || found {
		t.Fatalf("deleted artifact found=%t err=%v", found, err)
	}
}

func TestInspectKernelArtifactsForDeleteFailsWholeBatch(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.SaveArtifactVersion(SaveArtifactVersionInput{ArtifactID: "present", ProjectID: "project-a", Name: "present", Kind: "text", Content: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.InspectKernelArtifactsForDelete(context.Background(), "owner-a", "project-a", []string{"present", "missing"}); !errors.Is(err, ErrKernelArtifactNotFound) {
		t.Fatalf("batch preflight error=%v", err)
	}
	if _, found, err := store.GetArtifact("present"); err != nil || !found {
		t.Fatalf("preflight mutated present artifact found=%t err=%v", found, err)
	}
}

func TestKernelArtifactDeleteApprovalSizesDeduplicateOnlyTheBatchTotal(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-a", UserID: "owner-a", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"shared-a", "shared-b", "reference"} {
		_, version, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
			ArtifactID: id, ProjectID: "project-a", Name: id, Kind: "text", Content: []byte("0123456789"),
		})
		if err != nil {
			t.Fatal(err)
		}
		path := "shared/blob"
		if id == "reference" {
			path = "~/user-owned/reference.txt"
		}
		if _, err := store.db.Exec(`UPDATE artifact_versions SET storage_path=?,size_bytes=10 WHERE id=?`, path, version.ID); err != nil {
			t.Fatal(err)
		}
	}
	snapshots, total, err := store.InspectKernelArtifactsForDelete(
		context.Background(), "owner-a", "project-a", []string{"shared-a", "shared-b", "reference"},
	)
	if err != nil || len(snapshots) != 3 || snapshots[0].SizeBytes != 10 || snapshots[1].SizeBytes != 10 || snapshots[2].SizeBytes != 0 || !snapshots[2].IsReference || total != 10 {
		t.Fatalf("snapshots=%#v total=%d err=%v", snapshots, total, err)
	}
	results := store.DeleteKernelArtifactsAfterApproval(context.Background(), "owner-a", "project-a", snapshots[2:])
	if len(results) != 1 || results[0].Status != "deleted" || len(results[0].BlobPaths) != 0 {
		t.Fatalf("reference delete=%#v", results)
	}
}
