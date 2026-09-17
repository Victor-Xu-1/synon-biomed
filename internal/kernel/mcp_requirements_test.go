package kernel

import (
	"strings"
	"testing"
)

func TestMCPPythonRequirementsUseValidatedPinnedContract(t *testing.T) {
	manager := &Manager{}
	requirements, err := manager.MCPPythonRequirements()
	if err != nil {
		t.Fatal(err)
	}
	if len(requirements) < 20 {
		t.Fatalf("requirements=%d want the complete MCP runtime contract", len(requirements))
	}
	seen := make(map[string]struct{}, len(requirements))
	for _, requirement := range requirements {
		key := strings.ToLower(requirement)
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("duplicate requirement %q", requirement)
		}
		seen[key] = struct{}{}
	}
	for _, required := range []string{
		"mcp==2.1.1", "mcp-types==2.1.1", "httpx==0.28.1", "httpx2==2.12.0",
		"requests==2.34.2", "jsonschema==4.26.0", "lxml==6.1.1",
	} {
		if _, found := seen[required]; !found {
			t.Fatalf("missing direct MCP dependency %q", required)
		}
	}
}
