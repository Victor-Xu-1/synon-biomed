package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/mcpdirectory"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestDiagnosticRealMCPToolSnapshot(t *testing.T) {
	databasePath := strings.TrimSpace(os.Getenv("SYNON_DIAGNOSTIC_WORKSPACE_DB"))
	homeDir := strings.TrimSpace(os.Getenv("SYNON_DIAGNOSTIC_HOME"))
	sourceRoot := strings.TrimSpace(os.Getenv("SYNON_DIAGNOSTIC_SOURCE_ROOT"))
	if databasePath == "" || homeDir == "" || sourceRoot == "" {
		t.Skip("real MCP snapshot diagnostic inputs are not configured")
	}
	const frameID = "frame"
	source, err := sql.Open("sqlite", "file:"+databasePath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	store, err := workspace.Open(filepath.Join(t.TempDir(), "workspace.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	enabled := true
	if _, err := store.CreateAgent(workspace.CreateAgentInput{
		ID: "operon", UserID: "local", Name: "OPERON", DisplayName: "OPERON",
		Description: "real MCP snapshot diagnostic", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project", UserID: "local", Name: "MCP diagnostic", Path: homeDir,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: "project", AgentName: "OPERON", Status: "processing",
		ConversationType: "agent", Name: "MCP diagnostic",
	}); err != nil {
		t.Fatal(err)
	}
	contextData := map[string]any{
		"web_assistant": map[string]any{
			"conversation_overrides": map[string]any{
				"model":      "ark-code-latest",
				"permission": "default",
			},
			"id":     "synonbiomed:operon",
			"locale": "zh-CN",
		},
		"web_extra": map[string]any{
			"backend":          "synonbiomed",
			"custom_workspace": false,
			"default_files":    []any{},
			"project_id":       "project",
			"workspace":        "synonbiomed://project/project",
		},
	}
	if _, err := store.SetFrameRuntimeMetadata(frameID, workspace.FrameRuntimeMetadata{ContextData: contextData}); err != nil {
		t.Fatal(err)
	}
	stateRows, err := source.Query(`SELECT user_id,source,connector_id,enabled,last_status,last_error,tool_count,schema_sha256,updated_at FROM mcp_connector_runtime_state WHERE user_id='local'`)
	if err != nil {
		t.Fatal(err)
	}
	for stateRows.Next() {
		var state workspace.MCPConnectorRuntimeState
		if err := stateRows.Scan(&state.UserID, &state.Source, &state.ConnectorID, &state.Enabled, &state.LastStatus, &state.LastError, &state.ToolCount, &state.SchemaSHA256, &state.UpdatedAt); err != nil {
			stateRows.Close()
			t.Fatal(err)
		}
		if err := store.PutMCPConnectorRuntimeState(state); err != nil {
			stateRows.Close()
			t.Fatal(err)
		}
	}
	if err := stateRows.Close(); err != nil {
		t.Fatal(err)
	}
	catalogRows, err := source.Query(`SELECT user_id,source,connector_id,config_sha256,catalog_sha256,catalog_json,refreshed_at FROM mcp_connector_tool_catalogs WHERE user_id='local'`)
	if err != nil {
		t.Fatal(err)
	}
	for catalogRows.Next() {
		var catalog workspace.MCPConnectorToolCatalog
		if err := catalogRows.Scan(&catalog.UserID, &catalog.Source, &catalog.ConnectorID, &catalog.ConfigSHA256, &catalog.CatalogSHA256, &catalog.CatalogJSON, &catalog.RefreshedAt); err != nil {
			catalogRows.Close()
			t.Fatal(err)
		}
		if err := store.PutMCPConnectorToolCatalog(catalog); err != nil {
			catalogRows.Close()
			t.Fatal(err)
		}
	}
	if err := catalogRows.Close(); err != nil {
		t.Fatal(err)
	}
	directory := mcpdirectory.New(store, homeDir, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := directory.Close(ctx); err != nil {
			t.Errorf("close MCP directory: %v", err)
		}
	})
	app := New(Options{
		Workspace: store, FileRoot: homeDir, MCPDirectory: directory,
		SkillDirectories:  []string{filepath.Join(sourceRoot, "skills")},
		AgentCatalogRoot:  filepath.Join(sourceRoot, "assets", "synonbiomed", "agents"),
		AgentManifestPath: filepath.Join(sourceRoot, "assets", "synonbiomed", "agents.manifest.json"),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runtimeContext, found, err := app.workspaceMCPRuntimeContextWithContext(ctx, frameID)
	if err != nil || !found {
		t.Fatalf("runtime context found=%v err=%v", found, err)
	}
	t.Logf("runtime connectors=%d", len(runtimeContext.Connectors))
	discovery := app.agentRuntimeWorkspaceMCPToolSchemas(ctx, frameID, nil, nil)
	t.Logf("workspace MCP schemas=%d unavailable=%v", len(discovery.Schemas), discovery.Unavailable)
	schemas := app.agentRuntimeToolSchemasWithContext(ctx, nil, frameID)
	mcpCount := 0
	foundPubMed := false
	for _, schema := range schemas {
		if strings.HasPrefix(schema.Name, "mcp__") {
			mcpCount++
		}
		if schema.Name == "mcp__pubmed__search_articles" {
			foundPubMed = true
		}
	}
	t.Logf("all schemas=%d MCP schemas=%d PubMed=%v", len(schemas), mcpCount, foundPubMed)
	if !foundPubMed {
		t.Fatal("real task snapshot omitted PubMed despite enabled durable connector catalog")
	}
	options, selected, err := app.applySessionRunnerAgentProfile(
		sessionstore.Session{ID: frameID},
		normalizeSessionRunnerChatOptions(SessionRunnerChatOptions{}),
		agentRuntimeToolSchemaNames(schemas),
	)
	if err != nil {
		t.Fatal(err)
	}
	filtered := filterAgentRuntimeToolSchemas(schemas, options.AllowedTools)
	active := partitionAgentRuntimeToolSchemasForModel(filtered)
	t.Logf("selected=%s allowed=%d filtered=%d active=%d", selected,
		len(options.AllowedTools), len(filtered), len(active))
	if !agentRuntimeToolSchemaNamed(filtered, "mcp__pubmed__search_articles") {
		t.Fatal("OPERON profile filtered PubMed from the real task snapshot")
	}
}
