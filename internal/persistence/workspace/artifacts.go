package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
)

const artifactSelect = `SELECT id, project_id, name, kind, current_version_number, COALESCE(folder_id, ''), priority, created_at, updated_at FROM artifacts`

func (s *Store) GetArtifact(id string) (Artifact, bool, error) {
	if s == nil || s.db == nil {
		return Artifact{}, false, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(id) == "" {
		return Artifact{}, false, errors.New("artifact id is required")
	}
	var artifact Artifact
	err := s.db.QueryRowContext(context.Background(), artifactSelect+` WHERE id = ?`, id).Scan(
		&artifact.ID, &artifact.ProjectID, &artifact.Name, &artifact.Kind,
		&artifact.CurrentVersionNumber, &artifact.FolderID, &artifact.Priority, &artifact.CreatedAt, &artifact.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Artifact{}, false, nil
	}
	if err != nil {
		return Artifact{}, false, fmt.Errorf("get artifact: %w", err)
	}
	return artifact, true, nil
}

func (s *Store) ArtifactVersionFilePath(version ArtifactVersion) (string, error) {
	if s == nil || s.db == nil {
		return "", errors.New("workspace store is closed")
	}
	if strings.TrimSpace(version.StoragePath) == "" {
		return "", errors.New("artifact version storage path is unavailable")
	}
	return s.blobAbsolute(version.StoragePath)
}

func (s *Store) ListArtifacts(projectID string, limit, offset int) ([]Artifact, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(projectID) == "" {
		return nil, errors.New("artifact project id is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	if offset < 0 {
		return nil, errors.New("artifact offset must be non-negative")
	}
	rows, err := s.db.QueryContext(context.Background(), artifactSelect+`
		WHERE project_id = ? ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, projectID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	defer rows.Close()
	artifacts := make([]Artifact, 0)
	for rows.Next() {
		var artifact Artifact
		if err := rows.Scan(&artifact.ID, &artifact.ProjectID, &artifact.Name, &artifact.Kind, &artifact.CurrentVersionNumber, &artifact.FolderID, &artifact.Priority, &artifact.CreatedAt, &artifact.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan artifact: %w", err)
		}
		artifacts = append(artifacts, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate artifacts: %w", err)
	}
	return artifacts, nil
}

func (s *Store) RenameArtifact(id, name string) (Artifact, error) {
	if s == nil || s.db == nil {
		return Artifact{}, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
		return Artifact{}, errors.New("artifact id and name are required")
	}
	result, err := s.db.ExecContext(context.Background(), `UPDATE artifacts SET name = ?, updated_at = ? WHERE id = ?`, name, s.now().UTC(), id)
	if err != nil {
		return Artifact{}, fmt.Errorf("rename artifact: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Artifact{}, fmt.Errorf("count renamed artifacts: %w", err)
	}
	if changed != 1 {
		return Artifact{}, fmt.Errorf("artifact %q does not exist", id)
	}
	artifact, ok, err := s.GetArtifact(id)
	if err != nil {
		return Artifact{}, err
	}
	if !ok {
		return Artifact{}, fmt.Errorf("artifact %q was removed during rename", id)
	}
	return artifact, nil
}

func (s *Store) DeleteArtifact(id string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if strings.TrimSpace(id) == "" {
		return errors.New("artifact id is required")
	}
	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, `SELECT storage_path FROM artifact_versions WHERE artifact_id = ? AND storage_path <> ''`, id)
	if err != nil {
		return fmt.Errorf("list artifact blobs before delete: %w", err)
	}
	paths := []string{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan artifact blob before delete: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("iterate artifact blobs before delete: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close artifact blob scan: %w", err)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM artifacts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete artifact: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted artifacts: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("artifact %q does not exist", id)
	}
	for _, path := range paths {
		absolute, err := s.blobAbsolute(path)
		if err != nil {
			return fmt.Errorf("clean deleted artifact blob: %w", err)
		}
		if err := os.Remove(absolute); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clean deleted artifact blob: %w", err)
		}
	}
	return nil
}
