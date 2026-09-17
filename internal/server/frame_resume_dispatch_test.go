package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestAutoResumeInterruptedFramesCreatesDispatchForExpiredRunnerLease(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "workspace.db")
	store, srv := newFrameResumeDispatchFixtureWithSeed(
		t, root, databasePath, "http://127.0.0.1:1", "expired-live-frame", "resume an expired runner", true,
	)
	status := "processing"
	if _, err := store.UpdateFrame("expired-live-frame", workspace.UpdateFrameInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: "frame:expired-live-frame", OwnerID: "local", ClientMessageID: "expired-live-follow-up",
		FrameEventID: "expired-live-follow-up-event", MessageUUID: "expired-live-follow-up-message",
		Text: "resume the expired execution", MessageOrigin: "task_intent", Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: "frame:expired-live-frame", OwnerID: "local", RunnerID: "stale-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim.Claim, ClientMessageID: "expired-live-checkpoint", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"step":"before-recovery"}`),
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.Claim.StreamUID, claim.Claim.Attempt); err != nil {
		t.Fatal(err)
	}
	srv.autoResumeInterruptedFrames(context.Background())
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("expired-live-frame")
	if err != nil || !found {
		t.Fatalf("dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	dispatchPayload, ok := dispatch.ResumeEvent.Payload["dispatch"].(map[string]any)
	if !ok || dispatch.Status != "registered" ||
		dispatchPayload["reasonCode"] != sessionRunnerExpiredLeaseRecoveryReasonCode {
		t.Fatalf("expired lease dispatch=%#v", dispatch)
	}
}

func TestAutoResumeDispatchReactivatesRecoverableFailedFrame(t *testing.T) {
	root := t.TempDir()
	store, srv := newFrameResumeDispatchFixtureWithSeed(
		t, root, filepath.Join(root, "workspace.db"), "http://127.0.0.1:1", "recoverable-failed-frame", "resume the recoverable task", true,
	)
	failed := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame("recoverable-failed-frame", workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	event, err := srv.createAutoResumeDispatch(context.Background(), transcriptstore.AutoResumeCandidate{
		FrameID: "recoverable-failed-frame", ReasonCode: sessionRunnerCompletionReviewRecoveryReasonCode,
		Attempt: 2, CheckpointSequence: 11,
	})
	if err != nil || event == nil {
		t.Fatalf("auto-resume event=%#v err=%v", event, err)
	}
	frame, found, err := store.GetFrame("recoverable-failed-frame")
	if err != nil || !found || frame.Status != workspace.FrameStatusProcessing {
		t.Fatalf("reactivated frame=%#v found=%t err=%v", frame, found, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("recoverable-failed-frame")
	if err != nil || !found || dispatch.Status != "registered" {
		t.Fatalf("registered dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
}

func TestAutoResumeExpiredCheckpointWakesOlderRegisteredDispatchFence(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "workspace.db")
	store, srv := newFrameResumeDispatchFixtureWithSeed(
		t, root, databasePath, "http://127.0.0.1:1", "expired-fenced-frame",
		"resume after a durable correction supersedes the old lease horizon", true,
	)
	status := "processing"
	if _, err := store.UpdateFrame("expired-fenced-frame", workspace.UpdateFrameInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: "frame:expired-fenced-frame", OwnerID: "local",
		ClientMessageID: "expired-fenced-follow-up", FrameEventID: "expired-fenced-follow-up-event",
		MessageUUID: "expired-fenced-follow-up-message", Text: "continue the same task",
		MessageOrigin: "task_intent", Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: "frame:expired-fenced-frame", OwnerID: "local", RunnerID: "stale-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claim.Claim, ClientMessageID: "expired-fenced-checkpoint",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"status":"interrupted","reason_code":"real_scientific_evidence_required"}`),
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claim.Claim.StreamUID, claim.Claim.Attempt); err != nil {
		t.Fatal(err)
	}
	srv.autoResumeInterruptedFrames(context.Background())
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("expired-fenced-frame")
	if err != nil || !found || dispatch.Status != "registered" {
		t.Fatalf("dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	claimedDispatch, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("stale-dispatch", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claimed dispatch=%#v claimed=%t err=%v", claimedDispatch, claimed, err)
	}
	if _, _, err := store.RequeueCompatibilityFrameResumeDispatch(workspace.RequeueCompatibilityFrameResumeDispatchInput{
		ResumeEventID: claimedDispatch.ResumeEvent.ID, ExpectedAttempt: claimedDispatch.Attempt,
		ClaimToken: claimedDispatch.ClaimToken, ReasonCode: "runner_still_active",
		RunnerAttempt: int(claim.Claim.Attempt), CheckpointEventID: claim.Claim.ResumeCheckpoint,
		NotBefore: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	fenced, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("expired-fenced-frame")
	if err != nil || !found || fenced.NotBefore.IsZero() {
		t.Fatalf("fenced dispatch=%#v found=%t err=%v", fenced, found, err)
	}
	srv.autoResumeInterruptedFrames(context.Background())
	woken, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("expired-fenced-frame")
	if err != nil || !found || !woken.NotBefore.IsZero() {
		t.Fatalf("woken dispatch=%#v found=%t err=%v", woken, found, err)
	}
}

func TestAutoResumeCandidateDoesNotWakeItsOwnKernelRecoveryFence(t *testing.T) {
	existing := workspace.CompatibilityFrameResumeDispatch{
		NotBefore: time.Now().UTC().Add(time.Hour),
		ResumeEvent: workspace.FrameEvent{Payload: map[string]any{
			"dispatch": map[string]any{
				"interruptionReasonCode": sessionRunnerKernelOperationPendingRecoveryReasonCode,
				"runnerAttempt":          13,
				"checkpointEventId":      2588,
			},
		}},
	}
	sameRecovery := transcriptstore.AutoResumeCandidate{
		Attempt: 13, CheckpointSequence: 2595,
		ReasonCode: sessionRunnerKernelOperationPendingRecoveryReasonCode,
	}
	if autoResumeCandidateSupersedesDispatchFence(existing, sameRecovery) {
		t.Fatal("same runner attempt and kernel recovery cause woke its own backstop fence")
	}
	existing.ResumeEvent.Payload["dispatch"].(map[string]any)["interruptionReasonCode"] = "awaiting_approval"
	if autoResumeCandidateSupersedesDispatchFence(existing, sameRecovery) {
		t.Fatal("the internal kernel recovery classification woke its own approval wait")
	}
	expiredLease := sameRecovery
	expiredLease.ReasonCode = sessionRunnerExpiredLeaseRecoveryReasonCode
	if autoResumeCandidateSupersedesDispatchFence(existing, expiredLease) {
		t.Fatal("the parked approval runner lease woke its own approval wait")
	}
	existing.ResumeEvent.Payload["dispatch"].(map[string]any)["interruptionReasonCode"] =
		sessionRunnerKernelOperationPendingRecoveryReasonCode
	newCause := sameRecovery
	newCause.ReasonCode = sessionRunnerExpiredLeaseRecoveryReasonCode
	if !autoResumeCandidateSupersedesDispatchFence(existing, newCause) {
		t.Fatal("a genuinely newer recovery cause did not supersede the old fence")
	}
	newAttempt := sameRecovery
	newAttempt.Attempt++
	if !autoResumeCandidateSupersedesDispatchFence(existing, newAttempt) {
		t.Fatal("a newer runner attempt did not supersede the old fence")
	}
}

func TestFrameResumeActiveRunnerDeferralPreservesRecoveryCoordinates(t *testing.T) {
	dispatch := workspace.CompatibilityFrameResumeDispatch{ResumeEvent: workspace.FrameEvent{
		Payload: map[string]any{"dispatch": map[string]any{
			"reasonCode":        sessionRunnerExpiredLeaseRecoveryReasonCode,
			"runnerAttempt":     7,
			"checkpointEventId": 42,
		}},
	}}
	reason, attempt, checkpoint := frameResumeActiveRunnerDeferralCoordinates(dispatch, 0)
	if reason != sessionRunnerExpiredLeaseRecoveryReasonCode || attempt != 7 || checkpoint != 42 {
		t.Fatalf("deferral coordinates reason=%q attempt=%d checkpoint=%d", reason, attempt, checkpoint)
	}
}

func TestFrameResumeActiveRunnerDeferralIsTranscriptOnly(t *testing.T) {
	for _, runErr := range []error{
		errFrameResumeRunnerStillActive,
		fmt.Errorf("wrapped: %w", ErrSessionRunAlreadyActive),
	} {
		if !frameResumeDispatchShouldRequeueActiveRunner(true, runErr) {
			t.Fatalf("Transcript active-run conflict was not selected for durable requeue: %v", runErr)
		}
		if frameResumeDispatchShouldRequeueActiveRunner(false, runErr) {
			t.Fatalf("legacy active-run conflict was incorrectly selected for fixed requeue: %v", runErr)
		}
	}
}

func TestAutoResumeRecoversExpiredApprovedKernelOperationBeforeExecution(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "workspace.db")
	store, srv := newFrameResumeDispatchFixtureWithSeed(
		t, root, databasePath, "http://127.0.0.1:1", "expired-approved-frame", "resume approved setup", true,
	)
	status := "processing"
	if _, err := store.UpdateFrame("expired-approved-frame", workspace.UpdateFrameInput{Status: &status}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "expired-approved-frame")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: "frame:expired-approved-frame", OwnerID: "local", ClientMessageID: "approved-follow-up",
		FrameEventID: "approved-follow-up-event", MessageUUID: "approved-follow-up-message",
		Text: "continue setup", MessageOrigin: "task_intent", Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: "frame:expired-approved-frame", OwnerID: "local", RunnerID: "approved-runner", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: "expired-approved-frame", Attempt: int(claimed.Claim.Attempt), ClaimToken: claimed.Claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim},
	}
	call := agentruntime.ToolCall{ID: "approved-setup-call", Name: "python", Arguments: json.RawMessage(`{"code":"print(1)","environment":"python"}`)}
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID, OutputLimitBytes: 1024}
	if err := srv.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	operation, found, err := store.GetKernelLocalOperationByToolCall(
		context.Background(), "local", claimed.Claim.StreamUID, call.ID,
	)
	if err != nil || !found {
		t.Fatalf("operation=%#v found=%t err=%v", operation, found, err)
	}
	approved, err := store.ResolveKernelLocalOperationApproval(context.Background(), workspace.ResolveKernelLocalOperationApprovalInput{
		OwnerUserID: operation.OwnerUserID, OperationID: operation.OperationID,
		ExpectedStateVersion: operation.StateVersion, ApprovalRequestID: operation.ApprovalRequestID,
		Approved: true, DecisionID: "approved-setup-decision", Scope: "once",
		Source: "policy", ActorID: "system", CurrentClaim: claimed.Claim,
	})
	if err != nil || approved.State != workspace.KernelLocalOperationStateApproved || approved.ExecutionID != "" {
		t.Fatalf("approved=%#v err=%v", approved, err)
	}
	if _, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "approved-setup-nonresumable",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: false,
		PayloadJSON: []byte(`{"stage":"environment_provisioning"}`),
	}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), claimed.Claim.StreamUID, claimed.Claim.Attempt); err != nil {
		t.Fatal(err)
	}
	if generic, err := repo.ListAllExpiredRunnerCandidates(context.Background(), 10); err != nil || len(generic) != 0 {
		t.Fatalf("generic recovery bypassed the non-resumable fence: candidates=%#v err=%v", generic, err)
	}
	approvedCandidates, err := store.ListExpiredApprovedKernelLocalOperationRecoveryCandidates(context.Background(), 10)
	if err != nil || len(approvedCandidates) != 1 || approvedCandidates[0].OperationID != operation.OperationID {
		t.Fatalf("approved recovery candidates=%#v err=%v", approvedCandidates, err)
	}
	resumable, found, err := repo.LatestResumableCheckpoint(context.Background(), claimed.Claim.StreamUID, claimed.Claim.OwnerID)
	if err != nil || !found {
		t.Fatalf("approved recovery checkpoint=%#v found=%t err=%v", resumable, found, err)
	}
	frameBeforeRecovery, found, err := store.GetFrame("expired-approved-frame")
	if err != nil || !found {
		t.Fatalf("approved recovery frame=%#v found=%t err=%v", frameBeforeRecovery, found, err)
	}
	if frameBeforeRecovery.Status != "processing" {
		t.Fatalf("approved recovery frame status=%s", frameBeforeRecovery.Status)
	}
	srv.autoResumeInterruptedFrames(context.Background())
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("expired-approved-frame")
	if err != nil || !found || dispatch.Status != "registered" {
		t.Fatalf("approved recovery dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	payload := mapValue(dispatch.ResumeEvent.Payload["dispatch"])
	if numberValue(payload["runnerAttempt"]) != claimed.Claim.Attempt ||
		numberValue(payload["checkpointEventId"]) <= 0 {
		t.Fatalf("approved recovery lost Transcript coordinates: %#v", payload)
	}
}

func TestKernelOperationRecoveryCandidateSupersedesExhaustedExpiredLeaseCandidate(t *testing.T) {
	candidates := []transcriptstore.AutoResumeCandidate{{
		StreamUID: "frame:kernel-recovery-priority", OwnerID: "local", FrameID: "kernel-recovery-priority",
		Attempt: 4, ReasonCode: sessionRunnerExpiredLeaseRecoveryReasonCode,
		InputRevisionBounces: 100,
	}}
	kernelRecovery := transcriptstore.AutoResumeCandidate{
		StreamUID: "frame:kernel-recovery-priority", OwnerID: "local", FrameID: "kernel-recovery-priority",
		Attempt: 2, CheckpointSequence: 9, ReasonCode: sessionRunnerKernelOperationPendingRecoveryReasonCode,
	}

	merged := prioritizeKernelOperationRecoveryCandidate(candidates, kernelRecovery)
	if len(merged) != 1 || merged[0].ReasonCode != sessionRunnerKernelOperationPendingRecoveryReasonCode ||
		merged[0].CheckpointSequence != kernelRecovery.CheckpointSequence {
		t.Fatalf("kernel recovery did not replace exhausted lease candidate: %#v", merged)
	}
}

func TestFrameResumeDispatchUsesProviderAuthorityAndRecoversExpiredClaimAfterRestart(t *testing.T) {
	var savedRequests atomic.Int64
	savedProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sequence := savedRequests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("saved provider request = %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Authorization") != "Bearer resume-key" {
			t.Errorf("saved provider authorization = %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var request struct {
			Model    string `json:"model"`
			Messages []struct {
				Role       string `json:"role"`
				Content    string `json:"content"`
				ToolCallID string `json:"tool_call_id"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode saved provider request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if request.Model != "resume-model" {
			t.Errorf("saved provider model = %q", request.Model)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(request.Messages) == 0 {
			t.Error("saved provider request has no messages")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		isReviewer := strings.Contains(request.Messages[0].Content, "You are the REVIEWER")
		isBookmarker := strings.Contains(request.Messages[0].Content, "You are the BOOKMARKER")
		last := request.Messages[len(request.Messages)-1]
		if !isReviewer && !isBookmarker && (last.Role != "user" || !strings.Contains(last.Content, "continue the interrupted analysis")) {
			t.Errorf("saved provider last message = %#v", last)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		content := "resumed analysis completed"
		if isReviewer || isBookmarker {
			hasToolResult := false
			for _, message := range request.Messages {
				if strings.TrimSpace(message.ToolCallID) != "" {
					hasToolResult = true
					break
				}
			}
			if !hasToolResult {
				submission := `{"human_description":"durable resume completed the requested analysis","findings":[]}`
				if isBookmarker {
					submission = `{"human_description":"No bookmark needed","bookmarks":[]}`
				}
				arguments, err := json.Marshal(submission)
				if err != nil {
					t.Errorf("encode reviewer arguments: %v", err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(`data: {"id":"resume-review","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"resume-review-submit","type":"function","function":{"name":"` + sessionReviewerSubmitToolName + `","arguments":` + string(arguments) + `}}]},"finish_reason":"tool_calls"}]}` + "\n\n"))
				_, _ = w.Write([]byte("data: [DONE]\n\n"))
				return
			}
			content = "fixed job submitted"
		} else if sequence != 1 {
			t.Errorf("unexpected saved provider request %d", sequence)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		encodedContent, err := json.Marshal(content)
		if err != nil {
			t.Errorf("encode saved provider response: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "resume-request-1")
		_, _ = w.Write([]byte("data: {\"id\":\"resume-request-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":" + string(encodedContent) + "},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"resume-request-1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":3,\"total_tokens\":7}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer savedProvider.Close()

	var staticRequests atomic.Int64
	staticProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		staticRequests.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer staticProvider.Close()

	root := t.TempDir()
	databasePath := filepath.Join(root, "workspace.db")
	store, srv := newFrameResumeDispatchFixture(t, root, databasePath, savedProvider.URL, "resume-root", "continue the interrupted analysis")
	app := srv.Handler()

	first := compatJSONRequest(t, app, http.MethodPost, "/api/frames/resume-root/resume", "local", map[string]any{
		"verifier_mode": "on", "memory_mode": "off", "model": "ignored-request-model",
	}, http.StatusOK)
	assertCompatibilityResumeResponse(t, first, "resume-root", "resume-root", "OPERON", 1)
	second := compatJSONRequest(t, app, http.MethodPost, "/api/frames/resume-root/resume", "local", map[string]any{}, http.StatusOK)
	assertCompatibilityResumeResponse(t, second, "resume-root", "resume-root", "OPERON", 1)
	if savedRequests.Load() != 0 || staticRequests.Load() != 0 {
		t.Fatalf("model request occurred before durable dispatch worker: saved=%d static=%d", savedRequests.Load(), staticRequests.Load())
	}

	claim, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("crashed-worker", 20*time.Millisecond)
	if err != nil || !claimed || claim.ResumeEvent.ID == "" || claim.Attempt != 1 {
		t.Fatalf("initial dispatch claim = %#v claimed=%v err=%v", claim, claimed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(35 * time.Millisecond)

	reopened, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedTranscript, err := reopened.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	restarted := New(Options{FileRoot: root, Workspace: reopened, Transcript: reopenedTranscript})
	result, err := restarted.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "restarted-worker",
		ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{
			Endpoint: staticProvider.URL + "/v1/chat/completions", APIKey: "static-key", Model: "static-model",
			LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
			RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1,
		},
	})
	if err != nil {
		t.Fatalf("RunFrameResumeDispatchOnce() error = %v", err)
	}
	if !result.Claimed || !result.Recovered || result.Attempt != 2 || result.Status != "completed" {
		session, _, _ := restarted.sessionStore.Get("resume-root")
		entries, _ := restarted.eventJournal.ReadAll("resume-root")
		dispatch, _, _ := reopened.GetCompatibilityFrameResumeDispatch(result.ResumeEventID)
		t.Fatalf("dispatch result = %+v session=%#v dispatch=%#v entries=%#v", result, session, dispatch, entries)
	}
	if savedRequests.Load() != 3 || staticRequests.Load() != 0 {
		session, _, _ := restarted.sessionStore.Get("resume-root")
		t.Fatalf("provider requests saved=%d static=%d session=%#v", savedRequests.Load(), staticRequests.Load(), session)
	}

	dispatch, found, err := reopened.GetCompatibilityFrameResumeDispatch(result.ResumeEventID)
	if err != nil || !found || dispatch.Status != "completed" || dispatch.Attempt != 2 {
		t.Fatalf("persisted dispatch = %#v found=%v err=%v", dispatch, found, err)
	}
	frame, found, err := reopened.GetFrame("resume-root")
	if err != nil || !found || frame.Status != "completed" {
		t.Fatalf("completed frame = %#v found=%v err=%v", frame, found, err)
	}
	history, authoritative, err := restarted.loadTranscriptWebHistory(context.Background(), "local", "resume-root")
	if err != nil || !authoritative || len(history) != 3 ||
		transcriptPayloadText(history[len(history)-1]["content"].(map[string]any)) != "resumed analysis completed" ||
		webString(history[len(history)-1]["terminal_status"]) != "completed" {
		t.Fatalf("history=%#v authoritative=%t err=%v", history, authoritative, err)
	}
	assertFrameResumeDispatchEvents(t, reopened, "resume-root", map[string]int{
		"frame_resumed":                   1,
		"frame_resume_dispatch_claimed":   2,
		"frame_resume_dispatch_recovered": 1,
		"frame_resume_dispatch_completed": 1,
	})

	session, found, err := restarted.sessionStore.Get("resume-root")
	if err != nil || found {
		t.Fatalf("transcript resume retained legacy session = %#v found=%v err=%v", session, found, err)
	}
	again, err := restarted.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "idle-worker", ClaimTTL: time.Second, Chat: SessionRunnerChatOptions{Endpoint: staticProvider.URL},
	})
	if err != nil || again.Claimed {
		t.Fatalf("idempotent dispatch run = %+v err=%v", again, err)
	}
	if savedRequests.Load() != 3 || staticRequests.Load() != 0 {
		t.Fatalf("duplicate model request saved=%d static=%d", savedRequests.Load(), staticRequests.Load())
	}
}

func TestTranscriptFrameResumeDefersDifferentLiveRunnerWithoutTerminalizingDispatch(t *testing.T) {
	var providerRequests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	root := t.TempDir()
	store, srv := newFreshFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), provider.URL, "transcript-live-collision", "defer transcript live runner")
	failed := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame("transcript-live-collision", workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/transcript-live-collision/resume", "local", map[string]any{
		"verifier_mode": "off", "memory_mode": "off",
	}, http.StatusOK)
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repository.GetFrameStreamBySession(context.Background(), "local", "transcript-live-collision")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	other, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "different-transcript-runner",
		TTL: 5 * time.Second, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !other.Claimed {
		t.Fatalf("other transcript claim=%#v err=%v", other, err)
	}
	result, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "resume-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	})
	if err != nil || !result.Claimed || result.Status != "deferred" {
		t.Fatalf("transcript live defer result=%+v err=%v", result, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("transcript-live-collision")
	if err != nil || !found || dispatch.Status != "registered" || !dispatch.NotBefore.Equal(other.Claim.ExpiresAt) {
		t.Fatalf("transcript live collision terminalized dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	again, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "resume-worker-two", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	})
	if err != nil || again.Claimed {
		t.Fatalf("deferred live runner dispatch was reclaimed before its lease expired: result=%+v err=%v", again, err)
	}
	if providerRequests.Load() != 0 {
		t.Fatalf("provider ran despite live transcript runner: %d", providerRequests.Load())
	}
}

func TestTranscriptFrameResumeConvergesIntoNewerExecutingRunner(t *testing.T) {
	var providerRequests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	root := t.TempDir()
	store, srv := newFreshFrameResumeDispatchFixture(
		t, root, filepath.Join(root, "workspace.db"), provider.URL,
		"transcript-live-convergence", "continue through one execution authority",
	)
	failed := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame("transcript-live-convergence", workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, srv.Handler(), http.MethodPost,
		"/api/frames/transcript-live-convergence/resume", "local",
		map[string]any{"verifier_mode": "off", "memory_mode": "off"}, http.StatusOK,
	)
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repository.GetFrameStreamBySession(
		context.Background(), "local", "transcript-live-convergence",
	)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	ordinary, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "ordinary-transcript-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !ordinary.Claimed {
		t.Fatalf("ordinary transcript claim=%#v err=%v", ordinary, err)
	}
	if _, _, _, err := repository.AppendRunnerCheckpoint(
		context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: ordinary.Claim, ClientMessageID: "ordinary-executing-checkpoint",
			Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true,
			PayloadJSON: []byte(`{"status":"running","stage":"tool_execution"}`),
		},
	); err != nil {
		t.Fatal(err)
	}

	result, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "resume-convergence-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	})
	if err != nil || !result.Claimed || result.Status != "completed" {
		t.Fatalf("transcript convergence result=%+v err=%v", result, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("transcript-live-convergence")
	if err != nil || !found || dispatch.Status != "completed" {
		t.Fatalf("converged dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	frame, found, err := store.GetFrame("transcript-live-convergence")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("active frame was terminalized=%#v found=%t err=%v", frame, found, err)
	}
	again, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "resume-convergence-worker-two", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	})
	if err != nil || again.Claimed {
		t.Fatalf("converged dispatch was reclaimed: result=%+v err=%v", again, err)
	}
	if providerRequests.Load() != 0 {
		t.Fatalf("provider ran despite newer executing authority: %d", providerRequests.Load())
	}
}

func TestTranscriptFrameResumeKeepsQueuedInputBehindExecutingRunner(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	root := t.TempDir()
	store, srv := newFreshFrameResumeDispatchFixture(
		t, root, filepath.Join(root, "workspace.db"), provider.URL,
		"transcript-queued-convergence", "preserve newer queued input",
	)
	failed := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame("transcript-queued-convergence", workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, srv.Handler(), http.MethodPost,
		"/api/frames/transcript-queued-convergence/resume", "local",
		map[string]any{"verifier_mode": "off", "memory_mode": "off"}, http.StatusOK,
	)
	repository, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repository.GetFrameStreamBySession(
		context.Background(), "local", "transcript-queued-convergence",
	)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	ordinary, err := repository.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "ordinary-queued-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !ordinary.Claimed {
		t.Fatalf("ordinary transcript claim=%#v err=%v", ordinary, err)
	}
	if _, _, _, err := repository.AppendRunnerCheckpoint(
		context.Background(), transcriptstore.AppendRunnerCheckpointInput{
			Claim: ordinary.Claim, ClientMessageID: "ordinary-queued-executing-checkpoint",
			Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true,
			PayloadJSON: []byte(`{"status":"running","stage":"tool_execution"}`),
		},
	); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repository.AppendFrameUserEvent(
		context.Background(), transcriptstore.AppendFrameUserEventInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			ClientMessageID: "queued-after-execution-start", FrameEventID: "queued-after-execution-start-frame",
			MessageUUID: "queued-after-execution-start-message", MessageOrigin: "task_intent",
			Text: "continue with this additional requirement", Destinations: []string{"ws"},
		},
	); err != nil {
		t.Fatal(err)
	}

	result, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "resume-queued-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	})
	if err != nil || !result.Claimed || result.Status != "deferred" {
		t.Fatalf("queued input dispatch result=%+v err=%v", result, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("transcript-queued-convergence")
	if err != nil || !found || dispatch.Status != "registered" || dispatch.NotBefore.IsZero() {
		t.Fatalf("queued input dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
}

func TestLegacyFrameResumeDispatchRecoveryWaitsForStartedRunnerLease(t *testing.T) {
	root := t.TempDir()
	const frameID = "legacy-resume-claim-fence"
	var providerRequests atomic.Int64
	var observedAttempt atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerRequests.Add(1)
		persisted, found, err := sessionstore.NewStore(root).Get(frameID)
		if err != nil || !found || persisted.Runner == nil {
			t.Errorf("provider observed runner=%#v found=%t err=%v", persisted.Runner, found, err)
		} else {
			observedAttempt.Store(int64(persisted.Runner.Attempt))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"legacy-resume\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"recovered exactly once\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer provider.Close()

	databasePath := filepath.Join(root, "workspace.db")
	store, _ := newFrameResumeDispatchFixture(t, root, databasePath, provider.URL, frameID, "resume legacy execution")
	failedStatus := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &failedStatus}); err != nil {
		t.Fatal(err)
	}
	legacy := New(Options{FileRoot: root, Workspace: store})
	if _, err := legacy.eventJournal.Append(frameID, eventjournal.Message{
		"type": "message", "role": "user", "text": "resume legacy execution",
	}, eventjournal.Metadata{ClientMessageID: "legacy-resume-user-message"}); err != nil {
		t.Fatal(err)
	}
	response := compatJSONRequest(t, legacy.Handler(), http.MethodPost, "/api/frames/"+frameID+"/resume", "local", map[string]any{
		"verifier_mode": "off", "memory_mode": "off",
	}, http.StatusOK)
	assertCompatibilityResumeResponse(t, response, frameID, frameID, "OPERON", 1)
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame(frameID)
	if err != nil || !found {
		t.Fatalf("resume dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	firstDispatch, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-a", 20*time.Millisecond)
	if err != nil || !claimed || firstDispatch.ResumeEvent.ID != dispatch.ResumeEvent.ID {
		t.Fatalf("first dispatch claim=%#v claimed=%t err=%v", firstDispatch, claimed, err)
	}
	runnerID := frameResumeRunnerID(dispatch.ResumeEvent.ID)
	reserved, runnerClaimed, err := legacy.sessionStore.ClaimRunner(frameID, runnerID, 5*time.Second)
	if err != nil || !runnerClaimed || reserved.Runner == nil || reserved.Runner.Attempt != 1 {
		t.Fatalf("reserved runner=%#v claimed=%t err=%v", reserved.Runner, runnerClaimed, err)
	}
	firstClaim := sessionstore.RunnerClaimFromSession(reserved)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(35 * time.Millisecond)

	reopened, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := New(Options{FileRoot: root, Workspace: reopened})
	deferred, err := restarted.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "worker-b", ClaimTTL: 20 * time.Millisecond, ReservationTTL: time.Minute,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	})
	if !errors.Is(err, errFrameResumeRunnerStillActive) || !deferred.Claimed || !deferred.Recovered {
		t.Fatalf("live runner recovery result=%+v err=%v", deferred, err)
	}
	if providerRequests.Load() != 0 {
		t.Fatalf("provider ran while old runner lease remained live: %d", providerRequests.Load())
	}
	stillReserved, found, getErr := restarted.sessionStore.Get(frameID)
	if getErr != nil || !found || stillReserved.Runner == nil || stillReserved.Runner.Attempt != 1 {
		t.Fatalf("live reservation changed=%#v found=%t err=%v", stillReserved.Runner, found, getErr)
	}
	if _, renewed, err := restarted.sessionStore.HeartbeatRunner(firstClaim, 50*time.Millisecond); err != nil || !renewed {
		t.Fatalf("shorten started runner lease renewed=%t err=%v", renewed, err)
	}
	time.Sleep(80 * time.Millisecond)

	result, err := restarted.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "worker-c", ClaimTTL: time.Second, ReservationTTL: time.Minute,
		Chat: SessionRunnerChatOptions{
			Endpoint: provider.URL + "/v1/chat/completions", Model: "fallback-model", APIKey: "fallback-key",
			LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
			RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1,
		},
	})
	if err != nil {
		t.Fatalf("recovered RunFrameResumeDispatchOnce() error=%v", err)
	}
	if !result.Claimed || !result.Recovered || result.Runner.Attempt != 2 || result.Status != "completed" {
		entries, _ := restarted.eventJournal.ReadAll(frameID)
		persisted, _, _ := reopened.GetCompatibilityFrameResumeDispatch(result.ResumeEventID)
		t.Fatalf("recovered result=%+v dispatch=%#v entries=%#v", result, persisted, entries)
	}
	if providerRequests.Load() != 1 || observedAttempt.Load() != 2 {
		t.Fatalf("provider requests=%d observed attempt=%d", providerRequests.Load(), observedAttempt.Load())
	}
	if _, err := restarted.sessionStore.ValidateRunnerClaim(firstClaim, false); !errors.Is(err, sessionstore.ErrRunnerClaimStale) {
		t.Fatalf("started worker retained authority after recovery: %v", err)
	}
}

func TestLegacyFrameResumeDispatchTerminalizesUncertainToolWithoutRetryLoop(t *testing.T) {
	root := t.TempDir()
	const frameID = "legacy-resume-tool-quarantine"
	var providerRequests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()

	databasePath := filepath.Join(root, "workspace.db")
	store, _ := newFrameResumeDispatchFixture(t, root, databasePath, provider.URL, frameID, "resume uncertain legacy execution")
	failedStatus := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &failedStatus}); err != nil {
		t.Fatal(err)
	}
	legacy := New(Options{FileRoot: root, Workspace: store})
	if _, err := legacy.eventJournal.Append(frameID, eventjournal.Message{
		"type": "message", "role": "user", "text": "resume uncertain legacy execution",
	}, eventjournal.Metadata{ClientMessageID: "legacy-uncertain-user-message"}); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, legacy.Handler(), http.MethodPost, "/api/frames/"+frameID+"/resume", "local", map[string]any{
		"verifier_mode": "off", "memory_mode": "off",
	}, http.StatusOK)
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame(frameID)
	if err != nil || !found {
		t.Fatalf("resume dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	firstDispatch, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-a", 20*time.Millisecond)
	if err != nil || !claimed || firstDispatch.ResumeEvent.ID != dispatch.ResumeEvent.ID {
		t.Fatalf("first dispatch claim=%#v claimed=%t err=%v", firstDispatch, claimed, err)
	}
	runnerID := frameResumeRunnerID(dispatch.ResumeEvent.ID)
	reserved, runnerClaimed, err := legacy.sessionStore.ClaimRunner(frameID, runnerID, 50*time.Millisecond)
	if err != nil || !runnerClaimed || reserved.Runner == nil {
		t.Fatalf("runner reservation=%#v claimed=%t err=%v", reserved.Runner, runnerClaimed, err)
	}
	firstClaim := sessionstore.RunnerClaimFromSession(reserved)
	toolStarted, err := legacy.eventJournal.Append(frameID, eventjournal.Message{
		"type": "runner_checkpoint", "role": "system", "message": "tool task_create started",
		"runnerId": runnerID, "runnerAttempt": 1, "toolCallId": "uncertain-call",
		"toolName": "task_create", "toolPhase": "start",
	}, eventjournal.Metadata{ClientMessageID: "legacy-uncertain-tool-start"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.sessionStore.CheckpointRunner(sessionstore.CheckpointRunnerInput{
		Claim: firstClaim, Status: "running", Checkpoint: "tool task_create started", EventID: toolStarted.EventID,
	}); err != nil {
		t.Fatalf("checkpoint mutating tool start: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(80 * time.Millisecond)

	reopened, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restarted := New(Options{FileRoot: root, Workspace: reopened})
	failed, err := restarted.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "worker-b", ClaimTTL: time.Second, ReservationTTL: time.Minute,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	})
	if err != nil || !failed.Claimed || !failed.Recovered || failed.Status != "failed" {
		t.Fatalf("failed recovery result=%+v err=%v", failed, err)
	}
	if providerRequests.Load() != 0 {
		t.Fatalf("provider ran with uncertain tool outcome: %d", providerRequests.Load())
	}
	persisted, found, err := reopened.GetCompatibilityFrameResumeDispatch(dispatch.ResumeEvent.ID)
	if err != nil || !found || persisted.Status != "failed" || persisted.Error != "tool_outcome_uncertain" {
		t.Fatalf("failed dispatch=%#v found=%t err=%v", persisted, found, err)
	}
	session, found, err := restarted.sessionStore.Get(frameID)
	if err != nil || !found || session.Runner == nil || session.Runner.Attempt != 1 {
		t.Fatalf("failed session=%#v found=%t err=%v", session, found, err)
	}
	loopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := restarted.RunFrameResumeDispatchLoop(loopCtx, FrameResumeDispatchOptions{
		WorkerID: "loop-worker", PollInterval: 5 * time.Millisecond, ClaimTTL: 20 * time.Millisecond,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	}); err != nil {
		t.Fatalf("failed dispatch stopped production loop: %v", err)
	}
	frame, found, err := reopened.GetFrame(frameID)
	if err != nil || !found || frame.Status != "failed" {
		t.Fatalf("failed frame=%#v found=%t err=%v", frame, found, err)
	}
}

func TestLegacyFrameResumeLoopDefersDifferentLiveRunnerWithoutOverwritingAuthority(t *testing.T) {
	root := t.TempDir()
	const frameID = "legacy-resume-different-live-runner"
	var providerRequests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerRequests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	store, _ := newFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), provider.URL, frameID, "preserve different live runner")
	failed := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	legacy := New(Options{FileRoot: root, Workspace: store})
	if _, err := legacy.eventJournal.Append(frameID, eventjournal.Message{
		"type": "message", "role": "user", "text": "preserve different live runner",
	}, eventjournal.Metadata{ClientMessageID: "different-live-user"}); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, legacy.Handler(), http.MethodPost, "/api/frames/"+frameID+"/resume", "local", map[string]any{
		"verifier_mode": "off", "memory_mode": "off",
	}, http.StatusOK)
	dispatch, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-a", 20*time.Millisecond)
	if err != nil || !claimed {
		t.Fatalf("dispatch=%#v claimed=%t err=%v", dispatch, claimed, err)
	}
	reserved, runnerClaimed, err := legacy.sessionStore.ClaimRunner(frameID, "different-live-runner", 5*time.Second)
	if err != nil || !runnerClaimed || reserved.Runner == nil {
		t.Fatalf("reservation=%#v claimed=%t err=%v", reserved.Runner, runnerClaimed, err)
	}
	wantClaim := sessionstore.RunnerClaimFromSession(reserved)
	time.Sleep(35 * time.Millisecond)
	loopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := legacy.RunFrameResumeDispatchLoop(loopCtx, FrameResumeDispatchOptions{
		WorkerID: "worker-b", PollInterval: 5 * time.Millisecond, ClaimTTL: 20 * time.Millisecond,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	}); err != nil {
		t.Fatalf("different live runner stopped dispatch loop: %v", err)
	}
	current, found, err := legacy.sessionStore.Get(frameID)
	if err != nil || !found || current.Runner == nil || current.Runner.RunnerID != "different-live-runner" ||
		current.Runner.Attempt != 1 || sessionstore.RunnerClaimFromSession(current) != wantClaim {
		t.Fatalf("different runner authority changed=%#v found=%t err=%v", current.Runner, found, err)
	}
	if providerRequests.Load() != 0 {
		t.Fatalf("provider ran while different runner was live: %d", providerRequests.Load())
	}
}

func TestLegacyFrameResumeDefersLocalActiveRunBeforeClaim(t *testing.T) {
	root := t.TempDir()
	const frameID = "legacy-resume-local-active-run"
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("provider must not run while local session run is active")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	store, _ := newFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), provider.URL, frameID, "defer local active run")
	failed := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	legacy := New(Options{FileRoot: root, Workspace: store})
	if _, err := legacy.eventJournal.Append(frameID, eventjournal.Message{
		"type": "message", "role": "user", "text": "defer local active run",
	}, eventjournal.Metadata{ClientMessageID: "local-active-user"}); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, legacy.Handler(), http.MethodPost, "/api/frames/"+frameID+"/resume", "local", map[string]any{
		"verifier_mode": "off", "memory_mode": "off",
	}, http.StatusOK)
	_, _, cleanup, err := legacy.registerActiveSessionRun(context.Background(), frameID, "local-active-runner")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	result, err := legacy.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "resume-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	})
	if !errors.Is(err, errFrameResumeRunnerStillActive) || !result.Claimed {
		t.Fatalf("active-run defer result=%+v err=%v", result, err)
	}
	session, found, err := legacy.sessionStore.Get(frameID)
	if err != nil || !found || session.Runner != nil {
		t.Fatalf("active-run defer created runner=%#v found=%t err=%v", session.Runner, found, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame(frameID)
	if err != nil || !found || dispatch.Status != "claimed" {
		t.Fatalf("active-run dispatch became terminal=%#v found=%t err=%v", dispatch, found, err)
	}
}

func TestMonitorFrameResumeDispatchReportsClaimLoss(t *testing.T) {
	root := t.TempDir()
	store, srv := newFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), "http://127.0.0.1:1", "monitor-claim-loss", "monitor claim loss")
	failed := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame("monitor-claim-loss", workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/monitor-claim-loss/resume", "local", map[string]any{}, http.StatusOK)
	dispatch, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("monitor-worker", 100*time.Millisecond)
	if err != nil || !claimed {
		t.Fatalf("dispatch=%#v claimed=%t err=%v", dispatch, claimed, err)
	}
	monitorCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	done := make(chan struct{})
	errCh := make(chan error, 1)
	go srv.monitorFrameResumeDispatch(monitorCtx, cancel, done, errCh, dispatch, 30*time.Millisecond)
	if _, _, err := store.FailCompatibilityFrameResumeDispatch(dispatch.ResumeEvent.ID, dispatch.Attempt, dispatch.ClaimToken, "test_claim_loss"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if !errors.Is(err, errFrameResumeDispatchClaimLost) {
			t.Fatalf("monitor claim loss error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("monitor did not report dispatch claim loss")
	}
	select {
	case <-monitorCtx.Done():
		var interruption sessionRunnerInfrastructureInterruption
		if !errors.As(context.Cause(monitorCtx), &interruption) || interruption.ReasonCode != sessionRunnerResumeDispatchInterruptedReasonCode {
			t.Fatalf("monitor cancellation cause=%v", context.Cause(monitorCtx))
		}
	case <-time.After(time.Second):
		t.Fatal("monitor did not cancel runner context after claim loss")
	}
}

func TestMonitorFrameResumeDispatchRenewsClaimAcrossPreflightWindow(t *testing.T) {
	root := t.TempDir()
	store, srv := newFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), "http://127.0.0.1:1", "monitor-preflight-renewal", "monitor preflight renewal")
	failed := workspace.FrameStatusFailed
	if _, err := store.UpdateFrame("monitor-preflight-renewal", workspace.UpdateFrameInput{Status: &failed}); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/monitor-preflight-renewal/resume", "local", map[string]any{}, http.StatusOK)
	const ttl = 90 * time.Millisecond
	dispatch, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("monitor-worker", ttl)
	if err != nil || !claimed {
		t.Fatalf("dispatch=%#v claimed=%t err=%v", dispatch, claimed, err)
	}

	monitorCtx, cancel := context.WithCancelCause(context.Background())
	done := make(chan struct{})
	errCh := make(chan error, 1)
	monitorExited := make(chan struct{})
	go func() {
		defer close(monitorExited)
		srv.monitorFrameResumeDispatch(monitorCtx, cancel, done, errCh, dispatch, ttl)
	}()
	t.Cleanup(func() {
		close(done)
		cancel(nil)
		<-monitorExited
	})

	deadline := time.Now().Add(time.Second)
	for {
		current, found, readErr := store.GetCompatibilityFrameResumeDispatch(dispatch.ResumeEvent.ID)
		if readErr != nil || !found {
			t.Fatalf("read renewed dispatch found=%t err=%v", found, readErr)
		}
		if current.LeaseExpiresAt.After(dispatch.LeaseExpiresAt) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("dispatch lease was not renewed beyond %s", dispatch.LeaseExpiresAt)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if stolen, secondClaimed, claimErr := store.ClaimNextCompatibilityFrameResumeDispatch("second-worker", ttl); claimErr != nil || secondClaimed {
		t.Fatalf("renewed dispatch was stolen=%#v claimed=%t err=%v", stolen, secondClaimed, claimErr)
	}
	select {
	case monitorErr := <-errCh:
		t.Fatalf("monitor failed while preserving preflight authority: %v", monitorErr)
	default:
	}
}

func TestNormalizeFrameResumeDispatchOptionsKeepsOuterLeaseBeyondRunnerLease(t *testing.T) {
	options := normalizeFrameResumeDispatchOptions(FrameResumeDispatchOptions{
		Chat: SessionRunnerChatOptions{LeaseTTL: 7 * time.Minute},
	})
	if options.ClaimTTL != 8*time.Minute {
		t.Fatalf("default dispatch lease=%s, want 8m", options.ClaimTTL)
	}

	explicit := normalizeFrameResumeDispatchOptions(FrameResumeDispatchOptions{
		ClaimTTL: 80 * time.Millisecond,
		Chat:     SessionRunnerChatOptions{LeaseTTL: 7 * time.Minute},
	})
	if explicit.ClaimTTL != 80*time.Millisecond {
		t.Fatalf("explicit dispatch lease was rewritten: %s", explicit.ClaimTTL)
	}
}

func TestFrameResumeDispatchClaimLossInterruptsRunnerWithoutUserCancellation(t *testing.T) {
	providerStarted := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case providerStarted <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer provider.Close()

	root := t.TempDir()
	store, srv := newFrameResumeDispatchFixture(
		t, root, filepath.Join(root, "workspace.db"), provider.URL,
		"dispatch-claim-loss-root", "keep this task resumable when dispatch ownership changes",
	)
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/dispatch-claim-loss-root/resume", "local", map[string]any{}, http.StatusOK)

	type dispatchResult struct {
		result FrameResumeDispatchResult
		err    error
	}
	completed := make(chan dispatchResult, 1)
	go func() {
		result, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
			WorkerID: "claim-loss-worker", ClaimTTL: 60 * time.Millisecond,
			Chat: SessionRunnerChatOptions{
				Endpoint: BuiltinSessionRunnerChatEndpoint, Model: BuiltinSessionRunnerChatModel,
				LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
				RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1,
				DisableSkillDiscovery: true, DisableMCPDiscovery: true,
			},
		})
		completed <- dispatchResult{result: result, err: err}
	}()

	select {
	case <-providerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("provider call did not start")
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("dispatch-claim-loss-root")
	if err != nil || !found || dispatch.Status != "claimed" {
		t.Fatalf("running dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	if _, _, err := store.RequeueCompatibilityFrameResumeDispatch(workspace.RequeueCompatibilityFrameResumeDispatchInput{
		ResumeEventID: dispatch.ResumeEvent.ID, ExpectedAttempt: dispatch.Attempt,
		ClaimToken: dispatch.ClaimToken, ReasonCode: "test_claim_loss", NotBefore: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	var observed dispatchResult
	select {
	case observed = <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not stop after claim loss")
	}
	if !errors.Is(observed.err, errFrameResumeDispatchClaimLost) {
		t.Fatalf("dispatch result=%+v err=%v", observed.result, observed.err)
	}
	frame, found, err := store.GetFrame("dispatch-claim-loss-root")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("claim-loss frame=%#v found=%t err=%v", frame, found, err)
	}
	state, found, err := srv.transcriptStore.GetLatestRunnerRuntimeState(
		context.Background(), "frame:dispatch-claim-loss-root", "local",
	)
	if err != nil || !found || state.Status != "running" || state.ExpiresAt.After(time.Now()) ||
		state.LastCheckpointSequence == 0 || observed.result.Runner.CheckpointEventID == 0 ||
		observed.result.Runner.Status != "interrupted" ||
		observed.result.Runner.InterruptionReasonCode != sessionRunnerResumeDispatchInterruptedReasonCode {
		t.Fatalf("claim-loss runner state=%#v found=%t result=%+v err=%v", state, found, observed.result, err)
	}
	assertFrameResumeDispatchEvents(t, store, "dispatch-claim-loss-root", map[string]int{
		"frame_cancelled":                 0,
		"frame_resume_dispatch_failed":    0,
		"frame_resume_dispatch_cancelled": 0,
	})
}

func TestRetryFrameResumeDispatchRenewalSurvivesTransientSQLiteContention(t *testing.T) {
	var calls int
	active, err := retryFrameResumeDispatchRenewal(context.Background(), 100*time.Millisecond, func() (bool, error) {
		calls++
		if calls < 3 {
			return false, fmt.Errorf("renew resume dispatch: database is locked (5) (SQLITE_BUSY)")
		}
		return true, nil
	})
	if err != nil || !active || calls != 3 {
		t.Fatalf("renewal active=%t calls=%d err=%v", active, calls, err)
	}
}

func TestRetryFrameResumeDispatchRenewalDoesNotHidePermanentFailure(t *testing.T) {
	var calls int
	active, err := retryFrameResumeDispatchRenewal(context.Background(), time.Second, func() (bool, error) {
		calls++
		return false, errors.New("workspace store is closed")
	})
	if err == nil || active || calls != 1 || !strings.Contains(err.Error(), "workspace store is closed") {
		t.Fatalf("renewal active=%t calls=%d err=%v", active, calls, err)
	}
}

func TestRetryFrameResumeDispatchRenewalHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls int
	active, err := retryFrameResumeDispatchRenewal(ctx, time.Second, func() (bool, error) {
		calls++
		return false, errors.New("SQLITE_LOCKED: database table is locked")
	})
	if !errors.Is(err, context.Canceled) || active || calls != 1 {
		t.Fatalf("renewal active=%t calls=%d err=%v", active, calls, err)
	}
}

