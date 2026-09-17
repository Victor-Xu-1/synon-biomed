package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	runtimekv "synon-go/internal/persistence/runtimekv"
	workspace "synon-go/internal/persistence/workspace"
	pluginhost "synon-go/internal/plugins/host"
	"synon-go/internal/providers"
	"synon-go/internal/skills"
	"synon-go/internal/tools/mcpstdio"
)

func TestServerAgentRuntimeBridgeRunsModelToolCallAgainstRealToolGateway(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		switch sequence {
		case 1:
			tools, ok := request["tools"].([]any)
			if !ok || !hasChatToolNamed(tools, "task_create") {
				t.Fatalf("first request missing task_create tool: %#v", request["tools"])
			}
			if hasChatToolNamed(tools, "file_write") {
				t.Fatalf("file_write should not be advertised by allowlisted runtime bridge: %#v", request["tools"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_task_1",
							"type": "function",
							"function": {
								"name": "task_create",
								"arguments": "{\"title\":\"bridge-created task\"}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages := request["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			if last["role"] != "tool" || last["tool_call_id"] != "call_task_1" || !strings.Contains(last["content"].(string), "bridge-created task") {
				t.Fatalf("second request missing real tool result: %#v", last)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {"role": "assistant", "content": "created through server bridge"}
				}]
			}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	engine := srv.newAgentRuntimeEngine(SessionRunnerChatOptions{
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "bridge-model",
		AllowedTools:     []string{"task_create"},
		RequestTimeout:   time.Minute,
		OutputLimitBytes: 64 * 1024,
	})

	result, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "create a task"}},
		Tools:         srv.agentRuntimeToolSchemas([]string{"task_create"}),
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalMessage.Content != "created through server bridge" || requests.Load() != 2 {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}

	tasks, err := srv.executeTaskTool("task_list", map[string]any{})
	if err != nil {
		t.Fatalf("task_list error = %v", err)
	}
	rawTasks, err := json.Marshal(tasks)
	if err != nil {
		t.Fatalf("marshal task_list result: %v", err)
	}
	if !strings.Contains(string(rawTasks), "bridge-created task") {
		t.Fatalf("task_create did not run through bridge: %s", rawTasks)
	}
}

func TestStaticRunnerModelAuditRedactsCredentialsAndBoundsErrors(t *testing.T) {
	profile := providers.ModelProfile{
		Provider: providers.ProviderProfile{
			Endpoint: "https://audit-user:audit-password@example.test/v1/chat/completions?api_key=query-secret#fragment",
		},
		APIKey: "header-secret",
	}
	message := staticRunnerModelAuditError(
		errors.New(profile.Provider.Endpoint+" header-secret "+strings.Repeat("x", 4096)),
		profile,
	)
	if strings.Contains(message, "audit-user") || strings.Contains(message, "audit-password") ||
		strings.Contains(message, "query-secret") || strings.Contains(message, "header-secret") {
		t.Fatalf("audit error leaked credentials: %q", message)
	}
	if !strings.Contains(message, "https://example.test/v1/chat/completions") || len([]rune(message)) > 2051 {
		t.Fatalf("audit error was not safely redacted and bounded: %q", message)
	}
}

func TestAgentRuntimeMCPToolListSchemasPreserveHealthyToolsAndReportPartialFailure(t *testing.T) {
	list := mcpstdio.ToolListResult{
		Servers: []mcpstdio.ServerProjection{
			{Name: "healthy", Status: "connected", Configured: true, ToolCount: 1},
			{Name: "private-connector", Status: "failed", Configured: true, Error: "dial https://secret.example.test: connection refused"},
		},
		Tools: []mcpstdio.ToolProjection{{
			Name: "mcp__healthy__echo", Server: "healthy", ToolName: "echo", Description: "Echo input.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		}},
	}

	schemas, unavailable := agentRuntimeMCPToolListSchemas(list, nil)
	if !unavailable {
		t.Fatal("partial MCP discovery failure was treated as healthy")
	}
	if len(schemas) != 1 || schemas[0].Name != "mcp__healthy__echo" {
		t.Fatalf("healthy MCP schemas were not preserved: %#v", schemas)
	}
}

func TestAgentRuntimeMCPDiscoveryFailureReturnsFixedRecoverableDiagnostic(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), []byte(`{"mcpServers":`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	t.Setenv("SYNON_MCP_CONFIG", filepath.Join(root, ".mcp.json"))
	srv := newV11TestServer(t, Options{FileRoot: root})
	schemas := srv.agentRuntimeMCPToolSchemas(context.Background(), nil)
	var diagnostic agentruntime.ToolSchema
	for _, schema := range schemas {
		if schema.Name == agentRuntimeMCPUnavailableToolName {
			diagnostic = schema
			break
		}
	}
	if diagnostic.Name == "" {
		t.Fatalf("MCP discovery failure did not produce an admission diagnostic: %#v", schemas)
	}
	if diagnostic.Description != agentRuntimeMCPUnavailableDescription || strings.Contains(diagnostic.Description, root) || strings.Contains(diagnostic.Description, ".mcp.json") {
		t.Fatalf("MCP diagnostic was not fixed and sanitized: %#v", diagnostic)
	}
	if diagnostic.Parameters["additionalProperties"] != false {
		t.Fatalf("MCP diagnostic schema accepts unexpected input: %#v", diagnostic.Parameters)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, toolSchemas: schemas, hasToolSnapshot: true}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "mcp-unavailable", Name: agentRuntimeMCPUnavailableToolName, Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := result.Value.(map[string]any)
	if !ok || value["ok"] != false || value["code"] != "mcp_schema_discovery_unavailable" || value["recoverable"] != true || value["recovery"] != "retry_after_connector_recovery_or_inspect_mcp_settings" {
		t.Fatalf("MCP recovery diagnostic result = %#v", result.Value)
	}
	encoded := fmt.Sprintf("%#v", value)
	if strings.Contains(encoded, root) || strings.Contains(encoded, ".mcp.json") {
		t.Fatalf("MCP recovery diagnostic leaked discovery details: %s", encoded)
	}
	if filtered := srv.agentRuntimeMCPToolSchemas(context.Background(), map[string]struct{}{"Read": {}}); agentRuntimeToolSchemaNamed(filtered, agentRuntimeMCPUnavailableToolName) {
		t.Fatalf("MCP diagnostic escaped a non-MCP tool boundary: %#v", filtered)
	}
}

func TestAgentRuntimeTodoWriteSchemaMatchesExecutionContract(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	var found agentruntime.ToolSchema
	for _, schema := range srv.agentRuntimeToolSchemas([]string{"TodoWrite"}) {
		if schema.Name == "TodoWrite" {
			found = schema
			break
		}
	}
	if found.Name == "" {
		t.Fatal("TodoWrite schema was not exposed")
	}
	properties, ok := found.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("TodoWrite properties = %#v", found.Parameters["properties"])
	}
	todos, ok := properties["todos"].(map[string]any)
	if !ok {
		t.Fatalf("TodoWrite todos = %#v", properties["todos"])
	}
	items, ok := todos["items"].(map[string]any)
	if !ok || items["additionalProperties"] != false {
		t.Fatalf("TodoWrite item schema = %#v", todos["items"])
	}
	itemProperties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("TodoWrite item properties = %#v", items["properties"])
	}
	status, ok := itemProperties["status"].(map[string]any)
	if !ok || !reflect.DeepEqual(status["enum"], []string{"pending", "in_progress", "completed"}) {
		t.Fatalf("TodoWrite status schema = %#v", itemProperties["status"])
	}
	if !reflect.DeepEqual(items["required"], []string{"content", "status", "activeForm"}) {
		t.Fatalf("TodoWrite required fields = %#v", items["required"])
	}
}

func TestServerAgentRuntimeSchemaSnapshotIsExecutionAuthority(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	gateway := serverAgentRuntimeToolGateway{
		server: srv, toolSchemas: []agentruntime.ToolSchema{
			{Name: "ToolSearch"}, {Name: "Read"},
		}, hasToolSnapshot: true, suppressHooks: true,
	}
	for _, call := range []agentruntime.ToolCall{
		{ID: "snapshot-bash", Name: "Bash", Arguments: json.RawMessage(`{"command":"echo forbidden"}`)},
		{ID: "snapshot-wait", Name: "wait_for_notification", Arguments: json.RawMessage(`{}`)},
	} {
		result, err := gateway.Execute(context.Background(), call)
		if err != nil {
			t.Fatal(err)
		}
		blocked := mapValue(result.Value)
		if blocked["ok"] != false ||
			(call.Name == "Bash" && blocked["code"] != "retired_tool") ||
			(call.Name != "Bash" && blocked["code"] != "tool_not_in_snapshot") {
			t.Fatalf("snapshot-external call %q result=%#v", call.Name, blocked)
		}
	}
	aliasGateway := serverAgentRuntimeToolGateway{
		server: srv, allowedTools: []string{"Python"},
		toolSchemas: []agentruntime.ToolSchema{{Name: "python"}}, hasToolSnapshot: true,
	}
	if !aliasGateway.toolAllowed("python") || aliasGateway.toolAllowed("Python") || aliasGateway.toolAllowed("Bash") {
		t.Fatalf("snapshot canonical authority was not enforced")
	}
	aliasResult, err := aliasGateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "snapshot-alias", Name: "Python", Arguments: json.RawMessage(`{"code":"print('forbidden alias')"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	aliasBlocked := mapValue(aliasResult.Value)
	if aliasBlocked["ok"] != false || aliasBlocked["code"] != "retired_tool" {
		t.Fatalf("snapshot alias execution was not blocked: %#v", aliasBlocked)
	}
	for name, paddedGateway := range map[string]serverAgentRuntimeToolGateway{
		"snapshot": {
			server: srv, toolSchemas: []agentruntime.ToolSchema{{Name: "ask_user"}}, hasToolSnapshot: true,
		},
		"legacy": {
			server: srv, allowedTools: []string{"ask_user"},
		},
	} {
		paddedResult, paddedErr := paddedGateway.Execute(context.Background(), agentruntime.ToolCall{
			ID: "padded-ask-user-" + name, Name: " ask_user ", Arguments: json.RawMessage(`{}`),
		})
		if paddedErr != nil {
			t.Fatal(paddedErr)
		}
		paddedBlocked := mapValue(paddedResult.Value)
		if paddedBlocked["ok"] != false || paddedBlocked["error"] != "tool name is invalid" {
			t.Fatalf("%s padded tool name was not rejected: %#v", name, paddedBlocked)
		}
	}
}

func TestServerAgentRuntimeRejectsLegacyListMCPAliasEvenWithCanonicalSnapshot(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	allowed := serverAgentRuntimeToolGateway{
		server: srv, toolSchemas: []agentruntime.ToolSchema{{Name: "ListMcpTools"}},
		hasToolSnapshot: true, suppressHooks: true,
	}
	if allowed.AdmitsToolCall(agentruntime.ToolCall{ID: "legacy-list-mcp", Name: "ListMcpToolsTool", Arguments: json.RawMessage(`{}`)}) {
		t.Fatal("retired ListMcpToolsTool alias was admitted by a canonical snapshot")
	}
	blocked := serverAgentRuntimeToolGateway{
		server: srv, toolSchemas: []agentruntime.ToolSchema{{Name: "Read"}},
		hasToolSnapshot: true, suppressHooks: true,
	}
	if blocked.AdmitsToolCall(agentruntime.ToolCall{ID: "legacy-list-mcp-blocked", Name: "ListMcpToolsTool", Arguments: json.RawMessage(`{}`)}) {
		t.Fatal("legacy ListMcpToolsTool alias must not broaden a snapshot without canonical ListMcpTools authority")
	}
	result, err := blocked.Execute(context.Background(), agentruntime.ToolCall{
		ID: "legacy-list-mcp-blocked", Name: "ListMcpToolsTool", Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := mapValue(result.Value)
	if payload["ok"] != false || payload["code"] != "retired_tool" {
		t.Fatalf("blocked legacy alias result=%#v", payload)
	}
}

func TestServerAgentRuntimeSkillDiscoveryDoesNotAdvertiseForbiddenToolRequirements(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	gateway := serverAgentRuntimeToolGateway{
		server: srv, allowedTools: []string{"search_skills", "skill", "read_file"}, suppressHooks: true,
	}
	search, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "authority-skill-search", Name: "search_skills",
		Arguments: json.RawMessage(`{"query":"select:synon-runtime"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := mapValue(search.Value)
	if matches := stringArrayValue(payload["matches"]); len(matches) != 0 {
		t.Fatalf("authority-scoped skill matches = %#v payload=%#v", matches, payload)
	}
	if missing := stringArrayValue(payload["missing_skills"]); !reflect.DeepEqual(missing, []string{"synon-runtime"}) {
		t.Fatalf("authority-scoped missing skills = %#v payload=%#v", missing, payload)
	}

	invocation, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "authority-skill", Name: "skill", Arguments: json.RawMessage(`{"skill":"synon-runtime"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	denied := mapValue(invocation.Value)
	if denied["ok"] != false || denied["error"] != "skill requires tools unavailable to this agent runtime" {
		t.Fatalf("authority-scoped Skill result = %#v", denied)
	}
}

func TestServerAgentRuntimeBundledGenMolSkillUsesAdmittedDiscoveryTools(t *testing.T) {
	catalog := skills.Load([]string{filepath.Join("..", "..", "skills", "synonbiomed")})
	if loadErrors := catalog.LoadErrors(); len(loadErrors) != 0 {
		t.Fatalf("load bundled skills: %#v", loadErrors)
	}
	srv := New(Options{FileRoot: t.TempDir(), SkillCatalog: catalog})
	gateway := serverAgentRuntimeToolGateway{
		server:        srv,
		allowedTools:  []string{"search_skills", "skill", "ask_user"},
		suppressHooks: true,
	}

	search, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "genmol-search", Name: "search_skills", Arguments: json.RawMessage(`{"query":"select:genmol-nim"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	searchPayload := mapValue(search.Value)
	if matches := stringArrayValue(searchPayload["matches"]); !reflect.DeepEqual(matches, []string{"genmol-nim"}) {
		t.Fatalf("genmol skill matches=%#v payload=%#v", matches, searchPayload)
	}

	invocation, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "genmol-invoke", Name: "skill", Arguments: json.RawMessage(`{"skill":"genmol-nim","args":"generate diverse ligands"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt, ok := invocation.Value.(string)
	if !ok || !strings.Contains(prompt, `<skill-metadata name="genmol-nim"`) ||
		!strings.Contains(strings.ToLower(prompt), "do not attempt an unadvertised shell") {
		t.Fatalf("genmol invocation=%#v", invocation.Value)
	}
}

func TestServerAgentRuntimeSkillDiscoveryUsesExactRequestAuthority(t *testing.T) {
	root := t.TempDir()
	skillRoot := filepath.Join(root, "skills")
	for name, body := range map[string]string{
		"host-model-analysis": "Standard tools: host.llm\n\nUse the active request model.",
		"missing-dynamic-mcp": "Standard tools: mcp__missing__query\n\nUse the requested connector.",
		"exact-web-alias":     "Standard tools: web_search\n\nUse the exact web_search tool.",
	} {
		directory := filepath.Join(skillRoot, name)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		content := "---\nname: " + name + "\ndescription: " + name + "\n---\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	srv := New(Options{FileRoot: root, SkillDirectories: []string{skillRoot}})

	tests := []struct {
		name         string
		skill        string
		allowedTools []string
		wantMatch    bool
	}{
		{name: "active request model satisfies host capability without saved provider", skill: "host-model-analysis", allowedTools: []string{"search_skills", "skill"}, wantMatch: true},
		{name: "allowed name without a real dynamic schema stays hidden", skill: "missing-dynamic-mcp", allowedTools: []string{"search_skills", "skill", "mcp__missing__query"}},
		{name: "different executable alias does not broaden authority", skill: "exact-web-alias", allowedTools: []string{"search_skills", "skill", "WebSearch"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: test.allowedTools, suppressHooks: true}
			result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
				ID: "authority-" + test.skill, Name: "search_skills",
				Arguments: json.RawMessage(`{"query":"select:` + test.skill + `"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			payload := mapValue(result.Value)
			matches := stringArrayValue(payload["matches"])
			matched := reflect.DeepEqual(matches, []string{test.skill})
			if matched != test.wantMatch {
				t.Fatalf("matches=%#v payload=%#v", matches, payload)
			}
			if !test.wantMatch {
				missing := stringArrayValue(payload["missing_skills"])
				if !reflect.DeepEqual(missing, []string{test.skill}) {
					t.Fatalf("missing=%#v payload=%#v", missing, payload)
				}
			}
		})
	}
}

func TestServerAgentRuntimeGatewayPropagatesGenerationStopCause(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	gateway := serverAgentRuntimeToolGateway{
		server: srv, allowedTools: []string{"Sleep"}, suppressHooks: true,
	}
	type gatewayReturn struct {
		result agentruntime.ToolResult
		err    error
	}
	ctx, cancel := context.WithCancelCause(context.Background())
	done := make(chan gatewayReturn, 1)
	go func() {
		result, err := gateway.Execute(ctx, agentruntime.ToolCall{
			ID: "cancelled-sleep", Name: "Sleep", Arguments: json.RawMessage(`{"durationMs":60000}`),
		})
		done <- gatewayReturn{result: result, err: err}
	}()
	time.Sleep(25 * time.Millisecond)
	cancel(fmt.Errorf("%w: test stop", ErrGenerationStopped))
	select {
	case returned := <-done:
		if !errors.Is(returned.err, ErrGenerationStopped) {
			t.Fatalf("gateway cancellation error = %v result=%#v", returned.err, returned.result)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled tool gateway did not settle")
	}
	entries, err := srv.runtimeStore.List(toolGatewayAuditNamespace)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		value := mapValue(entry.Value)
		if value["toolCallId"] == "cancelled-sleep" && value["status"] == "cancelled" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("cancelled gateway audit not found: %#v", entries)
	}
}

func TestAgentRuntimeToolSchemasExposeAllowedDynamicMCPTools(t *testing.T) {
	root := t.TempDir()
	client := configureMCPHTTPFixture(t, root)
	srv := newV11TestServer(t, Options{FileRoot: root, HTTPClient: client})

	schemas := srv.agentRuntimeToolSchemas([]string{"mcp__remote__echo"})
	var found *agentruntime.ToolSchema
	for i := range schemas {
		if schemas[i].Name == "mcp__remote__echo" {
			found = &schemas[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("dynamic MCP schema not exposed: %#v", schemas)
	}
	if !strings.Contains(found.Description, "Echo remote MCP text") {
		t.Fatalf("dynamic MCP schema description = %q", found.Description)
	}
	properties, ok := found.Parameters["properties"].(map[string]any)
	if !ok || properties["text"] == nil {
		t.Fatalf("dynamic MCP schema parameters = %#v", found.Parameters)
	}
	if !agentRuntimeToolNeedsApproval("mcp__remote__echo") {
		t.Fatal("dynamic MCP tools must require the agent runtime approval gate")
	}
}

func TestAgentRuntimeToolSchemasDisableFlattenedMCPDiscovery(t *testing.T) {
	root := t.TempDir()
	client := configureMCPHTTPFixture(t, root)
	srv := newV11TestServer(t, Options{FileRoot: root, HTTPClient: client})

	schemas := srv.agentRuntimeToolSchemasWithContextOptions(
		context.Background(), []string{"mcp__remote__echo"}, true,
	)
	for _, schema := range schemas {
		if strings.HasPrefix(strings.ToLower(schema.Name), "mcp__") {
			t.Fatalf("flattened MCP schema remained in canonical Session Runner snapshot: %#v", schema)
		}
	}
}

func TestAgentRuntimeToolSchemasKeepStructuredOutputPassthrough(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})

	schemas := srv.agentRuntimeToolSchemas([]string{"StructuredOutput"})
	var found *agentruntime.ToolSchema
	for i := range schemas {
		if schemas[i].Name == "StructuredOutput" {
			found = &schemas[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("StructuredOutput schema not exposed: %#v", schemas)
	}
	if found.Parameters["additionalProperties"] == false {
		t.Fatalf("StructuredOutput must preserve original passthrough payload schema: %#v", found.Parameters)
	}
}

func configureMCPHTTPFixture(t *testing.T, root string) *http.Client {
	t.Helper()
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	configPath := filepath.Join(root, ".mcp.json")
	t.Setenv("SYNON_MCP_CONFIG", configPath)
	mcpServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		switch req["method"] {
		case "server/discover":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req["id"],
				"error": map[string]any{"code": -32601, "message": "Method not found"},
			})
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "remote-fixture", "version": "1.0.0"},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]any{"tools": []any{map[string]any{
					"name":        "echo",
					"description": "Echo remote MCP text for agent runtime.",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
						"required":   []any{"text"},
					},
				}}},
			})
		case "tools/call":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]any{"content": []any{map[string]any{"type": "text", "text": "remote ok"}}},
			})
		default:
			t.Fatalf("unexpected MCP method %v", req["method"])
		}
	}))
	t.Cleanup(mcpServer.Close)
	advertisedURL, client := mcpDirectoryPublicTLSClient(t, mcpServer)
	raw := map[string]any{
		"mcpServers": map[string]any{
			"remote": map[string]any{
				"type": "http",
				"url":  advertisedURL,
			},
		},
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal MCP config: %v", err)
	}
	if err := os.WriteFile(configPath, encoded, 0o600); err != nil {
		t.Fatalf("write MCP config: %v", err)
	}
	return client
}

