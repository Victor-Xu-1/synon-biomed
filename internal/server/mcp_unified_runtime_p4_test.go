package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

func TestAgentRuntimeListMCPToolsUsesUnifiedWorkspaceCatalog(t *testing.T) {
	t.Setenv("SYNON_MCP_CONFIG", "")
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	root := t.TempDir()
	mcpURL, mcpClient, _ := workspaceMCPCompatibilityFixture(t)
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.CreateAgent(workspace.CreateAgentInput{
		ID: "catalog-agent", UserID: "local", Name: "CATALOG_AGENT", DisplayName: "Catalog Agent",
		Description: "unified MCP catalog fixture", SystemPrompt: "Inspect and use connector tools.", Enabled: boolPointer(true),
	}); err != nil {
		t.Fatal(err)
	}
	directory, err := store.CreateMCPDirectory(workspace.CreateMCPDirectoryInput{
		UserID: "local", Name: "Fixture Directory", URL: mcpURL + "/manifest", CatalogUUID: "fixture-catalog",
	})
	if err != nil {
		t.Fatal(err)
	}
	connectorID := "directory:fixture"
	if _, err := store.ReplaceMCPDirectoryConnectors(directory.ID, "local", "fixture-etag", []workspace.MCPDirectoryConnectorInput{{
		ID: connectorID, Name: "directory-fixture", Description: "real directory protocol fixture",
		URL: mcpURL, Transport: "streamable-http", ContentSHA: "fixture-content",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachAgentConnector("local", "CATALOG_AGENT", connectorID, "directory"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "catalog-project", UserID: "local", Name: "Catalog", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "catalog-frame", ProjectID: "catalog-project", AgentName: "CATALOG_AGENT",
		Status: "processing", ConversationType: "agent", Name: "Unified catalog",
	}); err != nil {
		t.Fatal(err)
	}

	srv := New(Options{Workspace: store, FileRoot: root, HTTPClient: mcpClient})
	gateway := serverAgentRuntimeToolGateway{
		server: srv, sessionID: "catalog-frame", allowedTools: []string{"ListMcpTools"},
	}
	list := func(server string) mcpstdio.ToolListResult {
		t.Helper()
		arguments, err := json.Marshal(map[string]any{"server": server})
		if err != nil {
			t.Fatal(err)
		}
		response, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
			ID: "list-" + strings.ReplaceAll(server, ":", "-"), Name: "ListMcpTools", Arguments: arguments,
		})
		if err != nil {
			t.Fatal(err)
		}
		body, ok := response.Value.(map[string]any)
		if !ok || body["ok"] != true {
			t.Fatalf("ListMcpTools response=%#v", response.Value)
		}
		catalog, ok := body["result"].(mcpstdio.ToolListResult)
		if !ok {
			t.Fatalf("ListMcpTools result type=%T value=%#v", body["result"], body["result"])
		}
		return catalog
	}

	catalog := list("directory-fixture")
	if len(catalog.Servers) != 1 || catalog.Servers[0].Status != "connected" || catalog.Servers[0].ToolCount != 1 {
		t.Fatalf("unified workspace server catalog=%#v", catalog.Servers)
	}
	if len(catalog.Tools) != 1 || catalog.Tools[0].Name != "mcp__directory-fixture__echo" || catalog.Tools[0].ToolName != "echo" {
		t.Fatalf("unified workspace tool catalog=%#v", catalog.Tools)
	}

	denied := false
	if _, err := store.SetMCPConnectorToolPolicy("local", connectorID, "echo", &denied); err != nil {
		t.Fatal(err)
	}
	catalog = list(connectorID)
	if len(catalog.Servers) != 1 || catalog.Servers[0].Status != "connected" || catalog.Servers[0].ToolCount != 0 || len(catalog.Tools) != 0 {
		t.Fatalf("denied workspace tool leaked through ListMcpTools: %#v", catalog)
	}

	catalog = list("missing-connector")
	if len(catalog.Servers) != 1 || catalog.Servers[0].Status != "not_found" || catalog.Servers[0].Configured || catalog.Servers[0].Error == "" {
		t.Fatalf("unknown workspace connector was not a structured diagnostic: %#v", catalog)
	}
}

func TestHostedScientificMCPsRequireConfirmationByDefault(t *testing.T) {
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv := New(Options{Workspace: store, FileRoot: t.TempDir()})
	connector := workspaceMCPRuntimeConnector{
		ID: "bundled:tamarind-bio", Name: "tamarind-bio", Source: "bundled", Enabled: true,
	}
	mode, explicit, err := srv.workspaceMCPRuntimeToolPolicy("owner", connector, "run_boltz")
	if err != nil || !explicit || mode != "confirm" {
		t.Fatalf("hosted compute policy mode=%q explicit=%t err=%v", mode, explicit, err)
	}
	allowed := true
	if _, err := store.SetMCPConnectorToolPolicy("owner", connector.ID, "run_boltz", &allowed); err != nil {
		t.Fatal(err)
	}
	mode, explicit, err = srv.workspaceMCPRuntimeToolPolicy("owner", connector, "run_boltz")
	if err != nil || !explicit || mode != "allow" {
		t.Fatalf("explicit owner override mode=%q explicit=%t err=%v", mode, explicit, err)
	}
	openTargets := workspaceMCPRuntimeConnector{
		ID: "bundled:open-targets-official", Name: "open-targets-official", Source: "bundled", Enabled: true,
	}
	if mode, explicit, err := srv.workspaceMCPRuntimeToolPolicy("owner", openTargets, "search_entities"); err != nil || explicit || mode != "" {
		t.Fatalf("read-only evidence connector policy mode=%q explicit=%t err=%v", mode, explicit, err)
	}
}

