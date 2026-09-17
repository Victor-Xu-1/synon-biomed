package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const MaxMCPConnectorToolCatalogBytes = 1 << 20

type MCPConnectorToolCatalog struct {
	UserID        string
	Source        string
	ConnectorID   string
	ConfigSHA256  string
	CatalogSHA256 string
	CatalogJSON   string
	RefreshedAt   time.Time
}

func (s *Store) GetMCPConnectorToolCatalog(userID, source, connectorID string) (MCPConnectorToolCatalog, bool, error) {
	if s == nil || s.db == nil {
		return MCPConnectorToolCatalog{}, false, errors.New("workspace store is closed")
	}
	var catalog MCPConnectorToolCatalog
	err := s.db.QueryRowContext(context.Background(), `SELECT user_id, source, connector_id,
		config_sha256, catalog_sha256, catalog_json, refreshed_at
		FROM mcp_connector_tool_catalogs WHERE user_id=? AND source=? AND connector_id=?`,
		strings.TrimSpace(userID), strings.TrimSpace(source), strings.TrimSpace(connectorID)).Scan(
		&catalog.UserID, &catalog.Source, &catalog.ConnectorID, &catalog.ConfigSHA256,
		&catalog.CatalogSHA256, &catalog.CatalogJSON, &catalog.RefreshedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return MCPConnectorToolCatalog{}, false, nil
	}
	if err != nil {
		return MCPConnectorToolCatalog{}, false, fmt.Errorf("get MCP connector tool catalog: %w", err)
	}
	return catalog, true, nil
}

func (s *Store) PutMCPConnectorToolCatalog(catalog MCPConnectorToolCatalog) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	catalog.UserID = strings.TrimSpace(catalog.UserID)
	catalog.Source = strings.TrimSpace(catalog.Source)
	catalog.ConnectorID = strings.TrimSpace(catalog.ConnectorID)
	catalog.ConfigSHA256 = strings.TrimSpace(catalog.ConfigSHA256)
	catalog.CatalogSHA256 = strings.TrimSpace(catalog.CatalogSHA256)
	if catalog.UserID == "" || catalog.Source == "" || catalog.ConnectorID == "" {
		return errors.New("MCP connector tool catalog owner, source, and id are required")
	}
	if catalog.Source != "bundled" && catalog.Source != "directory" && catalog.Source != "custom" {
		return errors.New("MCP connector tool catalog source is invalid")
	}
	if err := validateMCPConnectorToolCatalogSHA256(catalog.ConfigSHA256); err != nil {
		return fmt.Errorf("MCP connector configuration digest: %w", err)
	}
	if err := validateMCPConnectorToolCatalogSHA256(catalog.CatalogSHA256); err != nil {
		return fmt.Errorf("MCP connector catalog digest: %w", err)
	}
	raw := []byte(catalog.CatalogJSON)
	if len(raw) == 0 || len(raw) > MaxMCPConnectorToolCatalogBytes || !json.Valid(raw) {
		return errors.New("MCP connector tool catalog JSON is invalid or too large")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return errors.New("MCP connector tool catalog JSON must be an object")
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != catalog.CatalogSHA256 {
		return errors.New("MCP connector tool catalog digest does not match its JSON")
	}
	if catalog.RefreshedAt.IsZero() {
		catalog.RefreshedAt = s.now().UTC()
	}
	_, err := s.db.ExecContext(context.Background(), `INSERT INTO mcp_connector_tool_catalogs
		(user_id,source,connector_id,config_sha256,catalog_sha256,catalog_json,refreshed_at)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(user_id,source,connector_id) DO UPDATE SET
		config_sha256=excluded.config_sha256,catalog_sha256=excluded.catalog_sha256,
		catalog_json=excluded.catalog_json,refreshed_at=excluded.refreshed_at`,
		catalog.UserID, catalog.Source, catalog.ConnectorID, catalog.ConfigSHA256,
		catalog.CatalogSHA256, catalog.CatalogJSON, catalog.RefreshedAt.UTC())
	if err != nil {
		return fmt.Errorf("put MCP connector tool catalog: %w", err)
	}
	return nil
}

func (s *Store) DeleteMCPConnectorToolCatalog(userID, source, connectorID string) error {
	if s == nil || s.db == nil {
		return errors.New("workspace store is closed")
	}
	_, err := s.db.ExecContext(context.Background(), `DELETE FROM mcp_connector_tool_catalogs
		WHERE user_id=? AND source=? AND connector_id=?`, strings.TrimSpace(userID),
		strings.TrimSpace(source), strings.TrimSpace(connectorID))
	if err != nil {
		return fmt.Errorf("delete MCP connector tool catalog: %w", err)
	}
	return nil
}

func validateMCPConnectorToolCatalogSHA256(value string) error {
	if len(value) != sha256.Size*2 {
		return errors.New("SHA-256 digest must contain 64 hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("SHA-256 digest is invalid")
	}
	return nil
}