func configureMCPHTTPPolicyFixture(t *testing.T, root string, policy map[string]any, calls *atomic.Int64) {
	t.Helper()
	t.Setenv("SYNON_MCP_CONFIG_JSON", "")
	configPath := filepath.Join(root, ".mcp.json")
	t.Setenv("SYNON_MCP_CONFIG", configPath)
	mcpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		switch req["method"] {
		case "server/discover":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0", "id": req["id"],
				"error": map[string]any{"code": -32601, "message": "Method not found"},
			})
		case "initialize":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{},
					"serverInfo":      map[string]any{"name": "policy-fixture", "version": "1.0.0"},
				},
			})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result": map[string]any{"tools": []any{map[string]any{
					"name":        "echo",
					"description": "Echo remote MCP text for policy tests.",
					"inputSchema": map[string]any{
						"type":       "object",
						"properties": map[string]any{"text": map[string]any{"type": "string"}},
					},
				}}},
			})
		case "tools/call":
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      req["id"],
				"result":  map[string]any{"content": []any{map[string]any{"type": "text", "text": "policy call ok"}}},
			})
		default:
			t.Fatalf("unexpected MCP method %v", req["method"])
		}
	}))
	t.Cleanup(mcpServer.Close)
	serverConfig := map[string]any{
		"type": "http",
		"url":  mcpServer.URL,
	}
	for key, value := range policy {
		serverConfig[key] = value
	}
	raw := map[string]any{"mcpServers": map[string]any{"policy": serverConfig}}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal MCP config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".mcp.json"), encoded, 0o600); err != nil {
		t.Fatalf("write MCP config: %v", err)
	}
}

func TestToolsAPIFullAccessAutoApprovesMCPServerConfirmationPolicy(t *testing.T) {
	root := t.TempDir()
	var calls atomic.Int64
	configureMCPHTTPPolicyFixture(t, root, map[string]any{
		"permissionPolicy": "confirm",
		"requireReason":    true,
	}, &calls)
	srv := newV11TestServer(t, Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "allow"}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	input := map[string]any{
		"server":   "policy",
		"toolName": "echo",
		"input":    map[string]any{"text": "run without frontend approval"},
	}
	permission := srv.agentRuntimePermissionResultForSessionAndSource(
		"", "MCPTool", agentruntime.ToolCall{ID: "full-access-mcp", Name: "MCPTool"}, input, "direct-http",
	)
	if permission != nil {
		t.Fatalf("full access returned an approval request = %#v", permission)
	}
	if calls.Load() != 0 {
		t.Fatalf("permission resolution executed MCP prematurely, calls=%d", calls.Load())
	}
	entries, err := srv.runtimeStore.List(agentRuntimeApprovalNamespace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("full access persisted frontend approvals=%#v err=%v", entries, err)
	}
}

