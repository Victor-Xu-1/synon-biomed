package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

var ErrArtifactContentPruned = errors.New("artifact version content was pruned by working-data retention")

func (s *Store) ArtifactRetentionMode(artifactID string) (string, bool, error) {
	if s == nil || s.db == nil {
		return "", false, errors.New("workspace store is closed")
	}
	var mode string
	err := s.db.QueryRowContext(context.Background(), `SELECT retention_mode FROM artifacts WHERE id=?`, strings.TrimSpace(artifactID)).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return mode, true, nil
}

func (s *Store) SetArtifactRetentionMode(ctx context.Context, artifactID, projectID, ownerUserID, mode string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	mode = strings.TrimSpace(mode)
	if mode != "snapshot" && mode != "working_data" {
		return errors.New("artifact retention mode must be snapshot or working_data")
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE artifacts SET retention_mode=?,updated_at=?
		WHERE id=? AND project_id=? AND EXISTS (
			SELECT 1 FROM projects WHERE projects.id=artifacts.project_id AND projects.user_id=?
		)`, mode, s.now().UTC(), strings.TrimSpace(artifactID), strings.TrimSpace(projectID), strings.TrimSpace(ownerUserID))
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("artifact retention target is unavailable")
	}
	return nil
}

func (s *Store) PruneArtifactVersionContentExcept(ctx context.Context, artifactID, keepVersionID, projectID, ownerUserID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM artifact_versions v JOIN artifacts a ON a.id=v.artifact_id
		JOIN projects p ON p.id=a.project_id
		WHERE v.id=? AND a.id=? AND a.project_id=? AND p.user_id=?`,
		strings.TrimSpace(keepVersionID), strings.TrimSpace(artifactID), strings.TrimSpace(projectID), strings.TrimSpace(ownerUserID)).Scan(&count); err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, errors.New("artifact retention keep-version authority is unavailable")
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT storage_path FROM artifact_versions
		WHERE artifact_id=? AND id<>? AND content_available=1 AND storage_path<>''`, artifactID, keepVersionID)
	if err != nil {
		return nil, err
	}
	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return nil, err
		}
		paths = append(paths, path)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE artifact_versions SET content=X'',storage_path='',content_available=0
		WHERE artifact_id=? AND id<>? AND content_available=1`, artifactID, keepVersionID); err != nil {
		return nil, fmt.Errorf("prune working-data artifact versions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET retention_mode='working_data',updated_at=? WHERE id=?`, s.now().UTC(), artifactID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := s.RemoveArtifactBlobs(paths); err != nil {
		return paths, fmt.Errorf("pruned version metadata but blob cleanup is incomplete: %w", err)
	}
	return paths, nil
}

func (s *Store) ArtifactVersionContentAvailable(versionID string) (bool, bool, error) {
	if s == nil || s.db == nil {
		return false, false, errors.New("workspace store is closed")
	}
	var available bool
	err := s.db.QueryRowContext(context.Background(), `SELECT content_available FROM artifact_versions WHERE id=?`, strings.TrimSpace(versionID)).Scan(&available)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return available, err == nil, err
}
