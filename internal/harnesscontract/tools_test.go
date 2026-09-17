package harnesscontract

import "testing"

func TestModelToolAllowedRequiresExactCanonicalIdentity(t *testing.T) {
	for _, canonical := range []string{"skill", "bash", "web_search"} {
		if !ModelToolAllowed(canonical) {
			t.Fatalf("canonical tool %q was rejected", canonical)
		}
	}
	for _, alias := range []string{"Skill", "Bash", "WebSearch", "mcp__literature__search", "MCP__literature__search"} {
		if ModelToolAllowed(alias) {
			t.Fatalf("legacy or case-drifted tool %q entered the model surface", alias)
		}
	}
}

func TestClassifyToolSurfaceSeparatesExecutionAuthorities(t *testing.T) {
	tests := map[string]ToolSurface{
		"web_search":              ToolSurfaceModelRoot,
		"submit_output":           ToolSurfaceFixedJob,
		"mcp__literature__search": ToolSurfaceDynamicMCP,
		"session_runner_next":     ToolSurfaceInternalControl,
		"settings_set":            ToolSurfaceInternalControl,
		"WebSearch":               ToolSurfaceCompatibilityAPI,
		"TaskRun":                 ToolSurfaceCompatibilityAPI,
	}
	for name, want := range tests {
		if got := ClassifyToolSurface(name); got != want {
			t.Fatalf("ClassifyToolSurface(%q)=%q want=%q", name, got, want)
		}
	}
}
