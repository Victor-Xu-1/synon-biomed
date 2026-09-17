package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// ArtifactCurrentPresentationState reports the visibility state of the current
// immutable version. Intermediate snapshot versions are durable drafts: they
// can be validated and resumed, but must not appear in user-facing artifact
// collections until the owning runner reaches a validated terminal result.
func (s *Store) ArtifactCurrentPresentationState(artifactID string) (retention string, intermediate bool, found bool, err error) {
	if s == nil || s.db == nil {
		return "", false, false, errors.New("workspace store is closed")
	}
	err = s.db.QueryRowContext(context.Background(), `
		SELECT a.retention_mode, COALESCE(p.is_intermediate, 0)
		FROM artifacts a
		JOIN artifact_versions v ON v.artifact_id=a.id AND v.version_number=a.current_version_number
		LEFT JOIN artifact_version_provenance p ON p.version_id=v.id
		WHERE a.id=?`, strings.TrimSpace(artifactID)).Scan(&retention, &intermediate)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, false, nil
	}
	return strings.TrimSpace(retention), intermediate, err == nil, err
}

func (s *Store) ArtifactVersionIntermediate(versionID string) (bool, bool, error) {
	if s == nil || s.db == nil {
		return false, false, errors.New("workspace store is closed")
	}
	var intermediate bool
	err := s.db.QueryRowContext(context.Background(), `
		SELECT COALESCE(is_intermediate, 0) FROM artifact_version_provenance WHERE version_id=?`,
		strings.TrimSpace(versionID),
	).Scan(&intermediate)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	return intermediate, err == nil, err
}

// PublishArtifactVersion atomically promotes only the current, owned snapshot
// draft. A superseded version can never be made visible by a late completion.
func (s *Store) PublishArtifactVersion(ctx context.Context, versionID, artifactID, projectID, ownerUserID string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	versionID = strings.TrimSpace(versionID)
	artifactID = strings.TrimSpace(artifactID)
	projectID = strings.TrimSpace(projectID)
	ownerUserID = strings.TrimSpace(ownerUserID)
	if versionID == "" || artifactID == "" || projectID == "" || ownerUserID == "" {
		return errors.New("artifact publication identity is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var intermediate bool
	err = tx.QueryRowContext(ctx, `
		SELECT COALESCE(p.is_intermediate, 0)
		FROM artifact_version_provenance p
		JOIN artifact_versions v ON v.id=p.version_id
		JOIN artifacts a ON a.id=v.artifact_id AND a.current_version_number=v.version_number
		JOIN projects project ON project.id=a.project_id
		WHERE p.version_id=? AND a.id=? AND a.project_id=? AND project.user_id=?
			AND a.retention_mode='snapshot'`,
		versionID, artifactID, projectID, ownerUserID,
	).Scan(&intermediate)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("artifact publication target is unavailable")
	}
	if err != nil {
		return err
	}
	if intermediate {
		if _, err := tx.ExecContext(ctx, `UPDATE artifact_version_provenance SET is_intermediate=0 WHERE version_id=?`, versionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE artifacts SET updated_at=? WHERE id=?`, s.now().UTC(), artifactID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
