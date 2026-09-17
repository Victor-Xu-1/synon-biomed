package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"synon-go/internal/mcpdirectory"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAgentRuntimeWorkspaceMCPToolSchemasUsesDurablePubMedCatalog(t *testing.T) {
	root := t.TempDir()
	store, err := workspace.Open(filepath.Join(root, "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	enabled := true
	if _, err := store.CreateAgent(workspace.CreateAgentInput{
		ID: "operon", UserID: "local", Name: "OPERON", DisplayName: "OPERON",
		Description: "MCP catalog fixture", SystemPrompt: "Use PubMed evidence.", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project", UserID: "local", Name: "Project", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame", ProjectID: "project", AgentName: "OPERON", Status: "processing",
		ConversationType: "agent", Name: "MCP catalog",
	}); err != nil {
		t.Fatal(err)
	}
	directory := mcpdirectory.New(store, root, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := directory.Close(ctx); err != nil {
			t.Errorf("close MCP directory: %v", err)
		}
	})
	srv := New(Options{Workspace: store, FileRoot: root, MCPDirectory: directory})
	t.Cleanup(func() { closeTestServer(t, srv) })
	connectors, err := directory.ListUnifiedConnectors(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	for _, connector := range connectors {
		if connector.Source == "bundled" && connector.ID != "bundled:pubmed" {
			if _, err := directory.SetUnifiedEnabled(context.Background(), "local", connector.ID, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	warmCtx, warmCancel := context.WithTimeout(context.Background(), 30*time.Second)
	tools, err := directory.ListUnifiedConnectorTools(warmCtx, "local", "bundled:pubmed")
	warmCancel()
	if err != nil {
		t.Fatal(err)
	}
	foundPubMed := false
	for _, tool := range tools {
		if tool.Name == "mcp__pubmed__search_articles" {
			foundPubMed = true
			break
		}
	}
	if !foundPubMed {
		t.Fatalf("packaged PubMed catalog did not expose search_articles: %#v", tools)
	}

	catalogBefore, found, err := store.GetMCPConnectorToolCatalog("local", "bundled", "bundled:pubmed")
	if err != nil || !found {
		t.Fatalf("warm catalog found=%t err=%v", found, err)
	}
	defer func() {
		catalogAfter, found, err := store.GetMCPConnectorToolCatalog("local", "bundled", "bundled:pubmed")
		if err != nil || !found || catalogBefore.ConfigSHA256 != catalogAfter.ConfigSHA256 ||
			catalogBefore.CatalogSHA256 != catalogAfter.CatalogSHA256 || !catalogBefore.RefreshedAt.Equal(catalogAfter.RefreshedAt) {
			t.Fatalf("discovery did not reuse the warm durable catalog: found=%t err=%v before=%#v after=%#v", found, err, catalogBefore, catalogAfter)
		}
	}()
	// Cache reuse is asserted by the durable catalog's identity and refresh
	// receipt, not a 250 ms scheduling assumption on instrumented CI runners.
	discoveryCtx, discoveryCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer discoveryCancel()
	discovery := srv.agentRuntimeWorkspaceMCPToolSchemas(discoveryCtx, "frame", nil, nil)
	if discovery.Unavailable {
		t.Fatalf("durable PubMed catalog was reported unavailable: %#v", discovery)
	}
	for _, schema := range discovery.Schemas {
		if schema.Name == "mcp__pubmed__search_articles" {
			authority, err := srv.bindSessionRunnerToolAuthority(
				context.Background(), sessionstore.Session{ID: "frame"}, SessionRunnerChatOptions{
					// The dispatcher starts from the compact model-facing boundary.
					// Durable recovery must expand it with internal host authorities.
					AllowedTools: []string{"repl", "web_search"},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if !containsAgentToolName(authority.Options.AllowedTools, "mcp__pubmed__search_articles") {
				t.Fatalf("durable recovery authority omitted cached PubMed search: allowed=%v schemas=%v",
					authority.Options.AllowedTools, agentRuntimeToolSchemaNames(authority.Schemas))
			}
			if agentRuntimeToolSchemaNamed(
				partitionAgentRuntimeToolSchemasForModel(authority.Schemas),
				"mcp__pubmed__search_articles",
			) {
				t.Fatal("dynamic PubMed method leaked into the compact model-facing tool set")
			}
			return
		}
	}
	t.Fatalf("ARK tool snapshot omitted cached PubMed search: %#v", discovery.Schemas)
}
