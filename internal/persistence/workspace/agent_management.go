package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type UpdateAgentInput struct {
	Name                *string
	DisplayName         *string
	Description         *string
	SystemPrompt        *string
	IconKey             *string
	ColorKey            *string
	Tags                *[]string
	SkillNames          *[]string
	SkillTombstones     *[]string
	ConnectorTombstones *[]string
	Unrestricted        *bool
}

func (s *Store) GetAgent(userID, name string) (Agent, bool, error) {
	if s == nil || s.db == nil {
		return Agent{}, false, errors.New("workspace store is closed")
	}
	row := s.db.QueryRowContext(context.Background(), `
		SELECT id, user_id, name, display_name, description, system_prompt, icon_key, color_key, tags,
			skill_names, skill_tombstones, connector_tombstones, unrestricted, enabled, created_at, updated_at
		FROM user_agents WHERE user_id = ? AND name = ?`, userID, name)
	agent, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, false, nil
	}
	if err != nil {
		return Agent{}, false, fmt.Errorf("get agent: %w", err)
	}
	return agent, true, nil
}

func (s *Store) GetAgentByID(userID, id string) (Agent, bool, error) {
	if s == nil || s.db == nil {
		return Agent{}, false, errors.New("workspace store is closed")
	}
	row := s.db.QueryRowContext(context.Background(), `
		SELECT id, user_id, name, display_name, description, system_prompt, icon_key, color_key, tags,
			skill_names, skill_tombstones, connector_tombstones, unrestricted, enabled, created_at, updated_at
		FROM user_agents WHERE user_id = ? AND id = ?`, userID, id)
	agent, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, false, nil
	}
	if err != nil {
		return Agent{}, false, fmt.Errorf("get agent by id: %w", err)
	}
	return agent, true, nil
}