func TestFrameResumeDispatchStoreErrorClassifierOnlyRetriesSQLiteContention(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "busy code", err: errors.New("SQLITE_BUSY"), want: true},
		{name: "locked message", err: fmt.Errorf("wrapped: %w", errors.New("database is locked (5)")), want: true},
		{name: "locked table", err: errors.New("database table is locked"), want: true},
		{name: "closed store", err: errors.New("workspace store is closed"), want: false},
		{name: "disk full", err: errors.New("database or disk is full"), want: false},
		{name: "nil", err: nil, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isTransientSQLiteContention(test.err); got != test.want {
				t.Fatalf("transient=%t want=%t err=%v", got, test.want, test.err)
			}
		})
	}
}

func TestLegacyResumeToolOutcomeClassifierRequiresExactCertainPairing(t *testing.T) {
	checkpoint := func(runnerID string, attempt int, callID, toolName, phase, certainty string) eventjournal.Entry {
		message := eventjournal.Message{
			"type": "runner_checkpoint", "runnerId": runnerID, "runnerAttempt": attempt,
			"toolCallId": callID, "toolName": toolName, "toolPhase": phase,
		}
		if certainty != "" {
			message["toolOutcomeCertainty"] = certainty
		}
		return eventjournal.Entry{Message: message}
	}
	start := checkpoint("runner-a", 1, "call-1", "task_create", "start", "")
	for _, test := range []struct {
		name    string
		entries []eventjournal.Entry
		want    bool
	}{
		{name: "unresolved mutating", entries: []eventjournal.Entry{start}, want: true},
		{name: "ambiguous failure remains unresolved", entries: []eventjournal.Entry{start, checkpoint("runner-a", 1, "call-1", "task_create", "failed", "")}, want: true},
		{name: "definitely not started failure resolves", entries: []eventjournal.Entry{start, checkpoint("runner-a", 1, "call-1", "task_create", "failed", "not_started")}, want: false},
		{name: "completed exact invocation resolves", entries: []eventjournal.Entry{start, checkpoint("runner-a", 1, "call-1", "task_create", "completed", "committed")}, want: false},
		{name: "different attempt cannot resolve", entries: []eventjournal.Entry{start, checkpoint("runner-a", 2, "call-1", "task_create", "completed", "committed")}, want: true},
		{name: "different runner cannot resolve", entries: []eventjournal.Entry{start, checkpoint("runner-b", 1, "call-1", "task_create", "completed", "committed")}, want: true},
		{name: "different tool cannot resolve", entries: []eventjournal.Entry{start, checkpoint("runner-a", 1, "call-1", "file_write", "completed", "committed")}, want: true},
		{name: "read-only alias does not block", entries: []eventjournal.Entry{checkpoint("runner-a", 1, "call-read", "Read", "start", "")}, want: false},
		{name: "unknown tool fails closed", entries: []eventjournal.Entry{checkpoint("runner-a", 1, "call-unknown", "custom_tool", "start", "")}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := legacyResumeHasUnresolvedSideEffectingTool(test.entries); got != test.want {
				t.Fatalf("unresolved=%t want=%t entries=%#v", got, test.want, test.entries)
			}
		})
	}
}

