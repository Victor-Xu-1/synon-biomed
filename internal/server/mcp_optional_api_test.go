package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestOptionalMCPAPIListsDeferredConnectorsWithoutInstallingThem(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := New(Options{FileRoot: root, Workspace: store}).Handler()
	record := httptest.NewRecorder()
	request := authenticatedWorkspaceRequest(http.MethodGet, "/api/mcp-servers/optional", "local", nil)
	app.ServeHTTP(record, request)
	if record.Code != http.StatusOK {
		t.Fatalf("GET optional MCP catalog=%d: %s", record.Code, record.Body.String())
	}
	var items []map[string]any
	if err := json.Unmarshal(record.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("optional MCP catalog=%#v", items)
	}
	for _, item := range items {
		if item["status"] != "not-installed" || item["installed"] != false {
			t.Fatalf("optional MCP was not deferred: %#v", item)
		}
	}
}
