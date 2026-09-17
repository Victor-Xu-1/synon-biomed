package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type CustomAgentPrompt struct {
	AgentName  string    `json:"agentName"`
	PromptText string    `json:"promptText"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type AgentConnectorAttachment struct {
	ServerID  string    `json:"serverId"`
	UserID    string    `json:"userId"`
	AgentName string    `json:"agentName"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"createdAt"`
}

func (s *Store) GetCustomAgentPrompt(userID, agentName string) (CustomAgentPrompt, bool, error) {
	if s == nil || s.db == nil {
		return CustomAgentPrompt{}, false, errors.New("workspace store is closed")
	}
	var prompt CustomAgentPrompt
	err := s.db.QueryRowContext(context.Background(), `
		SELECT agent_name, prompt_text, created_at, updated_at
		FROM agent_custom_prompts WHERE user_id = ? AND agent_name = ?`,
		userID, agentName,
	).Scan(&prompt.AgentName, &prompt.PromptText, &prompt.CreatedAt, &prompt.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return CustomAgentPrompt{}, false, nil
	}
	if err != nil {
		return CustomAgentPrompt{}, false, fmt.Errorf("get custom agent prompt: %w", err)
	}
	return prompt, true, nil
}

func (s *Store) UpsertCustomAgentPrompt(userID, agentName, promptText string) (CustomAgentPrompt, error) {
	if s == nil || s.db == nil {
		return CustomAgentPrompt{}, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(promptText) == "" {
		return CustomAgentPrompt{}, errors.New("prompt text is required")
	}
	now := s.now().UTC()
	if _, err := s.db.ExecContext(context.Background(), `
		INSERT INTO agent_custom_prompts (user_id, agent_name, prompt_text, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, agent_name) DO UPDATE SET
			prompt_text = excluded.prompt_text, updated_at = excluded.updated_at`,
		userID, agentName, promptText, now, now,
	); err != nil {
		return CustomAgentPrompt{}, fmt.Errorf("upsert custom agent prompt: %w", err)
	}
	prompt, found, err := s.GetCustomAgentPrompt(userID, agentName)
	if err != nil {
		return CustomAgentPrompt{}, err
	}
	if !found {
		return CustomAgentPrompt{}, errors.New("custom agent prompt disappeared after upsert")
	}
	return prompt, nil
}

func (s *Store) DeleteCustomAgentPrompt(userID, agentName string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(context.Background(),
		`DELETE FROM agent_custom_prompts WHERE user_id = ? AND agent_name = ?`,
		userID, agentName,
	)
	if err != nil {
		return fmt.Errorf("delete custom agent prompt: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted custom agent prompts: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("custom prompt for agent %q does not exist", agentName)
	}
	return nil
}

func (s *Store) ListAgentConnectors(userID, agentName string) ([]MCPServer, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if _, found, err := s.GetAgent(userID, agentName); err != nil {
		return nil, err
	} else if !found {
		return nil, fmt.Errorf("agent %q for user %q does not exist", agentName, userID)
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT server.id, server.user_id, server.name, server.description, server.url,
			server.transport, server.oauth_server_url, server.client_id, server.scopes,
			server.headers_helper, server.config_json, server.builtin, server.enabled,
			server.created_at, server.updated_at
		FROM mcp_agent_assignments AS assignment
		JOIN custom_mcp_servers AS server ON server.id = assignment.mcp_server_id
		WHERE assignment.user_id = ? AND assignment.agent_name = ?
		ORDER BY server.name, server.id`, userID, agentName)
	if err != nil {
		return nil, fmt.Errorf("list agent connectors: %w", err)
	}
	defer rows.Close()
	connectors := make([]MCPServer, 0)
	for rows.Next() {
		var connector MCPServer
		if err := rows.Scan(
			&connector.ID, &connector.UserID, &connector.Name, &connector.Description, &connector.URL,
			&connector.Transport, &connector.OAuthServerURL, &connector.ClientID, &connector.Scopes,
			&connector.HeadersHelper, &connector.ConfigJSON, &connector.Builtin, &connector.Enabled,
			&connector.CreatedAt, &connector.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agent connector: %w", err)
		}
		connectors = append(connectors, connector)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent connectors: %w", err)
	}
	return connectors, nil
}

