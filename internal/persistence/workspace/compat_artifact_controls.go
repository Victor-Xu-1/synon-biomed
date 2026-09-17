package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	CompatibilityArtifactPriorityUserStarred    = string(ArtifactPriorityUserStarred)
	CompatibilityArtifactPriorityUserHidden     = string(ArtifactPriorityUserHidden)
	CompatibilityArtifactPriorityUserNoPriority = string(ArtifactPriorityUserNoPriority)
	CompatibilityArtifactPriorityUnknown        = string(ArtifactPriorityUnknown)
)

var ErrCompatibilityArtifactPriority = errors.New("compatibility artifact priority is invalid")

type CompatibilityArtifactDeleteResult struct {
	ArtifactID      string   `json:"artifact_id"`
	VersionsDeleted int64    `json:"versions_deleted"`
	BlobPaths       []string `json:"-"`
}

type CompatibilityArtifactRenameResult struct {
	ArtifactID  string `json:"artifact_id"`
	OldFilename string `json:"old_filename"`
	NewFilename string `json:"new_filename"`
}

func (s *Store) DeleteCompatibilityArtifactRealtime(
	ctx context.Context, ownerUserID, artifactID string,
) (CompatibilityArtifactDeleteResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityArtifactDeleteResult{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, artifactID = strings.TrimSpace(ownerUserID), strings.TrimSpace(artifactID)
	if ownerUserID == "" || artifactID == "" {
		return CompatibilityArtifactDeleteResult{}, ErrCompatibilityArtifactNotFound
	}
	result := CompatibilityArtifactDeleteResult{ArtifactID: artifactID, BlobPaths: []string{}}
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `
			SELECT a.project_id, COUNT(v.id)
			FROM artifacts a
			JOIN projects p ON p.id = a.project_id AND p.user_id = ?
			LEFT JOIN artifact_versions v ON v.artifact_id = a.id
			WHERE a.id = ?
			GROUP BY a.id, a.project_id`, ownerUserID, artifactID).Scan(&projectID, &result.VersionsDeleted); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCompatibilityArtifactNotFound
			}
			return fmt.Errorf("look up compatibility artifact for delete: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `
			SELECT DISTINCT storage_path
			FROM artifact_versions
			WHERE artifact_id = ? AND COALESCE(storage_path, '') <> ''`, artifactID)
		if err != nil {
			return fmt.Errorf("list compatibility artifact blobs before delete: %w", err)
		}
		for rows.Next() {
			var path string
			if err := rows.Scan(&path); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan compatibility artifact blob before delete: %w", err)
			}
			result.BlobPaths = append(result.BlobPaths, path)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("iterate compatibility artifact blobs before delete: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close compatibility artifact blob scan: %w", err)
		}
		deleteResult, err := tx.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ? AND project_id = ?`, artifactID, projectID)
		if err != nil {
			return fmt.Errorf("delete compatibility artifact: %w", err)
		}
		if err := requireOneMutationRow(deleteResult, "artifact", artifactID); err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_deleted", artifactID,
			map[string]any{"artifact_id": artifactID})
	})
	return result, err
}

func (s *Store) RenameCompatibilityArtifactRealtime(
	ctx context.Context, ownerUserID, artifactID, filename string,
) (CompatibilityArtifactRenameResult, error) {
	if s == nil || s.db == nil {
		return CompatibilityArtifactRenameResult{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, artifactID = strings.TrimSpace(ownerUserID), strings.TrimSpace(artifactID)
	if ownerUserID == "" || artifactID == "" {
		return CompatibilityArtifactRenameResult{}, ErrCompatibilityArtifactNotFound
	}
	if filename == "" {
		return CompatibilityArtifactRenameResult{}, errors.New("filename is required")
	}
	result := CompatibilityArtifactRenameResult{ArtifactID: artifactID, NewFilename: filename}
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `
			SELECT a.project_id, a.name
			FROM artifacts a
			JOIN projects p ON p.id = a.project_id AND p.user_id = ?
			WHERE a.id = ?`, ownerUserID, artifactID).Scan(&projectID, &result.OldFilename); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCompatibilityArtifactNotFound
			}
			return fmt.Errorf("look up compatibility artifact for rename: %w", err)
		}
		updateResult, err := tx.ExecContext(ctx, `UPDATE artifacts SET name = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
			filename, s.now().UTC(), artifactID, projectID)
		if err != nil {
			return fmt.Errorf("rename compatibility artifact: %w", err)
		}
		if err := requireOneMutationRow(updateResult, "artifact", artifactID); err != nil {
			return err
		}
		return s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_renamed", artifactID,
			map[string]any{"artifact_id": artifactID, "old_filename": result.OldFilename, "new_filename": filename})
	})
	return result, err
}

func (s *Store) UpdateCompatibilityArtifactPriorityRealtime(
	ctx context.Context, ownerUserID, artifactID, priority string,
) (CompatibilityConversationArtifact, error) {
	if s == nil || s.db == nil {
		return CompatibilityConversationArtifact{}, errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ownerUserID, artifactID, priority = strings.TrimSpace(ownerUserID), strings.TrimSpace(artifactID), strings.TrimSpace(priority)
	if !validCompatibilityArtifactPriority(priority) {
		return CompatibilityConversationArtifact{}, ErrCompatibilityArtifactPriority
	}
	if ownerUserID == "" || artifactID == "" {
		return CompatibilityConversationArtifact{}, ErrCompatibilityArtifactNotFound
	}
	var artifact CompatibilityConversationArtifact
	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		var projectID string
		if err := tx.QueryRowContext(ctx, `
			SELECT a.project_id
			FROM artifacts a
			JOIN projects p ON p.id = a.project_id AND p.user_id = ?
			WHERE a.id = ?`, ownerUserID, artifactID).Scan(&projectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCompatibilityArtifactNotFound
			}
			return fmt.Errorf("look up compatibility artifact for priority update: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET priority=?, updated_at=? WHERE id=?`,
			ArtifactPriority(priority), s.now().UTC(), artifactID); err != nil {
			return fmt.Errorf("update compatibility artifact priority: %w", err)
		}
		if err := s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, projectID, "artifact_priority_update", artifactID,
			map[string]any{"artifact_id": artifactID, "priority": priority}); err != nil {
			return err
		}
		var err error
		artifact, err = s.scanCompatibilityConversationArtifact(tx.QueryRowContext(ctx,
			compatibilityConversationArtifactSelect+` WHERE project.user_id = ? AND a.id = ?`, ownerUserID, artifactID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrCompatibilityArtifactNotFound
		}
		if err != nil {
			return fmt.Errorf("read compatibility artifact after priority update: %w", err)
		}
		return nil
	})
	return artifact, err
}

func validCompatibilityArtifactPriority(priority string) bool {
	switch priority {
	case CompatibilityArtifactPriorityUserStarred,
		CompatibilityArtifactPriorityUserHidden,
		CompatibilityArtifactPriorityUserNoPriority,
		CompatibilityArtifactPriorityUnknown:
		return true
	default:
		return false
	}
}
