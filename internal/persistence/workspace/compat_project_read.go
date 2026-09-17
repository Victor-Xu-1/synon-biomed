package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

func (s *Store) GetCompatibilityProject(userID, projectID string) (CompatibilityProject, bool, error) {
	if s == nil || s.db == nil {
		return CompatibilityProject{}, false, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	projectID = strings.TrimSpace(projectID)
	if userID == "" || projectID == "" {
		return CompatibilityProject{}, false, errors.New("project user id and project id are required")
	}
	project, err := scanCompatibilityProject(s.db.QueryRowContext(
		context.Background(), compatibilityProjectSelect+" WHERE p.user_id = ? AND p.id = ?",
		userID, projectID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return CompatibilityProject{}, false, nil
	}
	if err != nil {
		return CompatibilityProject{}, false, fmt.Errorf("get compatibility project: %w", err)
	}
	return project, true, nil
}

func (s *Store) CountCompatibilityProjects(userID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0, errors.New("project user id is required")
	}
	var total int
	if err := s.db.QueryRowContext(
		context.Background(), "SELECT COUNT(*) FROM projects WHERE user_id = ?", userID,
	).Scan(&total); err != nil {
		return 0, fmt.Errorf("count compatibility projects: %w", err)
	}
	return total, nil
}

func (s *Store) CompatibilityProjectLastActiveAt(userID, projectID string) (*time.Time, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	var value time.Time
	err := s.db.QueryRowContext(context.Background(), `
		SELECT f.updated_at
		FROM projects p JOIN frames f ON f.project_id = p.id
		LEFT JOIN frame_runtime_metadata m ON m.frame_id = f.id
		WHERE p.user_id = ? AND p.id = ? AND COALESCE(m.is_hidden, 0) = 0
		ORDER BY f.updated_at DESC, f.id DESC LIMIT 1`,
		strings.TrimSpace(userID), strings.TrimSpace(projectID)).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get compatibility project activity: %w", err)
	}
	result := value.UTC()
	return &result, nil
}
