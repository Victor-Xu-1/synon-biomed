package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"synon-go/internal/agentruntime"
)

func prepareApprovedNativeConflict(t *testing.T) (*correctionRouteFixture, SessionRunnerChatOptions, agentruntime.ToolCall, string) {
	t.Helper()
	fixture := newCorrectionRouteFixture(t)
	const name = "report.md"
	const original = "original test bytes"
	if err := os.WriteFile(filepath.Join(fixture.projectPath, name), []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"file_path": name, "old_string": "ABSENT_TEST_SENTINEL", "new_string": "repaired", "human_description": "Exercise native conflict after approval"}
	raw, _ := json.Marshal(input)
	call := agentruntime.ToolCall{ID: "approved-conflict", Name: "edit_file", Arguments: raw}
	run := fixture.run
	options := SessionRunnerChatOptions{SessionID: run.SessionID, RunnerID: run.Transcript.Claim.RunnerID, AllowedTools: []string{"edit_file", "read_file"}}
	if err := fixture.server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{Type: agentruntime.EventToolStarted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(raw)}); err != nil {
		t.Fatal(err)
	}
	id := agentRuntimeApprovalID(call.Name, call, input)
	if err := fixture.server.checkpointChatTool(options, run, "waiting", "waiting", call.ID, "waiting", map[string]any{"toolName": call.Name, "toolInput": input, "toolResult": map[string]any{"approval_kind": agentToolApprovalKind, "approval_id": id, "request_id": id, "tool_call_id": call.ID, "tool_name": call.Name, "frame_id": run.Transcript.Stream.FrameID}}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.server.runtimeStore.Set(agentRuntimeApprovalNamespace, id, map[string]any{"status": "pending", "sessionId": run.SessionID, "tool": call.Name, "toolCallId": call.ID, "input": input}); err != nil {
		t.Fatal(err)
	}
	if _, rejected, err := fixture.server.resolveCompatibilityAgentToolApproval(context.Background(), map[string]any{"approval_id": id}, compatibilityInputResponse{Action: "allow_once"}); err != nil || rejected {
		t.Fatalf("approval rejected=%v error=%v", rejected, err)
	}
	if err := fixture.server.bindRunnableToolCallBatches(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	return fixture, options, call, id
}

func TestApprovedNativeEditConflictReturnsDurableFeedbackAndContinues(t *testing.T) {
	fixture, options, call, id := prepareApprovedNativeConflict(t)
	run := fixture.run
	const name, original = "report.md", "original test bytes"
	resumed, err := fixture.server.resumePendingAgentToolCalls(context.Background(), options, run)
	if err != nil || !resumed {
		t.Fatalf("ordinary edit conflict terminated approval recovery: resumed=%v error=%v", resumed, err)
	}
	if repeated, err := fixture.server.resumePendingAgentToolCalls(context.Background(), options, run); err != nil || repeated {
		t.Fatalf("settled rejection re-executed: resumed=%v error=%v", repeated, err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.projectPath, name))
	if err != nil || string(data) != original {
		t.Fatalf("conflict altered file: %q %v", data, err)
	}
	items, _, err := fixture.server.loadTranscriptWebHistory(context.Background(), fixture.stream.OwnerID, fixture.stream.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, item := range items {
		if item["type"] != "tool_call" {
			continue
		}
		content := mapValue(item["content"])
		if content["call_id"] != call.ID {
			continue
		}
		seen++
		var output map[string]any
		if err := json.Unmarshal([]byte(stringValue(content["output"])), &output); err != nil {
			t.Fatal(err)
		}
		if content["status"] != "error" || content["phase"] != prestartToolFailurePhase || output["code"] != "edit_conflict" || output["executed"] != false {
			t.Fatalf("conflict receipt misrepresented: %#v", content)
		}
	}
	if seen != 1 {
		t.Fatalf("expected one durable rejected call, got %d", seen)
	}
	entry, found, err := fixture.server.runtimeStore.Get(agentRuntimeApprovalNamespace, id)
	if err != nil || !found || mapValue(entry.Value)["status"] != "failed" {
		t.Fatalf("gateway phase escaped into approval state: %#v %v", entry.Value, err)
	}
	if _, rejected, err := fixture.server.resolveCompatibilityAgentToolApproval(context.Background(), map[string]any{"approval_id": id}, compatibilityInputResponse{Action: "allow_once"}); err != nil || !rejected {
		t.Fatalf("duplicate decision lost the failed receipt: rejected=%v err=%v", rejected, err)
	}
}

func TestApprovedNativeFailureReceiptSurvivesReopenBeforeBatchSettlement(t *testing.T) {
	fixture, options, call, id := prepareApprovedNativeConflict(t)
	items, err := fixture.store.ListToolCallBatchItems(context.Background(), fixture.stream.OwnerID, fixture.run.ToolBatchIDs[call.ID])
	if err != nil || len(items) != 1 {
		t.Fatalf("batch items=%v error=%v", items, err)
	}
	gateway := fixture.server.approvedRunnerToolGateway(fixture.run, items[0], id)
	result, err := gateway.Execute(withTranscriptRunnerChatRun(context.Background(), fixture.run), call)
	if err != nil || !agentruntime.IsNonExecutingPreflight(result.Value) {
		t.Fatalf("expected real native rejection: %#v %v", result.Value, err)
	}
	entry, found, err := fixture.server.runtimeStore.Get(agentRuntimeApprovalNamespace, id)
	if err != nil || !found {
		t.Fatal("approval receipt unavailable")
	}
	receipt := mapValue(entry.Value)
	// Reproduce the observed older writer's recoverable gateway status at the
	// crash boundary between approval-result persistence and batch settlement.
	receipt["status"] = "partial"
	if _, err := fixture.server.runtimeStore.Set(agentRuntimeApprovalNamespace, id, receipt); err != nil {
		t.Fatal(err)
	}
	fixture.reopen(t)
	if resumed, err := fixture.server.resumePendingAgentToolCalls(context.Background(), options, fixture.run); err != nil || !resumed {
		t.Fatalf("durable failure could not recover: %v %v", resumed, err)
	}
	if resumed, err := fixture.server.resumePendingAgentToolCalls(context.Background(), options, fixture.run); err != nil || resumed {
		t.Fatalf("failure was replayed: %v %v", resumed, err)
	}
	entry, found, err = fixture.server.runtimeStore.Get(agentRuntimeApprovalNamespace, id)
	if err != nil || !found || mapValue(entry.Value)["completedAt"] != receipt["completedAt"] {
		t.Fatal("recovery executed the rejected tool again")
	}
	items, err = fixture.store.ListToolCallBatchItems(context.Background(), fixture.stream.OwnerID, fixture.run.ToolBatchIDs[call.ID])
	if err != nil || len(items) != 1 || items[0].State != "failed" || items[0].TerminalEventID == 0 {
		t.Fatalf("durable failure not settled: %#v %v", items, err)
	}
	data, err := os.ReadFile(filepath.Join(fixture.projectPath, "report.md"))
	if err != nil || string(data) != "original test bytes" {
		t.Fatalf("recovery changed rejected target: %q %v", data, err)
	}
}

func TestApprovalStatusPreservesPartialWorkAndFailsClosedWithoutReceipt(t *testing.T) {
	for _, test := range []struct {
		name, status, want string
		result             any
	}{
		{"native rejection", "partial", "failed", map[string]any{"ok": false, "executed": false, "status": "edit_preflight_required", "message": "edit conflict"}},
		{"hard failure", "partial", "failed", map[string]any{"ok": false}},
		{"usable partial", "partial", "completed", map[string]any{"partial": true, "ok": false, "artifacts": []any{map[string]any{"id": "saved"}}}},
		{"source unavailable", "partial", "completed", map[string]any{"sourceUnavailable": true}},
		{"missing receipt", "partial", "partial", nil},
		{"pending stays pending", "pending", "pending", map[string]any{"ok": false}},
		{"denial stays denied", "denied", "denied", map[string]any{"ok": true}},
		{"unknown stays invalid", "unknown", "unknown", map[string]any{"ok": true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := agentRuntimeApprovalStatus(test.status, test.result); got != test.want {
				t.Fatalf("status=%s want=%s", got, test.want)
			}
		})
	}
}
