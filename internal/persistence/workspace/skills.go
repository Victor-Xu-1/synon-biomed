package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) ListSkillPreferences(userID string) (map[string]bool, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("skill preference user id is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT skill_name, enabled FROM skill_preferences
		WHERE user_id = ? ORDER BY skill_name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list skill preferences: %w", err)
	}
	defer rows.Close()
	preferences := make(map[string]bool)
	for rows.Next() {
		var name string
		var enabled bool
		if err := rows.Scan(&name, &enabled); err != nil {
			return nil, fmt.Errorf("scan skill preference: %w", err)
		}
		preferences[name] = enabled
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skill preferences: %w", err)
	}
	return preferences, nil
}

func (s *Store) SetSkillEnabled(userID, skillName string, enabled bool) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if strings.TrimSpace(userID) == "" {
		return errors.New("skill preference user id is required")
	}
	if strings.TrimSpace(skillName) == "" {
		return errors.New("skill name is required")
	}
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO skill_preferences (user_id, skill_name, enabled, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, skill_name) DO UPDATE SET
			enabled = excluded.enabled, updated_at = excluded.updated_at`,
		userID, skillName, enabled, s.now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("set skill enabled: %w", err)
	}
	return nil
}
