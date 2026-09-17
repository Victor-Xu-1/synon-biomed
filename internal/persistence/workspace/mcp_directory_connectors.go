package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type MCPDirectoryConnector struct {
	UserID       string    `json:"userId,omitempty"`
	ID           string    `json:"id"`
	DirectoryID  string    `json:"directoryId"`
	Name         string    `json:"name"`
	Description  string    `json:"description,omitempty"`
	URL          string    `json:"url"`
	Transport    string    `json:"transport"`
	AuthRequired bool      `json:"authRequired"`
	AuthHint     string    `json:"authHint,omitempty"`
	Enabled      bool      `json:"enabled"`
	LastStatus   string    `json:"lastStatus"`
	LastError    string    `json:"lastError,omitempty"`
	ToolCount    int       `json:"toolCount"`
	ContentSHA   string    `json:"contentSha256"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type MCPDirectoryConnectorInput struct {
	ID           string
	Name         string
	Description  string
	URL          string
	Transport    string
	AuthRequired bool
	AuthHint     string
	ContentSHA   string
}

const mcpDirectoryConnectorSelect = `
	SELECT user_id, id, directory_id, name, description, url, transport,
		auth_required, auth_hint, enabled, last_status, last_error, tool_count,
		content_sha256, created_at, updated_at
	FROM mcp_directory_connectors`

func (s *Store) ReplaceMCPDirectoryConnectors(directoryID, userID, etag string, inputs []MCPDirectoryConnectorInput) ([]MCPDirectoryConnector, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	directoryID, userID = strings.TrimSpace(directoryID), strings.TrimSpace(userID)
	if directoryID == "" || userID == "" {
		return nil, errors.New("MCP directory id and user id are required")
	}
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin MCP directory reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var owned string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM mcp_directories WHERE id = ? AND user_id = ?`, directoryID, userID).Scan(&owned); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("MCP directory not found")
		}
		return nil, err
	}
	now := s.now().UTC()
	seen := map[string]bool{}
	for _, input := range inputs {
		input.ID = strings.TrimSpace(input.ID)
		if input.ID == "" || seen[input.ID] {
			return nil, fmt.Errorf("duplicate or empty MCP directory connector id %q", input.ID)
		}
		seen[input.ID] = true
		_, err := tx.ExecContext(ctx, `
			INSERT INTO mcp_directory_connectors
				(user_id, id, directory_id, name, description, url, transport,
				 auth_required, auth_hint, enabled, last_status, last_error,
				 tool_count, content_sha256, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, 'configured', '', 0, ?, ?, ?)
			ON CONFLICT(user_id, id) DO UPDATE SET
				directory_id = excluded.directory_id,
				name = excluded.name,
				description = excluded.description,
				url = excluded.url,
				transport = excluded.transport,
				auth_required = excluded.auth_required,
				auth_hint = excluded.auth_hint,
				content_sha256 = excluded.content_sha256,
				updated_at = excluded.updated_at`,
			userID, input.ID, directoryID, input.Name, input.Description, input.URL,
			input.Transport, input.AuthRequired, input.AuthHint, input.ContentSHA, now, now)
		if err != nil {
			return nil, fmt.Errorf("upsert MCP directory connector %q: %w", input.ID, err)
		}
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM mcp_directory_connectors WHERE directory_id = ? AND user_id = ?`,
		directoryID, userID)
	if err != nil {
		return nil, err
	}
	stale := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if !seen[id] {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agent_connector_tool_exclusions WHERE user_id = ? AND server_id = ?`, userID, id); err != nil {
			return nil, fmt.Errorf("delete stale MCP connector tool exclusions %q: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agent_connector_attachments WHERE user_id = ? AND server_id = ? AND source = 'directory'`, userID, id); err != nil {
			return nil, fmt.Errorf("delete stale MCP connector attachments %q: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM mcp_connector_tool_policies WHERE user_id = ? AND connector_id = ?`, userID, id); err != nil {
			return nil, fmt.Errorf("delete stale MCP connector policies %q: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM mcp_connector_runtime_state WHERE user_id = ? AND connector_id = ? AND source = 'directory'`, userID, id); err != nil {
			return nil, fmt.Errorf("delete stale MCP connector runtime state %q: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM mcp_directory_connectors WHERE user_id = ? AND id = ?`, userID, id); err != nil {
			return nil, fmt.Errorf("delete stale MCP directory connector %q: %w", id, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE mcp_directories SET etag = ?, last_status = 'ok', last_error = '',
			last_checked_at = ?, updated_at = ? WHERE id = ? AND user_id = ?`,
		etag, now, now, directoryID, userID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit MCP directory reconciliation: %w", err)
	}
	return s.ListMCPDirectoryConnectors(userID, directoryID)
}

func (s *Store) ListMCPDirectoryConnectors(userID, directoryID string) ([]MCPDirectoryConnector, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("MCP directory connector user id is required")
	}
	query := mcpDirectoryConnectorSelect + ` WHERE user_id = ?`
	args := []any{userID}
	if strings.TrimSpace(directoryID) != "" {
		query += ` AND directory_id = ?`
		args = append(args, strings.TrimSpace(directoryID))
	}
	query += ` ORDER BY name, id`
	rows, err := s.db.QueryContext(context.Background(), query, args...)
	if err != nil {
		return nil, fmt.Errorf("list MCP directory connectors: %w", err)
	}
	defer rows.Close()
	connectors := []MCPDirectoryConnector{}
	for rows.Next() {
		connector, _, err := scanMCPDirectoryConnector(rows)
		if err != nil {
			return nil, err
		}
		connectors = append(connectors, connector)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MCP directory connectors: %w", err)
	}
	return connectors, nil
}

func (s *Store) GetMCPDirectoryConnector(userID, id string) (MCPDirectoryConnector, bool, error) {
	if s == nil || s.db == nil {
		return MCPDirectoryConnector{}, false, errors.New("workspace store is closed")
	}
	return scanMCPDirectoryConnector(s.db.QueryRowContext(context.Background(),
		mcpDirectoryConnectorSelect+` WHERE user_id = ? AND id = ?`,
		strings.TrimSpace(userID), strings.TrimSpace(id)))
}

func (s *Store) SetMCPDirectoryConnectorEnabled(userID, id string, enabled bool) (MCPDirectoryConnector, error) {
	result, err := s.db.ExecContext(context.Background(), `
		UPDATE mcp_directory_connectors SET enabled = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`, enabled, s.now().UTC(), userID, id)
	if err != nil {
		return MCPDirectoryConnector{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return MCPDirectoryConnector{}, errors.New("MCP directory connector not found")
	}
	connector, found, err := s.GetMCPDirectoryConnector(userID, id)
	if err != nil {
		return MCPDirectoryConnector{}, err
	}
	if !found {
		return MCPDirectoryConnector{}, errors.New("MCP directory connector disappeared")
	}
	return connector, nil
}

func (s *Store) UpdateMCPDirectoryConnectorHealth(userID, id, status, message string, toolCount int) error {
	if toolCount < 0 {
		return errors.New("MCP connector tool count must be non-negative")
	}
	result, err := s.db.ExecContext(context.Background(), `
		UPDATE mcp_directory_connectors SET
			last_status = ?, last_error = ?, tool_count = ?, updated_at = ?
		WHERE user_id = ? AND id = ?`,
		strings.TrimSpace(status), strings.TrimSpace(message), toolCount, s.now().UTC(), userID, id)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("MCP directory connector not found")
	}
	return nil
}

type mcpDirectoryConnectorRow interface {
	Scan(...any) error
}

func scanMCPDirectoryConnector(row mcpDirectoryConnectorRow) (MCPDirectoryConnector, bool, error) {
	var connector MCPDirectoryConnector
	err := row.Scan(
		&connector.UserID, &connector.ID, &connector.DirectoryID, &connector.Name,
		&connector.Description, &connector.URL, &connector.Transport,
		&connector.AuthRequired, &connector.AuthHint, &connector.Enabled,
		&connector.LastStatus, &connector.LastError, &connector.ToolCount,
		&connector.ContentSHA, &connector.CreatedAt, &connector.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPDirectoryConnector{}, false, nil
	}
	if err != nil {
		return MCPDirectoryConnector{}, false, fmt.Errorf("scan MCP directory connector: %w", err)
	}
	return connector, true, nil
}
