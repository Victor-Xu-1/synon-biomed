package workspace

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestMCPServerSchemaUpgradePreservesLegacyRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE custom_mcp_servers (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		name TEXT NOT NULL,
		url TEXT NOT NULL,
		transport TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		UNIQUE (user_id, name)
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO custom_mcp_servers
		(id,user_id,name,url,transport,enabled,created_at,updated_at)
		VALUES('legacy','owner','legacy-server','https://1.1.1.1/mcp','sse',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade MCP server schema: %v", err)
	}
	defer store.Close()
	columns := tableColumns(t, store.db, "custom_mcp_servers")
	for _, column := range []string{"description", "oauth_server_url", "client_id", "scopes", "headers_helper", "config_json", "builtin"} {
		if !columns[column] {
			t.Fatalf("custom_mcp_servers missing upgraded column %s", column)
		}
	}
	server, found, err := store.GetMCPServer("legacy", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if !found || server.Name != "legacy-server" || server.URL != "https://1.1.1.1/mcp" ||
		server.Description != "" || server.OAuthServerURL != "" || server.ClientID != "" ||
		server.Scopes != "" || server.HeadersHelper != "" || server.ConfigJSON != "{}" || server.Builtin {
		t.Fatalf("upgraded server=%#v found=%v", server, found)
	}
}
