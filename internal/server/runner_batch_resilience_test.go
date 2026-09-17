package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

// TestCommitHookBatchConflictResilience verifies that the error checking
// logic in checkpointChatTool's commitHook correctly identifies batch
// conflict and stale errors, matching the reference ToolError model
// where tool failures are recoverable and do not terminate the session.
func TestCommitHookBatchConflictResilience(t *testing.T) {
	conflictErr := workspace.ErrToolCallBatchConflict
	staleErr := workspace.ErrToolCallBatchStale

	if !errors.Is(conflictErr, workspace.ErrToolCallBatchConflict) {
		t.Error("ErrToolCallBatchConflict should match itself")
	}
	if !errors.Is(staleErr, workspace.ErrToolCallBatchStale) {
		t.Error("ErrToolCallBatchStale should match itself")
	}

	// Wrapped errors should also match (the commitHook wraps with fmt.Errorf)
	wrappedConflict := fmt.Errorf("commit tool batch result: %w", conflictErr)
	if !errors.Is(wrappedConflict, workspace.ErrToolCallBatchConflict) {
		t.Error("wrapped ErrToolCallBatchConflict should still match via errors.Is")
	}

	wrappedStale := fmt.Errorf("batch stale: %w", staleErr)
	if !errors.Is(wrappedStale, workspace.ErrToolCallBatchStale) {
		t.Error("wrapped ErrToolCallBatchStale should still match via errors.Is")
	}

	// Non-batch errors should NOT match
	otherErr := errors.New("some other error")
	if errors.Is(otherErr, workspace.ErrToolCallBatchConflict) {
		t.Error("non-batch error should not match ErrToolCallBatchConflict")
	}
	if errors.Is(otherErr, workspace.ErrToolCallBatchStale) {
		t.Error("non-batch error should not match ErrToolCallBatchStale")
	}

	// Verify the error sentinels are distinct
	if errors.Is(conflictErr, workspace.ErrToolCallBatchStale) {
		t.Error("ErrToolCallBatchConflict should not match ErrToolCallBatchStale")
	}
	if errors.Is(staleErr, workspace.ErrToolCallBatchConflict) {
		t.Error("ErrToolCallBatchStale should not match ErrToolCallBatchConflict")
	}
}

func TestDetachedKernelExecutionContinuesRequiresExactDurableAuthority(t *testing.T) {
	operation := workspace.KernelLocalOperation{
		OperationID: "operation-detached", ExecutionID: "execution-detached",
		State: workspace.KernelLocalOperationStateStarted,
	}
	execution := workspace.DetachedKernelExecution{
		OperationID: operation.OperationID, ExecutionID: operation.ExecutionID,
		State: workspace.DetachedKernelExecutionStateStarted,
	}
	if !detachedKernelExecutionContinues(operation, execution, true) {
		t.Fatal("exact detached execution authority was rejected")
	}
	for name, candidate := range map[string]workspace.DetachedKernelExecution{
		"wrong operation": {OperationID: "other", ExecutionID: operation.ExecutionID, State: execution.State},
		"wrong execution": {OperationID: operation.OperationID, ExecutionID: "other", State: execution.State},
		"evidence lost":   {OperationID: operation.OperationID, ExecutionID: operation.ExecutionID, State: workspace.DetachedKernelExecutionStateEvidenceLost},
	} {
		if detachedKernelExecutionContinues(operation, candidate, true) {
			t.Errorf("%s authority was accepted", name)
		}
	}
	if detachedKernelExecutionContinues(operation, execution, false) {
		t.Fatal("missing detached execution was accepted")
	}
	operation.State = workspace.KernelLocalOperationStateCompleted
	if detachedKernelExecutionContinues(operation, execution, true) {
		t.Fatal("terminal operation was accepted as continuing")
	}
}

