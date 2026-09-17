package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type MCPConnectorToolPolicy struct {
	UserID      string    `json:"userId"`
	ConnectorID string    `json:"connectorId"`
	ToolName    string    `json:"toolName"`
	Enabled     bool      `json:"enabled"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func (s *Store) ListMCPConnectorToolPolicies(userID, connectorID string) ([]MCPConnectorToolPolicy, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(connectorID) == "" {
		return nil, errors.New("MCP connector policy user and connector are required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT user_id, connector_id, tool_name, enabled, updated_at
		FROM mcp_connector_tool_policies
		WHERE user_id = ? AND connector_id = ?
		ORDER BY tool_name`, userID, connectorID)
	if err != nil {
		return nil, fmt.Errorf("list MCP connector tool policies: %w", err)
	}
	defer rows.Close()
	policies := make([]MCPConnectorToolPolicy, 0)
	for rows.Next() {
		var policy MCPConnectorToolPolicy
		if err := rows.Scan(&policy.UserID, &policy.ConnectorID, &policy.ToolName, &policy.Enabled, &policy.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan MCP connector tool policy: %w", err)
		}
		policies = append(policies, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MCP connector tool policies: %w", err)
	}
	return policies, nil
}

func (s *Store) ListAllMCPConnectorToolPolicies(userID string) ([]MCPConnectorToolPolicy, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("MCP connector policy user is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT user_id, connector_id, tool_name, enabled, updated_at
		FROM mcp_connector_tool_policies WHERE user_id = ?
		ORDER BY connector_id, tool_name`, userID)
	if err != nil {
		return nil, fmt.Errorf("list all MCP connector tool policies: %w", err)
	}
	defer rows.Close()
	policies := make([]MCPConnectorToolPolicy, 0)
	for rows.Next() {
		var policy MCPConnectorToolPolicy
		if err := rows.Scan(&policy.UserID, &policy.ConnectorID, &policy.ToolName, &policy.Enabled, &policy.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan MCP connector tool policy: %w", err)
		}
		policies = append(policies, policy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MCP connector tool policies: %w", err)
	}
	return policies, nil
}

func (s *Store) SetMCPConnectorToolPolicy(userID, connectorID, toolName string, enabled *bool) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	connectorID = strings.TrimSpace(connectorID)
	toolName = strings.TrimSpace(toolName)
	if userID == "" || connectorID == "" || toolName == "" {
		return false, errors.New("MCP connector policy user, connector, and tool are required")
	}
	if len(connectorID) > 512 || len(toolName) > 512 {
		return false, errors.New("MCP connector policy identifier exceeds 512 bytes")
	}
	if enabled == nil {
		result, err := s.db.ExecContext(context.Background(), `
			DELETE FROM mcp_connector_tool_policies
			WHERE user_id = ? AND connector_id = ? AND tool_name = ?`, userID, connectorID, toolName)
		if err != nil {
			return false, fmt.Errorf("delete MCP connector tool policy: %w", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return false, fmt.Errorf("count deleted MCP connector tool policies: %w", err)
		}
		return count > 0, nil
	}
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO mcp_connector_tool_policies (user_id, connector_id, tool_name, enabled, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_id, connector_id, tool_name) DO UPDATE SET
			enabled = excluded.enabled, updated_at = excluded.updated_at`,
		userID, connectorID, toolName, *enabled, s.now().UTC())
	if err != nil {
		return false, fmt.Errorf("set MCP connector tool policy: %w", err)
	}
	return true, nil
}
