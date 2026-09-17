package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// BulkMoveCompatibilityArtifactsRealtime applies the v1.1 public bulk-move
// contract atomically. Duplicate IDs retain v1.1's requested count while each
// artifact is moved and announced only once.
func (s *Store) BulkMoveCompatibilityArtifactsRealtime(
	ctx context.Context, ownerUserID string, artifactIDs []string, folderID string,
) (int, error) {
	ownerUserID = strings.TrimSpace(ownerUserID)
	folderID = strings.TrimSpace(folderID)
	if ownerUserID == "" {
		return 0, errors.New("owner user id is required")
	}
	if len(artifactIDs) == 0 || len(artifactIDs) > 1000 {
		return 0, errors.New("artifact bulk move requires between 1 and 1000 ids")
	}
	ids := make([]string, 0, len(artifactIDs))
	seen := make(map[string]struct{}, len(artifactIDs))
	for _, raw := range artifactIDs {
		id := strings.TrimSpace(raw)
		if id == "" {
			return 0, ErrCompatibilityArtifactNotFound
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}

	err := s.WithTransaction(ctx, func(tx *sql.Tx) error {
		folderProjectID := ""
		if folderID != "" {
			err := tx.QueryRowContext(ctx, `
				SELECT f.project_id
				FROM artifact_folders f
				JOIN projects p ON p.id = f.project_id
				WHERE f.id = ? AND p.user_id = ?`, folderID, ownerUserID).Scan(&folderProjectID)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCompatibilityFolderMoveTarget
			}
			if err != nil {
				return fmt.Errorf("look up compatibility bulk-move folder: %w", err)
			}
		}
		for _, id := range ids {
			artifact, err := scanArtifactTx(tx.QueryRowContext(ctx, artifactSelect+`
				WHERE id = ? AND project_id IN (SELECT id FROM projects WHERE user_id = ?)`, id, ownerUserID))
			if errors.Is(err, sql.ErrNoRows) {
				return ErrCompatibilityArtifactNotFound
			}
			if err != nil {
				return fmt.Errorf("look up compatibility bulk-move artifact: %w", err)
			}
			if folderProjectID != "" && folderProjectID != artifact.ProjectID {
				return ErrCompatibilityFolderMoveTarget
			}
			oldFolderID := artifact.FolderID
			if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET folder_id = ?, updated_at = ? WHERE id = ?`,
				nullableString(folderID), s.now().UTC(), id); err != nil {
				return fmt.Errorf("move compatibility artifact %q: %w", id, err)
			}
			if err := s.enqueueProjectDomainEventTx(ctx, tx, ownerUserID, artifact.ProjectID,
				"artifact_moved", artifact.ID, map[string]any{
					"artifact_id":   artifact.ID,
					"old_folder_id": nullablePayloadString(oldFolderID),
					"new_folder_id": nullablePayloadString(folderID),
				}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return len(artifactIDs), nil
}