func TestServerAgentRuntimeDynamicMCPToolUsesServerDenyPolicy(t *testing.T) {
	root := t.TempDir()
	var calls atomic.Int64
	configureMCPHTTPPolicyFixture(t, root, map[string]any{"permissionPolicy": "deny"}, &calls)
	srv := newV11TestServer(t, Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "allow"}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"mcp__policy__echo"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_mcp_policy_deny",
		Name:      "mcp__policy__echo",
		Arguments: json.RawMessage(`{"text":"blocked"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	payload := result.Value.(map[string]any)
	if payload["decision"] != "denied" || payload["policySource"] != "mcp-server" || payload["mcpServer"] != "policy" {
		t.Fatalf("dynamic MCP deny payload = %#v", payload)
	}
	if calls.Load() != 0 {
		t.Fatalf("MCP tool was called despite deny policy, calls=%d", calls.Load())
	}
}

func TestConversationDenyOverridesMCPServerAllowPolicy(t *testing.T) {
	srv := newV11TestServer(t, Options{FileRoot: t.TempDir()})
	result := srv.agentRuntimeMCPServerPermissionResult(
		mcpServerPermissionPolicy{Server: "policy", Tool: "echo", Mode: "allow"},
		"deny", "MCPTool", agentruntime.ToolCall{ID: "deny-mcp", Name: "MCPTool"},
		map[string]any{"server": "policy", "toolName": "echo"}, "agent-runtime",
	)
	if result == nil || result["decision"] != "denied" || result["policySource"] != "conversation" {
		t.Fatalf("conversation deny was bypassed by MCP allow policy: %#v", result)
	}
}

func TestServerAgentRuntimeBridgeDeniesMutatingToolWhenApprovalDefaultsDeny(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		switch sequence {
		case 1:
			tools, ok := request["tools"].([]any)
			if !ok || !hasChatToolNamed(tools, "file_write") {
				t.Fatalf("first request missing file_write tool: %#v", request["tools"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_write_1",
							"type": "function",
							"function": {
								"name": "file_write",
								"arguments": "{\"path\":\"blocked.txt\",\"content\":\"should not be written\",\"overwrite\":true}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages := request["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			content := last["content"].(string)
			if last["role"] != "tool" || last["tool_call_id"] != "call_write_1" || !strings.Contains(content, "permission denied") {
				t.Fatalf("second request missing permission denial tool result: %#v", last)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {"role": "assistant", "content": "write denied by policy"}
				}]
			}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode":              "deny",
		"autoAllowReadOnly": false,
		"requireReason":     true,
		"rememberDecisions": false,
	}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	engine := srv.newAgentRuntimeEngine(SessionRunnerChatOptions{
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "bridge-model",
		AllowedTools:     []string{"file_write"},
		RequestTimeout:   time.Minute,
		OutputLimitBytes: 64 * 1024,
	})

	result, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "write a file"}},
		Tools:         srv.agentRuntimeToolSchemas([]string{"file_write"}),
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalMessage.Content != "write denied by policy" || requests.Load() != 2 {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked file should not exist, stat err=%v", err)
	}
}

func TestServerAgentRuntimeBridgeQueuesPendingApprovalWhenApprovalDefaultsAsk(t *testing.T) {
	var requests atomic.Int64
	var approvalID string
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		switch sequence {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_write_ask_1",
							"type": "function",
							"function": {
								"name": "file_write",
								"arguments": "{\"path\":\"needs-approval.txt\",\"content\":\"pending\",\"overwrite\":true}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages := request["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			content := last["content"].(string)
			if last["role"] != "tool" || !strings.Contains(content, "pending_approval") || !strings.Contains(content, "approvalId") {
				t.Fatalf("second request missing pending approval tool result: %#v", last)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(content), &payload); err != nil {
				t.Fatalf("decode pending approval payload: %v", err)
			}
			approvalID, _ = payload["approvalId"].(string)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {"role": "assistant", "content": "waiting for approval"}
				}]
			}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode":              "ask",
		"autoAllowReadOnly": false,
		"requireReason":     true,
		"rememberDecisions": false,
	}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	engine := srv.newAgentRuntimeEngine(SessionRunnerChatOptions{
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "bridge-model",
		AllowedTools:     []string{"file_write"},
		RequestTimeout:   time.Minute,
		OutputLimitBytes: 64 * 1024,
	})

	result, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "write after approval"}},
		Tools:         srv.agentRuntimeToolSchemas([]string{"file_write"}),
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalMessage.Content != "waiting for approval" || approvalID == "" {
		t.Fatalf("result=%#v approvalID=%q", result, approvalID)
	}
	if _, err := os.Stat(filepath.Join(root, "needs-approval.txt")); !os.IsNotExist(err) {
		t.Fatalf("pending file should not exist before approval, stat err=%v", err)
	}
	entry, ok, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !ok {
		t.Fatalf("pending approval not stored ok=%v err=%v", ok, err)
	}
	value := entry.Value.(map[string]any)
	if value["status"] != "pending" || value["tool"] != "file_write" || value["decision"] != "ask" {
		t.Fatalf("pending approval value = %#v", value)
	}
}

func TestServerAgentRuntimeApprovalResponseExecutesPendingTool(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode":              "ask",
		"autoAllowReadOnly": false,
		"requireReason":     true,
		"rememberDecisions": false,
	}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"file_write"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_file_write_pending_1",
		Name:      "file_write",
		Arguments: json.RawMessage(`{"path":"approved.txt","content":"approved content","overwrite":true}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	payload := result.Value.(map[string]any)
	approvalID := stringValue(payload["approvalId"])
	if payload["decision"] != "pending_approval" || approvalID == "" {
		t.Fatalf("pending approval payload = %#v", payload)
	}
	if _, err := os.Stat(filepath.Join(root, "approved.txt")); !os.IsNotExist(err) {
		t.Fatalf("file should not exist before approval, stat err=%v", err)
	}

	resolved, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type":       "agent_runtime_approval_response",
			"approvalId": approvalID,
			"approve":    true,
			"reason":     "user approved file write",
		},
	})
	if err != nil {
		t.Fatalf("approval response error = %v", err)
	}
	resolvedMap := resolved.(map[string]any)
	if resolvedMap["success"] != true || resolvedMap["status"] != "completed" {
		t.Fatalf("approval response = %#v", resolvedMap)
	}
	written, err := os.ReadFile(filepath.Join(root, "approved.txt"))
	if err != nil {
		t.Fatalf("approved file was not written: %v", err)
	}
	if string(written) != "approved content" {
		t.Fatalf("approved file content = %q", written)
	}
	entry, ok, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !ok {
		t.Fatalf("approval entry missing ok=%v err=%v", ok, err)
	}
	value := entry.Value.(map[string]any)
	if value["status"] != "completed" || value["result"] == nil {
		t.Fatalf("completed approval value = %#v", value)
	}
	entries, err := srv.runtimeStore.List(toolGatewayAuditNamespace)
	if err != nil {
		t.Fatalf("list approved tool gateway audit entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("approved flow should record pending and approved execution audits = %#v", entries)
	}
	approvedAudit := findToolGatewayAuditValue(entries, "approved-deferred", "completed")
	if approvedAudit == nil {
		t.Fatalf("missing approved deferred execution audit: %#v", entries)
	}
	if approvedAudit["tool"] != "file_write" ||
		approvedAudit["toolCallId"] != "call_file_write_pending_1" ||
		approvedAudit["approvalId"] != approvalID ||
		approvedAudit["approvalSource"] != "agent-runtime" {
		t.Fatalf("approved deferred audit = %#v", approvedAudit)
	}
	if _, ok := approvedAudit["durationMs"].(float64); !ok {
		t.Fatalf("approved deferred audit missing normalized durationMs = %#v", approvedAudit)
	}
}

func TestServerAgentRuntimeApprovalResponsePersistsSemanticToolFailure(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	const approvalID = "approval-semantic-failure"
	if _, err := srv.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, map[string]any{
		"status": "pending", "tool": "Config", "toolCallId": "config-failure",
		"input":          map[string]any{"setting": "not-a-supported-setting", "value": true},
		"approvalSource": "agent-runtime", "rememberable": false,
	}); err != nil {
		t.Fatal(err)
	}

	resolved, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type": "agent_runtime_approval_response", "approvalId": approvalID,
			"approve": true, "reason": "exercise the semantic failure path",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := resolved.(map[string]any)
	if result["success"] != false || result["status"] != "failed" {
		t.Fatalf("approval response = %#v", result)
	}
	entry, found, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found {
		t.Fatalf("approval found=%t err=%v", found, err)
	}
	value := entry.Value.(map[string]any)
	if value["status"] != "failed" || value["error"] != "tool result reported failure" || value["result"] == nil {
		t.Fatalf("persisted approval = %#v", value)
	}
	entries, err := srv.runtimeStore.List(toolGatewayAuditNamespace)
	if err != nil {
		t.Fatal(err)
	}
	audit := findToolGatewayAuditValue(entries, "approved-deferred", "failed")
	if audit == nil || audit["toolCallId"] != "config-failure" || audit["error"] != "tool result reported failure" {
		t.Fatalf("failed deferred audit = %#v entries=%#v", audit, entries)
	}
}

func TestAgentRuntimeFailedToolValuePreservesBoundedStructuredDiagnostics(t *testing.T) {
	value := agentRuntimeFailedToolValue(map[string]any{
		"artifacts": []any{},
		"errors": []string{
			"report.md: unsupported_evidence_references (pmid:33711765)",
			"candidates.csv: unsupported_evidence_references (accession:pdb:7ACK)",
		},
	}, errors.New("save_artifacts did not publish any files"))
	if value["ok"] != false || value["error"] != "save_artifacts did not publish any files" {
		t.Fatalf("failed tool value=%#v", value)
	}
	details, ok := value["details"].(map[string]any)
	if !ok {
		t.Fatalf("failed tool details=%#v", value["details"])
	}
	diagnostics, ok := details["errors"].([]string)
	if !ok || len(diagnostics) != 2 || !strings.Contains(diagnostics[0], "pmid:33711765") ||
		!strings.Contains(diagnostics[1], "accession:pdb:7ACK") {
		t.Fatalf("failed tool diagnostics=%#v", details["errors"])
	}
}

func TestAgentRuntimeRecoverableToolErrorValueKeepsStructuralArtifactCorrectionInModelLoop(t *testing.T) {
	response := map[string]any{
		"artifacts": []any{},
		"errors": []any{map[string]any{
			"path": "evidence.csv", "code": "invalid_delimited_artifact", "retryable": true,
			"recovery": map[string]any{"action": "write_consistent_csv_or_tsv_then_retry_only_that_file"},
		}},
	}
	value, ok := agentRuntimeRecoverableToolErrorValue(
		"save_artifacts", response, agentSaveArtifactsNoResultsError(response["errors"].([]any)),
	)
	if !ok || value["partial"] != nil || value["ok"] != false || value["retryable"] != true ||
		!agentruntime.ClassifyToolResult(value).HardFailed() {
		t.Fatalf("recoverable value = %#v, ok=%t", value, ok)
	}
	if value["recovery"] != "correct_or_omit_the_failed_files_then_continue" {
		t.Fatalf("structural artifact recovery was not retained: %#v", value)
	}
	if _, ok := agentRuntimeRecoverableToolErrorValue("read_file", response, errAgentSaveArtifactsNoResults); ok {
		t.Fatal("non-save tool error was incorrectly classified as recoverable artifact correction")
	}
}