func TestP4DirectoryMCPPermissionsRuntimeRestartAndCleanup(t *testing.T) {
	root := t.TempDir()
	mcpURL, mcpClient, toolCalls := workspaceMCPCompatibilityFixture(t)
	databasePath := filepath.Join(root, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateAgent(workspace.CreateAgentInput{
		ID: "directory-agent", UserID: "local", Name: "DIRECTORY_AGENT", DisplayName: "Directory Agent",
		Description: "directory MCP runtime fixture", SystemPrompt: "Use directory tools.", Enabled: boolPointer(true),
	}); err != nil {
		t.Fatal(err)
	}
	directory, err := store.CreateMCPDirectory(workspace.CreateMCPDirectoryInput{
		UserID: "local", Name: "Fixture Directory", URL: mcpURL + "/manifest", CatalogUUID: "fixture-catalog",
	})
	if err != nil {
		t.Fatal(err)
	}
	connectorID := "directory:fixture"
	if _, err := store.ReplaceMCPDirectoryConnectors(directory.ID, "local", "fixture-etag", []workspace.MCPDirectoryConnectorInput{{
		ID: connectorID, Name: "directory-fixture", Description: "real directory protocol fixture",
		URL: mcpURL, Transport: "streamable-http", ContentSHA: "fixture-content",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.AttachAgentConnector("local", "DIRECTORY_AGENT", connectorID, "directory"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "directory-project", UserID: "local", Name: "Directory", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "directory-frame", ProjectID: "directory-project", AgentName: "DIRECTORY_AGENT",
		Status: "processing", ConversationType: "agent", Name: "Directory runtime",
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Workspace: store, FileRoot: root, HTTPClient: mcpClient})
	app := srv.Handler()
	encodedID := strings.ReplaceAll(connectorID, ":", "%3A")
	permissions := runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+encodedID+"/tool-permissions", "local", nil, http.StatusOK)
	tools := permissions["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["toolName"] != "echo" || tools[0].(map[string]any)["state"] != nil {
		t.Fatalf("directory MCP permissions=%#v", permissions)
	}
	foreign := runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+encodedID+"/tool-permissions", "other-user", nil, http.StatusOK)
	if foreign["error"] != "MCP connector not found" || len(foreign["tools"].([]any)) != 0 {
		t.Fatalf("foreign directory MCP projection leaked details=%#v", foreign)
	}
	runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers/"+encodedID+"/tool-grants", "local", map[string]any{
		"toolName": "echo", "decision": "deny",
	}, http.StatusOK)
	toolName := "mcp__directory-fixture__echo"
	if schemas := srv.agentRuntimeToolSchemas([]string{toolName}, "directory-frame"); hasAgentRuntimeSchema(schemas, toolName) {
		t.Fatalf("denied directory MCP tool remained model-visible: %#v", schemas)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, sessionID: "directory-frame", allowedTools: []string{toolName}}
	denied, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "directory-denied", Name: toolName, Arguments: json.RawMessage(`{"text":"blocked"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	deniedBody := denied.Value.(map[string]any)
	if deniedBody["decision"] != "denied" || deniedBody["policySource"] != "mcp-server" || toolCalls.Load() != 0 {
		t.Fatalf("directory MCP deny=%#v calls=%d", deniedBody, toolCalls.Load())
	}
	runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers/"+encodedID+"/tool-grants", "local", map[string]any{
		"toolName": "echo", "decision": "allow",
	}, http.StatusOK)

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	srv = New(Options{Workspace: store, FileRoot: root, HTTPClient: mcpClient})
	app = srv.Handler()
	permissions = runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+encodedID+"/tool-permissions", "local", nil, http.StatusOK)
	if got := permissions["tools"].([]any)[0].(map[string]any)["state"]; got != "allow" {
		t.Fatalf("directory MCP persisted permission=%#v", permissions)
	}
	if schemas := srv.agentRuntimeToolSchemas([]string{toolName}, "directory-frame"); !hasAgentRuntimeSchema(schemas, toolName) {
		t.Fatalf("allowed directory MCP schema missing: %#v", schemas)
	}
	gateway = serverAgentRuntimeToolGateway{server: srv, sessionID: "directory-frame", allowedTools: []string{toolName}}
	allowed, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "directory-allowed", Name: toolName, Arguments: json.RawMessage(`{"text":"allowed"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	allowedBody := allowed.Value.(map[string]any)
	if allowedBody["ok"] != true || allowedBody["result"] != "workspace ok" || toolCalls.Load() != 1 {
		t.Fatalf("directory MCP allow=%#v calls=%d", allowedBody, toolCalls.Load())
	}

	if _, err := store.ReplaceMCPDirectoryConnectors(directory.ID, "local", "empty-etag", nil); err != nil {
		t.Fatal(err)
	}
	attachments, err := store.ListAgentConnectorAttachments("local", "DIRECTORY_AGENT")
	if err != nil || len(attachments) != 0 {
		t.Fatalf("stale directory attachments=%#v err=%v", attachments, err)
	}
	policies, err := store.ListMCPConnectorToolPolicies("local", connectorID)
	if err != nil || len(policies) != 0 {
		t.Fatalf("stale directory policies=%#v err=%v", policies, err)
	}
	runtimeContext, found, err := srv.workspaceMCPRuntimeContext("directory-frame")
	if err != nil || !found || len(runtimeContext.Connectors) != 0 {
		t.Fatalf("directory cleanup runtime context found=%v err=%v value=%#v", found, err, runtimeContext)
	}
}
