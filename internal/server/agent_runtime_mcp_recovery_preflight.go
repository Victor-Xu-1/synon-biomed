package server

import (
	"context"
	"regexp"
	"strings"
)

var directHostMCPCallPattern = regexp.MustCompile(`host\s*\.\s*mcp\s*\(`)

// agentRuntimeRequiredMCPRecoveryPreflight prevents a correction unit from
// satisfying a missing-MCP review finding with an unrelated REPL cell. It is
// scoped to a durable server-authored correction and does not prescribe which
// connector or method the model must choose. Catalog inspection remains model
// directed; the admitted cell must also contain one real host.mcp invocation.
func agentRuntimeRequiredMCPRecoveryPreflight(
	ctx context.Context,
	toolName string,
	input map[string]any,
) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(toolName), "repl") {
		return nil
	}
	run, _ := ctx.Value(transcriptRunnerChatRunContextKey{}).(*sessionRunnerChatRun)
	if run == nil || run.CorrectionReason != "completion_review_correction_required" ||
		!runnerCorrectionRequiresMCPExecution(run.CorrectionDetail) {
		return nil
	}
	if directHostMCPCallPattern.MatchString(stringValue(input["code"])) {
		return nil
	}
	return map[string]any{
		"ok":       true,
		"status":   "mcp_evidence_preflight_required",
		"executed": false,
		"message":  "This recovery unit must execute one task-relevant live MCP method. Inspect host.mcp.list_servers()/list_methods() as needed, then call host.mcp(server, method, **arguments) in the same corrected REPL cell. Local file validation or re-saving artifacts does not satisfy the missing MCP evidence.",
	}
}
