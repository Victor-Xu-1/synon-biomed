package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestCompatibilityProjectCurrentArtifactPageHasStableCompleteKeyset(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.CreateCompatibilityProject(CreateCompatibilityProjectInput{
		ID: "project-page", UserID: "owner-page", Name: "Paged project",
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	seedCompatibilityProjectArtifacts(t, store, "project-page", "root-page", 1_005)

	ctx := context.Background()
	var cursor *CompatibilityProjectArtifactCursor
	seen := make(map[string]struct{}, 1_005)
	for pageNumber := 0; ; pageNumber++ {
		page, err := store.ListCompatibilityProjectCurrentArtifactPage(ctx, CompatibilityProjectArtifactPageInput{
			OwnerUserID: "owner-page", ProjectID: "project-page", ExcludeIntermediate: true,
			Limit: 128, Cursor: cursor,
		})
		if err != nil {
			t.Fatalf("page %d: %v", pageNumber, err)
		}
		if page.Total != 1_005 {
			t.Fatalf("page %d total=%d", pageNumber, page.Total)
		}
		for _, artifact := range page.Artifacts {
			if _, duplicate := seen[artifact.ID]; duplicate {
				t.Fatalf("duplicate artifact %q", artifact.ID)
			}
			seen[artifact.ID] = struct{}{}
		}
		if !page.HasMore {
			if page.NextCursor != nil {
				t.Fatalf("terminal page has cursor %#v", page.NextCursor)
			}
			break
		}
		if page.NextCursor == nil {
			t.Fatalf("page %d missing cursor", pageNumber)
		}
		cursor = page.NextCursor
	}
	if len(seen) != 1_005 {
		t.Fatalf("seen=%d", len(seen))
	}

	search, err := store.ListCompatibilityProjectCurrentArtifactPage(ctx, CompatibilityProjectArtifactPageInput{
		OwnerUserID: "owner-page", ProjectID: "project-page", ExcludeIntermediate: true,
		Search: "needle-1004", Limit: 10,
	})
	if err != nil {
		t.Fatalf("search old artifact: %v", err)
	}
	if search.Total != 1 || len(search.Artifacts) != 1 || search.Artifacts[0].Filename != "needle-1004.dat" {
		t.Fatalf("search page=%#v", search)
	}
}

func TestCompatibilityProjectCurrentArtifactPageExcludesInternalToolResults(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, _, err := store.CreateCompatibilityProject(CreateCompatibilityProjectInput{
		ID: "project-internal", UserID: "owner-internal", Name: "Internal filter project",
	}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	seedCompatibilityProjectArtifacts(t, store, "project-internal", "root-internal", 5)

	now := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < 3; index++ {
		artifactID := fmt.Sprintf("large-tool-result-%032d", index)
		versionID := fmt.Sprintf("internal-version-%d", index)
		if _, err := store.db.Exec(`INSERT INTO artifacts
			(id, project_id, name, kind, current_version_number, created_at, updated_at)
			VALUES (?, ?, ?, 'application/json', 1, ?, ?)`,
			artifactID, "project-internal", fmt.Sprintf("tool-result-%032x.json", index), now, now); err != nil {
			t.Fatalf("insert internal artifact %d: %v", index, err)
		}
		if _, err := store.db.Exec(`INSERT INTO artifact_versions
			(id, artifact_id, version_number, content, content_sha256, storage_path, size_bytes, created_by, created_at)
			VALUES (?, ?, 1, X'', ?, '', 1, 'runner', ?)`, versionID, artifactID, versionID, now); err != nil {
			t.Fatalf("insert internal version %d: %v", index, err)
		}
		if _, err := store.db.Exec(`INSERT INTO artifact_runtime_metadata
			(artifact_id, root_frame_id, frame_id, latest_version_id)
			VALUES (?, ?, ?, ?)`, artifactID, "root-internal", "root-internal", versionID); err != nil {
			t.Fatalf("insert internal metadata %d: %v", index, err)
		}
		if _, err := store.db.Exec(`INSERT INTO artifact_version_provenance
			(version_id, frame_id, content_type, is_intermediate)
			VALUES (?, ?, 'application/json', 0)`, versionID, "root-internal"); err != nil {
			t.Fatalf("insert internal provenance %d: %v", index, err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO artifacts
		(id, project_id, name, kind, current_version_number, retention_mode, created_at, updated_at)
		VALUES ('internal-working-data', 'project-internal', 'validation_record.json', 'application/json', 1, 'working_data', ?, ?)`, now, now); err != nil {
		t.Fatalf("insert working-data artifact: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifact_versions
		(id, artifact_id, version_number, content, content_sha256, storage_path, size_bytes, created_by, created_at)
		VALUES ('internal-working-version', 'internal-working-data', 1, X'7B7D', 'working-sha', '', 2, 'runner', ?)`, now); err != nil {
		t.Fatalf("insert working-data version: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifact_runtime_metadata
		(artifact_id, root_frame_id, frame_id, latest_version_id)
		VALUES ('internal-working-data', 'root-internal', 'root-internal', 'internal-working-version')`); err != nil {
		t.Fatalf("insert working-data metadata: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO artifact_version_provenance
		(version_id, frame_id, content_type, is_intermediate)
		VALUES ('internal-working-version', 'root-internal', 'application/json', 0)`); err != nil {
		t.Fatalf("insert working-data provenance: %v", err)
	}

	ctx := context.Background()
	all, err := store.ListCompatibilityProjectCurrentArtifactPage(ctx, CompatibilityProjectArtifactPageInput{
		OwnerUserID: "owner-internal", ProjectID: "project-internal", ExcludeIntermediate: true, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list all artifacts: %v", err)
	}
	if all.Total != 9 || len(all.Artifacts) != 9 {
		t.Fatalf("all page total=%d items=%d", all.Total, len(all.Artifacts))
	}
	visible, err := store.ListCompatibilityProjectCurrentArtifactPage(ctx, CompatibilityProjectArtifactPageInput{
		OwnerUserID: "owner-internal", ProjectID: "project-internal",
		ExcludeIntermediate: true, ExcludeInternal: true, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list visible artifacts: %v", err)
	}
	if visible.Total != 5 || len(visible.Artifacts) != 5 {
		t.Fatalf("visible page total=%d items=%d", visible.Total, len(visible.Artifacts))
	}
	for _, artifact := range visible.Artifacts {
		if len(artifact.ID) >= len("large-tool-result-") && artifact.ID[:len("large-tool-result-")] == "large-tool-result-" {
			t.Fatalf("internal tool-result artifact %q leaked into visible page", artifact.ID)
		}
	}
}

func seedCompatibilityProjectArtifacts(t *testing.T, store *Store, projectID, rootFrameID string, count int) {
	t.Helper()
	tx, err := store.db.Begin()
	if err != nil {
		t.Fatalf("begin seed: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for index := 0; index < count; index++ {
		artifactID := fmt.Sprintf("artifact-%06d", index)
		versionID := fmt.Sprintf("version-%06d", index)
		filename := fmt.Sprintf("result-%06d.dat", index)
		if index == count-1 {
			filename = fmt.Sprintf("needle-%d.dat", index)
		}
		createdAt := base.Add(time.Duration(index) * time.Second)
		if _, err := tx.Exec(`INSERT INTO artifacts
			(id, project_id, name, kind, current_version_number, created_at, updated_at)
			VALUES (?, ?, ?, 'application/octet-stream', 1, ?, ?)`, artifactID, projectID, filename, createdAt, createdAt); err != nil {
			t.Fatalf("insert artifact %d: %v", index, err)
		}
		if _, err := tx.Exec(`INSERT INTO artifact_versions
			(id, artifact_id, version_number, content, content_sha256, storage_path, size_bytes, created_by, created_at)
			VALUES (?, ?, 1, X'', ?, '', 0, 'test', ?)`, versionID, artifactID, versionID, createdAt); err != nil {
			t.Fatalf("insert version %d: %v", index, err)
		}
		if _, err := tx.Exec(`INSERT INTO artifact_runtime_metadata
			(artifact_id, root_frame_id, frame_id, latest_version_id)
			VALUES (?, ?, ?, ?)`, artifactID, rootFrameID, rootFrameID, versionID); err != nil {
			t.Fatalf("insert metadata %d: %v", index, err)
		}
		if _, err := tx.Exec(`INSERT INTO artifact_version_provenance
			(version_id, frame_id, content_type, is_intermediate)
			VALUES (?, ?, 'application/octet-stream', 0)`, versionID, rootFrameID); err != nil {
			t.Fatalf("insert provenance %d: %v", index, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
}
