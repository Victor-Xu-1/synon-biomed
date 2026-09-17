package workspace

import (
	"context"
	"errors"
)

const (
	mcpToolCatalogV53CallbackID        = "mcp-tool-catalog-v53-noop"
	mcpToolCatalogV53PreflightIdentity = "mcp-tool-catalog-absent-v1"
	mcpToolCatalogV53RuleSpec          = "synon.workspace.mcp-tool-catalog.v53"
)

var mcpToolCatalogV53Migration = versionedSchemaMigration{
	version: 53,
	name:    "durable-mcp-tool-catalog",
	statements: []string{
		`CREATE TABLE mcp_connector_tool_catalogs (
			user_id TEXT NOT NULL,
			source TEXT NOT NULL CHECK (source IN ('bundled','directory','custom')),
			connector_id TEXT NOT NULL,
			config_sha256 TEXT NOT NULL CHECK (length(config_sha256)=64),
			catalog_sha256 TEXT NOT NULL CHECK (length(catalog_sha256)=64),
			catalog_json TEXT NOT NULL CHECK (
				json_valid(catalog_json) AND json_type(catalog_json)='object' AND
				length(CAST(catalog_json AS BLOB))<=1048576
			),
			refreshed_at TIMESTAMP NOT NULL,
			PRIMARY KEY (user_id, source, connector_id)
		)`,
	},
	identityV2: &schemaMigrationIdentityV2{
		CallbackID:        mcpToolCatalogV53CallbackID,
		RuleSpec:          mcpToolCatalogV53RuleSpec,
		PreflightIdentity: mcpToolCatalogV53PreflightIdentity,
	},
}

func preflightMCPToolCatalogV53(ctx context.Context, executor schemaMigrationQueryExecutor) error {
	var count int
	if err := executor.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema
		WHERE name='mcp_connector_tool_catalogs'`).Scan(&count); err != nil {
		return errors.New("inspect MCP tool catalog schema")
	}
	if count != 0 {
		return errors.New("MCP tool catalog schema already exists")
	}
	return nil
}