func (s *Store) ListAgentConnectorAttachments(userID, agentName string) ([]AgentConnectorAttachment, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	agentName = strings.TrimSpace(agentName)
	if userID == "" || agentName == "" {
		return nil, errors.New("connector attachment user and agent are required")
	}
	if _, found, err := s.GetAgent(userID, agentName); err != nil {
		return nil, err
	} else if !found {
		return nil, fmt.Errorf("agent %q for user %q does not exist", agentName, userID)
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT server_id, user_id, agent_name, source, created_at
		FROM agent_connector_attachments
		WHERE user_id = ? AND agent_name = ?
		ORDER BY source, server_id`, userID, agentName)
	if err != nil {
		return nil, fmt.Errorf("list agent connector attachments: %w", err)
	}
	defer rows.Close()
	attachments := make([]AgentConnectorAttachment, 0)
	for rows.Next() {
		var attachment AgentConnectorAttachment
		if err := rows.Scan(
			&attachment.ServerID, &attachment.UserID, &attachment.AgentName,
			&attachment.Source, &attachment.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan agent connector attachment: %w", err)
		}
		if attachment.Source != "custom" && attachment.Source != "bundled" && attachment.Source != "directory" {
			return nil, fmt.Errorf("connector %q has unsupported source %q", attachment.ServerID, attachment.Source)
		}
		attachments = append(attachments, attachment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate agent connector attachments: %w", err)
	}
	return attachments, nil
}

func (s *Store) UnassignMCPServerFromAgent(userID, agentName, serverID string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(context.Background(), `
		DELETE FROM mcp_agent_assignments
		WHERE user_id = ? AND agent_name = ? AND mcp_server_id = ?`,
		userID, agentName, serverID,
	)
	if err != nil {
		return fmt.Errorf("unassign mcp server from agent: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count removed mcp assignments: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("connector %q is not attached to agent %q", serverID, agentName)
	}
	return nil
}

func (s *Store) SetAgentConnectorExclusions(userID, agentName string, connectorIDs []string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	normalized := normalizeConnectorIDs(connectorIDs)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin connector exclusion update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM user_agents WHERE user_id = ? AND name = ?`,
		userID, agentName,
	).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("agent %q for user %q does not exist", agentName, userID)
		}
		return nil, fmt.Errorf("look up connector exclusion agent: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_connector_exclusions WHERE user_id = ? AND agent_name = ?`,
		userID, agentName,
	); err != nil {
		return nil, fmt.Errorf("replace connector exclusions: %w", err)
	}
	now := s.now().UTC()
	for _, connectorID := range normalized {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_connector_exclusions (user_id, agent_name, connector_id, created_at)
			VALUES (?, ?, ?, ?)`, userID, agentName, connectorID, now); err != nil {
			return nil, fmt.Errorf("insert connector exclusion %q: %w", connectorID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit connector exclusions: %w", err)
	}
	return normalized, nil
}

func (s *Store) GetAgentConnectorExclusions(userID, agentName string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT connector_id FROM agent_connector_exclusions
		WHERE user_id = ? AND agent_name = ? ORDER BY connector_id`,
		userID, agentName,
	)
	if err != nil {
		return nil, fmt.Errorf("get connector exclusions: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan connector exclusion: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connector exclusions: %w", err)
	}
	return values, nil
}

func normalizeConnectorIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized
}

func (s *Store) DetachMCPServerFromAgent(userID, agentName, serverID string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin connector detach: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		DELETE FROM agent_connector_attachments WHERE user_id = ? AND agent_name = ? AND server_id = ?`, userID, agentName, serverID)
	if err != nil {
		return false, fmt.Errorf("detach mcp server from agent: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count detached mcp assignment: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mcp_agent_assignments WHERE user_id = ? AND agent_name = ? AND mcp_server_id = ?`, userID, agentName, serverID); err != nil {
		return false, fmt.Errorf("detach custom mcp assignment: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit connector detach: %w", err)
	}
	return changed > 0, nil
}