func (s *Store) UpdateAgent(userID, name string, input UpdateAgentInput) (Agent, error) {
	if s == nil || s.db == nil {
		return Agent{}, errors.New("workspace store is closed")
	}
	current, found, err := s.GetAgent(userID, name)
	if err != nil {
		return Agent{}, err
	}
	if !found {
		return Agent{}, fmt.Errorf("agent %q for user %q does not exist", name, userID)
	}
	updated := current
	if input.Name != nil {
		updated.Name = strings.TrimSpace(*input.Name)
	}
	if input.DisplayName != nil {
		updated.DisplayName = strings.TrimSpace(*input.DisplayName)
	}
	if input.Description != nil {
		updated.Description = strings.TrimSpace(*input.Description)
	}
	if input.SystemPrompt != nil {
		updated.SystemPrompt = *input.SystemPrompt
	}
	if input.IconKey != nil {
		updated.IconKey = strings.TrimSpace(*input.IconKey)
	}
	if input.ColorKey != nil {
		updated.ColorKey = strings.TrimSpace(*input.ColorKey)
	}
	if input.Tags != nil {
		updated.Tags = normalizeAgentTags(*input.Tags)
	}
	if input.SkillNames != nil {
		updated.SkillNames = normalizeSkillNames(*input.SkillNames)
	}
	if input.SkillTombstones != nil {
		updated.SkillTombstones = normalizeSkillNames(*input.SkillTombstones)
	}
	if input.ConnectorTombstones != nil {
		updated.ConnectorTombstones = normalizeConnectorIDs(*input.ConnectorTombstones)
	}
	if input.Unrestricted != nil {
		updated.Unrestricted = *input.Unrestricted
	}
	if updated.Name == "" || updated.DisplayName == "" || updated.Description == "" {
		return Agent{}, errors.New("agent name, displayName, and description are required")
	}
	tags, _ := json.Marshal(updated.Tags)
	skills, _ := json.Marshal(updated.SkillNames)
	skillTombstones, _ := json.Marshal(updated.SkillTombstones)
	connectorTombstones, _ := json.Marshal(updated.ConnectorTombstones)
	updated.UpdatedAt = s.now().UTC()

	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, fmt.Errorf("begin agent update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE user_agents SET name = ?, display_name = ?, description = ?, system_prompt = ?, icon_key = ?, color_key = ?,
			tags = ?, skill_names = ?, skill_tombstones = ?, connector_tombstones = ?, unrestricted = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		updated.Name, updated.DisplayName, updated.Description, updated.SystemPrompt, updated.IconKey, updated.ColorKey,
		string(tags), string(skills), string(skillTombstones), string(connectorTombstones), updated.Unrestricted, updated.UpdatedAt,
		updated.ID, userID)
	if err != nil {
		return Agent{}, fmt.Errorf("update agent: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return Agent{}, fmt.Errorf("agent update affected %d rows: %w", changed, err)
	}
	if updated.Name != current.Name {
		for _, statement := range []string{
			`UPDATE agent_custom_prompts SET agent_name = ? WHERE user_id = ? AND agent_name = ?`,
			`UPDATE agent_connector_exclusions SET agent_name = ? WHERE user_id = ? AND agent_name = ?`,
			`UPDATE mcp_agent_assignments SET agent_name = ? WHERE user_id = ? AND agent_name = ?`,
			`UPDATE mcp_tool_grants SET agent_name = ? WHERE user_id = ? AND agent_name = ?`,
		} {
			if _, err := tx.ExecContext(ctx, statement, updated.Name, userID, current.Name); err != nil {
				return Agent{}, fmt.Errorf("rename agent dependency: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, fmt.Errorf("commit agent update: %w", err)
	}
	return updated, nil
}

func (s *Store) DeleteAgent(userID, name string) error {
	result, err := s.db.ExecContext(context.Background(), `DELETE FROM user_agents WHERE user_id = ? AND name = ?`, userID, name)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted agents: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("agent %q for user %q does not exist", name, userID)
	}
	return nil
}

func (s *Store) SetAgentSkills(userID, name string, skillNames []string) (Agent, error) {
	normalized := normalizeSkillNames(skillNames)
	return s.UpdateAgent(userID, name, UpdateAgentInput{SkillNames: &normalized})
}

func (s *Store) UpdateAgentSkills(userID, name string, attach, detach []string) (Agent, error) {
	if s == nil || s.db == nil {
		return Agent{}, errors.New("workspace store is closed")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Agent{}, fmt.Errorf("begin agent skill update: %w", err)
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, `
		SELECT id, user_id, name, display_name, description, system_prompt, icon_key, color_key, tags,
			skill_names, skill_tombstones, connector_tombstones, unrestricted, enabled, created_at, updated_at
		FROM user_agents WHERE user_id = ? AND name = ?`, userID, name)
	agent, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, fmt.Errorf("agent %q for user %q does not exist", name, userID)
	}
	if err != nil {
		return Agent{}, fmt.Errorf("load agent for skill update: %w", err)
	}
	skills := stringSet(agent.SkillNames)
	tombstones := stringSet(agent.SkillTombstones)
	for _, skill := range normalizeSkillNames(attach) {
		delete(tombstones, skill)
		if !agent.Unrestricted {
			skills[skill] = struct{}{}
		}
	}
	for _, skill := range normalizeSkillNames(detach) {
		delete(skills, skill)
		tombstones[skill] = struct{}{}
	}
	agent.SkillNames = sortedStringSet(skills)
	agent.SkillTombstones = sortedStringSet(tombstones)
	rawSkills, _ := json.Marshal(agent.SkillNames)
	rawTombstones, _ := json.Marshal(agent.SkillTombstones)
	agent.UpdatedAt = s.now().UTC()
	if _, err := tx.ExecContext(ctx, `UPDATE user_agents SET skill_names = ?, skill_tombstones = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		string(rawSkills), string(rawTombstones), agent.UpdatedAt, agent.ID, userID); err != nil {
		return Agent{}, fmt.Errorf("persist agent skill update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Agent{}, fmt.Errorf("commit agent skill update: %w", err)
	}
	return agent, nil
}

func (s *Store) AddAgentSkill(userID, name, skillName string) (Agent, error) {
	return s.UpdateAgentSkills(userID, name, []string{skillName}, nil)
}

func (s *Store) RemoveAgentSkill(userID, name, skillName string) (Agent, error) {
	return s.UpdateAgentSkills(userID, name, nil, []string{skillName})
}

func normalizeSkillNames(values []string) []string {
	return sortedStringSet(stringSet(values))
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

func sortedStringSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
