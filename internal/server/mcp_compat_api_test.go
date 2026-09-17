package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
)

func TestMCPServerCompatibilityLifecycleIsDurableAndOwnerScoped(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	app := New(Options{Workspace: store}).Handler()

	createdAgent := agentCompatRequest(t, app, http.MethodPost, "/api/agents", map[string]any{
		"name": "mcp_research", "displayName": "MCP Research", "description": "fixture",
		"systemPrompt": "Research with MCP.", "skillNames": []string{}, "enabled": true,
	})
	if createdAgent.Code != http.StatusOK {
		t.Fatalf("create agent status=%d body=%s", createdAgent.Code, createdAgent.Body.String())
	}

	counts := runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/attachment-counts", "local", nil, http.StatusOK)
	if got := counts["counts"].(map[string]any)["OPERON"]; got != float64(len(bundledAgentConnectorIDs)) {
		t.Fatalf("initial OPERON connector count=%v", got)
	}

	created := runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers", "local", map[string]any{
		"name": "evidence-oracle", "description": "durable fixture", "url": "https://1.1.1.1/mcp",
		"transport": "streamable_http", "oauth_server_url": "https://1.0.0.1/oauth",
		"client_id": "client-a", "scopes": "read write", "headers_helper": "auth-helper",
	}, http.StatusCreated)
	serverID := created["id"].(string)
	if created["transport"] != "streamable_http" || created["description"] != "durable fixture" ||
		created["client_id"] != "client-a" || created["is_connected"] != false {
		t.Fatalf("created MCP server=%#v", created)
	}
	runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+serverID, "other-user", nil, http.StatusNotFound)

	updated := runtimeCompatJSON(t, app, http.MethodPatch, "/api/mcp-servers/"+serverID, "local", map[string]any{
		"name": "evidence-updated", "description": nil, "headers_helper": nil,
	}, http.StatusOK)
	if updated["name"] != "evidence-updated" || updated["description"] != nil || updated["headers_helper"] != nil {
		t.Fatalf("nullable update=%#v", updated)
	}

	attached := runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers/"+serverID+"/attach", "local", map[string]any{
		"agent_names": []string{"MCP_RESEARCH", "MCP_RESEARCH", "UNKNOWN_AGENT"},
	}, http.StatusOK)
	if got := attached["attached"].([]any); len(got) != 1 || got[0] != "MCP_RESEARCH" {
		t.Fatalf("selected attachments=%#v", attached)
	}
	if got := attached["skipped"].([]any); len(got) != 1 || got[0] != "UNKNOWN_AGENT" {
		t.Fatalf("skipped attachments=%#v", attached)
	}

	all := runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers/"+serverID+"/attach-all", "local", nil, http.StatusOK)
	if got := all["attached"].([]any); len(got) != len(New(Options{}).agentCatalog.Agents())+1 {
		t.Fatalf("attach-all count=%d response=%#v", len(got), all)
	}
	counts = runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/attachment-counts", "local", nil, http.StatusOK)
	if got := counts["counts"].(map[string]any)["MCP_RESEARCH"]; got != float64(1) {
		t.Fatalf("custom agent connector count=%v", got)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app = New(Options{Workspace: store}).Handler()
	reloaded := runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+serverID, "local", nil, http.StatusOK)
	if reloaded["client_id"] != "client-a" || reloaded["scopes"] != "read write" ||
		len(reloaded["attached_agents"].([]any)) != len(New(Options{}).agentCatalog.Agents())+1 {
		t.Fatalf("reloaded MCP server=%#v", reloaded)
	}

	runtimeCompatJSON(t, app, http.MethodDelete, "/api/mcp-servers/"+serverID+"/agents/MCP_RESEARCH", "local", nil, http.StatusOK)
	detached := runtimeCompatJSON(t, app, http.MethodDelete, "/api/mcp-servers/"+serverID+"/detach-all", "local", nil, http.StatusOK)
	if detached["detached_count"] != float64(len(New(Options{}).agentCatalog.Agents())) {
		t.Fatalf("detach-all=%#v", detached)
	}
	deleted := runtimeCompatJSON(t, app, http.MethodDelete, "/api/mcp-servers/"+serverID, "local", nil, http.StatusOK)
	if deleted["assignments_deleted"] != float64(0) {
		t.Fatalf("delete response=%#v", deleted)
	}
	runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+serverID, "local", nil, http.StatusNotFound)
}

func TestMCPServerCompatibilityRejectsSSRFAndListsV11Shape(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{Workspace: store}).Handler()

	blocked := runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers", "local", map[string]any{
		"name": "private-host", "url": "https://127.0.0.1/mcp", "transport": "streamable_http",
	}, http.StatusBadRequest)
	if blocked["detail"] == nil {
		t.Fatalf("SSRF rejection=%#v", blocked)
	}

	response := agentCompatRequest(t, app, http.MethodGet, "/api/mcp-servers", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("list=%#v", listed)
	}
}
