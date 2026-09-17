package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

// TestFrameResumeDispatchRequeuesAwaitingApprovalAndWakesOnDecision covers
// the approval-resume contract: a runner paused at the WaitingApproval
// checkpoint must NOT be completed as a terminal Frame outcome, and the
// durable approval decision must wake the parked dispatch so the next claim
// delivers the tool result and finishes the task.
func TestFrameResumeDispatchRequeuesAwaitingApprovalAndWakesOnDecision(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-kernel-pause-dispatch", "frame-kernel-pause-dispatch")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := server.Close(ctx); err != nil {
			t.Errorf("close server: %v", err)
		}
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-kernel-pause-dispatch", MessageUUID: "message-kernel-pause-dispatch",
		ClientMessageID: "client-kernel-pause-dispatch", Text: "run a durable kernel operation",
	}); err != nil {
		t.Fatal(err)
	}
	seedAnsweredTaskIntake(t, server, "local", "frame-kernel-pause-dispatch")
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-kernel-pause-dispatch")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "kernel-pause-dispatch-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	call := agentruntime.ToolCall{
		ID: "kernel-pause-dispatch-call", Name: "python",
		Arguments: json.RawMessage(`{"code":"print(1)","environment":"python"}`),
	}
	if err := server.checkpointChatModelToolCalls(
		SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}, run, []agentruntime.ToolCall{call},
	); err != nil {
		t.Fatal(err)
	}
	intent, found, err := repo.EnsureActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("task intent found=%t err=%v", found, err)
	}
	memorySnapshot, err := transcriptstore.SealRunnerTaskMemorySnapshot(transcriptstore.RunnerTaskMemorySnapshot{
		TaskIntentID: intent.ID, TaskIntentRevision: intent.Revision,
		InitialInputRevision: claimed.Claim.ClaimedInputRevision,
		SessionMemory:        "session-memory-pause-dispatch", WorkspaceMemory: "workspace-memory-pause-dispatch",
		PolicyVersion: runnerTaskMemoryPolicyVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryPayloadMap, err := runnerTaskMemorySnapshotPayload(memorySnapshot)
	if err != nil {
		t.Fatal(err)
	}
	memoryPayload, err := json.Marshal(memoryPayloadMap)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "kernel-pause-dispatch-memory-snapshot",
		Phase: transcriptstore.RunnerPhasePlanning, Resumable: false, PayloadJSON: memoryPayload,
	}); err != nil || !created {
		t.Fatalf("memory snapshot created=%t err=%v", created, err)
	}
	var operationID, requestID string
	if err := db.QueryRow(`SELECT operation_id,approval_request_id FROM kernel_local_operations WHERE stream_uid=?`,
		stream.UID).Scan(&operationID, &requestID); err != nil {
		t.Fatal(err)
	}
	operation, found, err := store.GetKernelLocalOperation(context.Background(), stream.OwnerID, operationID)
	if err != nil || !found || operation.State != workspace.KernelLocalOperationStatePendingApproval {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	// Advance the durable batch to the running state exactly as if the tool
	// had started before the process paused for approval. The resume path
	// then parks on the pending operation without needing a kernel runtime.
	if err := server.checkpointChatTool(
		SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}, run,
		"running", "tool python started", call.ID, "start",
		map[string]any{"toolName": "python", "toolInput": map[string]any{"code": "print(1)", "environment": "python"}},
	); err != nil {
		t.Fatal(err)
	}
	// The original runner claim is abandoned (expired) before the frame is
	// resumed, matching production: the dispatch must be able to take over.
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=?
		WHERE stream_uid=? AND attempt=?`, time.Now().UTC().Add(-time.Minute), stream.UID, claimed.Claim.Attempt); err != nil {
		t.Fatal(err)
	}

	// Simulate the observed production aftermath: the frame was left terminal
	// by the previous buggy dispatch completion path, and the user resumes it.
	// The dispatch runner itself creates the WaitingApproval checkpoint when it
	// encounters the durable pending operation.
	failed := "failed"
	if _, err := store.UpdateFrame("frame-kernel-pause-dispatch", workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeCompatibilityFrameConversation("frame-kernel-pause-dispatch", workspace.ResumeCompatibilityFrameInput{}); err != nil {
		t.Fatal(err)
	}

	var providerCalls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		providerCalls.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		messages, _ := json.Marshal(payload["messages"])
		if !strings.Contains(string(messages), "approval_denied") || !strings.Contains(string(messages), "do not retry") {
			t.Fatalf("provider did not receive the durable denied tool result: %s", messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Denied execution handled."}}]}`))
	}))
	defer provider.Close()
	options := SessionRunnerChatOptions{
		RunnerID: "kernel-pause-dispatch-resumer", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, MaxAttempts: 1,
		ReplayLimit: 100, DisableSkillDiscovery: true,
	}
	dispatchOptions := FrameResumeDispatchOptions{
		WorkerID: "kernel-pause-dispatch-worker", ClaimTTL: time.Second, Chat: options,
	}

	paused, err := server.RunFrameResumeDispatchOnce(context.Background(), dispatchOptions)
	if err != nil || !paused.Claimed || !paused.Runner.Claimed || paused.Status != "awaiting_approval" {
		var finishPayload string
		_ = db.QueryRow(`SELECT payload_json FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished' ORDER BY event_id DESC LIMIT 1`, stream.UID).Scan(&finishPayload)
		var events []string
		rows, _ := db.Query(`SELECT event_id||':'||event_type||':'||substr(payload_json,1,160) FROM transcript_events WHERE stream_uid=? ORDER BY event_id DESC LIMIT 8`, stream.UID)
		for rows.Next() {
			var line string
			_ = rows.Scan(&line)
			events = append(events, line)
		}
		_ = rows.Close()
		t.Fatalf("paused dispatch=%#v err=%v finish=%s events=%v", paused, err, finishPayload, events)
	}
	if providerCalls.Load() != 0 {
		t.Fatalf("provider called while approval was unresolved: %d", providerCalls.Load())
	}
	requeued, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("frame-kernel-pause-dispatch")
	if err != nil || !found || requeued.Status != "registered" ||
		!requeued.NotBefore.After(time.Now().UTC()) {
		t.Fatalf("requeued dispatch=%#v found=%t err=%v", requeued, found, err)
	}
	server.autoResumeInterruptedFrames(context.Background())
	stillParked, found, err := store.GetCompatibilityFrameResumeDispatchByFrame(
		"frame-kernel-pause-dispatch",
	)
	if err != nil || !found || stillParked.Status != "registered" ||
		stillParked.Attempt != requeued.Attempt || stillParked.NotBefore.IsZero() {
		t.Fatalf("unresolved approval self-woke its recovery dispatch=%#v found=%t err=%v", stillParked, found, err)
	}

	resolved, err := store.ResolveKernelLocalOperationApproval(context.Background(), workspace.ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: stream.OwnerID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: requestID,
		Approved: false, DecisionID: "kernel-pause-dispatch-deny", Scope: "once",
		Source: "user", ActorID: stream.OwnerID, ReasonCode: "approval_denied",
		AdmitRunnerRevision: true,
	})
	if err != nil || resolved.State != workspace.KernelLocalOperationStateFailed {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if err := server.wakeFrameResumeDispatchAfterKernelTransition(context.Background(), resolved); err != nil {
		t.Fatal(err)
	}
	woken, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("frame-kernel-pause-dispatch")
	if err != nil || !found || woken.Status != "registered" || !woken.NotBefore.IsZero() {
		t.Fatalf("woken dispatch=%#v found=%t err=%v", woken, found, err)
	}

	completed, err := server.RunFrameResumeDispatchOnce(context.Background(), dispatchOptions)
	if err != nil || !completed.Claimed || !completed.Runner.Claimed ||
		completed.Status != "completed" || providerCalls.Load() != 1 {
		var finishPayload string
		_ = db.QueryRow(`SELECT payload_json FROM transcript_events WHERE stream_uid=? AND event_type='runner_finished' ORDER BY event_id DESC LIMIT 1`, stream.UID).Scan(&finishPayload)
		t.Fatalf("completed dispatch=%#v providerCalls=%d err=%v finish=%s", completed, providerCalls.Load(), err, finishPayload)
	}
	terminal, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("frame-kernel-pause-dispatch")
	if err != nil || !found || terminal.Status != "completed" {
		t.Fatalf("terminal dispatch=%#v found=%t err=%v", terminal, found, err)
	}
}

func TestRunnerInterruptionPauseStatusRecognizesWaitingCheckpoints(t *testing.T) {
	for _, status := range []string{"awaiting_approval", "awaiting_user_response"} {
		if !runnerInterruptionPauseStatus(status) {
			t.Fatalf("pause status %q not recognized", status)
		}
	}
	for _, status := range []string{"", "completed", "failed", "interrupted", "running"} {
		if runnerInterruptionPauseStatus(status) {
			t.Fatalf("non-pause status %q recognized", status)
		}
	}
}
