package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCompatibilityArtifactDownloadMetadataProjectsOptionalProvenance(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateProject(CreateProjectInput{ID: "project-1", UserID: "user-1", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	_, version, err := store.SaveArtifactVersion(SaveArtifactVersionInput{
		ArtifactID: "artifact-1", ProjectID: "project-1", Name: "report.txt",
		Kind: "text/plain", Content: []byte("evidence"), CreatedBy: "OPERON",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO artifact_runtime_metadata (artifact_id, latest_version_id, is_user_upload)
		VALUES (?, ?, 1)
		ON CONFLICT(artifact_id) DO UPDATE SET latest_version_id = excluded.latest_version_id, is_user_upload = 1`,
		"artifact-1", version.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO artifact_version_provenance
			(version_id, content_type, extracted_code, agent_name, environment_snapshot)
		VALUES (?, ?, ?, ?, ?)`, version.ID, "text/plain", "print('evidence')", "OPERON", `{"python":"3.12"}`); err != nil {
		t.Fatal(err)
	}

	for name, load := range map[string]func() (CompatibilityArtifactDownloadMetadata, bool, error){
		"current": func() (CompatibilityArtifactDownloadMetadata, bool, error) {
			return store.GetCompatibilityCurrentArtifactDownloadMetadata(context.Background(), "user-1", "artifact-1")
		},
		"version": func() (CompatibilityArtifactDownloadMetadata, bool, error) {
			return store.GetCompatibilityVersionDownloadMetadata(context.Background(), "user-1", version.ID)
		},
	} {
		t.Run(name, func(t *testing.T) {
			metadata, found, err := load()
			if err != nil || !found {
				t.Fatalf("metadata found=%v err=%v", found, err)
			}
			environment, ok := metadata.Environment.(map[string]any)
			if metadata.ArtifactID != "artifact-1" || metadata.VersionID != version.ID ||
				metadata.AgentName == nil || *metadata.AgentName != "OPERON" || !metadata.IsUserUpload ||
				metadata.Checksum == nil || *metadata.Checksum != version.ContentSHA256 ||
				metadata.ReproductionCode == nil || *metadata.ReproductionCode != "print('evidence')" ||
				!ok || environment["python"] != "3.12" {
				t.Fatalf("metadata = %#v", metadata)
			}
		})
	}
	if _, found, err := store.GetCompatibilityVersionDownloadMetadata(context.Background(), "user-2", version.ID); err != nil || found {
		t.Fatalf("foreign metadata found=%v err=%v", found, err)
	}
}
