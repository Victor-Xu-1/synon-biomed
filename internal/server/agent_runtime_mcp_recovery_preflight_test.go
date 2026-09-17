package server

import (
	"context"
	"testing"
)

func TestAgentRuntimeRequiredMCPRecoveryPreflight(t *testing.T) {
	run := &sessionRunnerChatRun{
		CorrectionReason: "completion_review_correction_required",
		CorrectionDetail: "explicit user-required tool evidence is incomplete; the explicitly requested real MCP method execution has no completed MCP tool result",
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	if got := agentRuntimeRequiredMCPRecoveryPreflight(ctx, "repl", map[string]any{
		"code": `print(open("validation_record.json").read())`,
	}); stringValue(got["status"]) != "mcp_evidence_preflight_required" || got["executed"] != false {
		t.Fatalf("local-only recovery preflight=%#v", got)
	}
	if got := agentRuntimeRequiredMCPRecoveryPreflight(ctx, "repl", map[string]any{
		"code": `servers = host.mcp.list_servers()`,
	}); stringValue(got["status"]) != "mcp_evidence_preflight_required" {
		t.Fatalf("catalog-only recovery preflight=%#v", got)
	}
	if got := agentRuntimeRequiredMCPRecoveryPreflight(ctx, "repl", map[string]any{
		"code": `result = host.mcp("pubmed", "search_articles", query="nirmatrelvir")`,
	}); got != nil {
		t.Fatalf("real MCP recovery call was blocked: %#v", got)
	}
	if got := agentRuntimeRequiredMCPRecoveryPreflight(context.Background(), "repl", map[string]any{
		"code": `print("ordinary task")`,
	}); got != nil {
		t.Fatalf("ordinary repl was constrained: %#v", got)
	}
}