func TestServerAgentRuntimeApprovalResponseCanonicalizesPersistedAskUserAlias(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	input := map[string]any{"questions": []any{map[string]any{
		"question": "Continue?", "header": "Choice",
		"options": []any{
			askUserDecisionOption(
				"Continue", "Continue the verified run.", "Preserves progress.", "Uses the current route.",
				"Ready from the pending approval.", []any{"approval:legacy-ask-user"}, "No additional resources.",
				"The run continues from its checkpoint.", "Recommended when the current route remains valid.", true,
			),
			askUserDecisionOption(
				"Stop", "Stop before further execution.", "Avoids further work.", "Leaves the run incomplete.",
				"Ready immediately.", []any{"approval:legacy-ask-user"}, "No additional resources.",
				"The run remains stopped.", "Choose when the run should not continue.", false,
			),
		},
	}}}
	if _, err := srv.runtimeStore.Set(agentRuntimeApprovalNamespace, "legacy-ask-user", map[string]any{
		"status": "pending", "tool": "AskUserQuestion", "toolCallId": "legacy-call", "input": input,
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type": "agent_runtime_approval_response", "approvalId": "legacy-ask-user", "approve": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := resolved.(map[string]any)
	if result["success"] != true || result["status"] != "completed" || result["tool"] != "ask_user" {
		t.Fatalf("approval response = %#v", result)
	}
	entry, found, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, "legacy-ask-user")
	if err != nil || !found {
		t.Fatalf("approval found=%t err=%v", found, err)
	}
	stored := entry.Value.(map[string]any)
	if stored["status"] != "completed" || stored["tool"] != "ask_user" || stored["result"] == nil {
		t.Fatalf("stored approval = %#v", stored)
	}
}

func TestServerAgentRuntimeApprovalResponseRejectsPersistedAskUserNearName(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	if _, err := srv.runtimeStore.Set(agentRuntimeApprovalNamespace, "invalid-ask-user", map[string]any{
		"status": "pending", "tool": " ask_user", "toolCallId": "invalid-call",
		"input": map[string]any{"questions": []any{}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type": "agent_runtime_approval_response", "approvalId": "invalid-ask-user", "approve": true,
		},
	})
	if err == nil || err.Error() != "agent runtime approval invalid-ask-user is missing tool" {
		t.Fatalf("error = %v", err)
	}
	entry, found, getErr := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, "invalid-ask-user")
	if getErr != nil || !found || entry.Value.(map[string]any)["status"] != "pending" {
		t.Fatalf("approval=%#v found=%t err=%v", entry.Value, found, getErr)
	}
	if entries, listErr := srv.runtimeStore.List(toolGatewayAuditNamespace); listErr != nil || len(entries) != 0 {
		t.Fatalf("gateway audit entries=%#v err=%v", entries, listErr)
	}
	if entries, listErr := srv.runtimeStore.List(agentRuntimeHookAuditNamespace); listErr != nil || len(entries) != 0 {
		t.Fatalf("hook audit entries=%#v err=%v", entries, listErr)
	}
}

func TestServerAgentRuntimeApprovalDenialCanonicalizesPersistedAskUserAlias(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	if _, err := srv.runtimeStore.Set(agentRuntimeApprovalNamespace, "legacy-ask-user-denied", map[string]any{
		"status": "pending", "tool": "ask_user_question", "toolCallId": "legacy-denied-call",
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type": "agent_runtime_approval_response", "approvalId": "legacy-ask-user-denied", "approve": false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := resolved.(map[string]any)
	if result["success"] != true || result["status"] != "denied" {
		t.Fatalf("approval response = %#v", result)
	}
	entry, found, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, "legacy-ask-user-denied")
	if err != nil || !found {
		t.Fatalf("approval found=%t err=%v", found, err)
	}
	stored := entry.Value.(map[string]any)
	if stored["status"] != "denied" || stored["tool"] != "ask_user" {
		t.Fatalf("stored approval = %#v", stored)
	}
}

func TestServerAgentRuntimeApprovalDenialRejectsPersistedAskUserNearName(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	if _, err := srv.runtimeStore.Set(agentRuntimeApprovalNamespace, "invalid-ask-user-denied", map[string]any{
		"status": "pending", "tool": " ask_user", "toolCallId": "invalid-denied-call",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type": "agent_runtime_approval_response", "approvalId": "invalid-ask-user-denied", "approve": false,
		},
	})
	if err == nil || err.Error() != "agent runtime approval invalid-ask-user-denied is missing tool" {
		t.Fatalf("error = %v", err)
	}
	entry, found, getErr := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, "invalid-ask-user-denied")
	if getErr != nil || !found || entry.Value.(map[string]any)["status"] != "pending" {
		t.Fatalf("approval=%#v found=%t err=%v", entry.Value, found, getErr)
	}
	if entries, listErr := srv.runtimeStore.List(toolGatewayAuditNamespace); listErr != nil || len(entries) != 0 {
		t.Fatalf("gateway audit entries=%#v err=%v", entries, listErr)
	}
	if entries, listErr := srv.runtimeStore.List(agentRuntimeHookAuditNamespace); listErr != nil || len(entries) != 0 {
		t.Fatalf("hook audit entries=%#v err=%v", entries, listErr)
	}
}

func TestAgentRuntimeHookToolMatchesCanonicalAskUserAliasesOnly(t *testing.T) {
	for _, pattern := range []string{"ask_user", "AskUserQuestion", "ask_user_question"} {
		if !agentRuntimeHookToolMatches(pattern, "ask_user") {
			t.Fatalf("exact pattern %q did not match", pattern)
		}
	}
	for _, pattern := range []string{"Read, AskUserQuestion", "Read| ask_user_question", "Read; ask_user"} {
		if !agentRuntimeHookToolMatches(pattern, "ask_user") {
			t.Fatalf("list pattern %q did not match", pattern)
		}
	}
	for _, pattern := range []string{" ask_user", "ASK_USER", "\ufeffask_user", "AskUserQueſtion"} {
		if agentRuntimeHookToolMatches(pattern, "ask_user") {
			t.Fatalf("near-name pattern %q matched", pattern)
		}
	}
	for _, pattern := range []string{"Read, ASK_USER", "Read, \ufeffask_user", "Read, AskUserQuestion "} {
		if agentRuntimeHookToolMatches(pattern, "ask_user") {
			t.Fatalf("list near-name pattern %q matched", pattern)
		}
	}
}

func TestServerAgentRuntimeRememberedApprovalAllowsRepeatedApprovedTool(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode":              "ask",
		"autoAllowReadOnly": false,
		"requireReason":     true,
		"rememberDecisions": true,
	}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"file_write"}}
	call := agentruntime.ToolCall{
		ID:        "call_file_write_remembered",
		Name:      "file_write",
		Arguments: json.RawMessage(`{"path":"remembered.txt","content":"remembered content","overwrite":true}`),
	}
	result, err := gateway.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	payload := result.Value.(map[string]any)
	approvalID := stringValue(payload["approvalId"])
	if payload["decision"] != "pending_approval" || approvalID == "" {
		t.Fatalf("pending approval payload = %#v", payload)
	}
	resolved, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type":       "agent_runtime_approval_response",
			"approvalId": approvalID,
			"approve":    true,
			"reason":     "remember this exact file write",
			"remember":   true,
		},
	})
	if err != nil {
		t.Fatalf("approval response error = %v", err)
	}
	resolvedMap := resolved.(map[string]any)
	if resolvedMap["success"] != true || resolvedMap["remembered"] != true {
		t.Fatalf("remembered approval response = %#v", resolvedMap)
	}
	if count := srv.rememberedApprovalDecisionCount(); count != 1 {
		t.Fatalf("remembered approval count = %d", count)
	}
	if err := os.Remove(filepath.Join(root, "remembered.txt")); err != nil {
		t.Fatalf("remove remembered file before repeat: %v", err)
	}
	repeated, err := gateway.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("repeated gateway execute error = %v", err)
	}
	repeatedPayload := repeated.Value.(map[string]any)
	if repeatedPayload["decision"] == "pending_approval" {
		t.Fatalf("repeated exact approval should not ask again: %#v", repeatedPayload)
	}
	written, err := os.ReadFile(filepath.Join(root, "remembered.txt"))
	if err != nil {
		t.Fatalf("remembered file was not written on repeat: %v", err)
	}
	if string(written) != "remembered content" {
		t.Fatalf("remembered file content = %q", written)
	}
}

func TestServerAgentRuntimeRememberedApprovalDoesNotApplyToDifferentInput(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode":              "ask",
		"autoAllowReadOnly": false,
		"requireReason":     true,
		"rememberDecisions": true,
	}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"file_write"}}
	first := agentruntime.ToolCall{
		ID:        "call_file_write_remembered_first",
		Name:      "file_write",
		Arguments: json.RawMessage(`{"path":"first.txt","content":"first","overwrite":true}`),
	}
	result, err := gateway.Execute(context.Background(), first)
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	approvalID := stringValue(result.Value.(map[string]any)["approvalId"])
	if approvalID == "" {
		t.Fatalf("missing approval id: %#v", result.Value)
	}
	if _, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type":       "agent_runtime_approval_response",
			"approvalId": approvalID,
			"approve":    true,
			"reason":     "remember only the first input",
			"remember":   true,
		},
	}); err != nil {
		t.Fatalf("approval response error = %v", err)
	}
	second, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_file_write_remembered_second",
		Name:      "file_write",
		Arguments: json.RawMessage(`{"path":"second.txt","content":"second","overwrite":true}`),
	})
	if err != nil {
		t.Fatalf("second gateway execute error = %v", err)
	}
	secondPayload := second.Value.(map[string]any)
	if secondPayload["decision"] != "pending_approval" || stringValue(secondPayload["approvalId"]) == "" {
		t.Fatalf("different input should still require approval: %#v", secondPayload)
	}
	if _, err := os.Stat(filepath.Join(root, "second.txt")); !os.IsNotExist(err) {
		t.Fatalf("different input file should not exist before approval, stat err=%v", err)
	}
}

func TestServerApprovalRememberedToolsListAndRevokeAgentRuntimeDecision(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedTranscriptWebFrame(t, store, "local", "remembered-project", "remembered-frame")
	srv := New(Options{FileRoot: root, Workspace: store})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Close(ctx)
	})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode":              "ask",
		"autoAllowReadOnly": false,
		"requireReason":     true,
		"rememberDecisions": true,
	}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, sessionID: "remembered-frame", allowedTools: []string{"file_write"}}
	call := agentruntime.ToolCall{
		ID:        "call_file_write_remembered_management",
		Name:      "file_write",
		Arguments: json.RawMessage(`{"path":"managed-remembered.txt","content":"managed remembered","overwrite":true}`),
	}
	result, err := gateway.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	approvalID := stringValue(result.Value.(map[string]any)["approvalId"])
	if approvalID == "" {
		t.Fatalf("missing approval id: %#v", result.Value)
	}
	spoofed := postToolInputStatus(t, httpServer.URL, "SendMessage", map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type":       "agent_runtime_approval_response",
			"approvalId": approvalID,
			"approve":    true,
			"reason":     "remember then manage",
			"remember":   true,
		},
	})
	if spoofed.status != http.StatusBadRequest || !strings.Contains(toolErrorMessage(spoofed.body), "cannot resolve user approval") {
		t.Fatalf("ordinary tool message gained approval authority: %#v", spoofed)
	}
	if _, err := store.SetFrameRuntimeMetadata("remembered-frame", workspace.FrameRuntimeMetadata{
		ContextData: map[string]any{"_pending_input_requests": []any{map[string]any{
			"tool_id": approvalID, "approval_id": approvalID, "kind": agentToolApprovalKind,
			"frame_id": "remembered-frame", "tool_name": call.Name,
		}}},
	}); err != nil {
		t.Fatal(err)
	}
	waiting := "awaiting_user_response"
	if _, err := store.UpdateFrame("remembered-frame", workspace.UpdateFrameInput{Status: &waiting}); err != nil {
		t.Fatal(err)
	}
	approvalResponse := map[string]any{"responses": []any{map[string]any{
		"tool_id": approvalID, "action": "allow_always",
	}}}
	const approvalPath = "/api/frames/remembered-frame/resolve-input"
	compatJSONRequest(t, srv.Handler(), http.MethodPost, approvalPath, "other-user", approvalResponse, http.StatusNotFound)
	missingReason := compatJSONRequest(t, srv.Handler(), http.MethodPost, approvalPath, "local", approvalResponse, http.StatusBadRequest)
	if !strings.Contains(stringValue(missingReason["detail"]), "requires reason") {
		t.Fatalf("missing approval reason was not rejected: %#v", missingReason)
	}
	entry, found, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found || mapValue(entry.Value)["status"] != "pending" || srv.rememberedApprovalDecisionCount() != 0 {
		t.Fatalf("rejected approval changed authority: %#v err=%v", entry.Value, err)
	}
	if _, err := os.Stat(filepath.Join(root, "managed-remembered.txt")); !os.IsNotExist(err) {
		t.Fatalf("unapproved tool wrote file: %v", err)
	}
	mapValue(anySliceValue(approvalResponse["responses"])[0])["message"] = "  remember then manage  "
	compatJSONRequest(t, srv.Handler(), http.MethodPost, approvalPath, "local", approvalResponse, http.StatusOK)
	entry, found, err = srv.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found || mapValue(entry.Value)["status"] != "completed" || srv.rememberedApprovalDecisionCount() != 1 {
		t.Fatalf("dedicated approval did not complete and remember decision: %#v err=%v", entry.Value, err)
	}
	written, err := os.ReadFile(filepath.Join(root, "managed-remembered.txt"))
	if err != nil || string(written) != "managed remembered" {
		t.Fatalf("approved runtime file content=%q err=%v", written, err)
	}

	listed := postToolInput(t, httpServer.URL, "approval_remembered_list", map[string]any{"source": "agent-runtime"})
	listedResult := listed["result"].(map[string]any)
	decisions := listedResult["decisions"].([]any)
	if listedResult["total"] != float64(1) || len(decisions) != 1 {
		t.Fatalf("remembered list = %#v", listedResult)
	}
	row := decisions[0].(map[string]any)
	if row["source"] != "agent-runtime" || row["reason"] != "remember then manage" || !strings.HasPrefix(stringValue(row["action"]), "agent-runtime:file_write:") {
		t.Fatalf("remembered row = %#v", row)
	}

	revoked := postToolInput(t, httpServer.URL, "approval_remembered_revoke", map[string]any{"key": row["key"]})
	revokedResult := revoked["result"].(map[string]any)
	if revokedResult["revoked"] != true || revokedResult["remaining"] != float64(0) {
		t.Fatalf("remembered revoke = %#v", revokedResult)
	}
	empty := postToolInput(t, httpServer.URL, "approval_remembered_list", map[string]any{"source": "agent-runtime"})
	if empty["result"].(map[string]any)["total"] != float64(0) {
		t.Fatalf("remembered list after revoke = %#v", empty)
	}
	if err := os.Remove(filepath.Join(root, "managed-remembered.txt")); err != nil {
		t.Fatalf("remove remembered file before revoke verification: %v", err)
	}
	repeated, err := gateway.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("repeated gateway execute error = %v", err)
	}
	repeatedPayload := repeated.Value.(map[string]any)
	if repeatedPayload["decision"] != "pending_approval" || stringValue(repeatedPayload["approvalId"]) == "" {
		t.Fatalf("revoked remembered approval should ask again: %#v", repeatedPayload)
	}
	if _, err := os.Stat(filepath.Join(root, "managed-remembered.txt")); !os.IsNotExist(err) {
		t.Fatalf("revoked approval executed without a new user decision: %v", err)
	}
}

