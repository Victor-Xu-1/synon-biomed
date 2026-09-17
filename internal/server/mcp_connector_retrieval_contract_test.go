package server

import (
	"fmt"
	"strings"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestRuntimeMCPConnectorSkillKeepsOnlyExecutableSchemaGuidance(t *testing.T) {
	methods := make([]agentruntime.ToolSchema, 0, 15)
	for index := 0; index < 15; index++ {
		methods = append(methods, agentruntime.ToolSchema{
			Name:        fmt.Sprintf("mcp__literature__search_source_%02d", index),
			Description: "Search literature evidence with cursor pagination.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string"}, "cursor": map[string]any{"type": "string"},
			}},
		})
	}
	selected := runtimeMCPConnectorFilteredMethods(methods, "search source")
	if len(selected) != 15 {
		t.Fatalf("filtered methods=%d want 15", len(selected))
	}
	body, _ := runtimeMCPConnectorFilteredSkillBody("literature", methods, "search source")
	for _, required := range []string{
		"exact live, session-scoped MCP schema snapshot", "host.mcp(server, method, input)",
		"displayed method name and JSON Schema exactly", "connector runtime owns pagination",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("connector Skill body missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"both Chinese and English", "100-500 candidates", "independent evidence tracks",
		"zero-result or very small first page", "never present one short page",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("connector Skill body retained workflow prompt %q", forbidden)
		}
	}
}
