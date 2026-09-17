package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) CountActiveFrames() (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	var count int
	err := s.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM frames
		WHERE status = 'processing'`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active frames: %w", err)
	}
	return count, nil
}

// HasUserRootFrameOutsideAgents reports whether a user has started a normal
// root conversation. Onboarding-only roots do not complete first-run setup.
func (s *Store) HasUserRootFrameOutsideAgents(userID string, excludedAgents []string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false, errors.New("workspace user id is required")
	}
	excluded := make(map[string]struct{}, len(excludedAgents))
	for _, agent := range excludedAgents {
		agent = strings.ToUpper(strings.TrimSpace(agent))
		if agent != "" {
			excluded[agent] = struct{}{}
		}
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT f.agent_name
		FROM frames f
		JOIN projects p ON p.id = f.project_id
		WHERE p.user_id = ? AND f.parent_frame_id IS NULL
		ORDER BY f.created_at ASC, f.id ASC`, userID)
	if err != nil {
		return false, fmt.Errorf("query user root frames: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var agent string
		if err := rows.Scan(&agent); err != nil {
			return false, fmt.Errorf("scan user root frame: %w", err)
		}
		if _, skip := excluded[strings.ToUpper(strings.TrimSpace(agent))]; !skip {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate user root frames: %w", err)
	}
	return false, nil
}
