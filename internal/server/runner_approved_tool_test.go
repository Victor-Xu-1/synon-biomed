package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRunnerApprovalDecisionDoesNotExecuteAndResumesOnce(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "approval-progress", "approval-progress")
	root := t.TempDir()
	srv := New(Options{Workspace: store, Transcript: repo, FileRoot: root})
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	_, _, err := srv.submitFrameMessage(store, frameMessageSubmission{FrameID: "approval-progress", MessageUUID: "approve-message", ClientMessageID: "approve-message", Text: "write one file"})
	if err != nil {
		t.Fatal(err)
	}
	stream, _, err := repo.GetFrameStreamBySession(context.Background(), "local", "approval-progress")
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "approval-progress", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim: %v", err)
	}
	run := &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken, Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}}
	options := SessionRunnerChatOptions{SessionID: stream.SessionID, RunnerID: claimed.Claim.RunnerID, AllowedTools: []string{"file_write"}}
	input := map[string]any{"path": "approval-once.txt", "content": "approved", "overwrite": false}
	raw, _ := json.Marshal(input)
	call := agentruntime.ToolCall{ID: "approved-write", Name: "file_write", Arguments: raw}
	if err := srv.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := srv.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{Type: agentruntime.EventToolStarted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(raw)}); err != nil {
		t.Fatal(err)
	}
	id := agentRuntimeApprovalID(call.Name, call, input)
	if err := srv.checkpointChatTool(options, run, "waiting", "waiting", call.ID, "waiting", map[string]any{"toolName": call.Name, "toolResult": map[string]any{"approval_kind": agentToolApprovalKind, "approval_id": id, "request_id": id, "tool_call_id": call.ID, "tool_name": call.Name, "frame_id": stream.FrameID}}); err != nil {
		t.Fatal(err)
	}
	_, err = srv.runtimeStore.Set(agentRuntimeApprovalNamespace, id, map[string]any{"status": "pending", "sessionId": stream.SessionID, "tool": call.Name, "toolCallId": call.ID, "input": input})
	if err != nil {
		t.Fatal(err)
	}
	content, rejected, err := srv.resolveCompatibilityAgentToolApproval(context.Background(), map[string]any{"approval_id": id}, compatibilityInputResponse{Action: "allow_once"})
	if err != nil || rejected {
		t.Fatalf("approval: %s rejected=%t err=%v", content, rejected, err)
	}
	var decision map[string]any
	_ = json.Unmarshal([]byte(content), &decision)
	if decision["status"] != "approved" {
		t.Fatalf("approval executed instead of authorizing: %s", content)
	}
	if _, err := os.Stat(filepath.Join(root, "approval-once.txt")); !os.IsNotExist(err) {
		t.Fatalf("approval callback wrote file: %v", err)
	}
	if _, _, _, err := repo.PauseRunnerForApproval(context.Background(), transcriptstore.AppendRunnerCheckpointInput{Claim: claimed.Claim, ClientMessageID: "park-approval", Phase: transcriptstore.RunnerPhaseWaitingApproval, Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_approval"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "approval-response", FrameEventID: "approval-response-event", MessageUUID: "approval-response-message", Text: "approved"}); err != nil {
		t.Fatal(err)
	}
	resumedClaim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "approval-resumed-runner", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh})
	if err != nil || !resumedClaim.Claimed {
		t.Fatalf("resume claim: %#v %v", resumedClaim, err)
	}
	run.Attempt = int(resumedClaim.Claim.Attempt)
	run.ClaimToken = resumedClaim.Claim.ClaimToken
	run.Transcript.Claim = resumedClaim.Claim
	options.RunnerID = resumedClaim.Claim.RunnerID
	if err := srv.bindRunnableToolCallBatches(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.resumePendingAgentToolCalls(context.Background(), options, run); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.resumePendingAgentToolCalls(context.Background(), options, run); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "approval-once.txt"))
	if err != nil || string(data) != "approved" {
		t.Fatalf("runner did not execute: %q %v", data, err)
	}
	items, _, err := srv.loadTranscriptWebHistory(context.Background(), stream.OwnerID, stream.SessionID)
	if err != nil {
		t.Fatalf("history rejected resumed lifecycle: %v", err)
	}
	rows := 0
	for _, m := range items {
		if m["type"] == "tool_call" {
			rows++
		}
	}
	if rows != 1 {
		t.Fatalf("expected one authoritative tool row, got %d", rows)
	}
	content, rejected, err = srv.resolveCompatibilityAgentToolApproval(context.Background(), map[string]any{"approval_id": id}, compatibilityInputResponse{Action: "allow_once"})
	if err != nil || rejected {
		t.Fatalf("duplicate decision: %s %t %v", content, rejected, err)
	}
}

func TestToolGatewayCannotApproveItsOwnExecution(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	const id = "ar-approval-self-grant"
	_, err := srv.runtimeStore.Set(agentRuntimeApprovalNamespace, id, map[string]any{"status": "pending", "tool": "file_write", "toolCallId": "protected-write", "input": map[string]any{"path": "protected.txt", "content": "not approved", "overwrite": false}})
	if err != nil {
		t.Fatal(err)
	}
	receipt := srv.executeExactToolGateway(context.Background(), "agent-runtime", "", "self-grant", "SendMessage", map[string]any{"to": "agent-runtime", "message": map[string]any{"type": "agent_runtime_approval_response", "approvalId": id, "approve": true}}, exactServerToolGatewayOptions{})
	if receipt.Err == nil && !agentruntime.ClassifyToolResult(receipt.Value).HardFailed() {
		t.Fatalf("ordinary tool attempted approval: %#v", receipt.Value)
	}
	entry, _, err := srv.runtimeStore.Get(agentRuntimeApprovalNamespace, id)
	if err != nil || mapValue(entry.Value)["status"] != "pending" {
		t.Fatalf("approval changed: %#v %v", entry.Value, err)
	}
}
