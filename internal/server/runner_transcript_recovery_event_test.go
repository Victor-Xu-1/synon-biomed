package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRunnerReplayExcludesTerminalToolRecoveryAuditEvent(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-recovery-audit", "frame-recovery-audit")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-recovery-audit", MessageUUID: "message-recovery-audit",
		ClientMessageID: "client-recovery-audit", Text: "Complete this scientific task.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-recovery-audit")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-recovery-audit",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	checkpointPayload, err := json.Marshal(map[string]any{
		"status": "running",
		"modelToolCalls": []any{map[string]any{
			"id": "tool-recovery-audit", "type": "function", "name": "read_file",
			"arguments": map[string]any{"path": "result.md"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var batchID string
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "checkpoint-recovery-audit",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true, PayloadJSON: checkpointPayload,
		CommitHook: func(ctx context.Context, tx *transcriptstore.ImmediateTransaction, event transcriptstore.Event, _ bool) (transcriptstore.RunnerCheckpointCommitReceipt, error) {
			batch, _, _, createErr := store.CreateToolCallBatchForCheckpointTx(ctx, tx, event)
			batchID = batch.BatchID
			return transcriptstore.RunnerCheckpointCommitReceipt{
				ToolBatch: &transcriptstore.RunnerCheckpointToolBatchReceipt{BatchID: batch.BatchID, CallCount: batch.CallCount},
			}, createErr
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: claimed.Claim, ClientMessageID: "assistant-recovery-audit", Type: "assistant_message",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"Scientific result ready."}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-recovery-audit", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if reconciled, err := store.ReconcileTerminalFrameToolCallBatch(
		context.Background(), stream.OwnerID, batchID,
	); err != nil || !reconciled {
		t.Fatalf("reconciled=%t err=%v", reconciled, err)
	}
	messages, snapshot, err := server.projectTranscriptWebHistory(
		context.Background(), stream, stream.OwnerID, stream.SessionID, "",
	)
	if err != nil {
		t.Fatalf("Web history rejected machine-only recovery audit: %v", err)
	}
	metadata, err := server.projectTranscriptWebSourceMetadata(
		context.Background(), stream, stream.OwnerID, stream.SessionID, snapshot,
	)
	if err != nil {
		t.Fatalf("Web source metadata rejected machine-only recovery audit: %v", err)
	}
	if len(messages) != len(metadata.coordinates) {
		t.Fatalf("visible messages=%d coordinates=%d", len(messages), len(metadata.coordinates))
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	entries, err := server.loadTranscriptRunnerReplay(context.Background(), authority, 100, 100)
	if err != nil {
		t.Fatalf("replay rejected machine-only recovery audit: %v", err)
	}
	for _, entry := range entries {
		if entry.Message["type"] == "tool_recovery_settlement" {
			t.Fatalf("recovery audit leaked into model replay: %#v", entry)
		}
		if entry.Message["toolCallId"] == "tool-recovery-audit" &&
			entry.SourceEventType != transcriptstore.TerminalToolRecoveryEventType {
			t.Fatalf("recovery settlement lost source authority: %#v", entry)
		}
	}
}

func TestPriorTerminalCorrectionNeverCrossesLogicalInputRevision(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-correction-scope", "frame-correction-scope")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-correction-scope", MessageUUID: "message-correction-scope-1",
		ClientMessageID: "client-correction-scope-1", Text: "Create the first report.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-correction-scope")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-correction-scope-1",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	finishPayload := []byte(`{"status":"failed","detail":"runner completion reference integrity failed (unsupported_citations=1)"}`)
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "finish-correction-scope-1", Status: "failed", PayloadJSON: finishPayload,
	}); err != nil {
		t.Fatal(err)
	}
	sameInputAuthority := &transcriptRunnerAuthority{Stream: stream, Claim: transcriptstore.RunnerClaim{
		Attempt: claimed.Claim.Attempt + 1, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ClaimedInputRevision: claimed.Claim.ClaimedInputRevision,
	}}
	if _, correctionFound, err := server.loadPriorTerminalCorrection(context.Background(), sameInputAuthority); err != nil || !correctionFound {
		t.Fatalf("same-input correction found=%t err=%v", correctionFound, err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-correction-scope", MessageUUID: "message-correction-scope-2",
		ClientMessageID: "client-correction-scope-2", Text: "Calculate 23 times 29.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err = repo.GetFrameStreamBySession(context.Background(), "local", "frame-correction-scope")
	if err != nil || !found || stream.InputRevision <= claimed.Claim.ClaimedInputRevision {
		t.Fatalf("updated stream=%#v found=%t err=%v", stream, found, err)
	}
	newInputAuthority := &transcriptRunnerAuthority{Stream: stream, Claim: transcriptstore.RunnerClaim{
		Attempt: claimed.Claim.Attempt + 1, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ClaimedInputRevision: stream.InputRevision,
	}}
	if stale, correctionFound, err := server.loadPriorTerminalCorrection(context.Background(), newInputAuthority); err != nil || correctionFound {
		t.Fatalf("new logical input inherited prior correction=%#v found=%t err=%v", stale, correctionFound, err)
	}
}

func TestWebProjectionRebasesAssistantAfterToolLifecyclePersistenceRecovery(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-tool-lifecycle-recovery", "frame-tool-lifecycle-recovery")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-tool-lifecycle-recovery", MessageUUID: "message-tool-lifecycle-recovery",
		ClientMessageID: "client-tool-lifecycle-recovery", Text: "Continue after an interrupted durable tool lifecycle.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-tool-lifecycle-recovery")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool-lifecycle-before",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "tool-lifecycle-before", "content_delta", map[string]any{
		"text": "before", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "tool-lifecycle-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-tool-lifecycle", "toolName": "software_runtime",
		"toolInput": map[string]any{"capability": "test-capability"},
	})
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "tool-lifecycle-interrupted",
		ReasonCode: sessionRunnerToolLifecyclePersistenceReasonCode, AutoResume: true,
	})
	if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-tool-lifecycle-after",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	appendRunnerPayloadEvent(t, repo, reclaimed.Claim, "tool-lifecycle-after", "content_delta", map[string]any{
		"text": "after", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: reclaimed.Claim, ClientMessageID: "tool-lifecycle-finished", Status: "completed",
		PayloadJSON: []byte(`{"status":"completed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	messages, snapshot, err := server.projectTranscriptWebHistory(
		context.Background(), stream, stream.OwnerID, stream.SessionID, "",
	)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	metadata, err := server.projectTranscriptWebSourceMetadata(
		context.Background(), stream, stream.OwnerID, stream.SessionID, snapshot,
	)
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	if len(messages) != len(metadata.coordinates) || len(messages) < 4 {
		t.Fatalf("messages=%d coordinates=%d", len(messages), len(metadata.coordinates))
	}
}
