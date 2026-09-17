package server

import (
	"testing"

	"synon-go/internal/agentruntime"
)

func TestGovernedScientificExecutionAuthorityOnlyRetiresAggregateExecutor(t *testing.T) {
	schemas := []agentruntime.ToolSchema{
		{Name: "web_search"}, {Name: "read_file"}, {Name: "edit_file"},
		{Name: softwareRuntimeToolName}, {Name: manageEnvironmentsToolName}, {Name: managePackagesToolName},
		{Name: "python"}, {Name: "r"}, {Name: "repl"}, {Name: "bash"},
		{Name: "save_artifacts"}, {Name: "mcp__chemistry__lookup"},
	}
	filtered := enforceGovernedScientificExecutionAuthority(schemas)
	for _, name := range []string{
		"web_search", "read_file", "edit_file", manageEnvironmentsToolName, managePackagesToolName,
		"python", "r", "repl", "bash", "save_artifacts", "mcp__chemistry__lookup",
	} {
		if !agentRuntimeToolSchemaNamed(filtered, name) {
			t.Fatalf("capability Tool %q was removed from %#v", name, agentRuntimeToolSchemaNames(filtered))
		}
	}
	if agentRuntimeToolSchemaNamed(filtered, softwareRuntimeToolName) {
		t.Fatalf("retired aggregate executor remained available: %#v", agentRuntimeToolSchemaNames(filtered))
	}
}