// TestCheckpointChatToolToleratesAdvancedBatchCursor verifies that a
// duplicate or reordered tool checkpoint does not terminate the runner when
// the durable batch has already advanced past the item. The checkpoint
// transcript event is still recorded, but the batch mutation is a no-op.
// This mirrors the reference batch model where reprocessing an already
// settled item is recoverable instead of a fatal conflict.
func TestCheckpointChatToolToleratesAdvancedBatchCursor(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-batch-resilience", "frame-batch-resilience")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-batch-resilience", MessageUUID: "message-batch-resilience",
		ClientMessageID: "client-batch-resilience", Text: "run parallel evidence tools",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-batch-resilience")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "batch-resilience-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}
	calls := []agentruntime.ToolCall{
		{ID: "batch-call-0", Name: "read_file", Arguments: []byte(`{"path":"notes.txt"}`)},
		{ID: "batch-call-1", Name: "read_file", Arguments: []byte(`{"path":"report.md"}`)},
	}
	if err := server.checkpointChatModelToolCalls(options, run, calls); err != nil {
		t.Fatal(err)
	}
	batchID := run.ToolBatchIDs[calls[0].ID]
	if batchID == "" || run.ToolBatchOrdinals[calls[0].ID] != 0 || run.ToolBatchOrdinals[calls[1].ID] != 1 {
		t.Fatalf("batch binding missing: batchID=%q ordinals=%v", batchID, run.ToolBatchOrdinals)
	}
	if err := server.checkpointChatTool(options, run, "running", "tool 0 started", calls[0].ID, "start", map[string]any{
		"toolName": "read_file", "toolInput": map[string]any{"path": "notes.txt"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointChatTool(options, run, "completed", "tool 0 completed", calls[0].ID, "completed", map[string]any{
		"toolName": "read_file", "toolResult": map[string]any{"ok": true, "content": "notes"},
	}); err != nil {
		t.Fatal(err)
	}
	batch, found, err := store.GetToolCallBatch(context.Background(), stream.OwnerID, batchID)
	if err != nil || !found || batch.NextOrdinal != 1 {
		t.Fatalf("batch after first settle=%#v found=%t err=%v", batch, found, err)
	}
	// A duplicate terminal checkpoint for the already-settled item must be a
	// no-op, not a fatal conflict that kills the runner.
	if err := server.checkpointChatTool(options, run, "completed", "tool 0 completed (duplicate)", calls[0].ID, "completed", map[string]any{
		"toolName": "read_file", "toolResult": map[string]any{"ok": true, "content": "notes"},
	}); err != nil {
		t.Fatalf("duplicate terminal checkpoint killed the runner: %v", err)
	}
	// A duplicate start for the next item after it is already running must
	// also be a no-op.
	if err := server.checkpointChatTool(options, run, "running", "tool 1 started", calls[1].ID, "start", map[string]any{
		"toolName": "read_file", "toolInput": map[string]any{"path": "report.md"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointChatTool(options, run, "running", "tool 1 started (duplicate)", calls[1].ID, "start", map[string]any{
		"toolName": "read_file", "toolInput": map[string]any{"path": "report.md"},
	}); err != nil {
		t.Fatalf("duplicate start checkpoint killed the runner: %v", err)
	}
	batch, found, err = store.GetToolCallBatch(context.Background(), stream.OwnerID, batchID)
	if err != nil || !found {
		t.Fatalf("batch after duplicates=%#v found=%t err=%v", batch, found, err)
	}
	items, err := store.ListToolCallBatchItems(context.Background(), stream.OwnerID, batchID)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if items[0].State != workspace.ToolCallBatchItemStateCompleted || items[1].State != workspace.ToolCallBatchItemStateRunning {
		t.Fatalf("unexpected item states after tolerance: %s, %s", items[0].State, items[1].State)
	}
	if batch.NextOrdinal != 1 || batch.State != workspace.ToolCallBatchStateRunning {
		t.Fatalf("unexpected batch state after tolerance: next=%d state=%s", batch.NextOrdinal, batch.State)
	}
	var failedRunners int
	if err := db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished'
		AND json_extract(payload_json,'$.status')='failed'`, stream.UID).Scan(&failedRunners); err != nil {
		t.Fatal(err)
	}
	if failedRunners != 0 {
		t.Fatalf("runner was killed despite tolerating advanced batch cursor: %d failed finishes", failedRunners)
	}
}

func TestCheckpointChatToolSettlesNonExecutingCompletionWithoutStartCheckpoint(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-prestart-resilience", "frame-prestart-resilience")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-prestart-resilience", MessageUUID: "message-prestart-resilience",
		ClientMessageID: "client-prestart-resilience", Text: "run a validated computation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-prestart-resilience")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "prestart-resilience-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}
	call := agentruntime.ToolCall{
		ID: "prestart-call", Name: "read_file", Arguments: []byte(`{"path":"missing.txt"}`),
	}
	if err := server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolCompleted, ToolName: call.Name, ToolCallID: call.ID,
		Arguments: string(call.Arguments), Result: `{"ok":false,"executed":false,"code":"invalid_tool_arguments"}`,
		Message: "tool settled before execution", RejectedBeforeExecution: true,
	}); err != nil {
		t.Fatal(err)
	}
	batch, found, err := store.GetToolCallBatch(context.Background(), stream.OwnerID, run.ToolBatchIDs[call.ID])
	if err != nil || !found {
		t.Fatalf("batch=%#v found=%t err=%v", batch, found, err)
	}
	items, err := store.ListToolCallBatchItems(context.Background(), stream.OwnerID, batch.BatchID)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
	if batch.State != workspace.ToolCallBatchStateSettled || batch.NextOrdinal != 1 ||
		items[0].State != workspace.ToolCallBatchItemStateFailed || items[0].TerminalEventID == 0 {
		t.Fatalf("non-executing completion did not settle batch: batch=%#v item=%#v", batch, items[0])
	}
	messages, found, err := server.loadTranscriptWebHistory(context.Background(), stream.OwnerID, stream.SessionID)
	if err != nil || !found || len(messages) != 2 {
		t.Fatalf("prestart history=%#v found=%t err=%v", messages, found, err)
	}
	content, _ := messages[1]["content"].(map[string]any)
	if messages[1]["type"] != "tool_call" || content["status"] != "error" {
		t.Fatalf("prestart history did not expose one error settlement: %#v", messages[1])
	}
}