func TestFrameResumeDispatchCancellationIsTerminalAndNeverCallsProvider(t *testing.T) {
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"unexpected"}}]}`))
	}))
	defer provider.Close()

	root := t.TempDir()
	store, srv := newFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), provider.URL, "cancel-root", "do not execute after cancellation")
	app := srv.Handler()
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/cancel-root/resume", "local", map[string]any{}, http.StatusOK)
	cancelled := compatJSONRequest(t, app, http.MethodPost, "/api/frames/cancel-root/cancel?reason=user-stop", "local", nil, http.StatusOK)
	if !equalStringArray(cancelled["cancelled_frames"], []string{"cancel-root"}) {
		t.Fatalf("cancel response = %#v", cancelled)
	}

	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("cancel-root")
	if err != nil || !found || dispatch.Status != "cancelled" {
		t.Fatalf("cancelled dispatch = %#v found=%v err=%v", dispatch, found, err)
	}
	result, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "cancel-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions"},
	})
	if err != nil || result.Claimed {
		t.Fatalf("cancelled dispatch worker = %+v err=%v", result, err)
	}
	if requests.Load() != 0 {
		t.Fatalf("cancelled dispatch provider requests = %d", requests.Load())
	}
	assertFrameResumeDispatchEvents(t, store, "cancel-root", map[string]int{
		"frame_resumed":                   1,
		"frame_cancelled":                 1,
		"frame_resume_dispatch_cancelled": 1,
	})
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/cancel-root/cancel?reason=user-stop-again", "local", nil, http.StatusOK)
	assertFrameResumeDispatchEvents(t, store, "cancel-root", map[string]int{
		"frame_resume_dispatch_cancelled": 1,
	})
}

func TestFrameResumeDispatchProviderFailureTerminalizesUntilModelSwitch(t *testing.T) {
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"quota exhausted"}}`))
	}))
	defer provider.Close()

	root := t.TempDir()
	store, srv := newFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), provider.URL, "failed-root", "force the provider failure")
	app := srv.Handler()
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/failed-root/resume", "local", map[string]any{}, http.StatusOK)

	result, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "failure-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{
			Endpoint: BuiltinSessionRunnerChatEndpoint, Model: BuiltinSessionRunnerChatModel,
			LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
			RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1,
			DisableSkillDiscovery: true, DisableMCPDiscovery: true,
		},
	})
	if err != nil {
		t.Fatalf("RunFrameResumeDispatchOnce() error = %v", err)
	}
	if !result.Claimed || result.Status != "failed" || result.Runner.Status != "interrupted" || result.Runner.InterruptionReasonCode != sessionRunnerModelProviderUnavailableReasonCode {
		t.Fatalf("provider-unavailable dispatch result = %+v", result)
	}
	if requests.Load() != 1 {
		t.Fatalf("failed provider requests = %d", requests.Load())
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatch(result.ResumeEventID)
	if err != nil || !found || dispatch.Status != "failed" || dispatch.Error != sessionRunnerModelProviderUnavailableReasonCode {
		t.Fatalf("provider-unavailable dispatch = %#v found=%v err=%v", dispatch, found, err)
	}
	frame, found, err := store.GetFrame("failed-root")
	if err != nil || !found || frame.Status != "failed" {
		t.Fatalf("provider-unavailable frame = %#v found=%v err=%v", frame, found, err)
	}
	assertFrameResumeDispatchEvents(t, store, "failed-root", map[string]int{
		"frame_resume_dispatch_claimed": 1,
		"frame_resume_dispatch_blocked": 0,
		"frame_resume_dispatch_failed":  1,
	})

	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "alternate-provider", UserID: "local", Name: "Alternate provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "alternate-model",
		SecretRef: "secret://resume-provider-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	response := compatJSONRequest(t, app, http.MethodPut, "/api/conversations/failed-root/config-options/model", "local", map[string]any{
		"value": "alternate-model",
	}, http.StatusOK)
	if response["applies_to"] != "next_model_call" || response["resume"] != "woken" {
		t.Fatalf("model switch response=%#v", response)
	}
	oldDispatch, found, err := store.GetCompatibilityFrameResumeDispatch(result.ResumeEventID)
	if err != nil || !found || oldDispatch.Status != "failed" {
		t.Fatalf("terminal provider dispatch=%#v found=%v err=%v", oldDispatch, found, err)
	}
	dispatch, found, err = store.GetCompatibilityFrameResumeDispatchByFrame("failed-root")
	if err != nil || !found || dispatch.ResumeEvent.ID == result.ResumeEventID || dispatch.Status != "registered" {
		t.Fatalf("fresh model-switch dispatch=%#v found=%v err=%v", dispatch, found, err)
	}
}