func TestServerAgentRuntimeApprovalResponseStillRunsPreToolHook(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode":              "ask",
		"autoAllowReadOnly": false,
		"requireReason":     true,
	}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"file_write"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_file_write_pre_hook_pending",
		Name:      "file_write",
		Arguments: json.RawMessage(`{"path":"blocked-after-approval.txt","content":"blocked","overwrite":true}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	approvalID := stringValue(result.Value.(map[string]any)["approvalId"])
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "block-approved-write", map[string]any{
		"enabled":  true,
		"event":    "preTool",
		"tool":     "file_write",
		"decision": "block",
		"reason":   "approved writes are still hook-gated",
	}); err != nil {
		t.Fatalf("set pre tool hook: %v", err)
	}
	resolved, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type":       "agent_runtime_approval_response",
			"approvalId": approvalID,
			"approve":    true,
			"reason":     "approve but still run hooks",
		},
	})
	if err != nil {
		t.Fatalf("approval response error = %v", err)
	}
	resolvedMap := resolved.(map[string]any)
	if resolvedMap["success"] != false || resolvedMap["status"] != "blocked" {
		t.Fatalf("approval response = %#v", resolvedMap)
	}
	if _, err := os.Stat(filepath.Join(root, "blocked-after-approval.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked file should not exist after approval, stat err=%v", err)
	}
	entry, ok, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !ok {
		t.Fatalf("approval entry missing ok=%v err=%v", ok, err)
	}
	value := entry.Value.(map[string]any)
	if value["status"] != "blocked" || value["result"] == nil {
		t.Fatalf("blocked approval value = %#v", value)
	}
}

func TestServerAgentRuntimeCommandHookUsesShellSandboxPolicy(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode":              "allow",
		"autoAllowReadOnly": false,
	}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "dangerous-pre-tool-hook", map[string]any{
		"enabled": true,
		"event":   "preTool",
		"tool":    "file_write",
		"type":    "command",
		"shell":   "Bash",
		"command": "rm -rf ..",
	}); err != nil {
		t.Fatalf("set dangerous hook: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"file_write"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_file_write_dangerous_hook",
		Name:      "file_write",
		Arguments: json.RawMessage(`{"path":"should-not-write.txt","content":"blocked","overwrite":true}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	payload := result.Value.(map[string]any)
	if payload["decision"] != "blocked_by_hook" || !strings.Contains(stringValue(payload["error"]), "sandbox policy") {
		t.Fatalf("dangerous hook result = %#v", payload)
	}
	if _, err := os.Stat(filepath.Join(root, "should-not-write.txt")); !os.IsNotExist(err) {
		t.Fatalf("file should not exist after dangerous hook block, stat err=%v", err)
	}
}

