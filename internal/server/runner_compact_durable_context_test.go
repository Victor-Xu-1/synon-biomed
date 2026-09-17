package server

import (
	"strings"
	"testing"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
)

func TestDeterministicCompactSummaryPreservesDurableFailureAndPendingRepair(t *testing.T) {
	entries := []eventjournal.Entry{
		{EventID: 1, Message: eventjournal.Message{"type": "message", "role": "user", "text": "运行复杂科学任务并校验全部结果"}},
		{EventID: 2, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "start", "toolCallId": "call-runtime",
			"toolName": softwareRuntimeToolName,
			"toolInput": map[string]any{
				"provider": "local-conda", "working_dir": ".", "executable": "python",
				"args": []any{"pipeline.py"}, "expected_outputs": []any{map[string]any{"path": "output/validation.json"}},
			},
		}},
		{EventID: 3, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "failed", "toolCallId": "call-runtime",
			"toolName": softwareRuntimeToolName,
			"toolResult": map[string]any{
				"ok": false, "status": "failed", "code": "quality_contract_failed", "retryable": true,
				"recovery": "repair_the_result_basis_then_rerun_same_provider_plan",
				"stderr":   "comparison_contract_missing:output/results.csv:Cmax_Cmin_ratio",
			},
		}},
	}
	summary, err := buildCompactModelContextSummary(
		sessionstore.Session{ID: "frame-compact", Title: "compact test"}, entries, "preserve exact failure", "auto",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"quality_contract_failed", "comparison_contract_missing:output/results.csv:Cmax_Cmin_ratio",
		"repair_the_result_basis_then_rerun_same_provider_plan", "call-runtime", "Pending work",
	} {
		if !strings.Contains(summary, required) {
			t.Fatalf("deterministic compact summary is missing %q:\n%s", required, summary)
		}
	}

	successfulEntries := []eventjournal.Entry{
		{EventID: 11, Message: eventjournal.Message{
			"type": "runner_checkpoint", "toolPhase": "completed", "toolCallId": "call-validated",
			"toolName": softwareRuntimeToolName, "toolInput": map[string]any{
				"args": []any{"validate_report.py"}, "executable": "python",
			}, "toolResult": map[string]any{
				"ok": true, "status": "completed", "request_digest": strings.Repeat("a", 64),
				"outputs": []any{map[string]any{"path": "validate_report_result.json"}},
			},
		}},
	}
	successSummary, err := buildCompactModelContextSummary(
		sessionstore.Session{ID: "frame-compact-success", Title: "compact success test"}, successfulEntries, "", "auto",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(successSummary, "Continuation status") ||
		!strings.Contains(successSummary, "Do not create a new inventory") ||
		!strings.Contains(successSummary, "finalize the task") {
		t.Fatalf("successful continuation state is missing:\n%s", successSummary)
	}
	prompt, err := buildCompactModelPrompt(
		sessionstore.Session{ID: "frame-compact", Title: "compact test"}, entries, "preserve exact failure", "auto",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "## Durable Runtime State") ||
		!strings.Contains(prompt, "comparison_contract_missing:output/results.csv:Cmax_Cmin_ratio") {
		t.Fatalf("model compact prompt lost durable runtime state:\n%s", prompt)
	}
}