func TestConversationModelSwitchResumesProviderInterruptionWithoutExistingDispatch(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer provider.Close()

	root := t.TempDir()
	store, srv := newFreshFrameResumeDispatchFixture(
		t, root, filepath.Join(root, "workspace.db"), provider.URL, "initial-provider-interruption", "continue after switching models",
	)
	processing := "processing"
	if _, err := store.UpdateFrame("initial-provider-interruption", workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "initial-provider-interruption")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "initial-provider-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	if _, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claim.Claim, ClientMessageID: "initial-provider-unavailable",
		ReasonCode:   sessionRunnerModelProviderUnavailableReasonCode,
		ResumeDetail: "provider quota exhausted", AutoResume: false, Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("initial-provider-interruption"); err != nil || found {
		t.Fatalf("unexpected dispatch before model switch found=%t err=%v", found, err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "initial-alternate-provider", UserID: "local", Name: "Alternate provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "alternate-model", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}

	response := compatJSONRequest(t, srv.Handler(), http.MethodPut,
		"/api/conversations/initial-provider-interruption/config-options/model", "local",
		map[string]any{"value": "alternate-model"}, http.StatusOK)
	if response["confirmation"] != "observed" || response["resume"] != "woken" {
		t.Fatalf("model switch response=%#v", response)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("initial-provider-interruption")
	if err != nil || !found || dispatch.Status != "registered" {
		t.Fatalf("created dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
}

func TestConversationModelSwitchArmsClaimedDispatchBeforeProviderFailureSettles(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer provider.Close()

	root := t.TempDir()
	store, srv := newFrameResumeDispatchFixture(
		t, root, filepath.Join(root, "workspace.db"), provider.URL, "claimed-model-switch", "keep this task alive",
	)
	app := srv.Handler()
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/claimed-model-switch/resume", "local", map[string]any{}, http.StatusOK)
	claimed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("claimed-model-worker", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim=%#v ok=%t err=%v", claimed, ok, err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "claimed-alternate-provider", UserID: "local", Name: "Alternate provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "claimed-alternate-model", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	response := compatJSONRequest(t, app, http.MethodPut,
		"/api/conversations/claimed-model-switch/config-options/model", "local",
		map[string]any{"value": "claimed-alternate-model"}, http.StatusOK)
	if response["confirmation"] != "observed" || response["resume"] != "armed" {
		t.Fatalf("model switch response=%#v", response)
	}
	interrupted, resumed, err := store.FailCompatibilityFrameResumeDispatch(
		claimed.ResumeEvent.ID, claimed.Attempt, claimed.ClaimToken, sessionRunnerModelProviderUnavailableReasonCode,
	)
	if err != nil || !resumed || interrupted.Type != "frame_resume_dispatch_interrupted" {
		t.Fatalf("settlement event=%#v resumed=%t err=%v", interrupted, resumed, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatch(claimed.ResumeEvent.ID)
	if err != nil || !found || dispatch.Status != "registered" || dispatch.Error != "" {
		t.Fatalf("requeued dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
}

func TestFrameResumeDispatchModelConfigurationFailureWaitsForModelSwitch(t *testing.T) {
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"must not be called"}}]}`))
	}))
	defer provider.Close()

	root := t.TempDir()
	store, srv := newFrameResumeDispatchFixture(
		t, root, filepath.Join(root, "workspace.db"), provider.URL,
		"model-config-root", "preserve this task when the selected model credential is unavailable",
	)
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "missing-secret-provider", UserID: "local", Name: "Missing secret", Type: "openai-compatible",
		BaseURL: provider.URL + "/v1", Model: "missing-secret-model",
		SecretRef: "secret://does-not-exist", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SetCompatibilityConversationModel("model-config-root", "missing-secret-model"); err != nil {
		t.Fatal(err)
	}
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/model-config-root/resume", "local", map[string]any{}, http.StatusOK)

	result, err := srv.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "model-config-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{
			Endpoint: BuiltinSessionRunnerChatEndpoint, Model: BuiltinSessionRunnerChatModel,
			LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
			RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1,
			DisableSkillDiscovery: true, DisableMCPDiscovery: true,
		},
	})
	if err != nil {
		t.Fatalf("RunFrameResumeDispatchOnce() error = %v", err)
	}
	if result.Status != "paused" || result.Runner.Status != "interrupted" ||
		result.Runner.InterruptionReasonCode != sessionRunnerModelProviderUnavailableReasonCode ||
		!result.Runner.AwaitingModelSelection {
		t.Fatalf("model configuration dispatch result=%+v", result)
	}
	if requests.Load() != 0 {
		t.Fatalf("provider calls=%d, want 0 before credential resolution succeeds", requests.Load())
	}
	frame, found, err := store.GetFrame("model-config-root")
	if err != nil || !found || frame.Status != workspace.FrameStatusProcessing {
		t.Fatalf("model configuration frame=%#v found=%t err=%v", frame, found, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("model-config-root")
	if err != nil || !found || dispatch.Status != "registered" ||
		dispatch.WaitingFor != workspace.CompatibilityFrameResumeDispatchWaitModelSelection {
		t.Fatalf("model configuration dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	projected := compatJSONRequest(t, srv.Handler(), http.MethodGet,
		"/api/frames/model-config-root", "local", nil, http.StatusOK)
	if projected["runtime_paused"] != true ||
		projected["runtime_interruption_reason"] != sessionRunnerModelProviderUnavailableReasonCode {
		t.Fatalf("model configuration projection=%#v", projected)
	}
}

func TestFrameResumeDispatchContinuesInternalProviderSegmentWithoutNewParentAttempt(t *testing.T) {
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		if requests.Add(1) == 1 {
			_, _ = w.Write([]byte("data: {\"id\":\"interrupted-resume\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"partial output\"}}]}\n\n"))
			flusher.Flush()
			return
		}
		_, _ = w.Write([]byte("data: {\"id\":\"completed-resume\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"durable answer\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	}))
	defer provider.Close()

	root := t.TempDir()
	store, srv := newFreshFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), provider.URL, "interrupt-root", "resume after a local provider interruption")
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/interrupt-root/resume", "local", map[string]any{}, http.StatusOK)
	options := FrameResumeDispatchOptions{
		WorkerID: "interrupt-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{
			Endpoint: BuiltinSessionRunnerChatEndpoint, Model: BuiltinSessionRunnerChatModel,
			LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
			RequestTimeout: time.Minute, MaxAttempts: 1, MaxToolRounds: 1,
			DisableSkillDiscovery: true,
		},
	}
	interrupted, err := srv.RunFrameResumeDispatchOnce(context.Background(), options)
	if err != nil || !interrupted.Claimed || interrupted.Status != "interrupted" || interrupted.TerminalEvent != nil ||
		interrupted.Runner.Attempt != 1 {
		t.Fatalf("interrupted dispatch=%#v err=%v", interrupted, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatch(interrupted.ResumeEventID)
	if err != nil || !found || dispatch.Status != "registered" || dispatch.Attempt != 1 {
		t.Fatalf("interrupted persisted dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	dispatchPayload, _ := dispatch.ResumeEvent.Payload["dispatch"].(map[string]any)
	if got := int(numberValue(dispatchPayload["runnerAttempt"])); got != interrupted.Runner.Attempt {
		t.Fatalf("persisted runner attempt=%d want=%d payload=%#v", got, interrupted.Runner.Attempt, dispatchPayload)
	}
	if got := numberValue(dispatchPayload["checkpointEventId"]); got != interrupted.Runner.CheckpointEventID || got == 0 {
		t.Fatalf("persisted checkpoint event=%d want=%d payload=%#v", got, interrupted.Runner.CheckpointEventID, dispatchPayload)
	}
	frame, found, err := store.GetFrame("interrupt-root")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("interrupted frame=%#v found=%t err=%v", frame, found, err)
	}

	next, err := srv.RunFrameResumeDispatchOnce(context.Background(), options)
	if err != nil || !next.Claimed || next.Status != "completed" || next.Runner.Attempt != 1 || requests.Load() != 2 {
		detail := ""
		if next.TerminalEvent != nil {
			raw, _ := json.Marshal(next.TerminalEvent)
			detail = string(raw)
		}
		journal := ""
		if entries, readErr := srv.eventJournal.ReadAll("interrupt-root"); readErr == nil {
			for _, entry := range entries {
				raw, _ := json.Marshal(entry)
				journal += string(raw) + "\n"
			}
		}
		t.Fatalf("continued segment=%#v requests=%d err=%v terminal=%s journal=%s", next, requests.Load(), err, detail, journal)
	}
	assertFrameResumeDispatchEvents(t, store, "interrupt-root", map[string]int{
		"frame_resume_dispatch_claimed":     2,
		"frame_resume_dispatch_interrupted": 1,
		"frame_resume_dispatch_recovered":   1,
		"frame_resume_dispatch_completed":   1,
		"frame_resume_dispatch_blocked":     0,
		"frame_resume_dispatch_failed":      0,
		"frame_resume_dispatch_cancelled":   0,
	})
}

func TestPlanApprovalCheckpointOnlyGatesFirstDispatchAttempt(t *testing.T) {
	dispatch := workspace.CompatibilityFrameResumeDispatch{
		Attempt: 1,
		ResumeEvent: workspace.FrameEvent{Payload: map[string]any{
			"reason": "plan_approved",
		}},
	}
	if !frameResumeUsesPlanApprovalCheckpoint(dispatch) {
		t.Fatal("first plan-approved dispatch must validate the approved waiting checkpoint")
	}
	dispatch.Attempt = 2
	if frameResumeUsesPlanApprovalCheckpoint(dispatch) {
		t.Fatal("an interrupted plan-approved dispatch must resume from the latest durable checkpoint")
	}
}

func TestFrameResumeDispatchBacksOffExhaustedToolRoundNoProgressWithoutFailingTask(t *testing.T) {
	todoArgs := `{"todos":[{"content":"search PubMed","activeForm":"Searching PubMed","status":"completed"}]}`
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{
				"role": "assistant", "content": "",
				"tool_calls": []any{map[string]any{
					"id": fmt.Sprintf("call_loop_%d", calls.Add(1)), "type": "function",
					"function": map[string]any{"name": "TodoWrite", "arguments": todoArgs},
				}},
			},
		}}})
	}))
	defer provider.Close()

	root := t.TempDir()
	store, srv := newFreshFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), provider.URL, "loop-root", "complete the analysis")
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/loop-root/resume", "local", map[string]any{}, http.StatusOK)
	options := FrameResumeDispatchOptions{
		WorkerID: "loop-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{
			Endpoint: BuiltinSessionRunnerChatEndpoint, Model: BuiltinSessionRunnerChatModel,
			LeaseTTL: time.Minute, ReplayLimit: 100, OutputLimitBytes: 64 * 1024,
			RequestTimeout: time.Minute, MaxAttempts: 1,
			AllowedTools:          []string{"TodoWrite"},
			DisableSkillDiscovery: true,
		},
	}
	for attempt := 1; attempt <= 2; attempt++ {
		result, err := srv.RunFrameResumeDispatchOnce(context.Background(), options)
		if err != nil || !result.Claimed || result.Status != "interrupted" ||
			result.Runner.InterruptionReasonCode != sessionRunnerToolRoundNoProgressReasonCode {
			terminal := "nil"
			if result.TerminalEvent != nil {
				raw, _ := json.Marshal(result.TerminalEvent.Payload)
				terminal = string(raw)
			}
			t.Fatalf("interrupted dispatch attempt %d result=%#v terminal=%s err=%v", attempt, result, terminal, err)
		}
	}
	exhausted, err := srv.RunFrameResumeDispatchOnce(context.Background(), options)
	if err != nil || !exhausted.Claimed || exhausted.Status != "interrupted" ||
		exhausted.Runner.InterruptionReasonCode != sessionRunnerToolRoundNoProgressExhaustedReasonCode ||
		!exhausted.Runner.InterruptionAutoResume {
		t.Fatalf("exhausted route did not remain resumable: result=%#v err=%v", exhausted, err)
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("loop-root")
	if err != nil || !found || dispatch.Status != "registered" || dispatch.NotBefore.IsZero() {
		t.Fatalf("backed-off dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	next, err := srv.RunFrameResumeDispatchOnce(context.Background(), options)
	if err != nil || next.Claimed {
		t.Fatalf("backed-off dispatch was hot-reclaimed=%#v err=%v", next, err)
	}
	assertFrameResumeDispatchEvents(t, store, "loop-root", map[string]int{
		"frame_resume_dispatch_claimed":     3,
		"frame_resume_dispatch_recovered":   2,
		"frame_resume_dispatch_interrupted": 3,
		"frame_resume_dispatch_failed":      0,
		"frame_resume_dispatch_blocked":     0,
		"frame_resume_dispatch_woken":       0,
	})
}

func TestFrameResumeDispatchReconcilesTerminalFrame(t *testing.T) {
	var providerRequests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerRequests.Add(1)
		t.Error("provider must not run for a terminal frame")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()

	root := t.TempDir()
	store, srv := newFreshFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), provider.URL, "terminal-root", "reconcile terminal frame")
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/terminal-root/resume", "local", map[string]any{}, http.StatusOK)
	// Simulate the real stale-dispatch shape: a prior worker claimed the
	// dispatch and its lease expired after the frame completed.
	first, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("stale-worker", time.Millisecond)
	if err != nil || !claimed || first.Attempt != 1 {
		t.Fatalf("stale claim=%#v claimed=%t err=%v", first, claimed, err)
	}
	time.Sleep(10 * time.Millisecond)
	completed := workspace.FrameStatusCompleted
	if _, err := store.UpdateFrame("terminal-root", workspace.UpdateFrameInput{Status: &completed}); err != nil {
		t.Fatal(err)
	}
	options := FrameResumeDispatchOptions{
		WorkerID: "terminal-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{Endpoint: "http://127.0.0.1:1", Model: "test-model"},
	}
	result, err := srv.RunFrameResumeDispatchOnce(context.Background(), options)
	if err != nil || !result.Claimed || result.Status != "completed" {
		t.Fatalf("reconciled dispatch=%#v err=%v", result, err)
	}
	if providerRequests.Load() != 0 {
		t.Fatalf("terminal frame provider requests=%d", providerRequests.Load())
	}
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("terminal-root")
	if err != nil || !found || dispatch.Status != "completed" {
		t.Fatalf("completed dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
}

func TestAutoResumeScanNeverRevivesTerminalDispatch(t *testing.T) {
	root := t.TempDir()
	store, srv := newFreshFrameResumeDispatchFixture(
		t, root, filepath.Join(root, "workspace.db"), "http://127.0.0.1:1",
		"eligible-blocked-root", "repair the malformed artifact link",
	)
	processing := workspace.FrameStatusProcessing
	if _, err := store.UpdateFrame("eligible-blocked-root", workspace.UpdateFrameInput{Status: &processing}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: "frame:eligible-blocked-root", OwnerID: "local", RunnerID: "blocked-correction-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	currentClaim := claim.Claim
	for bounce := 1; bounce <= 12; bounce++ {
		interrupted, interruptErr := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
			Claim: currentClaim, ClientMessageID: fmt.Sprintf("blocked-correction-interruption-%d", bounce),
			ReasonCode:   "artifact_reference_correction_required",
			ResumeDetail: "malformed artifact references malformed_placeholder@byte_1280",
			AutoResume:   true, Destinations: []string{"ws"},
		})
		if interruptErr != nil || !interrupted.Created {
			t.Fatalf("interrupted bounce %d=%#v err=%v", bounce, interrupted, interruptErr)
		}
		if bounce == 12 {
			break
		}
		next, claimErr := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
			StreamUID: currentClaim.StreamUID, OwnerID: currentClaim.OwnerID,
			RunnerID: fmt.Sprintf("blocked-correction-runner-%d", bounce+1), TTL: time.Minute,
			ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: interrupted.Checkpoint.Sequence,
		})
		if claimErr != nil || !next.Claimed {
			t.Fatalf("reclaim bounce %d=%#v err=%v", bounce, next, claimErr)
		}
		currentClaim = next.Claim
	}
	frame, found, err := store.GetFrame("eligible-blocked-root")
	if err != nil || !found {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	resume, err := store.CreateAutoResumeDispatch(
		frame.ID, frame.RootFrameID, frame.ProjectID, frame.AgentName,
		"artifact_reference_correction_required",
	)
	if err != nil || resume.Event == nil {
		t.Fatalf("resume=%#v err=%v", resume, err)
	}
	dispatch, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("obsolete-budget-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("dispatch=%#v claimed=%t err=%v", dispatch, claimed, err)
	}
	if _, resumed, err := store.FailCompatibilityFrameResumeDispatch(
		dispatch.ResumeEvent.ID, dispatch.Attempt, dispatch.ClaimToken,
		"artifact_reference_correction_required",
	); err != nil || resumed {
		t.Fatalf("blocked dispatch resumed=%t err=%v", resumed, err)
	}

	srv.autoResumeInterruptedFrames(context.Background())
	dispatch, found, err = store.GetCompatibilityFrameResumeDispatch(resume.Event.ID)
	if err != nil || !found || dispatch.Status != "failed" {
		t.Fatalf("terminal dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	assertFrameResumeDispatchEvents(t, store, frame.ID, map[string]int{
		"frame_resume_dispatch_failed": 1,
		"frame_resume_dispatch_woken":  0,
	})
}

func TestFrameResumeDispatchAutoResumePolicyBacksOffKernelRecoveryWithoutCountingApprovalWakeups(t *testing.T) {
	now := time.Date(2026, time.August, 14, 3, 30, 0, 0, time.UTC)
	notBefore := frameResumeDispatchAutoResumePolicy(
		sessionRunnerKernelOperationPendingRecoveryReasonCode, 1, "frame-kernel", now,
	)
	if !notBefore.Equal(now.Add(frameResumeKernelRecoveryRecheckInterval)) {
		t.Fatalf("kernel recovery policy notBefore=%s", notBefore)
	}
	if delay := notBefore.Sub(now); delay < 15*time.Second || delay > frameResumeKernelRecoveryRecheckInterval {
		t.Fatalf("kernel recovery backstop delay=%s; want a short lost-wake fence", delay)
	}

	if notBefore = frameResumeDispatchAutoResumePolicy(
		sessionRunnerKernelOperationPendingRecoveryReasonCode, 100, "frame-kernel", now,
	); !notBefore.Equal(now.Add(frameResumeKernelRecoveryRecheckInterval)) {
		t.Fatalf("kernel recovery after approval wakeups notBefore=%s", notBefore)
	}
	if notBefore = frameResumeDispatchAutoResumePolicy(
		sessionRunnerToolRoundLimitReasonCode, 1000, "frame-progress", now,
	); !notBefore.IsZero() {
		t.Fatalf("progressing tool-round boundary notBefore=%s", notBefore)
	}
	if notBefore = frameResumeDispatchAutoResumePolicy("artifact_reference_correction_required", 1000, "frame-artifact", now); notBefore.Sub(now) < 20*time.Second || notBefore.Sub(now) > 22*time.Second {
		t.Fatalf("recoverable correction backoff=%s", notBefore.Sub(now))
	}
	if notBefore = frameResumeDispatchAutoResumePolicy("artifact_reference_correction_required", 1, "frame-artifact", now); notBefore.Sub(now) < 2*time.Second || notBefore.Sub(now) > 3*time.Second {
		t.Fatalf("ordinary recovery backoff=%s", notBefore.Sub(now))
	}
	if notBefore = frameResumeDispatchAutoResumePolicy(sessionRunnerToolFailedReasonCode, 1000, "frame-tool", now); !notBefore.IsZero() {
		t.Fatalf("recoverable tool failure received a fixed continuation ceiling: notBefore=%s", notBefore)
	}
	notBefore = frameResumeDispatchAutoResumePolicy("provider_stream_no_progress", 1000, "frame-provider", now)
	if delay := notBefore.Sub(now); delay < 20*time.Second || delay > 22*time.Second {
		t.Fatalf("provider no-progress backoff=%s", delay)
	}
}

func TestFrameResumeDispatchRequeueFencesEveryOldWorkerMutation(t *testing.T) {
	root := t.TempDir()
	store, srv := newFrameResumeDispatchFixture(t, root, filepath.Join(root, "workspace.db"), "http://127.0.0.1:1", "fenced-root", "fence old dispatcher generations")
	compatJSONRequest(t, srv.Handler(), http.MethodPost, "/api/frames/fenced-root/resume", "local", map[string]any{}, http.StatusOK)
	first, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-one", time.Second)
	if err != nil || !claimed {
		t.Fatalf("first claim=%#v claimed=%t err=%v", first, claimed, err)
	}
	interruptedEvent, replayed, err := store.RequeueCompatibilityFrameResumeDispatch(workspace.RequeueCompatibilityFrameResumeDispatchInput{
		ResumeEventID: first.ResumeEvent.ID, ExpectedAttempt: first.Attempt, ClaimToken: first.ClaimToken,
		ReasonCode: "provider_stream_interrupted", RunnerAttempt: 2, CheckpointEventID: 41,
	})
	if err != nil || replayed || interruptedEvent.Type != "frame_resume_dispatch_interrupted" {
		t.Fatalf("requeue event=%#v replayed=%t err=%v", interruptedEvent, replayed, err)
	}
	if replay, duplicate, replayErr := store.RequeueCompatibilityFrameResumeDispatch(workspace.RequeueCompatibilityFrameResumeDispatchInput{
		ResumeEventID: first.ResumeEvent.ID, ExpectedAttempt: first.Attempt, ClaimToken: first.ClaimToken,
		ReasonCode: "provider_stream_interrupted", RunnerAttempt: 2, CheckpointEventID: 41,
	}); replayErr != nil || !duplicate || replay.ID != interruptedEvent.ID {
		t.Fatalf("idempotent requeue event=%#v replayed=%t err=%v", replay, duplicate, replayErr)
	}
	second, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-two", time.Second)
	if err != nil || !claimed || second.Attempt != first.Attempt+1 || second.ClaimToken == first.ClaimToken {
		t.Fatalf("second claim=%#v claimed=%t err=%v", second, claimed, err)
	}
	if active, renewErr := store.RenewCompatibilityFrameResumeDispatch(first.ResumeEvent.ID, first.Attempt, first.ClaimToken, time.Second); renewErr != nil || active {
		t.Fatalf("stale renew active=%t err=%v", active, renewErr)
	}
	if _, _, requeueErr := store.RequeueCompatibilityFrameResumeDispatch(workspace.RequeueCompatibilityFrameResumeDispatchInput{
		ResumeEventID: first.ResumeEvent.ID, ExpectedAttempt: first.Attempt, ClaimToken: first.ClaimToken,
		ReasonCode: "provider_stream_interrupted", RunnerAttempt: 2, CheckpointEventID: 41,
	}); requeueErr == nil {
		t.Fatal("stale worker requeue unexpectedly succeeded")
	}
	if _, _, blockErr := store.FailCompatibilityFrameResumeDispatch(first.ResumeEvent.ID, first.Attempt, first.ClaimToken, "stale_worker"); blockErr == nil {
		t.Fatal("stale worker block unexpectedly succeeded")
	}
	if _, _, completeErr := store.CompleteCompatibilityFrameResumeDispatch(workspace.CompleteCompatibilityFrameResumeDispatchInput{
		ResumeEventID: first.ResumeEvent.ID, ExpectedAttempt: first.Attempt, ClaimToken: first.ClaimToken, Status: "failed",
	}); completeErr == nil {
		t.Fatal("stale worker completion unexpectedly succeeded")
	}
	current, found, err := store.GetCompatibilityFrameResumeDispatch(first.ResumeEvent.ID)
	if err != nil || !found || current.Status != "claimed" || current.Attempt != second.Attempt || current.ClaimToken != second.ClaimToken {
		t.Fatalf("new worker authority changed=%#v found=%t err=%v", current, found, err)
	}
}

func TestExplicitResumeWakesFencedRecoveryDispatch(t *testing.T) {
	root := t.TempDir()
	store, srv := newFreshFrameResumeDispatchFixture(
		t, root, filepath.Join(root, "workspace.db"), "http://127.0.0.1:1",
		"fenced-manual-resume-root", "continue after the completed background operation",
	)
	app := srv.Handler()
	compatJSONRequest(t, app, http.MethodPost, "/api/frames/fenced-manual-resume-root/resume", "local", map[string]any{}, http.StatusOK)

	claim, claimed, err := store.ClaimNextCompatibilityFrameResumeDispatch("interrupted-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("initial dispatch claim=%#v claimed=%t err=%v", claim, claimed, err)
	}
	if _, _, err := store.RequeueCompatibilityFrameResumeDispatch(workspace.RequeueCompatibilityFrameResumeDispatchInput{
		ResumeEventID: claim.ResumeEvent.ID, ExpectedAttempt: claim.Attempt, ClaimToken: claim.ClaimToken,
		ReasonCode:    sessionRunnerKernelOperationPendingRecoveryReasonCode,
		RunnerAttempt: 2, CheckpointEventID: 41, NotBefore: time.Now().UTC().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	compatJSONRequest(t, app, http.MethodPost, "/api/frames/fenced-manual-resume-root/resume", "local", map[string]any{}, http.StatusOK)
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("fenced-manual-resume-root")
	if err != nil || !found || dispatch.Status != "registered" || !dispatch.NotBefore.IsZero() {
		t.Fatalf("woken dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	assertFrameResumeDispatchEvents(t, store, "fenced-manual-resume-root", map[string]int{
		"frame_resume_dispatch_woken": 1,
	})
}

func newFrameResumeDispatchFixture(t *testing.T, root, databasePath, providerURL, frameID, userText string) (*workspace.Store, *Server) {
	t.Helper()
	return newFrameResumeDispatchFixtureWithSeed(t, root, databasePath, providerURL, frameID, userText, true)
}

func newFreshFrameResumeDispatchFixture(t *testing.T, root, databasePath, providerURL, frameID, userText string) (*workspace.Store, *Server) {
	t.Helper()
	return newFrameResumeDispatchFixtureWithSeed(t, root, databasePath, providerURL, frameID, userText, false)
}

func newFrameResumeDispatchFixtureWithSeed(
	t *testing.T,
	root, databasePath, providerURL, frameID, userText string,
	seedCancelledAttempt bool,
) (*workspace.Store, *Server) {
	t.Helper()
	store, err := workspace.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "resume-project", UserID: "local", Name: "Resume project", Path: root,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: frameID, ProjectID: "resume-project", AgentName: "OPERON",
		Status: "processing", ConversationType: "agent",
	}); err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:" + frameID, OwnerID: "local", ExternalID: frameID, SessionID: frameID,
		Kind: transcriptstore.StreamKindFrameRef, ProjectID: "resume-project", RootFrameID: frameID, FrameID: frameID, Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: "local", ClientMessageID: frameID + "-client",
		FrameEventID: frameID + "-user-message", MessageUUID: frameID + "-message", Text: userText,
		MessageOrigin: "task_intent", Destinations: []string{"ws"},
	}); err != nil {
		t.Fatal(err)
	}
	if !seedCancelledAttempt {
		cancelled := "cancelled"
		if _, err := store.UpdateFrame(frameID, workspace.UpdateFrameInput{Status: &cancelled}); err != nil {
			t.Fatal(err)
		}
	}
	if seedCancelledAttempt {
		claim, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
			StreamUID: stream.UID, OwnerID: "local", RunnerID: "seed-cancelled-runner", TTL: time.Minute,
			ResumeSource: transcriptstore.ResumeSourceFresh,
		})
		if err != nil || !claim.Claimed {
			t.Fatalf("seed claim=%#v err=%v", claim, err)
		}
		if _, _, created, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
			Claim: claim.Claim, ClientMessageID: "seed-cancelled-terminal", Status: "cancelled",
			PayloadJSON: []byte(`{"status":"cancelled","detail":"seed resumable state"}`), Destinations: []string{"ws"},
		}); err != nil || !created {
			t.Fatalf("seed terminal created=%t err=%v", created, err)
		}
	}
	srv := newV11TestServer(t, Options{FileRoot: root, Workspace: store, Transcript: repo})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Close(ctx); err != nil {
			t.Errorf("close frame resume server: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Errorf("close frame resume store: %v", err)
		}
	})
	if _, err := srv.settingsStore.Set("model.activeProviderId", "resume-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.secretStore.Create(secretstore.Secret{
		ID: "resume-provider-key", UserID: "local", Provider: "openai", Value: "resume-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "resume-provider", UserID: "local", Name: "Resume provider", Type: "openai",
		BaseURL: providerURL + "/v1", Model: "resume-model",
		SecretRef: "secret://resume-provider-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	return store, srv
}

func assertFrameResumeDispatchEvents(t *testing.T, store *workspace.Store, frameID string, want map[string]int) {
	t.Helper()
	events, err := store.ListFrameEvents(frameID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, event := range events {
		counts[event.Type]++
	}
	for eventType, count := range want {
		if counts[eventType] != count {
			t.Fatalf("frame %s event %s count=%d want=%d events=%#v", frameID, eventType, counts[eventType], count, events)
		}
	}
}
