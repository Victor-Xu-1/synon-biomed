package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

func TestMCPPermissionsOAuthCompatibilityAndRuntimeEnforcement(t *testing.T) {
	root := t.TempDir()
	mcpURL, mcpClient, toolCalls := workspaceMCPCompatibilityFixture(t)
	databasePath := filepath.Join(root, "workspace.db")
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Workspace: store, FileRoot: root, HTTPClient: mcpClient})
	app := srv.Handler()

	createdAgent := agentCompatRequest(t, app, http.MethodPost, "/api/agents", map[string]any{
		"name": "mcp_research", "displayName": "MCP Research", "description": "runtime fixture",
		"systemPrompt": "Use the attached MCP connector.", "skillNames": []string{}, "enabled": true,
	})
	if createdAgent.Code != http.StatusOK {
		t.Fatalf("create agent status=%d body=%s", createdAgent.Code, createdAgent.Body.String())
	}
	created := runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers", "local", map[string]any{
		"name": "workspace-fixture", "description": "real protocol fixture",
		"url": mcpURL, "transport": "streamable_http",
	}, http.StatusCreated)
	serverID := created["id"].(string)
	runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers/"+serverID+"/attach", "local", map[string]any{
		"agent_names": []string{"MCP_RESEARCH"},
	}, http.StatusOK)

	agentServers := agentCompatRequest(t, app, http.MethodGet, "/api/agents/MCP_RESEARCH/mcp-servers?include_tools=true", nil)
	if agentServers.Code != http.StatusOK {
		t.Fatalf("agent MCP servers status=%d body=%s", agentServers.Code, agentServers.Body.String())
	}
	var listedServers []map[string]any
	if err := json.Unmarshal(agentServers.Body.Bytes(), &listedServers); err != nil {
		t.Fatal(err)
	}
	if len(listedServers) != 1 || listedServers[0]["id"] != serverID {
		t.Fatalf("agent MCP servers=%#v", listedServers)
	}
	listedTools := listedServers[0]["tools"].([]any)
	if len(listedTools) != 1 || listedTools[0].(map[string]any)["toolName"] != "echo" {
		t.Fatalf("agent MCP tools=%#v", listedTools)
	}

	permissions := runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+serverID+"/tool-permissions", "local", nil, http.StatusOK)
	tools := permissions["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["toolName"] != "echo" || tools[0].(map[string]any)["state"] != nil {
		t.Fatalf("initial tool permissions=%#v", permissions)
	}
	missing := runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/00000000-0000-0000-0000-000000000301/tool-permissions", "local", nil, http.StatusOK)
	if missing["skipApprovalsActive"] != false || missing["error"] == nil || len(missing["tools"].([]any)) != 0 {
		t.Fatalf("missing connector permissions=%#v", missing)
	}

	runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers/"+serverID+"/tool-grants", "local", map[string]any{
		"toolName": "echo", "decision": "deny",
	}, http.StatusOK)
	permissions = runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+serverID+"/tool-permissions", "local", nil, http.StatusOK)
	if got := permissions["tools"].([]any)[0].(map[string]any)["state"]; got != "deny" {
		t.Fatalf("deny permission state=%v", got)
	}
	assertMCPGrantList(t, app, serverID, "deny")

	if _, err := store.CreateProject(workspace.CreateProjectInput{ID: "project-mcp", UserID: "local", Name: "MCP", Path: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-mcp", ProjectID: "project-mcp", AgentName: "MCP_RESEARCH",
		Status: "processing", ConversationType: "agent", Name: "MCP runtime",
	}); err != nil {
		t.Fatal(err)
	}
	gateway := serverAgentRuntimeToolGateway{
		server: srv, sessionID: "frame-mcp", allowedTools: []string{"mcp__workspace-fixture__echo"},
	}
	denied, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "call-denied", Name: "mcp__workspace-fixture__echo", Arguments: json.RawMessage(`{"text":"blocked"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	deniedBody := denied.Value.(map[string]any)
	if deniedBody["decision"] != "denied" || deniedBody["policySource"] != "mcp-server" || toolCalls.Load() != 0 {
		t.Fatalf("runtime deny=%#v calls=%d", deniedBody, toolCalls.Load())
	}

	runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers/"+serverID+"/tool-grants", "local", map[string]any{
		"toolName": "echo", "decision": "allow",
	}, http.StatusOK)
	expiresAt := time.Now().UTC().Truncate(time.Second).Add(time.Hour)
	if err := store.UpsertMCPOAuthStatus(workspace.MCPOAuthStatusInput{
		MCPServerID: serverID, UserID: "local", AccessTokenRef: "vault://mcp/workspace-fixture",
		TokenType: "Bearer", ExpiresAt: &expiresAt, Scopes: []string{"read", "write"},
	}); err != nil {
		t.Fatal(err)
	}

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
	gateway = serverAgentRuntimeToolGateway{
		server: srv, sessionID: "frame-mcp", allowedTools: []string{"mcp__workspace-fixture__echo"},
	}
	assertMCPGrantList(t, app, serverID, "allow")
	runtimeContext, found, err := srv.workspaceMCPRuntimeContext("frame-mcp")
	if err != nil || !found || len(runtimeContext.Servers) != 1 {
		t.Fatalf("workspace MCP runtime context found=%v err=%v value=%#v", found, err, runtimeContext)
	}
	discovered, err := srv.discoverCompatMCPTools(context.Background(), runtimeContext.Servers[0])
	if err != nil || len(discovered) != 1 {
		t.Fatalf("workspace MCP discovery err=%v tools=%#v", err, discovered)
	}
	schemas := srv.agentRuntimeToolSchemas([]string{"mcp__workspace-fixture__echo"}, "frame-mcp")
	if !hasAgentRuntimeSchema(schemas, "mcp__workspace-fixture__echo") {
		t.Fatalf("workspace MCP schema missing: %#v", schemas)
	}
	allowed, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "call-allowed", Name: "mcp__workspace-fixture__echo", Arguments: json.RawMessage(`{"text":"allowed"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	allowedBody := allowed.Value.(map[string]any)
	if allowedBody["ok"] != true || allowedBody["result"] != "workspace ok" || toolCalls.Load() != 1 {
		t.Fatalf("runtime allow=%#v calls=%d", allowedBody, toolCalls.Load())
	}
	if _, err := store.SetAgentConnectorToolExclusions("local", "MCP_RESEARCH", serverID, []string{"echo"}); err != nil {
		t.Fatal(err)
	}
	if schemas := srv.agentRuntimeToolSchemas([]string{"mcp__workspace-fixture__echo"}, "frame-mcp"); hasAgentRuntimeSchema(schemas, "mcp__workspace-fixture__echo") {
		t.Fatalf("excluded workspace MCP tool remained model-visible: %#v", schemas)
	}
	excluded, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "call-excluded", Name: "mcp__workspace-fixture__echo", Arguments: json.RawMessage(`{"text":"excluded"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	excludedBody := excluded.Value.(map[string]any)
	if excludedBody["decision"] != "denied" || toolCalls.Load() != 1 {
		t.Fatalf("runtime exclusion=%#v calls=%d", excludedBody, toolCalls.Load())
	}
	if _, err := store.SetAgentConnectorToolExclusions("local", "MCP_RESEARCH", serverID, nil); err != nil {
		t.Fatal(err)
	}

	status := runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+serverID+"/oauth/status", "local", nil, http.StatusOK)
	if status["is_connected"] != true || status["mcp_server_id"] != serverID || len(status["scopes"].([]any)) != 2 {
		t.Fatalf("connected OAuth status=%#v", status)
	}
	runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+serverID+"/oauth/status", "other-user", nil, http.StatusNotFound)
	disconnected := runtimeCompatJSON(t, app, http.MethodDelete, "/api/mcp-servers/"+serverID+"/oauth/disconnect", "local", nil, http.StatusOK)
	if disconnected["success"] != true || disconnected["deleted"] != true {
		t.Fatalf("OAuth disconnect=%#v", disconnected)
	}
	disconnected = runtimeCompatJSON(t, app, http.MethodDelete, "/api/mcp-servers/"+serverID+"/oauth/disconnect", "local", nil, http.StatusOK)
	if disconnected["deleted"] != false {
		t.Fatalf("repeated OAuth disconnect=%#v", disconnected)
	}
	status = runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+serverID+"/oauth/status", "local", nil, http.StatusOK)
	if status["is_connected"] != false || status["expires_at"] != nil || status["scopes"] != nil {
		t.Fatalf("disconnected OAuth status=%#v", status)
	}

	runtimeCompatJSON(t, app, http.MethodPost, "/api/mcp-servers/"+serverID+"/tool-grants", "local", map[string]any{
		"toolName": "echo", "decision": nil,
	}, http.StatusOK)
	assertMCPGrantList(t, app, serverID, "")
	runtimeCompatJSON(t, app, http.MethodGet, "/api/mcp-servers/"+serverID+"/tool-grants", "other-user", nil, http.StatusNotFound)
}

func assertMCPGrantList(t *testing.T, app http.Handler, serverID, expected string) {
	t.Helper()
	response := agentCompatRequest(t, app, http.MethodGet, "/api/mcp-servers/"+serverID+"/tool-grants", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("list grants status=%d body=%s", response.Code, response.Body.String())
	}
	var grants []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &grants); err != nil {
		t.Fatal(err)
	}
	if expected == "" {
		if len(grants) != 0 {
			t.Fatalf("grants=%#v, want empty", grants)
		}
		return
	}
	if len(grants) != 1 || grants[0]["toolName"] != "echo" || grants[0]["decision"] != expected {
		t.Fatalf("grants=%#v, want %s", grants, expected)
	}
}

func hasAgentRuntimeSchema(schemas []agentruntime.ToolSchema, name string) bool {
	for _, schema := range schemas {
		if schema.Name == name {
			return true
		}
	}
	return false
}

func workspaceMCPCompatibilityFixture(t *testing.T) (string, *http.Client, *atomic.Int64) {
	t.Helper()
	var toolCalls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode MCP request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch request.Method {
		case "server/discover":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"error": map[string]any{"code": -32601, "message": "Method not found"},
			})
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Mcp-Session-Id", "workspace-session")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05", "capabilities": map[string]any{},
					"serverInfo": map[string]any{"name": "workspace-fixture", "version": "1.0.0"},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"tools": []any{map[string]any{
					"name": "echo", "description": "echo input",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
				}}},
			})
		case "tools/call":
			toolCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": request.ID,
				"result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "workspace ok"}}},
			})
		default:
			t.Errorf("unexpected MCP method %q", request.Method)
			http.Error(w, "unexpected method", http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport.(*http.Transport).Clone()
	baseDial := transport.DialContext
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return baseDial(ctx, network, serverURL.Host)
	}
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = serverURL.Hostname()
	client := *server.Client()
	client.Transport = transport
	return "https://8.8.8.8:" + port, &client, &toolCalls
}
