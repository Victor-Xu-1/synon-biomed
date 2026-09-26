package agentruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRejectedAndFailedToolReceiptsNeverReportProgress(t *testing.T) {
	for _, value := range []map[string]any{
		{"ok": false, "executed": false, "code": "invalid_file_structure"},
		{"ok": false, "executed": false, "status": "edit_preflight_required", "code": "edit_conflict"},
		{"ok": false, "error": "native failure"},
		{"success": true, "changed": false},
		{"ok": true, "reused": false, "effect": ToolEffectValue(ToolEffectUnchanged, "file")},
	} {
		if !toolResultReportsNoProgress(value) {
			t.Errorf("failure/no-op became progress: %v", value)
		}
	}
	engine := Engine{}
	call := ToolCall{ID: "rejected", Name: "edit_file", Arguments: json.RawMessage(`{}`)}
	batch, err := engine.executeToolCallRoundWithRejections(context.Background(), []ToolCall{call}, map[int]toolCallRejection{0: {Code: "invalid_file_structure", Message: "Rejected", Preflight: true}}, MediaPolicy{}, 0)
	if err != nil || !batch.NoProgress {
		t.Fatalf("rejection batch falsely refreshed progress: %#v %v", batch, err)
	}
}

func TestSemanticRecoveryKeepsToolAvailableForActuallyCorrectedProposal(t *testing.T) {
	responses := []ModelResponse{}
	for index := 0; index < 4; index++ {
		responses = append(responses, ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: fmt.Sprintf("invalid-%d", index), Name: "edit_file", Arguments: json.RawMessage(`{"blocked":true,"file_path":"target.txt"}`)}}}})
	}
	responses = append(responses,
		ModelResponse{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "valid", Name: "edit_file", Arguments: json.RawMessage(`{"blocked":false,"file_path":"target.txt"}`)}}}},
		ModelResponse{Message: Message{Role: "assistant", Content: "Ready for independent validation."}})
	model := &capturingRequestModelClient{responses: responses}
	var executions atomic.Int64
	engine := Engine{Model: model, Tools: preflightAwareGateway{executions: &executions}}
	_, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "Repair the candidate."}}, Tools: []ToolSchema{{Name: "edit_file", Parameters: map[string]any{"type": "object"}}}, MaxToolRounds: 4})
	if err != nil || executions.Load() != 1 {
		t.Fatalf("proposal family exhaustion disabled a valid capability: executions=%d error=%v", executions.Load(), err)
	}
	if len(model.requests[4].Tools) != 1 || model.requests[4].Tools[0].Name != "edit_file" {
		t.Fatal("tool was removed after repeated invalid proposals")
	}
}

func TestSemanticRecoveryUnrelatedReceiptDoesNotResetFamily(t *testing.T) {
	call := ToolCall{ID: "failed", Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"target.txt"}`)}
	rejection := toolCallRejection{Code: "edit_conflict", Diagnostic: "exact input does not match", Retryable: true}
	family := toolCallRejectionFamily(call, rejection)
	attempts := map[string]int{family: 4}
	for _, unrelated := range []ToolCall{
		{ID: "read", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"target.txt"}`)},
		{ID: "other-edit", Name: "edit_file", Arguments: json.RawMessage(`{"file_path":"other.txt"}`)},
	} {
		clearResolvedToolRejectionFamilies([]ToolCall{unrelated}, []Message{{Role: "tool", ToolCallID: unrelated.ID, Content: `{"ok":true,"changed":true}`}}, attempts)
		if attempts[family] != 4 {
			t.Fatal("unrelated progress reset the failure family")
		}
	}
	alias := call
	alias.Arguments = json.RawMessage(`{"path":"target.txt"}`)
	if toolCallRejectionFamily(alias, rejection) != family {
		t.Fatal("native path alias manufactured a new recovery target")
	}
	clearResolvedToolRejectionFamilies([]ToolCall{alias}, []Message{{Role: "tool", ToolCallID: alias.ID, Content: `{"ok":true,"changed":true}`}}, attempts)
	if len(attempts) != 0 {
		t.Fatal("successful target repair retained a stale private family budget")
	}
}

func TestNoProgressFeedbackNeverCallsFailedActionsSuccessful(t *testing.T) {
	model := &capturingRequestModelClient{responses: []ModelResponse{
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "failed", Name: "python", Arguments: json.RawMessage(`{"code":"invalid()"}`)}}}},
		{Message: Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "reused", Name: "read_file", Arguments: json.RawMessage(`{"file_path":"existing.txt"}`)}}}},
		{Message: Message{Role: "assistant", Content: "The failed action remains unresolved."}},
	}}
	engine := Engine{Model: model, Tools: FuncToolGateway(func(_ context.Context, call ToolCall) (ToolResult, error) {
		if call.Name == "python" {
			return ToolResult{Value: map[string]any{"ok": false, "error": "The action failed."}}, nil
		}
		return ToolResult{Value: map[string]any{"ok": true, "reused": true}}, nil
	})}
	_, err := engine.Run(context.Background(), RunRequest{Messages: []Message{{Role: "user", Content: "Inspect the task state."}}, Tools: []ToolSchema{{Name: "python"}, {Name: "read_file"}}})
	if err != nil {
		t.Fatal(err)
	}
	afterFailure := model.requests[1].Messages
	if last := afterFailure[len(afterFailure)-1]; last.Role != "tool" || last.ToolCallID != "failed" {
		t.Fatal("failed action was replaced by a success/reuse instruction")
	}
	afterRead := model.requests[2].Messages
	if last := afterRead[len(afterRead)-1]; last.Role != "system" || strings.Contains(last.Content, "python") || !strings.Contains(last.Content, "read_file") {
		t.Fatalf("reused read promoted earlier failure to success: %q", last.Content)
	}
}
