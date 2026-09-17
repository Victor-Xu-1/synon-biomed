package server

import (
	"testing"

	"synon-go/internal/agentruntime"
)

func TestCompactRuntimeSnapshotKeepsDiscoveryAndInvocationPairsTogether(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "ToolSearch"},
		{Name: "search_skills"},
		{Name: "skill"},
		{Name: "repl"},
		{Name: "ListMcpTools"},
	}
	for index := 0; index < 72; index++ {
		schemas = append(schemas, agentruntime.ToolSchema{Name: "deferred_non_reference"})
	}

	active := partitionAgentRuntimeToolSchemasForModel(schemas)
	for _, name := range []string{"search_skills", "skill", "repl"} {
		if !agentRuntimeToolSchemaNamed(active, name) {
			t.Fatalf("active tool snapshot omitted %s: %#v", name, active)
		}
	}
	if agentRuntimeToolSchemaNamed(active, "ToolSearch") ||
		agentRuntimeToolSchemaNamed(active, "ListMcpTools") {
		t.Fatalf("retired ToolSearch leaked into the model snapshot: active=%#v", active)
	}
}
