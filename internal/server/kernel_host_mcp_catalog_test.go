package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

func TestKernelMCPCatalogParametersPreserveNullableEnumAndArrayItemSchema(t *testing.T) {
	parameters := kernelMCPCatalogParameters(mcpstdio.ToolProjection{
		InputProperties: []string{"phase", "study_type"},
		InputSchema: map[string]any{"properties": map[string]any{
			"study_type": map[string]any{"anyOf": []any{
				map[string]any{"type": "string", "enum": []any{"INTERVENTIONAL", "OBSERVATIONAL"}},
				map[string]any{"type": "null"},
			}},
			"phase": map[string]any{"anyOf": []any{
				map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"PHASE1", "PHASE2"}}},
				map[string]any{"type": "null"},
			}},
		}},
	})
	if len(parameters) != 2 {
		t.Fatalf("parameters=%#v", parameters)
	}
	phase, studyType := parameters[0], parameters[1]
	if phase["type"] != "array" || phase["nullable"] != true || mapValue(phase["items"])["type"] != "string" {
		t.Fatalf("phase projection=%#v", phase)
	}
	if studyType["type"] != "string" || studyType["nullable"] != true || len(anySliceValue(studyType["enum"])) != 2 {
		t.Fatalf("study type projection=%#v", studyType)
	}
}

func TestKernelMCPCatalogToolExposesBoundedOutputSchemaOnlyForFilteredDiscovery(t *testing.T) {
	tool := mcpstdio.ToolProjection{
		Name: "mcp__pubmed__batch_fetch_metadata", ToolName: "batch_fetch_metadata",
		HasOutputSchema: true,
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"publication_date": map[string]any{"type": "object", "properties": map[string]any{
				"year": map[string]any{"type": "string"},
			}},
		}},
	}
	connector := workspaceMCPRuntimeConnector{ID: "pubmed", Name: "PubMed"}
	filtered := kernelMCPCatalogTool(connector, tool, true)
	if mapValue(filtered["output_schema"])["type"] != "object" {
		t.Fatalf("filtered output schema=%#v", filtered)
	}
	unfiltered := kernelMCPCatalogTool(connector, tool, false)
	if _, found := unfiltered["output_schema"]; found {
		t.Fatalf("unfiltered catalog leaked full output schema=%#v", unfiltered)
	}
}

