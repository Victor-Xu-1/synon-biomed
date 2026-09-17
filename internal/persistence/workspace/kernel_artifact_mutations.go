package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const MaxKernelArtifactDeleteBatch = 200

var (
	ErrKernelArtifactNotFound      = errors.New("kernel artifact was not found")
	ErrKernelArtifactNotAgentOwned = errors.New("kernel artifact is not agent-owned")
)

type KernelArtifactDeleteSnapshot struct {
	ArtifactID   string    `json:"artifact_id"`
	Filename     string    `json:"filename"`
	ProjectID    string    `json:"-"`
	SizeBytes    int64     `json:"size_bytes"`
	VersionCount int       `json:"version_count"`
	IsUserUpload bool      `json:"is_user_upload"`
	IsBranchMint bool      `json:"is_branch_mint,omitempty"`
	IsReference  bool      `json:"is_ref"`
	CreatedAt    time.Time `json:"created_at"`
}

type KernelArtifactDeleteResult struct {
	ArtifactID      string   `json:"artifact_id"`
	Filename        string   `json:"filename"`
	Status          string   `json:"status"`
	VersionsDeleted int      `json:"versions_deleted,omitempty"`
	Error           string   `json:"error,omitempty"`
	BlobPaths       []string `json:"-"`
}

func (s *Store) InspectKernelArtifactsForDelete(
	ctx context.Context, ownerUserID, projectID string, artifactIDs []string,
) ([]KernelArtifactDeleteSnapshot, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, errors.New("workspace store is closed")
	}
	ownerUserID, projectID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID)
	if ownerUserID == "" || projectID == "" || len(artifactIDs) == 0 || len(artifactIDs) > MaxKernelArtifactDeleteBatch {
		return nil, 0, errors.New("kernel artifact owner, project, and bounded ids are required")
	}
	result := make([]KernelArtifactDeleteSnapshot, 0, len(artifactIDs))
	batchPaths := make(map[string]struct{})
	var totalBytes int64
	for _, artifactID := range artifactIDs {
		var item KernelArtifactDeleteSnapshot
		item.ArtifactID = artifactID
		err := s.db.QueryRowContext(ctx, `
			SELECT a.name,a.project_id,a.created_at,
				COALESCE(m.is_user_upload,0),COALESCE(m.is_branch_mint,0),COUNT(v.id)
			FROM artifacts a
			JOIN projects p ON p.id=a.project_id AND p.user_id=?
			LEFT JOIN artifact_runtime_metadata m ON m.artifact_id=a.id
			LEFT JOIN artifact_versions v ON v.artifact_id=a.id
			WHERE a.id=? AND a.project_id=?
			GROUP BY a.id,a.name,a.project_id,a.created_at,m.is_user_upload,m.is_branch_mint`,
			ownerUserID, artifactID, projectID,
		).Scan(&item.Filename, &item.ProjectID, &item.CreatedAt, &item.IsUserUpload, &item.IsBranchMint, &item.VersionCount)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, ErrKernelArtifactNotFound
		}
		if err != nil {
			return nil, 0, fmt.Errorf("inspect kernel artifact delete: %w", err)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT storage_path,size_bytes FROM artifact_versions WHERE artifact_id=? ORDER BY version_number`, artifactID)
		if err != nil {
			return nil, 0, fmt.Errorf("inspect kernel artifact versions: %w", err)
		}
		itemPaths := make(map[string]struct{})
		for rows.Next() {
			var path string
			var size int64
			if err := rows.Scan(&path, &size); err != nil {
				_ = rows.Close()
				return nil, 0, fmt.Errorf("scan kernel artifact version: %w", err)
			}
			path = strings.TrimSpace(path)
			if strings.HasPrefix(path, "~/") {
				item.IsReference = true
				continue
			}
			if path != "" {
				if _, counted := itemPaths[path]; counted {
					continue
				}
				itemPaths[path] = struct{}{}
			}
			item.SizeBytes += size
			if path == "" {
				totalBytes += size
			} else if _, counted := batchPaths[path]; !counted {
				batchPaths[path] = struct{}{}
				totalBytes += size
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, 0, fmt.Errorf("iterate kernel artifact versions: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, 0, fmt.Errorf("close kernel artifact versions: %w", err)
		}
		result = append(result, item)
	}
	return result, totalBytes, nil
}

func (s *Store) RenameKernelArtifact(
	ctx context.Context, ownerUserID, projectID, artifactID, filename string,
) (CompatibilityArtifactRenameResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityArtifactRenameResult{}, errors.New("workspace store is closed")
	}
	ownerUserID, projectID = strings.TrimSpace(ownerUserID), strings.TrimSpace(projectID)
	if ownerUserID == "" || projectID == "" || artifactID == "" || filename == "" {
		return CompatibilityArtifactRenameResult{}, errors.New("kernel artifact owner, project, id, and filename are required")
	}
	result := CompatibilityArtifactRenameResult{ArtifactID: artifactID, NewFilename: filename}
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var upload, branch bool
		if err := tx.QueryRowContext(ctx, `
			SELECT a.name,COALESCE(m.is_user_upload,0),COALESCE(m.is_branch_mint,0)
			FROM artifacts a JOIN projects p ON p.id=a.project_id AND p.user_id=?
			LEFT JOIN artifact_runtime_metadata m ON m.artifact_id=a.id
			WHERE a.id=? AND a.project_id=?`, ownerUserID, artifactID, projectID,
		).Scan(&result.OldFilename, &upload, &branch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrKernelArtifactNotFound
			}
			return fmt.Errorf("look up kernel artifact for rename: %w", err)
		}
		if upload || branch {
			return ErrKernelArtifactNotAgentOwned
		}
		updated, err := tx.ExecContext(ctx, `UPDATE artifacts SET name=?,updated_at=? WHERE id=? AND project_id=?`, filename, s.now().UTC(), artifactID, projectID)
		if err != nil {
			return fmt.Errorf("rename kernel artifact: %w", err)
		}
		if err := requireOneMutationRow(updated, "artifact", artifactID); err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_renamed", artifactID,
			map[string]any{"artifact_id": artifactID, "old_filename": result.OldFilename, "new_filename": filename})
	})
	return result, err
}

func (s *Store) DeleteKernelArtifactsAfterApproval(
	ctx context.Context, ownerUserID, projectID string, approved []KernelArtifactDeleteSnapshot,
) []KernelArtifactDeleteResult {
	results := make([]KernelArtifactDeleteResult, 0, len(approved))
	for _, snapshot := range approved {
		item := KernelArtifactDeleteResult{ArtifactID: snapshot.ArtifactID, Filename: snapshot.Filename}
		err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
			var currentProject, filename string
			var versionCount int
			if err := tx.QueryRowContext(ctx, `
				SELECT a.project_id,a.name,COUNT(v.id)
				FROM artifacts a JOIN projects p ON p.id=a.project_id AND p.user_id=?
				LEFT JOIN artifact_versions v ON v.artifact_id=a.id
				WHERE a.id=? GROUP BY a.id,a.project_id,a.name`, ownerUserID, snapshot.ArtifactID,
			).Scan(&currentProject, &filename, &versionCount); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					item.Status = "not_found"
					return nil
				}
				item.Status, item.Error = "skipped", "artifact could not be revalidated"
				return nil
			}
			item.Filename = filename
			if currentProject != projectID || currentProject != snapshot.ProjectID {
				item.Status, item.Error = "skipped", "artifact project changed after approval"
				return nil
			}
			if versionCount > snapshot.VersionCount {
				item.Status, item.Error = "skipped", "artifact gained a version after approval"
				return nil
			}
			rows, err := tx.QueryContext(ctx, `SELECT DISTINCT storage_path FROM artifact_versions WHERE artifact_id=? AND storage_path<>'' AND storage_path NOT LIKE '~/%'`, snapshot.ArtifactID)
			if err != nil {
				return fmt.Errorf("list kernel artifact blobs before delete: %w", err)
			}
			for rows.Next() {
				var path string
				if err := rows.Scan(&path); err != nil {
					_ = rows.Close()
					return err
				}
				item.BlobPaths = append(item.BlobPaths, path)
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			deleted, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id=? AND project_id=?`, snapshot.ArtifactID, projectID)
			if err != nil {
				return fmt.Errorf("delete kernel artifact: %w", err)
			}
			if err := requireOneMutationRow(deleted, "artifact", snapshot.ArtifactID); err != nil {
				return err
			}
			if err := s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_deleted", snapshot.ArtifactID,
				map[string]any{"artifact_id": snapshot.ArtifactID}); err != nil {
				return err
			}
			item.Status, item.VersionsDeleted = "deleted", versionCount
			return nil
		})
		if err != nil {
			item.Status, item.Error = "error", "artifact deletion failed"
			item.BlobPaths = nil
		}
		results = append(results, item)
	}
	return results
}
