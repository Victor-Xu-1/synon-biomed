package server

import (
	"context"
	"strings"
	"synon-go/internal/agentruntime"
	"testing"
)

func TestWorkspaceEditReceiptShowsResultingContent(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	gateway := serverAgentRuntimeToolGateway{
		server: fixture.server, kernel: fixture.identity, sessionID: fixture.stream.SessionID,
		toolSchemas:     []agentruntime.ToolSchema{agentWorkspaceEditFileToolSchema()},
		hasToolSnapshot: true, suppressHooks: true,
	}
	executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": "report.md", "old_string": "", "new_string": "# Report\nFirst section\n",
	})
	result := executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": "report.md", "old_string": "First section", "new_string": "Second section",
	})
	view := mapValue(result["file_view"])
	content := stringValue(view["content"])
	if result["success"] != true || !strings.Contains(content, "# Report") || !strings.Contains(content, "Second section") || strings.Contains(content, "First section") {
		t.Fatalf("receipt did not expose actual replacement outcome: %#v", result)
	}
	large := strings.Repeat("长文\"\\\n", 20000)
	result = executeAgentWorkspaceToolForTest(t, gateway, "edit_file", map[string]any{
		"file_path": "report.md", "old_string": "", "new_string": large,
	})
	view = mapValue(result["file_view"])
	content = stringValue(view["content"])
	if result["success"] != true || view["truncated"] != true || len(content) == 0 || !strings.HasPrefix(large, content) || !agentWorkspaceReadResultFits(withAgentWorkspaceReadBudget(context.Background(), runnerLargeToolResultInlineLimitBytes), result) {
		t.Fatalf("large edit feedback lost its inline content or exceeded transport: %#v", result)
	}
}
