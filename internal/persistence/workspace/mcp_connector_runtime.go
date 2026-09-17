package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type MCPConnectorRuntimeState struct {
	UserID, Source, ConnectorID string
	Enabled                     bool
	LastStatus, LastError       string
	ToolCount                   int
	SchemaSHA256                string
	UpdatedAt                   time.Time
}

const mcpConnectorRuntimeSelect = `SELECT user_id, source, connector_id, enabled, last_status, last_error, tool_count, schema_sha256, updated_at FROM mcp_connector_runtime_state`

func (s *Store) ListMCPConnectorRuntimeStates(userID string) ([]MCPConnectorRuntimeState, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	rows, err := s.db.QueryContext(context.Background(), mcpConnectorRuntimeSelect+` WHERE user_id = ? ORDER BY source, connector_id`, strings.TrimSpace(userID))
	if err != nil {
		return nil, fmt.Errorf("list MCP connector runtime states: %w", err)
	}
	defer rows.Close()
	var states []MCPConnectorRuntimeState
	for rows.Next() {
		var x MCPConnectorRuntimeState
		if err := rows.Scan(&x.UserID, &x.Source, &x.ConnectorID, &x.Enabled, &x.LastStatus, &x.LastError, &x.ToolCount, &x.SchemaSHA256, &x.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan MCP connector runtime state: %w", err)
		}
		states = append(states, x)
	}
	return states, rows.Err()
}

func (s *Store) GetMCPConnectorRuntimeState(userID, source, connectorID string) (MCPConnectorRuntimeState, bool, error) {
	if s == nil || s.db == nil {
		return MCPConnectorRuntimeState{}, false, errors.New("workspace store is closed")
	}
	var x MCPConnectorRuntimeState
	err := s.db.QueryRowContext(context.Background(), mcpConnectorRuntimeSelect+` WHERE user_id = ? AND source = ? AND connector_id = ?`, strings.TrimSpace(userID), strings.TrimSpace(source), strings.TrimSpace(connectorID)).Scan(&x.UserID, &x.Source, &x.ConnectorID, &x.Enabled, &x.LastStatus, &x.LastError, &x.ToolCount, &x.SchemaSHA256, &x.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPConnectorRuntimeState{}, false, nil
	}
	if err != nil {
		return MCPConnectorRuntimeState{}, false, fmt.Errorf("get MCP connector runtime state: %w", err)
	}
	return x, true, nil
}

func (s *Store) PutMCPConnectorRuntimeState(x MCPConnectorRuntimeState) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	x.UserID, x.Source, x.ConnectorID = strings.TrimSpace(x.UserID), strings.TrimSpace(x.Source), strings.TrimSpace(x.ConnectorID)
	if x.UserID == "" || x.Source == "" || x.ConnectorID == "" {
		return errors.New("MCP connector runtime owner, source, and id are required")
	}
	if x.ToolCount < 0 {
		return errors.New("MCP connector tool count must be non-negative")
	}
	if x.UpdatedAt.IsZero() {
		x.UpdatedAt = s.now().UTC()
	}
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO mcp_connector_runtime_state (user_id, source, connector_id, enabled, last_status, last_error, tool_count, schema_sha256, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(user_id, source, connector_id) DO UPDATE SET enabled=excluded.enabled,last_status=excluded.last_status,last_error=excluded.last_error,tool_count=excluded.tool_count,schema_sha256=excluded.schema_sha256,updated_at=excluded.updated_at`, x.UserID, x.Source, x.ConnectorID, x.Enabled, strings.TrimSpace(x.LastStatus), strings.TrimSpace(x.LastError), x.ToolCount, strings.TrimSpace(x.SchemaSHA256), x.UpdatedAt.UTC())
	if err != nil {
		return fmt.Errorf("put MCP connector runtime state: %w", err)
	}
	return nil
}

// CreateMCPConnectorRuntimeState records an initial connector state without
// overwriting a concurrent explicit user decision. The boolean result reports
// whether this call created the row.
func (s *Store) CreateMCPConnectorRuntimeState(x MCPConnectorRuntimeState) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	x.UserID, x.Source, x.ConnectorID = strings.TrimSpace(x.UserID), strings.TrimSpace(x.Source), strings.TrimSpace(x.ConnectorID)
	if x.UserID == "" || x.Source == "" || x.ConnectorID == "" {
		return false, errors.New("MCP connector runtime owner, source, and id are required")
	}
	if x.ToolCount < 0 {
		return false, errors.New("MCP connector tool count must be non-negative")
	}
	if x.UpdatedAt.IsZero() {
		x.UpdatedAt = s.now().UTC()
	}
	result, err := s.db.ExecContext(context.Background(), `INSERT INTO mcp_connector_runtime_state (user_id, source, connector_id, enabled, last_status, last_error, tool_count, schema_sha256, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(user_id, source, connector_id) DO NOTHING`, x.UserID, x.Source, x.ConnectorID, x.Enabled, strings.TrimSpace(x.LastStatus), strings.TrimSpace(x.LastError), x.ToolCount, strings.TrimSpace(x.SchemaSHA256), x.UpdatedAt.UTC())
	if err != nil {
		return false, fmt.Errorf("create MCP connector runtime state: %w", err)
	}
	created, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read MCP connector runtime state creation result: %w", err)
	}
	return created == 1, nil
}

// RefreshMCPConnectorRuntimeStateIfEnabledAndStale publishes a probe result
// only while the owner still has the connector enabled and the stored health
// snapshot predates staleBefore. The conditional update prevents a background
// probe from overwriting a concurrent disable or a newer explicit probe.
func (s *Store) RefreshMCPConnectorRuntimeStateIfEnabledAndStale(x MCPConnectorRuntimeState, staleBefore time.Time) (bool, error) {
	if s == nil || s.db == nil {
		return false, errors.New("workspace store is closed")
	}
	x.UserID, x.Source, x.ConnectorID = strings.TrimSpace(x.UserID), strings.TrimSpace(x.Source), strings.TrimSpace(x.ConnectorID)
	if x.UserID == "" || x.Source == "" || x.ConnectorID == "" {
		return false, errors.New("MCP connector runtime owner, source, and id are required")
	}
	if x.ToolCount < 0 {
		return false, errors.New("MCP connector tool count must be non-negative")
	}
	if staleBefore.IsZero() {
		return false, errors.New("MCP connector runtime stale boundary is required")
	}
	if x.UpdatedAt.IsZero() {
		x.UpdatedAt = s.now().UTC()
	}
	result, err := s.db.ExecContext(context.Background(), `UPDATE mcp_connector_runtime_state SET last_status=?,last_error=?,tool_count=?,schema_sha256=?,updated_at=? WHERE user_id=? AND source=? AND connector_id=? AND enabled=1 AND updated_at < ?`, strings.TrimSpace(x.LastStatus), strings.TrimSpace(x.LastError), x.ToolCount, strings.TrimSpace(x.SchemaSHA256), x.UpdatedAt.UTC(), x.UserID, x.Source, x.ConnectorID, staleBefore.UTC())
	if err != nil {
		return false, fmt.Errorf("refresh MCP connector runtime state: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read MCP connector runtime state refresh result: %w", err)
	}
	return updated == 1, nil
}
