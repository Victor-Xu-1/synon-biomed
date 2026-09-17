package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestP4WebMCPBridgeStdioLifecycleIsEncryptedAndRestartSafe(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	appServer := New(Options{FileRoot: root, Workspace: store})
	app := appServer.Handler()
	userID := "mcp-owner"
	secretValue := "p4-mcp-credential-should-be-encrypted"
	originalJSON := `{"mcpServers":{"p4":{"env":{"P4_MCP_SECRET":"` + secretValue + `"}}}}`
	created := compatJSONRequest(t, app, http.MethodPost, "/api/mcp/servers", userID, map[string]any{
		"name":        "P4 local MCP",
		"description": "real stdio fixture",
		"transport": map[string]any{
			"type": "stdio", "command": os.Args[0],
			"args": []string{"-test.run=TestP4WebMCPBridgeStdioHelper"},
			"env":  map[string]string{"SYNON_P4_MCP_HELPER": "1", "P4_MCP_SECRET": secretValue},
		},
		"original_json": originalJSON,
		"builtin":       false,
	}, http.StatusCreated)
	serverID, _ := created["id"].(string)
	if serverID == "" || created["enabled"] != true || created["original_json"] != originalJSON {
		t.Fatalf("created MCP server=%#v", created)
	}
	assertP4MCPSecretAbsentFromFiles(t, root, secretValue)

	listed := compatJSONArrayRequest(t, app, http.MethodGet, "/api/mcp/servers", userID, nil, http.StatusOK)
	if len(listed) != 1 || listed[0]["id"] != serverID || listed[0]["original_json"] != originalJSON {
		t.Fatalf("listed MCP servers=%#v", listed)
	}
	if transport, _ := listed[0]["transport"].(map[string]any); transport["command"] != os.Args[0] || transport["type"] != "stdio" {
		t.Fatalf("listed MCP transport=%#v", transport)
	}

	tested := compatJSONRequest(t, app, http.MethodPost, "/api/mcp/test-connection", userID, map[string]any{
		"id": serverID, "runtime_scope_id": serverID, "name": "tampered",
		"transport":     map[string]any{"type": "stdio", "command": "must-not-run"},
		"original_json": "{}",
	}, http.StatusOK)
	if tested["success"] != true {
		t.Fatalf("stdio MCP connection test=%#v", tested)
	}
	tools, _ := tested["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Fatalf("stdio MCP tools=%#v", tools)
	}
	if _, err := store.CreateAgent(workspace.CreateAgentInput{
		ID: "p4-agent", UserID: userID, Name: "P4_AGENT", DisplayName: "P4 Agent",
		Description: "encrypted MCP runtime fixture", SystemPrompt: "Use the fixture.", Enabled: boolPointer(true),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AssignMCPServerToAgent(workspace.MCPAssignmentInput{
		ID: "p4-assignment", MCPServerID: serverID, UserID: userID, AgentName: "P4_AGENT",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMCPToolGrant(workspace.MCPToolGrantInput{
		ID: "p4-grant", MCPServerID: serverID, UserID: userID,
		AgentName: workspace.GlobalMCPToolGrantAgent, ToolName: "echo", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "p4-project", UserID: userID, Name: "P4", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "p4-frame", ProjectID: "p4-project", AgentName: "P4_AGENT",
		Status: "processing", ConversationType: "agent", Name: "P4 runtime",
	}); err != nil {
		t.Fatal(err)
	}
	toolName := "mcp__P4_local_MCP__echo"
	schemas := appServer.agentRuntimeToolSchemas([]string{toolName}, "p4-frame")
	if !hasAgentRuntimeSchema(schemas, toolName) {
		t.Fatalf("encrypted stdio MCP schema missing: %#v", schemas)
	}
	gateway := serverAgentRuntimeToolGateway{server: appServer, sessionID: "p4-frame", allowedTools: []string{toolName}}
	called, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "p4-call", Name: toolName, Arguments: json.RawMessage(`{"text":"runtime"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	calledBody := called.Value.(map[string]any)
	if calledBody["ok"] != true || calledBody["result"] != "p4 runtime ok" {
		t.Fatalf("encrypted stdio MCP runtime result=%#v", calledBody)
	}

	toggled := compatJSONRequest(t, app, http.MethodPost, "/api/mcp/servers/"+serverID+"/toggle", userID, nil, http.StatusOK)
	if toggled["enabled"] != false {
		t.Fatalf("toggled MCP server=%#v", toggled)
	}
	updated := compatJSONRequest(t, app, http.MethodPut, "/api/mcp/servers/"+serverID, userID, map[string]any{
		"name":        "P4 local MCP updated",
		"description": "updated fixture",
		"transport": map[string]any{
			"type": "stdio", "command": os.Args[0],
			"args": []string{"-test.run=TestP4WebMCPBridgeStdioHelper"},
			"env":  map[string]string{"SYNON_P4_MCP_HELPER": "1", "P4_MCP_SECRET": secretValue},
		},
		"original_json": originalJSON,
		"builtin":       false,
	}, http.StatusOK)
	if updated["name"] != "P4 local MCP updated" || updated["enabled"] != false {
		t.Fatalf("updated MCP server=%#v", updated)
	}
	assertP4MCPSecretAbsentFromFiles(t, root, secretValue)

	restarted := New(Options{FileRoot: root, Workspace: store})
	restartedList := compatJSONArrayRequest(t, restarted.Handler(), http.MethodGet, "/api/mcp/servers", userID, nil, http.StatusOK)
	if len(restartedList) != 1 || restartedList[0]["name"] != "P4 local MCP updated" || restartedList[0]["enabled"] != false {
		t.Fatalf("restarted MCP list=%#v", restartedList)
	}
	foreign := compatJSONArrayRequest(t, restarted.Handler(), http.MethodGet, "/api/mcp/servers", "other-user", nil, http.StatusOK)
	if len(foreign) != 0 {
		t.Fatalf("foreign MCP list=%#v", foreign)
	}

	deleted := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(deleted, compatRequest(t, http.MethodDelete, "/api/mcp/servers/"+serverID, userID, nil))
	if deleted.Code != http.StatusNoContent || deleted.Body.Len() != 0 {
		t.Fatalf("delete MCP status=%d body=%q", deleted.Code, deleted.Body.String())
	}
	if _, found, err := store.GetMCPServer(serverID, userID); err != nil || found {
		t.Fatalf("deleted MCP record found=%v err=%v", found, err)
	}
	if _, found, err := restarted.secretStore.ResolveForUser(webMCPConfigSecretPrefix+serverID, userID); err != nil || found {
		t.Fatalf("deleted MCP secret found=%v err=%v", found, err)
	}
}

func TestP4WebMCPBridgeBatchImportIsAtomicAndRejectsUnsafeRemoteTargets(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{FileRoot: root, Workspace: store}).Handler()
	userID := "mcp-owner"
	server := func(name string) map[string]any {
		return map[string]any{
			"name": name,
			"transport": map[string]any{
				"type": "stdio", "command": os.Args[0],
				"args": []string{"-test.run=TestP4WebMCPBridgeStdioHelper"},
				"env":  map[string]string{"SYNON_P4_MCP_HELPER": "1"},
			},
			"original_json": "{}", "builtin": false,
		}
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/mcp/servers/import", userID, map[string]any{
		"servers": []any{server("duplicate"), server("duplicate")},
	}, http.StatusBadRequest)
	if servers := compatJSONArrayRequest(t, app, http.MethodGet, "/api/mcp/servers", userID, nil, http.StatusOK); len(servers) != 0 {
		t.Fatalf("failed batch left MCP records=%#v", servers)
	}
	secrets, err := New(Options{FileRoot: root}).secretStore.ListForUser(userID)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if strings.HasPrefix(secret.ID, webMCPConfigSecretPrefix) {
			t.Fatalf("failed batch left MCP secret=%s", secret.ID)
		}
	}

	for _, target := range []string{
		"http://example.com/mcp",
		"https://127.0.0.1/mcp",
		"https://169.254.169.254/latest/meta-data",
		"https://user:password@example.com/mcp",
	} {
		compatJSONRequest(t, app, http.MethodPost, "/api/mcp/servers", userID, map[string]any{
			"name": "unsafe", "transport": map[string]any{"type": "http", "url": target}, "original_json": "{}",
		}, http.StatusBadRequest)
	}
	if servers := compatJSONArrayRequest(t, app, http.MethodGet, "/api/mcp/servers", userID, nil, http.StatusOK); len(servers) != 0 {
		t.Fatalf("unsafe target left MCP records=%#v", servers)
	}
}

func TestP4WebMCPBridgeOAuthStatusUsesOwnedURLAndRealVaultState(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	app := New(Options{FileRoot: root, Workspace: store}).Handler()
	userID := "mcp-owner"
	created := compatJSONRequest(t, app, http.MethodPost, "/api/mcp/servers", userID, map[string]any{
		"name": "remote", "transport": map[string]any{"type": "http", "url": "https://8.8.8.8/mcp"}, "original_json": "{}",
	}, http.StatusCreated)
	if created["id"] == "" {
		t.Fatalf("remote MCP create=%#v", created)
	}
	status := compatJSONRequest(t, app, http.MethodPost, "/api/mcp/oauth/check-status", userID, map[string]any{
		"server_url": "https://8.8.8.8/mcp",
	}, http.StatusOK)
	if status["authenticated"] != false {
		t.Fatalf("OAuth status=%#v", status)
	}
	authenticatedResponse := httptest.NewRecorder()
	app.ServeHTTP(authenticatedResponse, compatRequest(t, http.MethodGet, "/api/mcp/oauth/authenticated", userID, nil))
	if authenticatedResponse.Code != http.StatusOK {
		t.Fatalf("authenticated MCP status=%d body=%s", authenticatedResponse.Code, authenticatedResponse.Body.String())
	}
	var authenticated []string
	if err := json.NewDecoder(authenticatedResponse.Body).Decode(&authenticated); err != nil {
		t.Fatal(err)
	}
	if len(authenticated) != 0 {
		t.Fatalf("authenticated MCP URLs=%#v", authenticated)
	}
	compatJSONRequest(t, app, http.MethodPost, "/api/mcp/oauth/check-status", "other-user", map[string]any{
		"server_url": "https://8.8.8.8/mcp",
	}, http.StatusNotFound)
}

func TestP4WebMCPBridgeStdioHelper(t *testing.T) {
	if os.Getenv("SYNON_P4_MCP_HELPER") != "1" {
		return
	}
	if os.Getenv("P4_MCP_SECRET") == "" {
		os.Exit(12)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			JSONRPC string `json:"jsonrpc"`
			ID      any    `json:"id"`
			Method  string `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		switch request.Method {
		case "server/discover":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"error": map[string]any{"code": -32601, "message": "Method not found"},
			})
		case "initialize":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
					"serverInfo": map[string]any{"name": "p4-go-fixture", "version": "1.0.0"},
				},
			})
		case "notifications/initialized":
		case "tools/list":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"tools": []any{map[string]any{
					"name": "echo", "description": "real Go stdio fixture",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
				}}},
			})
		case "tools/call":
			_ = encoder.Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"content": []any{map[string]any{
					"type": "text", "text": "p4 runtime ok",
				}}},
			})
		}
	}
	os.Exit(0)
}

func assertP4MCPSecretAbsentFromFiles(t *testing.T, root, secret string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(raw), secret) {
			t.Fatalf("MCP secret leaked to %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
