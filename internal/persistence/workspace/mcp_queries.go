package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

func (s *Store) ListMCPServers(userID string) ([]MCPServer, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	if strings.TrimSpace(userID) == "" {
		return nil, errors.New("mcp server user id is required")
	}
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT id, user_id, name, description, url, transport, oauth_server_url,
			client_id, scopes, headers_helper, config_json, builtin, enabled, created_at, updated_at
		FROM custom_mcp_servers WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list mcp servers: %w", err)
	}
	defer rows.Close()
	servers := make([]MCPServer, 0)
	for rows.Next() {
		var server MCPServer
		if err := rows.Scan(
			&server.ID, &server.UserID, &server.Name, &server.Description, &server.URL,
			&server.Transport, &server.OAuthServerURL, &server.ClientID, &server.Scopes,
			&server.HeadersHelper, &server.ConfigJSON, &server.Builtin, &server.Enabled,
			&server.CreatedAt, &server.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan mcp server: %w", err)
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mcp servers: %w", err)
	}
	return servers, nil
}
