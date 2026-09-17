package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/skills"
)

func TestRuntimeMCPConnectorSkillsExposeExactSessionScopedSchemas(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{
			Name: "mcp__pubmed__get_article_metadata", Description: "Fetch article metadata by PMID.",
			Parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"pmids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				},
				"required": []string{"pmids"},
			},
			OutputSchema: map[string]any{
				"type": "object", "properties": map[string]any{
					"count": map[string]any{"type": "number"},
					"articles": map[string]any{"type": "array", "items": map[string]any{
						"type": "object", "properties": map[string]any{"pmid": map[string]any{"type": "string"}},
					}},
				},
			},
		},
		{
			Name: "mcp__chembl__compound_search", Description: "Search compounds.",
			Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"search_term": map[string]any{"type": "string"}},
				"required": []string{"search_term"},
			},
		},
	}
	dynamic := runtimeMCPConnectorSkills(schemas)
	if len(dynamic) != 2 {
		t.Fatalf("runtime connector skills=%#v", dynamic)
	}
	var pubmed skills.Skill
	for _, candidate := range dynamic {
		if candidate.Name == "mcp-pubmed" {
			pubmed = candidate
		}
	}
	if pubmed.Name == "" || !strings.Contains(pubmed.Body, `"pmids"`) ||
		!strings.Contains(pubmed.Description, "Fetch article metadata by PMID") ||
		!strings.Contains(pubmed.Body, `host.mcp("pubmed", "get_article_metadata", {"pmids":["value"]})`) ||
		!strings.Contains(pubmed.Body, "articles:array<object>") ||
		!strings.Contains(pubmed.Body, "Use the displayed method name and JSON Schema exactly") ||
		!strings.Contains(pubmed.Body, "inspect its documented shape before reading fields") ||
		!strings.Contains(pubmed.Body, "`host.mcp.collect`") ||
		!strings.HasPrefix(pubmed.Path, "builtin:") {
		t.Fatalf("pubmed runtime Skill did not preserve exact schema and example: %#v", pubmed)
	}

	server := New(Options{FileRoot: t.TempDir(), SkillCatalog: skills.NewCatalog()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	authority := map[string]struct{}{"repl": {}}
	searched, err := server.executeSkillSearchToolWithRuntimeSkills(
		map[string]any{"query": "PubMed PMID metadata"}, dynamic, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	matches := stringArrayValue(searched.(map[string]any)["matches"])
	if len(matches) == 0 || matches[0] != "mcp-pubmed" {
		t.Fatalf("session MCP Skill search matches=%#v", matches)
	}
	loaded, err := server.executeSkillToolWithRuntimeSkills(
		context.Background(), map[string]any{"skill": "mcp-pubmed"}, dynamic, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := loaded.(map[string]any)
	if !strings.Contains(stringValue(result["prompt"]), `"pmids"`) {
		t.Fatalf("loaded runtime MCP Skill=%#v", result)
	}
	filtered, err := server.executeSkillToolWithRuntimeSkills(
		context.Background(), map[string]any{"skill": "mcp-pubmed", "filter": "article metadata"}, dynamic, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	filteredResult := filtered.(map[string]any)
	if stringValue(filteredResult["filter"]) != "article metadata" ||
		!strings.Contains(stringValue(filteredResult["prompt"]), "Methods matching `article metadata`") ||
		!strings.Contains(stringValue(filteredResult["prompt"]), "## get_article_metadata") {
		t.Fatalf("filtered runtime MCP Skill=%#v", filteredResult)
	}
	if _, found := findCatalogSkill(server.skillCatalog, "mcp-pubmed"); found {
		t.Fatal("session-scoped MCP Skill leaked into the global catalog")
	}
}

func TestRuntimeMCPConnectorSkillIndexesLargeConnectorsUntilFiltered(t *testing.T) {
	schemas := make([]agentruntime.ToolSchema, 0, runtimeMCPConnectorInlineMethodLimit+1)
	for index := 0; index <= runtimeMCPConnectorInlineMethodLimit; index++ {
		schemas = append(schemas, agentruntime.ToolSchema{
			Name:        fmt.Sprintf("mcp__large__method_%02d", index),
			Description: fmt.Sprintf("Method %02d searches a distinct evidence domain.", index),
			Parameters: map[string]any{
				"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}},
			},
		})
	}
	body, _ := runtimeMCPConnectorSkillBody("large", schemas)
	if !strings.Contains(body, "Full parameter schemas are omitted") || strings.Contains(body, "Input schema:") {
		t.Fatalf("large connector body did not stay index-only: %s", body)
	}
	filtered, _ := runtimeMCPConnectorFilteredSkillBody("large", schemas, "method 07")
	if !strings.Contains(filtered, "## method_07") || !strings.Contains(filtered, "Input schema:") {
		t.Fatalf("filtered connector body missing exact method schema: %s", filtered)
	}
}

func TestRuntimeSkillsByNameWithConnectorSchemasRestoresDynamicSkillWithoutGlobalCatalog(t *testing.T) {
	schemas := []agentruntime.ToolSchema{{
		Name: "mcp__pubmed__search_articles",
		Parameters: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"query": map[string]any{"type": "string"}},
			"required":   []string{"query"},
		},
	}}
	server := New(Options{FileRoot: t.TempDir(), SkillCatalog: skills.NewCatalog()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	selected, err := server.runtimeSkillsByNameWithConnectorSchemas(
		[]string{"mcp-pubmed"}, nil, schemas,
	)
	if err != nil || len(selected) != 1 || selected[0].Name != "mcp-pubmed" ||
		!strings.Contains(selected[0].Body, `"query"`) {
		t.Fatalf("restored connector Skills=%#v err=%v", selected, err)
	}
	selected, err = server.runtimeSkillsByNameWithConnectorSchemas(
		[]string{"mcp-pubmed"}, []string{"mcp-pubmed"}, schemas,
	)
	if err != nil || len(selected) != 0 {
		t.Fatalf("excluded connector Skills=%#v err=%v", selected, err)
	}
}

func TestRuntimeSkillsResolveDeclaredDynamicConnectorDependency(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{
		Name: "research-mode", RequiredSkills: []string{"mcp-synon-research"},
	})
	server := New(Options{FileRoot: t.TempDir(), SkillCatalog: catalog})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	schemas := []agentruntime.ToolSchema{{
		Name:       "mcp__synon-research__life_science_sources",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
	}}
	selected, err := server.runtimeSkillsByNameWithConnectorSchemas([]string{"research-mode"}, nil, schemas)
	if err != nil || len(selected) != 2 {
		t.Fatalf("connector dependency selection=%#v err=%v", selected, err)
	}
	if selected[0].Name != "research-mode" || selected[1].Name != "mcp-synon-research" {
		t.Fatalf("connector dependency roots=%#v", selected)
	}
	allowed := runtimeSkillAllowedNamesWithConnectorDependencies([]string{"research-mode"}, selected)
	closure, err := server.expandRuntimeSkillDependencies(
		selected, nil, allowed, true, map[string]struct{}{"repl": {}},
	)
	if err != nil || len(closure) != 2 || closure[0].Name != "mcp-synon-research" || closure[1].Name != "research-mode" {
		t.Fatalf("connector dependency closure=%#v allowed=%#v err=%v", closure, allowed, err)
	}
}

func TestRuntimeSelectionDetectsOnlyConnectorSkillsForRestartRefresh(t *testing.T) {
	if !runtimeSelectionContainsMCPConnectorSkill([]string{"process-development", "/MCP-ChEMBL"}) {
		t.Fatal("connector Skill selection did not request the bounded restart refresh")
	}
	if runtimeSelectionContainsMCPConnectorSkill([]string{"process-development", "literature-review"}) {
		t.Fatal("ordinary Skill selection would trigger an unrelated MCP refresh")
	}
}

func TestRuntimeMCPConnectorSkillCarriesBundledChEMBLInputAndOutputEnvelopes(t *testing.T) {
	dynamic := runtimeMCPConnectorSkills(bundledChEMBLToolSchemas(t))
	if len(dynamic) != 1 || dynamic[0].Name != "mcp-chembl" {
		t.Fatalf("ChEMBL runtime Skills=%#v", dynamic)
	}
	body := dynamic[0].Body
	for _, required := range []string{
		"## compound_search", `"name"`, "compounds:array<object>",
		"## target_search", `"target_name"`, "targets:array<object>",
		"## get_bioactivity", "activities:array<object>", "inspect its documented shape before reading fields",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("ChEMBL runtime Skill missing %q (bytes=%d)", required, len(body))
		}
	}
}
