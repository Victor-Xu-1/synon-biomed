package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type MCPDirectory struct {
	ID            string     `json:"id"`
	UserID        string     `json:"userId,omitempty"`
	Name          string     `json:"name"`
	URL           string     `json:"url"`
	CatalogUUID   string     `json:"catalogUuid"`
	ETag          string     `json:"etag,omitempty"`
	LastStatus    string     `json:"lastStatus"`
	LastError     string     `json:"lastError,omitempty"`
	LastCheckedAt *time.Time `json:"lastCheckedAt,omitempty"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

type CreateMCPDirectoryInput struct {
	UserID      string
	Name        string
	URL         string
	CatalogUUID string
}

const mcpDirectorySelect = `
	SELECT id, user_id, name, url, catalog_uuid, etag, last_status, last_error,
		last_checked_at, created_at, updated_at
	FROM mcp_directories`

func (s *Store) CreateMCPDirectory(input CreateMCPDirectoryInput) (MCPDirectory, error) {
	if s == nil || s.db == nil {
		return MCPDirectory{}, errors.New("workspace store is closed")
	}
	input.UserID = strings.TrimSpace(input.UserID)
	input.Name = strings.TrimSpace(input.Name)
	input.URL = strings.TrimSpace(input.URL)
	input.CatalogUUID = strings.TrimSpace(input.CatalogUUID)
	if input.UserID == "" || input.Name == "" || input.URL == "" || input.CatalogUUID == "" {
		return MCPDirectory{}, errors.New("MCP directory user, name, URL, and catalog UUID are required")
	}
	now := s.now().UTC()
	directory := MCPDirectory{
		ID: uuid.NewString(), UserID: input.UserID, Name: input.Name, URL: input.URL,
		CatalogUUID: input.CatalogUUID, LastStatus: "pending", CreatedAt: now, UpdatedAt: now,
	}
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO mcp_directories
			(id, user_id, name, url, catalog_uuid, etag, last_status, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, '', 'pending', '', ?, ?)`,
		directory.ID, directory.UserID, directory.Name, directory.URL, directory.CatalogUUID, now, now)
	if err != nil {
		return MCPDirectory{}, fmt.Errorf("insert MCP directory: %w", err)
	}
	return directory, nil
}

func (s *Store) GetMCPDirectory(id, userID string) (MCPDirectory, bool, error) {
	if s == nil || s.db == nil {
		return MCPDirectory{}, false, errors.New("workspace store is closed")
	}
	return scanMCPDirectory(s.db.QueryRowContext(context.Background(),
		mcpDirectorySelect+` WHERE id = ? AND user_id = ?`, strings.TrimSpace(id), strings.TrimSpace(userID)))
}

func (s *Store) ListMCPDirectories(userID string) ([]MCPDirectory, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("workspace store is closed")
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, errors.New("MCP directory user id is required")
	}
	rows, err := s.db.QueryContext(context.Background(),
		mcpDirectorySelect+` WHERE user_id = ? ORDER BY name, id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list MCP directories: %w", err)
	}
	defer rows.Close()
	directories := []MCPDirectory{}
	for rows.Next() {
		directory, _, err := scanMCPDirectory(rows)
		if err != nil {
			return nil, err
		}
		directories = append(directories, directory)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate MCP directories: %w", err)
	}
	return directories, nil
}

func (s *Store) RecordMCPDirectoryHealth(id, userID, status, message, etag string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	status = strings.TrimSpace(status)
	if status != "ok" && status != "error" && status != "not-modified" {
		return errors.New("invalid MCP directory health status")
	}
	now := s.now().UTC()
	result, err := s.db.ExecContext(context.Background(), `
		UPDATE mcp_directories SET
			etag = CASE WHEN ? = '' THEN etag ELSE ? END,
			last_status = ?, last_error = ?, last_checked_at = ?, updated_at = ?
		WHERE id = ? AND user_id = ?`,
		etag, etag, status, strings.TrimSpace(message), now, now, id, userID)
	if err != nil {
		return fmt.Errorf("record MCP directory health: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return errors.New("MCP directory not found")
	}
	return nil
}

type mcpDirectoryRow interface {
	Scan(...any) error
}

func scanMCPDirectory(row mcpDirectoryRow) (MCPDirectory, bool, error) {
	var directory MCPDirectory
	var checked sql.NullTime
	err := row.Scan(
		&directory.ID, &directory.UserID, &directory.Name, &directory.URL,
		&directory.CatalogUUID, &directory.ETag, &directory.LastStatus,
		&directory.LastError, &checked, &directory.CreatedAt, &directory.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPDirectory{}, false, nil
	}
	if err != nil {
		return MCPDirectory{}, false, fmt.Errorf("scan MCP directory: %w", err)
	}
	if checked.Valid {
		directory.LastCheckedAt = &checked.Time
	}
	return directory, true, nil
}