func TestReplHostMCPCatalogReturnsAuthorizedExactMethodsWithoutCallingThem(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	marker := filepath.Join(t.TempDir(), "mcp-calls.log")
	command := writeKernelMCPStdioFixture(t, marker)
	enabled := true
	server, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "custom:remote", UserID: identity.access.UserID, Name: "remote",
		URL: command, Transport: "stdio", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMCPToolGrant(workspace.MCPToolGrantInput{
		ID: "grant-remote-echo", MCPServerID: server.ID, UserID: identity.access.UserID,
		AgentName: workspace.GlobalMCPToolGrantAgent, ToolName: "echo", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code": `import host, json
print(json.dumps(host.mcp(), sort_keys=True))
`,
	}, []string{"mcp__remote__echo"})
	if err != nil || result["ok"] != true {
		t.Fatalf("host MCP catalog result=%#v err=%v", result, err)
	}
	var catalog map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(stringValue(result["stdout"]))), &catalog); err != nil {
		t.Fatalf("decode catalog: %v; stdout=%q", err, result["stdout"])
	}
	tools, _ := catalog["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("catalog tools = %#v", tools)
	}
	tool := mapValue(tools[0])
	if tool["server"] != "custom:remote" || tool["method"] != "echo" || tool["tool"] != "mcp__remote__echo" {
		t.Fatalf("catalog tool = %#v", tool)
	}
	parameters, _ := tool["parameters"].([]any)
	if len(parameters) != 1 || mapValue(parameters[0])["name"] != "text" || mapValue(parameters[0])["required"] != true {
		t.Fatalf("catalog parameters = %#v", parameters)
	}
	if usage := stringValue(catalog["usage"]); !strings.Contains(usage, "exact listed server and method") || !strings.Contains(usage, "Do not probe or guess") {
		t.Fatalf("catalog usage = %q", usage)
	}
	if catalog["total_tools"] != float64(1) || catalog["returned"] != float64(1) || catalog["has_more"] != false {
		t.Fatalf("catalog coverage = %#v", catalog)
	}
	servers, _ := catalog["servers"].([]any)
	if len(servers) != 1 || servers[0] != "custom:remote" {
		t.Fatalf("catalog servers = %#v", servers)
	}
	aliasResult, aliasErr := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code": `import host, json
print(json.dumps(host.mcp.list_methods("remote")))`,
	}, []string{"mcp__remote__echo"})
	if aliasErr != nil || aliasResult["ok"] != true {
		t.Fatalf("alias discovery failed: %v %v", aliasResult, aliasErr)
	}
	var methods []map[string]any
	if err := json.Unmarshal([]byte(stringValue(aliasResult["stdout"])), &methods); err != nil || len(methods) != 1 || methods[0]["server"] != "custom:remote" || methods[0]["name"] != "echo" {
		t.Fatalf("real bridge lost canonical methods: %v %v", methods, err)
	}
	if called, readErr := os.ReadFile(marker); readErr == nil && strings.Contains(string(called), "call\n") {
		t.Fatalf("catalog discovery invoked an MCP method: %q", called)
	}
}

func TestParseKernelMCPCatalogRequestAcceptsBoundedFilteredPages(t *testing.T) {
	request, err := parseKernelMCPCatalogRequest(nil, map[string]any{
		"server": " bundled:bio ", "offset": float64(256), "max_results": int64(64),
	})
	if err != nil || request.server != "bundled:bio" || request.offset != 256 || request.maxResults != 64 {
		t.Fatalf("catalog request=%#v err=%v", request, err)
	}
	for _, kwargs := range []map[string]any{
		{"server": ""}, {"offset": -1}, {"max_results": 0}, {"max_results": maxKernelMCPCatalogTools + 1}, {"unknown": true},
	} {
		if _, err := parseKernelMCPCatalogRequest(nil, kwargs); err == nil {
			t.Fatalf("invalid catalog request accepted: %#v", kwargs)
		}
	}
}

func TestReplHostMCPResolvesOneReadOnlySchemaCompatibleSubsetMethod(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "workspace.db")
	store, manager, app, identity := newKernelHostTestRuntime(t, databasePath, true)
	defer closeKernelHostTestRuntime(t, app, manager, store)

	marker := filepath.Join(t.TempDir(), "mcp-calls.log")
	command := writeKernelMCPStdioFixtureWithTool(t, marker, "record_echo")
	enabled := true
	server, err := store.CreateMCPServer(workspace.MCPServerInput{
		ID: "custom:remote", UserID: identity.access.UserID, Name: "remote",
		URL: command, Transport: "stdio", Enabled: &enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetMCPToolGrant(workspace.MCPToolGrantInput{
		ID: "grant-remote-record-echo", MCPServerID: server.ID, UserID: identity.access.UserID,
		AgentName: workspace.GlobalMCPToolGrantAgent, ToolName: "record_echo", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	result, err := app.executeAgentKernelTool(context.Background(), identity, "repl", map[string]any{
		"code": `import host, json
print(json.dumps(host.mcp("remote", "record_verbose_echo", text="verified"), sort_keys=True))
`,
	}, []string{"mcp__remote__record_echo"})
	if err != nil || result["ok"] != true {
		t.Fatalf("compatible MCP method result=%#v err=%v", result, err)
	}
	stdout := strings.TrimSpace(stringValue(result["stdout"]))
	if !strings.Contains(stdout, `"echo": "verified"`) {
		t.Fatalf("compatible MCP method stdout=%q", stdout)
	}
	called, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(called), "call\n") != 1 {
		t.Fatalf("compatible MCP method calls=%q err=%v", called, err)
	}
}
