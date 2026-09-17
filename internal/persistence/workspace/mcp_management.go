package workspace

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

const GlobalMCPToolGrantAgent = "*"

type UpdateMCPServerInput struct {
	Name           *string
	Description    *string
	URL            *string
	Transport      *string
	OAuthServerURL *string
	ClientID       *string
	Scopes         *string
	HeadersHelper  *string
	ConfigJSON     *string
	Builtin        *bool
	Enabled        *bool
}

func (s *Store) GetMCPServer(serverID, userID string) (MCPServer, bool, error) {
	if s == nil || s.db == nil {
		return MCPServer{}, false, errors.New("workspace store is closed")
	}
	var server MCPServer
	err := s.db.QueryRowContext(context.Background(), `
		SELECT id, user_id, name, description, url, transport, oauth_server_url,
			client_id, scopes, headers_helper, config_json, builtin, enabled, created_at, updated_at
		FROM custom_mcp_servers WHERE id = ? AND user_id = ?`,
		serverID, userID,
	).Scan(
		&server.ID, &server.UserID, &server.Name, &server.Description, &server.URL,
		&server.Transport, &server.OAuthServerURL, &server.ClientID, &server.Scopes,
		&server.HeadersHelper, &server.ConfigJSON, &server.Builtin, &server.Enabled,
		&server.CreatedAt, &server.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPServer{}, false, nil
	}
	if err != nil {
		return MCPServer{}, false, fmt.Errorf("get mcp server: %w", err)
	}
	return server, true, nil
}