func TestServerAgentRuntimeApprovalResponseRunsHooksAndAuditsApprovedTool(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{
		"mode":              "ask",
		"autoAllowReadOnly": false,
		"requireReason":     true,
	}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"file_write"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_file_write_hooked_pending",
		Name:      "file_write",
		Arguments: json.RawMessage(`{"path":"original-after-approval.txt","content":"original","overwrite":true}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	approvalID := stringValue(result.Value.(map[string]any)["approvalId"])
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "rewrite-approved-write", map[string]any{
		"enabled": true,
		"event":   "preTool",
		"tool":    "file_write",
		"updatedInput": map[string]any{
			"path":    "rewritten-after-approval.txt",
			"content": "rewritten content",
		},
	}); err != nil {
		t.Fatalf("set pre tool hook: %v", err)
	}
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "audit-approved-write", map[string]any{
		"enabled": true,
		"event":   "postTool",
		"tool":    "file_write",
	}); err != nil {
		t.Fatalf("set post tool hook: %v", err)
	}
	resolved, err := srv.executeSendMessageTool(context.Background(), map[string]any{
		"to": "agent-runtime",
		"message": map[string]any{
			"type":       "agent_runtime_approval_response",
			"approvalId": approvalID,
			"approve":    true,
			"reason":     "approve and audit",
		},
	})
	if err != nil {
		t.Fatalf("approval response error = %v", err)
	}
	resolvedMap := resolved.(map[string]any)
	if resolvedMap["success"] != true || resolvedMap["status"] != "completed" {
		t.Fatalf("approval response = %#v", resolvedMap)
	}
	if _, err := os.Stat(filepath.Join(root, "original-after-approval.txt")); !os.IsNotExist(err) {
		t.Fatalf("original file should not exist after hook rewrite, stat err=%v", err)
	}
	written, err := os.ReadFile(filepath.Join(root, "rewritten-after-approval.txt"))
	if err != nil {
		t.Fatalf("rewritten file was not written: %v", err)
	}
	if string(written) != "rewritten content" {
		t.Fatalf("rewritten file content = %q", written)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	value := findHookAuditValue(entries, "audit-approved-write", "postTool")
	if value == nil {
		t.Fatalf("missing post hook audit entry: %#v", entries)
	}
	if value["hook"] != "audit-approved-write" || value["tool"] != "file_write" || value["status"] != "completed" {
		t.Fatalf("hook audit value = %#v", value)
	}
	entry, ok, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !ok {
		t.Fatalf("approval entry missing ok=%v err=%v", ok, err)
	}
	approvalValue := entry.Value.(map[string]any)
	executedInput := approvalValue["executedInput"].(map[string]any)
	if approvalValue["status"] != "completed" || executedInput["path"] != "rewritten-after-approval.txt" {
		t.Fatalf("approval value = %#v", approvalValue)
	}
}

func TestServerAgentRuntimeBridgePreToolHookBlocksToolBeforeExecution(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode model request %d: %v", sequence, err)
		}
		switch sequence {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_hook_block_1",
							"type": "function",
							"function": {
								"name": "file_write",
								"arguments": "{\"path\":\"hook-blocked.txt\",\"content\":\"blocked\",\"overwrite\":true}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages := request["messages"].([]any)
			last := messages[len(messages)-1].(map[string]any)
			content := last["content"].(string)
			if last["role"] != "tool" || !strings.Contains(content, "blocked_by_hook") || !strings.Contains(content, "block-file-write") {
				t.Fatalf("second request missing hook block tool result: %#v", last)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {"role": "assistant", "content": "blocked by pre tool hook"}
				}]
			}`))
		default:
			t.Fatalf("unexpected model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "allow"}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "block-file-write", map[string]any{
		"enabled":  true,
		"event":    "preTool",
		"tool":     "file_write",
		"decision": "block",
		"reason":   "writes are blocked by test hook",
	}); err != nil {
		t.Fatalf("set pre tool hook: %v", err)
	}
	engine := srv.newAgentRuntimeEngine(SessionRunnerChatOptions{
		Endpoint:         modelAPI.URL + "/v1/chat/completions",
		Model:            "bridge-model",
		AllowedTools:     []string{"file_write"},
		RequestTimeout:   time.Minute,
		OutputLimitBytes: 64 * 1024,
	})

	result, err := engine.Run(context.Background(), agentruntime.RunRequest{
		Messages:      []agentruntime.Message{{Role: "user", Content: "write a hook blocked file"}},
		Tools:         srv.agentRuntimeToolSchemas([]string{"file_write"}),
		MaxToolRounds: 2,
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.FinalMessage.Content != "blocked by pre tool hook" || requests.Load() != 2 {
		t.Fatalf("result=%#v requests=%d", result, requests.Load())
	}
	if _, err := os.Stat(filepath.Join(root, "hook-blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("hook-blocked file should not exist, stat err=%v", err)
	}
}

func TestServerAgentRuntimeBridgePostToolHookAuditsCompletedTool(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "audit-task-create", map[string]any{
		"enabled": true,
		"event":   "postTool",
		"tool":    "task_create",
		"action":  "audit",
	}); err != nil {
		t.Fatalf("set post tool hook: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_post_hook_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"post hook task"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "post hook task") {
		t.Fatalf("task_create result = %s", rawResult)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("hook audit entries = %#v", entries)
	}
	value := entries[0].Value.(map[string]any)
	if value["hook"] != "audit-task-create" || value["event"] != "postTool" || value["tool"] != "task_create" || value["status"] != "completed" {
		t.Fatalf("hook audit value = %#v", value)
	}
	gatewayEntries, err := srv.runtimeStore.List(toolGatewayAuditNamespace)
	if err != nil {
		t.Fatalf("list tool gateway audit entries: %v", err)
	}
	gatewayAudit := findToolGatewayAuditValue(gatewayEntries, "agent-runtime", "completed")
	if gatewayAudit == nil {
		t.Fatalf("missing agent runtime tool gateway audit entry: %#v", gatewayEntries)
	}
	if gatewayAudit["tool"] != "task_create" || gatewayAudit["toolCallId"] != "call_post_hook_1" {
		t.Fatalf("agent runtime gateway audit = %#v", gatewayAudit)
	}
	if _, ok := gatewayAudit["durationMs"].(float64); !ok {
		t.Fatalf("agent runtime gateway audit missing normalized durationMs = %#v", gatewayAudit)
	}
}

func TestServerAgentRuntimeGatewayAuditRecordsTimeoutMetadata(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "allow"}); err != nil {
		t.Fatalf("set approval defaults: %v", err)
	}
	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"TaskOutput"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_task_output_timeout_audit",
		Name:      "TaskOutput",
		Arguments: json.RawMessage(`{"task_id":"missing-task","timeout":25}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	payload := result.Value.(map[string]any)
	if payload["ok"] != true {
		t.Fatalf("TaskOutput result = %#v", payload)
	}
	entries, err := srv.runtimeStore.List(toolGatewayAuditNamespace)
	if err != nil {
		t.Fatalf("list tool gateway audit entries: %v", err)
	}
	audit := findToolGatewayAuditValue(entries, "agent-runtime", "completed")
	if audit == nil {
		t.Fatalf("missing TaskOutput timeout audit: %#v", entries)
	}
	if audit["tool"] != "TaskOutput" ||
		audit["toolCallId"] != "call_task_output_timeout_audit" ||
		audit["timeoutMs"] != float64(25) ||
		audit["timeoutSource"] != "input.timeout_ms" {
		t.Fatalf("TaskOutput timeout audit = %#v", audit)
	}
}

func TestServerAgentRuntimeBridgePreToolHookUpdatesInputBeforeExecution(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "rewrite-task-title", map[string]any{
		"enabled":  true,
		"event":    "preTool",
		"tool":     "task_create",
		"decision": "allow",
		"updatedInput": map[string]any{
			"title": "rewritten by pre tool hook",
		},
	}); err != nil {
		t.Fatalf("set pre tool hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_pre_update_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"original title"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "rewritten by pre tool hook") || strings.Contains(string(rawResult), "original title") {
		t.Fatalf("task_create result did not use updated input: %s", rawResult)
	}
	tasks, err := srv.executeTaskTool("task_list", map[string]any{})
	if err != nil {
		t.Fatalf("task_list error = %v", err)
	}
	rawTasks, _ := json.Marshal(tasks)
	if !strings.Contains(string(rawTasks), "rewritten by pre tool hook") || strings.Contains(string(rawTasks), "original title") {
		t.Fatalf("task list did not use updated input: %s", rawTasks)
	}
}

func TestServerAgentRuntimeBridgeRejectsInvalidPostHookInputBeforeExecution(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "invalidate-task-title", map[string]any{
		"enabled": true, "event": "preTool", "tool": "task_create", "decision": "allow",
		"updatedInput": map[string]any{"title": 7, "unexpected": true},
	}); err != nil {
		t.Fatalf("set pre tool hook: %v", err)
	}
	allowed := []string{"task_create"}
	gateway := serverAgentRuntimeToolGateway{
		server: srv, allowedTools: allowed,
		toolSchemas: srv.agentRuntimeToolSchemasWithContext(context.Background(), allowed), hasToolSnapshot: true,
	}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "call_pre_invalid_update", Name: "task_create", Arguments: json.RawMessage(`{"title":"valid"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	value, _ := result.Value.(map[string]any)
	if value["code"] != "invalid_tool_arguments" {
		t.Fatalf("post-hook validation result = %#v", result.Value)
	}
	tasks, err := srv.executeTaskTool("task_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(tasks)
	if strings.Contains(string(raw), "valid") {
		t.Fatalf("invalid post-hook input reached execution: %s", raw)
	}
}

func TestServerAgentRuntimeBridgePreToolCommandHookUpdatesInputFromStdout(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	command := `$stdin = [Console]::In.ReadToEnd(); Set-Content -LiteralPath 'pre-hook-stdin.json' -Value $stdin; Write-Output '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"title":"rewritten by command hook"}}}'`
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "rewrite-task-title-command", map[string]any{
		"enabled": true,
		"event":   "preTool",
		"tool":    "task_create",
		"type":    "command",
		"shell":   "PowerShell",
		"command": command,
		"timeout": 30,
	}); err != nil {
		t.Fatalf("set pre tool command hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_pre_command_update_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"original command title"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "rewritten by command hook") || strings.Contains(string(rawResult), "original command title") {
		t.Fatalf("task_create result did not use command hook updated input: %s", rawResult)
	}
	stdin, err := os.ReadFile(filepath.Join(root, "pre-hook-stdin.json"))
	if err != nil {
		t.Fatalf("read pre hook stdin: %v", err)
	}
	if !strings.Contains(string(stdin), `"hook_event_name":"PreToolUse"`) || !strings.Contains(string(stdin), `"tool_name":"task_create"`) || !strings.Contains(string(stdin), "original command title") {
		t.Fatalf("pre hook stdin payload = %s", stdin)
	}
}

func TestServerAgentRuntimeBridgePreToolCommandHookFailureBlocksExecution(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "failed-pre-tool-command", map[string]any{
		"enabled": true,
		"event":   "preTool",
		"tool":    "task_create",
		"type":    "command",
		"shell":   "PowerShell",
		"command": `Write-Error 'pre tool hook failed'; exit 1`,
		"timeout": 30,
	}); err != nil {
		t.Fatalf("set failing pre tool hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID: "call_pre_command_failure", Name: "task_create", Arguments: json.RawMessage(`{"title":"must not execute"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), `"decision":"blocked_by_hook"`) {
		t.Fatalf("failing pre tool hook did not block: %s", rawResult)
	}
	tasks, err := srv.executeTaskTool("task_list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	rawTasks, _ := json.Marshal(tasks)
	if strings.Contains(string(rawTasks), "must not execute") {
		t.Fatalf("tool executed after failed pre tool hook: %s", rawTasks)
	}
}

func TestServerAgentRuntimeBridgePostToolCommandHookReceivesToolResponse(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	command := `$stdin = [Console]::In.ReadToEnd(); Set-Content -LiteralPath 'post-hook-stdin.json' -Value $stdin; Write-Output 'post command hook ok'`
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "audit-task-create-command", map[string]any{
		"enabled": true,
		"event":   "postTool",
		"tool":    "task_create",
		"type":    "command",
		"shell":   "PowerShell",
		"command": command,
		"timeout": commandHookFixtureTimeoutSeconds,
	}); err != nil {
		t.Fatalf("set post tool command hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_post_command_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"post command task"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "post command task") {
		t.Fatalf("task_create result = %s", rawResult)
	}
	stdin, err := os.ReadFile(filepath.Join(root, "post-hook-stdin.json"))
	if err != nil {
		t.Fatalf("read post hook stdin: %v", err)
	}
	if !strings.Contains(string(stdin), `"hook_event_name":"PostToolUse"`) || !strings.Contains(string(stdin), `"tool_response"`) || !strings.Contains(string(stdin), "post command task") {
		t.Fatalf("post hook stdin payload = %s", stdin)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("hook audit entries = %#v", entries)
	}
	value := entries[0].Value.(map[string]any)
	commandAudit := value["command"].(map[string]any)
	if numberValue(commandAudit["exitCode"]) != 0 || !strings.Contains(commandAudit["stdout"].(string), "post command hook ok") {
		t.Fatalf("post command audit = %#v", commandAudit)
	}
}

func TestServerAgentRuntimeBridgeAsyncPostToolHookDoesNotBlockToolResult(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	command := `input=$(cat); sleep 0.7; printf '%s' "$input" > async-post-hook-stdin.json; printf 'async post hook ok\n'`
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "async-post-task-create", map[string]any{
		"enabled": true,
		"event":   "postTool",
		"tool":    "task_create",
		"type":    "command",
		"shell":   "Bash",
		"command": command,
		"timeout": 5,
		"async":   true,
	}); err != nil {
		t.Fatalf("set async post hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	started := time.Now()
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_async_post_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"async post command task"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 600*time.Millisecond {
		t.Fatalf("async post hook blocked tool result for %s", elapsed)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "async post command task") {
		t.Fatalf("task_create result = %s", rawResult)
	}
	queuedEntries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list queued hook audit entries: %v", err)
	}
	queued := findHookAuditValue(queuedEntries, "async-post-task-create", "postTool")
	if queued["async"] != true || queued["asyncStatus"] != "queued" {
		t.Fatalf("queued async audit = %#v", queued)
	}

	completed := waitForHookAuditValue(t, srv, "async-post-task-create", "postTool", func(value map[string]any) bool {
		return value["asyncStatus"] == "completed" && value["command"] != nil
	})
	commandAudit := completed["command"].(map[string]any)
	if numberValue(commandAudit["exitCode"]) != 0 || !strings.Contains(commandAudit["stdout"].(string), "async post hook ok") {
		t.Fatalf("async post command audit = %#v", commandAudit)
	}
	stdin, err := os.ReadFile(filepath.Join(root, "async-post-hook-stdin.json"))
	if err != nil {
		t.Fatalf("read async post hook stdin: %v", err)
	}
	if !strings.Contains(string(stdin), `"hook_event_name":"PostToolUse"`) || !strings.Contains(string(stdin), `"tool_response"`) || !strings.Contains(string(stdin), "async post command task") {
		t.Fatalf("async post hook stdin payload = %s", stdin)
	}
	rewakeEntries, err := srv.runtimeStore.List(agentRuntimeHookRewakeNamespace)
	if err != nil {
		t.Fatalf("list hook rewake entries: %v", err)
	}
	rewake := findHookRewakeValue(rewakeEntries, "async-post-task-create", "call_async_post_1")
	if rewake == nil {
		t.Fatalf("missing async hook rewake entry: %#v", rewakeEntries)
	}
	if rewake["status"] != "completed" ||
		rewake["tool"] != "task_create" ||
		rewake["auditId"] != completed["id"] ||
		rewake["event"] != "postTool" {
		t.Fatalf("async hook rewake entry = %#v", rewake)
	}
}

func TestServerAgentRuntimeBridgePostToolUseFailureHookReceivesFailedToolResponse(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	failureCommand := `$stdin = [Console]::In.ReadToEnd(); Set-Content -LiteralPath 'post-failure-hook-stdin.json' -Value $stdin; Write-Output 'post failure hook ok'`
	successCommand := `Set-Content -LiteralPath 'post-success-should-not-run.txt' -Value 'unexpected'`
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PostToolUseFailure": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"shell":   "PowerShell",
						"command": failureCommand,
						"timeout": commandHookFixtureTimeoutSeconds,
					},
				},
			},
		},
		"PostToolUse": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"shell":   "PowerShell",
						"command": successCommand,
						"timeout": commandHookFixtureTimeoutSeconds,
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set post failure hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_post_failure_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	value := result.Value.(map[string]any)
	if value["ok"] != false || !strings.Contains(stringValue(value["error"]), "title") {
		t.Fatalf("failed task_create result = %#v", value)
	}
	stdin, err := os.ReadFile(filepath.Join(root, "post-failure-hook-stdin.json"))
	if err != nil {
		t.Fatalf("read post failure hook stdin: %v", err)
	}
	if !strings.Contains(string(stdin), `"hook_event_name":"PostToolUseFailure"`) || !strings.Contains(string(stdin), `"tool_response"`) || !strings.Contains(string(stdin), "title") {
		t.Fatalf("post failure hook stdin payload = %s", stdin)
	}
	if _, err := os.Stat(filepath.Join(root, "post-success-should-not-run.txt")); !os.IsNotExist(err) {
		t.Fatalf("PostToolUse hook should not run after failed tool, stat err=%v", err)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	audit := findHookAuditValue(entries, "settings:hooks:PostToolUseFailure:0:0", "postToolFailure")
	if audit == nil || audit["status"] != "failed" {
		t.Fatalf("post failure audit = %#v entries=%#v", audit, entries)
	}
	commandAudit := audit["command"].(map[string]any)
	if numberValue(commandAudit["exitCode"]) != 0 || !strings.Contains(commandAudit["stdout"].(string), "post failure hook ok") {
		t.Fatalf("post failure command audit = %#v", commandAudit)
	}
}

func TestServerAgentRuntimeBridgePreToolHTTPHookUpdatesInput(t *testing.T) {
	var requests atomic.Int64
	hookAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost {
			t.Fatalf("hook method = %s", r.Method)
		}
		if r.Header.Get("X-Hook-Test") != "pre-http" {
			t.Fatalf("missing hook header: %#v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode hook payload: %v", err)
		}
		if payload["hook_event_name"] != "PreToolUse" || payload["tool_name"] != "task_create" {
			t.Fatalf("hook payload = %#v", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"title":"rewritten by http hook"}}}`))
	}))
	defer hookAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{
						"type":    "http",
						"url":     hookAPI.URL,
						"timeout": 5,
						"headers": map[string]any{"X-Hook-Test": "pre-http"},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set http hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_pre_http_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"http original title"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "rewritten by http hook") || strings.Contains(string(rawResult), "http original title") {
		t.Fatalf("task_create result did not use http hook input: %s", rawResult)
	}
	if requests.Load() != 1 {
		t.Fatalf("hook requests = %d", requests.Load())
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	value := findHookAuditValue(entries, "settings:hooks:PreToolUse:0:0", "preTool")
	if value == nil {
		t.Fatalf("missing pre http hook audit: %#v", entries)
	}
	commandAudit := value["command"].(map[string]any)
	if commandAudit["type"] != "http" || commandAudit["statusCode"] != float64(http.StatusOK) {
		t.Fatalf("http hook audit = %#v", commandAudit)
	}
	if updated := value["updatedInput"].(map[string]any); updated["title"] != "rewritten by http hook" {
		t.Fatalf("updatedInput audit = %#v", updated)
	}
}

func TestServerAgentRuntimeBridgePreToolHTTPHookBlocksTool(t *testing.T) {
	hookAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"decision":"block","reason":"blocked by http hook"}`))
	}))
	defer hookAPI.Close()

	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{"type": "http", "url": hookAPI.URL, "timeout": 5},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set blocking http hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_pre_http_block_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"must not create"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	value := result.Value.(map[string]any)
	if value["decision"] != "blocked_by_hook" || !strings.Contains(stringValue(value["error"]), "blocked by http hook") {
		t.Fatalf("http hook block result = %#v", value)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	audit := findHookAuditValue(entries, "settings:hooks:PreToolUse:0:0", "preTool")
	if audit == nil || audit["decision"] != "deny" || audit["reason"] != "blocked by http hook" {
		t.Fatalf("http hook block audit = %#v entries=%#v", audit, entries)
	}
}

