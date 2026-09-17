package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRunnerApprovalRestoresNormalizedInputFromWaitingAuthority(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	s := f.server
	if _, err := s.settingsStore.Set(approvalDefaultsSettingKey, map[string]any{"mode": "ask"}); err != nil {
		t.Fatal(err)
	}
	run := &sessionRunnerChatRun{SessionID: f.stream.SessionID, Attempt: int(f.claim.Attempt), ClaimToken: f.claim.ClaimToken, Transcript: &transcriptRunnerAuthority{Stream: f.stream, Claim: f.claim}}
	options := SessionRunnerChatOptions{SessionID: run.SessionID, RunnerID: f.claim.RunnerID, AllowedTools: []string{"edit_file"}, OutputLimitBytes: 64 << 10}
	input := map[string]any{"file_path": "normalized-result.json", "old_string": "", "new_string": map[string]any{"count": 3, "status": "observed"}, "human_description": "Save structured result"}
	raw, _ := json.Marshal(input)
	call := agentruntime.ToolCall{ID: "normalized-edit", Name: "edit_file", Arguments: raw}
	if err := s.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	ctx := withTranscriptRunnerChatRun(context.Background(), run)
	engine := s.newAgentRuntimeEngineWithContext(ctx, options, []agentruntime.ToolSchema{agentWorkspaceEditFileToolSchema()})
	engine.OnEventError = func(event agentruntime.Event) error {
		return s.checkpointSessionRunnerToolEvent(ctx, options, run, event)
	}
	_, err := engine.ExecuteToolBatch(ctx, []agentruntime.ToolCall{call}, 0, agentruntime.MediaPolicy{}, 0)
	var pause *agentruntime.PauseError
	if !errors.As(err, &pause) {
		t.Fatalf("expected approval pause: %v", err)
	}
	approvalID := stringValue(pause.Data["approval_id"])
	if approvalID == "" || approvalID == agentRuntimeApprovalID(call.Name, call, input) {
		t.Fatalf("normalization did not change approval digest: %q", approvalID)
	}
	entry, found, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil || !found {
		t.Fatal("approval absent", err)
	}
	normalized, ok := mapValue(mapValue(entry.Value)["input"])["new_string"].(string)
	if !ok {
		t.Fatal("gateway did not persist normalized text")
	}
	batchID := run.ToolBatchIDs[call.ID]
	items, err := f.store.ListToolCallBatchItems(ctx, f.stream.OwnerID, batchID)
	if err != nil || len(items) != 1 || items[0].State != workspace.ToolCallBatchItemStateWaiting {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	var persisted map[string]any
	_ = json.Unmarshal(items[0].ArgumentsJSON, &persisted)
	if _, ok := persisted["new_string"].(map[string]any); !ok {
		t.Fatal("test lost the original structured model arguments")
	}
	path := filepath.Join(f.projectPath, "normalized-result.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("tool executed before approval: %v", err)
	}
	_, rejected, err := s.resolveCompatibilityAgentToolApproval(context.Background(), map[string]any{"approval_id": approvalID}, compatibilityInputResponse{Action: "allow_once"})
	if err != nil || rejected {
		t.Fatalf("approve rejected=%t err=%v", rejected, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("approval callback executed the edit")
	}
	// A different record derived from the raw model input is not authority.
	if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, agentRuntimeApprovalID(call.Name, call, input), map[string]any{"status": "denied", "tool": call.Name, "toolCallId": call.ID, "sessionId": run.SessionID, "input": input}); err != nil {
		t.Fatal(err)
	}
	_, state, restoredID, found, err := s.agentToolApprovalBatchResult(ctx, items[0])
	if err != nil || !found || state != "approved" || restoredID != approvalID {
		t.Fatalf("wrong approval authority: %s %s %t %v", state, restoredID, found, err)
	}
	for _, mutate := range []func(*workspace.ToolCallBatchItem){
		func(item *workspace.ToolCallBatchItem) { item.StreamUID = "foreign-stream" },
		func(item *workspace.ToolCallBatchItem) { item.WaitingEventID = item.StartedEventID },
		func(item *workspace.ToolCallBatchItem) { item.ToolCallID = "foreign-call" },
		func(item *workspace.ToolCallBatchItem) { item.BatchID = "foreign-batch" },
	} {
		invalid := items[0]
		mutate(&invalid)
		if _, _, _, _, err := s.agentToolApprovalBatchResult(ctx, invalid); err == nil {
			t.Fatal("accepted mismatched waiting checkpoint")
		}
	}
	approvedEntry, _, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil {
		t.Fatal(err)
	}
	foreign := copyMapAny(mapValue(approvedEntry.Value))
	foreign["sessionId"] = "foreign-frame"
	if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, foreign); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := s.agentToolApprovalBatchResult(ctx, items[0]); err == nil {
		t.Fatal("accepted cross-frame approval")
	}
	if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, approvedEntry.Value); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.repo.PauseRunnerForApproval(ctx, transcriptstore.AppendRunnerCheckpointInput{Claim: f.claim, ClientMessageID: "park-normalized-edit", Phase: transcriptstore.RunnerPhaseWaitingApproval, Resumable: true, PayloadJSON: []byte(`{"status":"awaiting_approval"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{StreamUID: f.stream.UID, OwnerID: f.stream.OwnerID, ClientMessageID: "resume-normalized-edit", FrameEventID: "resume-normalized-edit-event", MessageUUID: "resume-normalized-edit-message", Text: "Continue approved operation"}); err != nil {
		t.Fatal(err)
	}
	next, err := f.repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{StreamUID: f.stream.UID, OwnerID: f.stream.OwnerID, RunnerID: "normalized-edit-resumer", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh})
	if err != nil || !next.Claimed || next.Claim.Attempt <= f.claim.Attempt {
		t.Fatalf("new claim=%#v %v", next, err)
	}
	run = &sessionRunnerChatRun{SessionID: f.stream.SessionID, Attempt: int(next.Claim.Attempt), ClaimToken: next.Claim.ClaimToken, Transcript: &transcriptRunnerAuthority{Stream: f.stream, Claim: next.Claim}}
	options.RunnerID = next.Claim.RunnerID
	if err := s.bindRunnableToolCallBatches(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if _, err := s.resumePendingAgentToolCalls(context.Background(), options, run); err != nil {
		t.Fatalf("approved normalized call did not resume: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != normalized {
		t.Fatalf("written=%q expected=%q err=%v", data, normalized, err)
	}
	batch, _, err := f.store.GetToolCallBatch(context.Background(), f.stream.OwnerID, batchID)
	if err != nil || batch.State != workspace.ToolCallBatchStateSettled || batch.NextOrdinal != 1 {
		t.Fatalf("batch did not settle: %#v %v", batch, err)
	}
	items, err = f.store.ListToolCallBatchItems(context.Background(), f.stream.OwnerID, batchID)
	if err != nil || items[0].State != workspace.ToolCallBatchItemStateCompleted || items[0].TerminalEventID == 0 {
		t.Fatalf("missing terminal receipt: %#v %v", items, err)
	}
	terminalID := items[0].TerminalEventID
	if err := os.WriteFile(path, []byte("later user edit"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.resumePendingAgentToolCalls(context.Background(), options, run); err != nil {
			t.Fatal(err)
		}
	}
	data, err = os.ReadFile(path)
	if err != nil || string(data) != "later user edit" {
		t.Fatal("recovery reexecuted a settled edit", err)
	}
	items, _ = f.store.ListToolCallBatchItems(context.Background(), f.stream.OwnerID, batchID)
	if items[0].TerminalEventID != terminalID {
		t.Fatal("duplicate terminal receipt")
	}
	var resumes int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_checkpoint' AND json_extract(payload_json,'$.toolCallId')=? AND json_extract(payload_json,'$.toolPhase')='approval_resumed'`, f.stream.UID, call.ID).Scan(&resumes); err != nil || resumes != 1 {
		t.Fatalf("resume checkpoints=%d err=%v", resumes, err)
	}
}