func (s *Store) UpdateMCPServer(serverID, userID string, input UpdateMCPServerInput) (MCPServer, error) {
	if input.Name == nil && input.Description == nil && input.URL == nil && input.Transport == nil &&
		input.OAuthServerURL == nil && input.ClientID == nil && input.Scopes == nil &&
		input.HeadersHelper == nil && input.ConfigJSON == nil && input.Builtin == nil && input.Enabled == nil {
		return MCPServer{}, errors.New("at least one mcp server field is required")
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) == "" {
		return MCPServer{}, errors.New("mcp server name cannot be empty")
	}
	if input.URL != nil {
		if _, err := url.ParseRequestURI(*input.URL); err != nil {
			return MCPServer{}, fmt.Errorf("invalid mcp server url: %w", err)
		}
	}
	if input.Transport != nil && !validMCPTransport(*input.Transport) {
		return MCPServer{}, fmt.Errorf("unsupported mcp transport %q", *input.Transport)
	}
	if input.ConfigJSON != nil && !json.Valid([]byte(*input.ConfigJSON)) {
		return MCPServer{}, errors.New("mcp server config JSON is invalid")
	}
	result, err := s.db.ExecContext(context.Background(), `
		UPDATE custom_mcp_servers SET
			name = COALESCE(?, name), description = COALESCE(?, description),
			url = COALESCE(?, url), transport = COALESCE(?, transport),
			oauth_server_url = COALESCE(?, oauth_server_url), client_id = COALESCE(?, client_id),
			scopes = COALESCE(?, scopes), headers_helper = COALESCE(?, headers_helper),
			config_json = COALESCE(?, config_json), builtin = COALESCE(?, builtin),
			enabled = COALESCE(?, enabled),
			updated_at = ?
		WHERE id = ? AND user_id = ?`,
		input.Name, input.Description, input.URL, input.Transport, input.OAuthServerURL,
		input.ClientID, input.Scopes, input.HeadersHelper, input.ConfigJSON, input.Builtin, input.Enabled,
		s.now().UTC(), serverID, userID,
	)
	if err != nil {
		return MCPServer{}, fmt.Errorf("update mcp server: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return MCPServer{}, fmt.Errorf("count updated mcp servers: %w", err)
	}
	if changed != 1 {
		return MCPServer{}, fmt.Errorf("mcp server %q does not exist", serverID)
	}
	server, found, err := s.GetMCPServer(serverID, userID)
	if err != nil {
		return MCPServer{}, err
	}
	if !found {
		return MCPServer{}, errors.New("mcp server disappeared after update")
	}
	return server, nil
}

func (s *Store) DeleteMCPServer(serverID, userID string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(context.Background(),
		`DELETE FROM custom_mcp_servers WHERE id = ? AND user_id = ?`,
		serverID, userID,
	)
	if err != nil {
		return fmt.Errorf("delete mcp server: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("count deleted mcp servers: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("mcp server %q does not exist", serverID)
	}
	return nil
}

func (s *Store) ListMCPAssignments(serverID, userID string) ([]MCPAssignment, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT assignment.id, assignment.mcp_server_id, assignment.user_id,
			assignment.agent_name, assignment.created_at
		FROM mcp_agent_assignments AS assignment
		JOIN custom_mcp_servers AS server ON server.id = assignment.mcp_server_id
		WHERE assignment.mcp_server_id = ? AND assignment.user_id = ? AND server.user_id = ?
		ORDER BY assignment.agent_name, assignment.id`,
		serverID, userID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list mcp assignments: %w", err)
	}
	defer rows.Close()
	assignments := make([]MCPAssignment, 0)
	for rows.Next() {
		var assignment MCPAssignment
		if err := rows.Scan(
			&assignment.ID, &assignment.MCPServerID, &assignment.UserID,
			&assignment.AgentName, &assignment.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan mcp assignment: %w", err)
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mcp assignments: %w", err)
	}
	return assignments, nil
}

func (s *Store) AssignMCPServerToAllAgents(serverID, userID string) ([]MCPAssignment, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin attach mcp to all agents: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var owner string
	if err := tx.QueryRowContext(ctx,
		`SELECT user_id FROM custom_mcp_servers WHERE id = ?`, serverID,
	).Scan(&owner); err != nil {
		return nil, fmt.Errorf("look up mcp server for bulk assignment: %w", err)
	}
	if owner != userID {
		return nil, errors.New("mcp assignment user must own the mcp server")
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT name FROM user_agents WHERE user_id = ? ORDER BY name`, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list agents for mcp assignment: %w", err)
	}
	names := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan mcp assignment agent: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	for _, name := range names {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO mcp_agent_assignments (id, mcp_server_id, user_id, agent_name, created_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(mcp_server_id, user_id, agent_name) DO NOTHING`,
			uuid.NewString(), serverID, userID, name, now,
		); err != nil {
			return nil, fmt.Errorf("attach mcp server to agent %q: %w", name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit bulk mcp assignment: %w", err)
	}
	return s.ListMCPAssignments(serverID, userID)
}

func (s *Store) DetachMCPServerFromAllAgents(serverID, userID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(context.Background(), `
		DELETE FROM mcp_agent_assignments
		WHERE mcp_server_id = ? AND user_id = ?
			AND EXISTS (
				SELECT 1 FROM custom_mcp_servers
				WHERE id = mcp_agent_assignments.mcp_server_id AND user_id = ?
			)`, serverID, userID, userID)
	if err != nil {
		return 0, fmt.Errorf("detach mcp server from all agents: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count detached mcp assignments: %w", err)
	}
	return count, nil
}

func (s *Store) MCPAttachmentCountsByAgent(userID string) (map[string]int, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT agent_name, COUNT(*) FROM mcp_agent_assignments
		WHERE user_id = ? GROUP BY agent_name ORDER BY agent_name`, userID)
	if err != nil {
		return nil, fmt.Errorf("count mcp attachments by agent: %w", err)
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return nil, fmt.Errorf("scan mcp attachment count: %w", err)
		}
		counts[name] = count
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mcp attachment counts: %w", err)
	}
	return counts, nil
}

func (s *Store) ListMCPToolGrants(serverID, userID string) ([]MCPToolGrant, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT grant.id, grant.mcp_server_id, grant.user_id, grant.agent_name,
			grant.tool_name, grant.enabled, grant.created_at, grant.updated_at
		FROM mcp_tool_grants AS grant
		JOIN custom_mcp_servers AS server ON server.id = grant.mcp_server_id
		WHERE grant.mcp_server_id = ? AND grant.user_id = ? AND server.user_id = ?
		ORDER BY grant.agent_name, grant.tool_name, grant.id`,
		serverID, userID, userID,
	)
	if err != nil {
		return nil, fmt.Errorf("list mcp tool grants: %w", err)
	}
	defer rows.Close()
	grants := make([]MCPToolGrant, 0)
	for rows.Next() {
		var grant MCPToolGrant
		if err := rows.Scan(
			&grant.ID, &grant.MCPServerID, &grant.UserID, &grant.AgentName,
			&grant.ToolName, &grant.Enabled, &grant.CreatedAt, &grant.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan mcp tool grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mcp tool grants: %w", err)
	}
	return grants, nil
}

func (s *Store) ListGlobalMCPToolGrants(serverID, userID string) ([]MCPToolGrant, error) {
	grants, err := s.ListMCPToolGrants(serverID, userID)
	if err != nil {
		return nil, err
	}
	filtered := make([]MCPToolGrant, 0, len(grants))
	for _, grant := range grants {
		if grant.AgentName == GlobalMCPToolGrantAgent {
			filtered = append(filtered, grant)
		}
	}
	return filtered, nil
}

func (s *Store) DeleteGlobalMCPToolGrant(serverID, userID, toolName string) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	result, err := s.db.ExecContext(context.Background(), `
		DELETE FROM mcp_tool_grants
		WHERE mcp_server_id = ? AND user_id = ? AND agent_name = ? AND tool_name = ?
			AND EXISTS (
				SELECT 1 FROM custom_mcp_servers
				WHERE id = mcp_tool_grants.mcp_server_id AND user_id = ?
			)`, serverID, userID, GlobalMCPToolGrantAgent, toolName, userID)
	if err != nil {
		return false, fmt.Errorf("delete global MCP tool grant: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count deleted global MCP tool grants: %w", err)
	}
	return count > 0, nil
}
