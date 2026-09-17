package server

import (
	"context"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
	"synon-go/internal/skills"
)

func TestConnectorSkillGatewaySharesAdmittedMethodAuthority(t *testing.T) {
	catalog := skills.NewCatalog()
	catalog.AddSkill(skills.Skill{Name: "research-workflow", RequiredSkills: []string{"mcp-fixture"}, Tools: []string{"repl"}})
	server := New(Options{FileRoot: t.TempDir(), SkillCatalog: catalog})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	gateway := serverAgentRuntimeToolGateway{
		server: server, hasToolSnapshot: true,
		toolSchemas: []agentruntime.ToolSchema{
			{Name: "repl"}, {Name: "skill"}, {Name: "search_skills"},
			{Name: "mcp__fixture__record", Parameters: map[string]any{"type": "object"}},
		},
		skillPolicy: runtimeSkillPolicyAuthority{Restrict: true, AllowedNames: []string{"research-workflow"}},
	}
	for _, name := range []string{"mcp-fixture", "research-workflow"} {
		search, err := gateway.executeAgentToolResponseUncached(context.Background(), agentruntime.ToolCall{}, "search_skills", map[string]any{"query": "select:" + name})
		if err != nil || len(stringArrayValue(mapValue(search)["matches"])) != 1 {
			t.Fatalf("admitted dependency not discoverable: name=%s result=%#v err=%v", name, search, err)
		}
		loaded, err := gateway.executeAgentToolResponseUncached(context.Background(), agentruntime.ToolCall{}, "skill", map[string]any{"skill": name})
		if err != nil || !strings.Contains(stringValue(loaded), "fixture") {
			t.Fatalf("admitted dependency not loadable: name=%s result=%#v err=%v", name, loaded, err)
		}
	}
	if _, found := findCatalogSkill(catalog, "mcp-fixture"); found {
		t.Fatal("session connector leaked into global catalog")
	}
	gateway.skillPolicy.ExcludedNames = []string{"mcp-fixture"}
	_, err := gateway.executeAgentToolResponseUncached(context.Background(), agentruntime.ToolCall{}, "skill", map[string]any{"skill": "mcp-fixture"})
	if err == nil {
		t.Fatal("explicit connector exclusion was bypassed")
	}
	gateway.skillPolicy.ExcludedNames = nil
	gateway.toolSchemas = gateway.toolSchemas[:3]
	_, err = gateway.executeAgentToolResponseUncached(context.Background(), agentruntime.ToolCall{}, "skill", map[string]any{"skill": "research-workflow"})
	if err == nil {
		t.Fatal("absent connector dependency was invented")
	}
}