func TestServerAgentRuntimeBridgePreToolPromptHookUpdatesInput(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode prompt hook model request: %v", err)
		}
		if request.Model != "hook-model" {
			t.Fatalf("model = %q", request.Model)
		}
		if len(request.Messages) < 2 || !strings.Contains(request.Messages[len(request.Messages)-1].Content, "prompt original title") {
			t.Fatalf("prompt hook request messages = %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\",\"updatedInput\":{\"title\":\"rewritten by prompt hook\"}}}"
				}
			}]
		}`))
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{
		FileRoot: root,
		CompactSummarizer: SessionRunnerChatOptions{
			Endpoint: modelAPI.URL + "/v1/chat/completions",
			Model:    "hook-model",
		},
	})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{
						"type":    "prompt",
						"prompt":  "Inspect this hook input and return JSON only: $ARGUMENTS",
						"timeout": 5,
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set prompt hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_pre_prompt_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"prompt original title"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "rewritten by prompt hook") || strings.Contains(string(rawResult), "prompt original title") {
		t.Fatalf("task_create result did not use prompt hook input: %s", rawResult)
	}
	if requests.Load() != 1 {
		t.Fatalf("prompt hook model requests = %d", requests.Load())
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	audit := findHookAuditValue(entries, "settings:hooks:PreToolUse:0:0", "preTool")
	if audit == nil {
		t.Fatalf("missing pre prompt hook audit: %#v", entries)
	}
	commandAudit := audit["command"].(map[string]any)
	if commandAudit["type"] != "prompt" || commandAudit["model"] != "hook-model" {
		t.Fatalf("prompt hook audit = %#v", commandAudit)
	}
	if updated := audit["updatedInput"].(map[string]any); updated["title"] != "rewritten by prompt hook" {
		t.Fatalf("updatedInput audit = %#v", updated)
	}
}

func TestServerAgentRuntimeBridgePromptHookWithoutModelRecordsAuditError(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{
						"type":   "prompt",
						"prompt": "Return a hook update for $ARGUMENTS",
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set prompt hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_pre_prompt_unconfigured_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"prompt unconfigured original"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "prompt unconfigured original") || strings.Contains(string(rawResult), "rewritten by prompt hook") {
		t.Fatalf("prompt hook should not rewrite without model config: %s", rawResult)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	audit := findHookAuditValue(entries, "settings:hooks:PreToolUse:0:0", "preTool")
	if audit == nil {
		t.Fatalf("missing pre prompt hook audit: %#v", entries)
	}
	commandAudit := audit["command"].(map[string]any)
	if commandAudit["type"] != "prompt" || !strings.Contains(stringValue(commandAudit["error"]), "requires compact summarizer endpoint and model") {
		t.Fatalf("prompt hook missing-model audit = %#v", commandAudit)
	}
}

func TestServerAgentRuntimeBridgePreToolAgentHookUsesCanonicalReadOnlyToolsAndUpdatesInput(t *testing.T) {
	var requests atomic.Int64
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := requests.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode agent hook model request %d: %v", sequence, err)
		}
		switch sequence {
		case 1:
			tools, ok := request["tools"].([]any)
			if !ok || !hasChatToolNamed(tools, "search_skills") {
				t.Fatalf("agent hook missing search_skills tool: %#v", request["tools"])
			}
			if hasChatToolNamed(tools, "file_write") || hasChatToolNamed(tools, "TaskRun") || hasChatToolNamed(tools, "Agent") {
				t.Fatalf("agent hook exposed mutating/nested tools: %#v", request["tools"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"tool_calls": [{
							"id": "call_hook_search_1",
							"type": "function",
							"function": {
								"name": "search_skills",
								"arguments": "{\"query\":\"runtime\"}"
							}
						}]
					}
				}]
			}`))
		case 2:
			messages, ok := request["messages"].([]any)
			if !ok || !hasToolResultMessage(messages, "call_hook_search_1", "") {
				t.Fatalf("agent hook second request missing capability-search result: %#v", request["messages"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"choices": [{
					"message": {
						"role": "assistant",
						"content": "{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\",\"updatedInput\":{\"title\":\"rewritten by agent hook\"}}}"
					}
				}]
			}`))
		default:
			t.Fatalf("unexpected agent hook model request %d", sequence)
		}
	}))
	defer modelAPI.Close()

	root := t.TempDir()
	srv := New(Options{
		FileRoot: root,
		CompactSummarizer: SessionRunnerChatOptions{
			Endpoint: modelAPI.URL + "/v1/chat/completions",
			Model:    "agent-hook-model",
		},
	})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{
						"type":          "agent",
						"prompt":        "Inspect the admitted runtime capability before allowing this task: $ARGUMENTS",
						"timeout":       5,
						"maxToolRounds": 2,
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set agent hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_pre_agent_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"agent original title"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "rewritten by agent hook") || strings.Contains(string(rawResult), "agent original title") {
		t.Fatalf("task_create result did not use agent hook input: %s", rawResult)
	}
	if requests.Load() != 2 {
		t.Fatalf("agent hook model requests = %d", requests.Load())
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	audit := findHookAuditValue(entries, "settings:hooks:PreToolUse:0:0", "preTool")
	if audit == nil {
		t.Fatalf("missing pre agent hook audit: %#v", entries)
	}
	commandAudit := audit["command"].(map[string]any)
	if commandAudit["type"] != "agent" || commandAudit["model"] != "agent-hook-model" || numberValue(commandAudit["allowedToolCount"]) == 0 {
		t.Fatalf("agent hook audit = %#v", commandAudit)
	}
}

func TestServerAgentRuntimeBridgeOuterToolDeadlineBoundsSynchronousAgentHook(t *testing.T) {
	var requests atomic.Int64
	release := make(chan struct{})
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		modelAPI.Close()
	}()

	srv := New(Options{
		FileRoot: t.TempDir(),
		CompactSummarizer: SessionRunnerChatOptions{
			Endpoint:       modelAPI.URL + "/v1/chat/completions",
			Model:          "blocking-agent-hook-model",
			RequestTimeout: time.Minute,
		},
	})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{
						"type":          "agent",
						"prompt":        "Inspect this task before it is created: $ARGUMENTS",
						"timeout":       5,
						"maxToolRounds": 3,
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set blocking agent hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	ctx := withAgentRuntimeToolExecutionTimeout(context.Background(), 25*time.Millisecond)
	started := time.Now()
	_, err := gateway.Execute(ctx, agentruntime.ToolCall{
		ID:        "call_pre_agent_deadline_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"must not be created after deadline"}`),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gateway deadline error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("outer tool deadline returned after %s", elapsed)
	}
	if requests.Load() != 1 {
		t.Fatalf("agent hook model requests = %d, want 1", requests.Load())
	}
	tasks, listErr := srv.executeTaskTool("task_list", map[string]any{})
	if listErr != nil {
		t.Fatalf("task_list error = %v", listErr)
	}
	rawTasks, marshalErr := json.Marshal(tasks)
	if marshalErr != nil {
		t.Fatalf("marshal task list: %v", marshalErr)
	}
	if strings.Contains(string(rawTasks), "must not be created after deadline") {
		t.Fatalf("timed-out hook allowed task side effect: %s", rawTasks)
	}
}

func TestServerAgentRuntimeBridgePostAgentHookTimeoutPreservesCommittedToolSuccess(t *testing.T) {
	var requests atomic.Int64
	release := make(chan struct{})
	modelAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		modelAPI.Close()
	}()

	srv := New(Options{
		FileRoot: t.TempDir(),
		CompactSummarizer: SessionRunnerChatOptions{
			Endpoint:       modelAPI.URL + "/v1/chat/completions",
			Model:          "blocking-post-agent-hook-model",
			RequestTimeout: time.Minute,
		},
	})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PostToolUse": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{
						"type":          "agent",
						"prompt":        "Audit the completed task: $ARGUMENTS",
						"timeout":       5,
						"maxToolRounds": 3,
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set blocking post agent hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	ctx := withAgentRuntimeToolExecutionTimeout(context.Background(), 25*time.Millisecond)
	started := time.Now()
	result, err := gateway.Execute(ctx, agentruntime.ToolCall{
		ID:        "call_post_agent_deadline_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"committed before post hook timeout"}`),
	})
	if err != nil {
		t.Fatalf("committed tool was reported as retryable failure: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("outer post-hook deadline returned after %s", elapsed)
	}
	rawResult, marshalErr := json.Marshal(result.Value)
	if marshalErr != nil || !strings.Contains(string(rawResult), "committed before post hook timeout") {
		t.Fatalf("committed result=%s marshalErr=%v", rawResult, marshalErr)
	}
	if requests.Load() != 1 {
		t.Fatalf("post agent hook model requests=%d, want 1", requests.Load())
	}
	tasks, listErr := srv.executeTaskTool("task_list", map[string]any{})
	if listErr != nil {
		t.Fatalf("task_list error=%v", listErr)
	}
	rawTasks, marshalErr := json.Marshal(tasks)
	var taskList struct {
		Tasks []struct {
			Title string `json:"title"`
		} `json:"tasks"`
	}
	if marshalErr != nil || json.Unmarshal(rawTasks, &taskList) != nil || len(taskList.Tasks) != 1 || taskList.Tasks[0].Title != "committed before post hook timeout" {
		t.Fatalf("committed task count is not exactly one: %s err=%v", rawTasks, marshalErr)
	}
	hookEntries, listErr := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if listErr != nil {
		t.Fatalf("list hook audit entries: %v", listErr)
	}
	hookAudit := findHookAuditValue(hookEntries, "settings:hooks:PostToolUse:0:0", "postTool")
	if hookAudit == nil {
		t.Fatalf("missing post agent hook audit: %#v", hookEntries)
	}
	commandAudit := hookAudit["command"].(map[string]any)
	if commandAudit["type"] != "agent" || strings.TrimSpace(stringValue(commandAudit["error"])) == "" {
		t.Fatalf("post agent hook failure audit=%#v", commandAudit)
	}
	gatewayEntries, listErr := srv.runtimeStore.List(toolGatewayAuditNamespace)
	if listErr != nil {
		t.Fatalf("list gateway audits: %v", listErr)
	}
	var completed map[string]any
	for _, entry := range gatewayEntries {
		value, _ := entry.Value.(map[string]any)
		if value["toolCallId"] == "call_post_agent_deadline_1" && value["status"] == "completed" {
			completed = value
		}
	}
	if completed == nil || completed["postHookStatus"] != "timeout" {
		t.Fatalf("completed gateway audit=%#v entries=%#v", completed, gatewayEntries)
	}
}

