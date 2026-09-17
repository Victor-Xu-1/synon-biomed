package server

import (
	"encoding/json"
	"testing"

	"synon-go/internal/agentruntime"
)

func TestFreshExecutionRequestRequiresCurrentSuccessfulExecution(t *testing.T) {
	request := []agentruntime.Message{{Role: "user", Content: "请重新计算这组稳定性数据，并更新报告。"}}
	if err := sessionRunnerFreshExecutionCompletionError(request, nil); err == nil {
		t.Fatal("fresh execution request accepted without current execution evidence")
	}

	result := []agentruntime.Message{
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{
			ID: "run-1", Name: "bash", Arguments: json.RawMessage(`{"command":"python analysis.py"}`),
		}}},
		{Role: "tool", ToolCallID: "run-1", Content: `{"ok":true,"executed":true,"exit_status":"ok","stdout":"complete"}`},
	}
	if err := sessionRunnerFreshExecutionCompletionError(request, result); err != nil {
		t.Fatalf("fresh successful execution rejected: %v", err)
	}
}

func TestFreshExecutionRequestDoesNotConstrainOrdinaryOrNegatedRequests(t *testing.T) {
	for _, content := range []string{
		"解释现有结果。",
		"不要重新计算，只总结现有报告。",
		"Do not rerun the analysis; summarize the existing result.",
	} {
		if sessionRunnerFreshExecutionRequested([]agentruntime.Message{{Role: "user", Content: content}}) {
			t.Fatalf("ordinary request %q incorrectly required fresh execution", content)
		}
	}
}

func TestRejectedOrFailedToolDoesNotSatisfyFreshExecution(t *testing.T) {
	for _, messages := range [][]agentruntime.Message{
		{
			{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "rejected", Name: "python", RejectedBeforeExecution: true}}},
			{Role: "tool", ToolCallID: "rejected", Content: `{"ok":true,"executed":true}`},
		},
		{
			{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "failed", Name: "python"}}},
			{Role: "tool", ToolCallID: "failed", Content: `{"ok":false,"executed":true,"error":"boom"}`},
		},
		{
			{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "read", Name: "read_file"}}},
			{Role: "tool", ToolCallID: "read", Content: `{"ok":true,"content":"old report"}`},
		},
	} {
		if sessionRunnerHasFreshSuccessfulExecution(messages) {
			t.Fatalf("non-execution result satisfied fresh execution: %#v", messages)
		}
	}
}

func TestFreshExecutionEvidenceMustComeAfterCurrentRequestWindow(t *testing.T) {
	request := []agentruntime.Message{
		{Role: "user", Content: "earlier task"},
		{Role: "assistant", ToolCalls: []agentruntime.ToolCall{{ID: "old-run", Name: "python"}}},
		{Role: "tool", ToolCallID: "old-run", Content: `{"ok":true,"executed":true}`},
		{Role: "user", Content: "请重新计算。"},
	}
	result := append([]agentruntime.Message(nil), request...)
	result = append(result, agentruntime.Message{Role: "assistant", Content: "复用旧结果。"})
	fresh := result[len(request):]
	if sessionRunnerHasFreshSuccessfulExecution(fresh) {
		t.Fatal("historical execution receipt leaked into current request evidence")
	}
}