func (s *Store) AttachAgentConnector(userID, agentName, serverID, source string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(agentName) == "" || strings.TrimSpace(serverID) == "" {
		return errors.New("connector user, agent, and server are required")
	}
	if source != "custom" && source != "bundled" && source != "directory" {
		return fmt.Errorf("unsupported connector source %q", source)
	}
	var exists int
	if err := s.db.QueryRowContext(context.Background(), `SELECT 1 FROM user_agents WHERE user_id = ? AND name = ?`, userID, agentName).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("agent %q for user %q does not exist", agentName, userID)
		}
		return fmt.Errorf("look up connector agent: %w", err)
	}
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO agent_connector_attachments (server_id, user_id, agent_name, source, created_at)
		VALUES (?, ?, ?, ?, ?) ON CONFLICT(server_id, user_id, agent_name) DO UPDATE SET source = excluded.source`,
		serverID, userID, agentName, source, s.now().UTC())
	if err != nil {
		return fmt.Errorf("attach agent connector: %w", err)
	}
	return nil
}

func (s *Store) SetAgentConnectorTombstone(userID, agentName, serverID string, detached bool) (Agent, error) {
	agent, found, err := s.GetAgent(userID, agentName)
	if err != nil {
		return Agent{}, err
	}
	if !found {
		return Agent{}, fmt.Errorf("agent %q for user %q does not exist", agentName, userID)
	}
	tombstones := stringSet(agent.ConnectorTombstones)
	if detached {
		tombstones[serverID] = struct{}{}
	} else {
		delete(tombstones, serverID)
	}
	values := sortedStringSet(tombstones)
	return s.UpdateAgent(userID, agentName, UpdateAgentInput{ConnectorTombstones: &values})
}

func (s *Store) SetAgentConnectorToolExclusions(userID, agentName, serverID string, excludedTools []string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	normalized := normalizeOrderedStrings(excludedTools)
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin connector tool exclusion update: %w", err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT 1 FROM agent_connector_attachments WHERE server_id = ? AND user_id = ? AND agent_name = ?`,
		serverID, userID, agentName).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("connector %q is not attached to agent %q", serverID, agentName)
		}
		return nil, fmt.Errorf("look up connector attachment: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM agent_connector_tool_exclusions WHERE server_id = ? AND user_id = ? AND agent_name = ?`,
		serverID, userID, agentName); err != nil {
		return nil, fmt.Errorf("replace connector tool exclusions: %w", err)
	}
	now := s.now().UTC()
	for position, toolName := range normalized {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO agent_connector_tool_exclusions (server_id, user_id, agent_name, tool_name, position, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`, serverID, userID, agentName, toolName, position, now); err != nil {
			return nil, fmt.Errorf("insert connector tool exclusion %q: %w", toolName, err)
		}
	}
	aggregated, err := queryAgentConnectorToolExclusions(ctx, tx, userID, agentName)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit connector tool exclusions: %w", err)
	}
	return aggregated, nil
}

func (s *Store) GetAgentConnectorToolExclusions(userID, agentName string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	return queryAgentConnectorToolExclusions(context.Background(), s.db, userID, agentName)
}

func (s *Store) GetAgentConnectorToolExclusionsForConnector(userID, agentName, serverID string) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	agentName = strings.TrimSpace(agentName)
	serverID = strings.TrimSpace(serverID)
	if userID == "" || agentName == "" || serverID == "" {
		return nil, errors.New("connector tool exclusion user, agent, and server are required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT tool_name FROM agent_connector_tool_exclusions
		WHERE user_id = ? AND agent_name = ? AND server_id = ?
		ORDER BY position, tool_name`, userID, agentName, serverID)
	if err != nil {
		return nil, fmt.Errorf("query connector-specific tool exclusions: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan connector-specific tool exclusion: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connector-specific tool exclusions: %w", err)
	}
	return values, nil
}

type connectorToolExclusionQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryAgentConnectorToolExclusions(ctx context.Context, query connectorToolExclusionQuery, userID, agentName string) ([]string, error) {
	rows, err := query.QueryContext(ctx, `
		SELECT tool_name FROM agent_connector_tool_exclusions
		WHERE user_id = ? AND agent_name = ? ORDER BY position, tool_name`, userID, agentName)
	if err != nil {
		return nil, fmt.Errorf("query connector tool exclusions: %w", err)
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan connector tool exclusion: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connector tool exclusions: %w", err)
	}
	return values, nil
}

func normalizeOrderedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