func TestServerAgentRuntimeBridgeAgentHookWithoutModelRecordsAuditError(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "task_create",
				"hooks": []any{
					map[string]any{
						"type":   "agent",
						"prompt": "Verify this hook input: $ARGUMENTS",
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set agent hook: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_pre_agent_unconfigured_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"agent unconfigured original"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "agent unconfigured original") || strings.Contains(string(rawResult), "rewritten by agent hook") {
		t.Fatalf("agent hook should not rewrite without model config: %s", rawResult)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	audit := findHookAuditValue(entries, "settings:hooks:PreToolUse:0:0", "preTool")
	if audit == nil {
		t.Fatalf("missing pre agent hook audit: %#v", entries)
	}
	commandAudit := audit["command"].(map[string]any)
	if commandAudit["type"] != "agent" || !strings.Contains(stringValue(commandAudit["error"]), "requires compact summarizer endpoint and model") {
		t.Fatalf("agent hook missing-model audit = %#v", commandAudit)
	}
}

func TestServerAgentRuntimeBridgeLoadsOriginalSettingsPreToolHookShape(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	command := `$stdin = [Console]::In.ReadToEnd(); Set-Content -LiteralPath 'settings-pre-hook-stdin.json' -Value $stdin; Write-Output '{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"title":"rewritten by settings hook"}}}'`
	if _, err := srv.settingsStore.Set("hooks", map[string]any{
		"PreToolUse": []any{
			map[string]any{
				"matcher": "task_create|file_write",
				"hooks": []any{
					map[string]any{
						"type":    "command",
						"shell":   "PowerShell",
						"command": command,
						"timeout": commandHookFixtureTimeoutSeconds,
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set settings hooks: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_settings_pre_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"settings original title"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "rewritten by settings hook") || strings.Contains(string(rawResult), "settings original title") {
		t.Fatalf("task_create result did not use settings hook input: %s", rawResult)
	}
	stdin, err := os.ReadFile(filepath.Join(root, "settings-pre-hook-stdin.json"))
	if err != nil {
		t.Fatalf("read settings pre hook stdin: %v", err)
	}
	if !strings.Contains(string(stdin), `"hook_event_name":"PreToolUse"`) || !strings.Contains(string(stdin), "settings original title") {
		t.Fatalf("settings pre hook stdin payload = %s", stdin)
	}
}

func TestServerAgentRuntimeBridgeLoadsOriginalRuntimePostToolHookShape(t *testing.T) {
	root := t.TempDir()
	srv := New(Options{FileRoot: root})
	command := `$stdin = [Console]::In.ReadToEnd(); Set-Content -LiteralPath 'runtime-post-hook-stdin.json' -Value $stdin; Write-Output 'original runtime post hook ok'`
	if _, err := srv.runtimeStore.Set(agentRuntimeHookNamespace, "original-hooks", map[string]any{
		"hooks": map[string]any{
			"PostToolUse": []any{
				map[string]any{
					"matcher": "task_create",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"shell":   "PowerShell",
							"command": command,
							"timeout": commandHookFixtureTimeoutSeconds,
						},
					},
				},
			},
		},
	}); err != nil {
		t.Fatalf("set runtime original hook shape: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	_, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_runtime_post_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"runtime post shape task"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	stdin, err := os.ReadFile(filepath.Join(root, "runtime-post-hook-stdin.json"))
	if err != nil {
		t.Fatalf("read runtime post hook stdin: %v", err)
	}
	if !strings.Contains(string(stdin), `"hook_event_name":"PostToolUse"`) || !strings.Contains(string(stdin), `"tool_response"`) || !strings.Contains(string(stdin), "runtime post shape task") {
		t.Fatalf("runtime post hook stdin payload = %s", stdin)
	}
	entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
	if err != nil {
		t.Fatalf("list hook audit entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("hook audit entries = %#v", entries)
	}
	value := entries[0].Value.(map[string]any)
	if value["hook"] != "runtime:original-hooks:PostToolUse:0:0" {
		t.Fatalf("hook audit source = %#v", value["hook"])
	}
	commandAudit := value["command"].(map[string]any)
	if !strings.Contains(commandAudit["stdout"].(string), "original runtime post hook ok") {
		t.Fatalf("runtime post command audit = %#v", commandAudit)
	}
}

func TestServerAgentRuntimeBridgeRunsPluginPreToolHook(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "terminal-tools")
	writeAgentRuntimePluginHookFixture(t, pluginDir)
	plugins, err := pluginhost.NewDefaultWithExternalDirectories([]string{pluginDir})
	if err != nil {
		t.Fatalf("NewDefaultWithExternalDirectories() error = %v", err)
	}
	srv := New(Options{FileRoot: root, Plugins: plugins})

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_plugin_pre_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"plugin original title"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "rewritten by plugin hook") || strings.Contains(string(rawResult), "plugin original title") {
		t.Fatalf("task_create result did not use plugin hook input: %s", rawResult)
	}
	stdin, err := os.ReadFile(filepath.Join(pluginDir, "plugin-pre-hook-stdin.json"))
	if err != nil {
		t.Fatalf("read plugin hook stdin: %v", err)
	}
	if !strings.Contains(string(stdin), `"hook_event_name":"PreToolUse"`) || !strings.Contains(string(stdin), "plugin original title") {
		t.Fatalf("plugin pre hook stdin payload = %s", stdin)
	}
	envSnapshot, err := os.ReadFile(filepath.Join(pluginDir, "plugin-pre-hook-env.txt"))
	if err != nil {
		t.Fatalf("read plugin hook env snapshot: %v", err)
	}
	if !strings.Contains(string(envSnapshot), "SYNON_PLUGIN_ID=terminal-tools\n") ||
		!strings.Contains(string(envSnapshot), "SYNON_PLUGIN_NAME=terminal-tools\n") ||
		!strings.Contains(string(envSnapshot), "SYNON_PLUGIN_ROOT="+pluginDir+"\n") {
		t.Fatalf("plugin hook env snapshot = %s", envSnapshot)
	}
}

func TestServerAgentRuntimeBridgeManagedHooksOnlySkipsPluginHook(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "terminal-tools")
	writeAgentRuntimePluginHookFixture(t, pluginDir)
	plugins, err := pluginhost.NewDefaultWithExternalDirectories([]string{pluginDir})
	if err != nil {
		t.Fatalf("NewDefaultWithExternalDirectories() error = %v", err)
	}
	srv := New(Options{FileRoot: root, Plugins: plugins})
	if _, err := srv.settingsStore.Set("agentRuntimeManagedHooksOnly", true); err != nil {
		t.Fatalf("set managed hooks only: %v", err)
	}

	gateway := serverAgentRuntimeToolGateway{server: srv, allowedTools: []string{"task_create"}}
	result, err := gateway.Execute(context.Background(), agentruntime.ToolCall{
		ID:        "call_plugin_skipped_1",
		Name:      "task_create",
		Arguments: json.RawMessage(`{"title":"plugin original title"}`),
	})
	if err != nil {
		t.Fatalf("gateway execute error = %v", err)
	}
	rawResult, _ := json.Marshal(result.Value)
	if !strings.Contains(string(rawResult), "plugin original title") || strings.Contains(string(rawResult), "rewritten by plugin hook") {
		t.Fatalf("plugin hook should be skipped in managed-only mode: %s", rawResult)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "plugin-pre-hook-stdin.json")); !os.IsNotExist(err) {
		t.Fatalf("plugin hook should not have run, stat err=%v", err)
	}
}

func writeAgentRuntimePluginHookFixture(t *testing.T, pluginDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(pluginDir, ".synon-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pluginDir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
  "id": "terminal-tools",
  "name": "terminal-tools",
  "version": "0.2.0",
  "server": {"api": {"mount": "/api/plugins/terminal-tools"}}
}`
	if err := os.WriteFile(filepath.Join(pluginDir, ".synon-plugin", "plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	hooks := `{
  "description": "plugin hook fixture",
  "hooks": {
    "PreToolUse": [{
      "matcher": "task_create",
      "hooks": [{
        "type": "command",
        "shell": "Bash",
        "timeout": 5,
        "command": "cat > \"${SYNON_PLUGIN_ROOT}/plugin-pre-hook-stdin.json\"; printf 'SYNON_PLUGIN_ID=%s\nSYNON_PLUGIN_NAME=%s\nSYNON_PLUGIN_ROOT=%s\n' \"$SYNON_PLUGIN_ID\" \"$SYNON_PLUGIN_NAME\" \"$SYNON_PLUGIN_ROOT\" > \"${SYNON_PLUGIN_ROOT}/plugin-pre-hook-env.txt\"; printf '%s\n' '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"allow\",\"updatedInput\":{\"title\":\"rewritten by plugin hook\"}}}'"
      }]
    }]
  }
}`
	if err := os.WriteFile(filepath.Join(pluginDir, "hooks", "hooks.json"), []byte(hooks), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForHookAuditValue(t *testing.T, srv *Server, hook string, event string, ready func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var last map[string]any
	for time.Now().Before(deadline) {
		entries, err := srv.runtimeStore.List(agentRuntimeHookAuditNamespace)
		if err != nil {
			t.Fatalf("list hook audit entries: %v", err)
		}
		last = findHookAuditValue(entries, hook, event)
		if last != nil && ready(last) {
			return last
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("hook audit %s/%s did not become ready, last=%#v", hook, event, last)
	return nil
}

func findHookAuditValue(entries []runtimekv.Entry, hook string, event string) map[string]any {
	for _, entry := range entries {
		value, ok := entry.Value.(map[string]any)
		if !ok {
			continue
		}
		if hook != "" && value["hook"] != hook {
			continue
		}
		if event != "" && value["event"] != event {
			continue
		}
		return value
	}
	return nil
}

func findToolGatewayAuditValue(entries []runtimekv.Entry, origin string, status string) map[string]any {
	for _, entry := range entries {
		value, ok := entry.Value.(map[string]any)
		if !ok {
			continue
		}
		if origin != "" && value["origin"] != origin {
			continue
		}
		if status != "" && value["status"] != status {
			continue
		}
		return value
	}
	return nil
}

func findHookRewakeValue(entries []runtimekv.Entry, hook string, toolCallID string) map[string]any {
	for _, entry := range entries {
		value, ok := entry.Value.(map[string]any)
		if !ok {
			continue
		}
		if hook != "" && value["hook"] != hook {
			continue
		}
		if toolCallID != "" && value["toolCallId"] != toolCallID {
			continue
		}
		return value
	}
	return nil
}

func TestAgentRuntimeToolResponseStatusClassifiesSemanticFailuresWithoutChangingPayload(t *testing.T) {
	tests := []struct {
		name       string
		result     map[string]any
		wantStatus string
	}{
		{name: "success", result: map[string]any{"records": []any{}}, wantStatus: "completed"},
		{name: "nested false", result: map[string]any{"ok": false, "error": "unavailable"}, wantStatus: "failed"},
		{name: "success false", result: map[string]any{"success": false}, wantStatus: "failed"},
		{name: "mcp is error", result: map[string]any{"isError": true}, wantStatus: "failed"},
		{name: "source unavailable", result: map[string]any{"sourceUnavailable": true}, wantStatus: "unavailable"},
		{name: "source stop reason", result: map[string]any{"stopReason": "source_unavailable"}, wantStatus: "unavailable"},
		{name: "mixed source status", result: map[string]any{"sources": []any{
			map[string]any{"status": "fetched"}, map[string]any{"status": "sourceUnavailable"},
		}}, wantStatus: "partial"},
		{name: "failure object", result: map[string]any{"failure": map[string]any{"kind": "search_unavailable"}}, wantStatus: "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := map[string]any{"ok": true, "result": test.result}
			status, _ := agentRuntimeToolResponseStatus(payload)
			if status != test.wantStatus {
				t.Fatalf("status = %q for %#v, want %q", status, payload, test.wantStatus)
			}
			if payload["ok"] != true || !reflect.DeepEqual(payload["result"], test.result) {
				t.Fatalf("classification changed payload = %#v", payload)
			}
		})
	}
}
