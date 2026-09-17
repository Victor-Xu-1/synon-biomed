package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) ListAgents(userID string) ([]Agent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("agent user id is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, user_id, name, display_name, description, system_prompt, icon_key, color_key, tags,
			skill_names, skill_tombstones, connector_tombstones, unrestricted, enabled, created_at, updated_at
		FROM user_agents WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list agents: %w", err)
	}
	defer rows.Close()
	agents := make([]Agent, 0)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan agent: %w", err)
		}
		agents = append(agents, agent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agents: %w", err)
	}
	return agents, nil
}

func (s *Store) SetAgentEnabled(userID, name string, enabled bool) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(name) == "" {
		return errors.New("agent user id and name are required")
	}
	result, err := s.db.ExecContext(context.Background(), `UPDATE user_agents SET enabled = ?, updated_at = ? WHERE user_id = ? AND name = ?`, enabled, s.now().UTC(), userID, name)
	if err != nil {
		return fmt.Errorf("set agent enabled: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count set agent enabled: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("agent %q for user %q does not exist", name, userID)
	}
	return nil
}

func (s *Store) SetAgentEnabledProfile(userID, name string, enabled bool) (Agent, error) {
	if err := s.SetAgentEnabled(userID, name, enabled); err != nil {
		return Agent{}, err
	}
	agent, found, err := s.GetAgent(userID, name)
	if err != nil {
		return Agent{}, err
	}
	if !found {
		return Agent{}, errors.New("agent disappeared after enabled update")
	}
	return agent, nil
}
