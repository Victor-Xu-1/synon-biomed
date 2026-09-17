package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRunnerTaskMemorySnapshotClientMessageIDIsContentIdempotent(t *testing.T) {
	first := transcriptstore.RunnerTaskMemorySnapshot{SHA256: "0123456789abcdef000000000000000000000000000000000000000000000000"}
	second := transcriptstore.RunnerTaskMemorySnapshot{SHA256: "fedcba987654321000000000000000000000000000000000000000000000000"}
	if got := runnerTaskMemorySnapshotClientMessageID(first); got == runnerTaskMemorySnapshotClientMessageID(second) {
		t.Fatalf("different snapshot content reused client message id: %q", got)
	}
	if got := runnerTaskMemorySnapshotClientMessageID(first); got != "runner-task-memory-snapshot-v1-0123456789abcdef" {
		t.Fatalf("unexpected content-derived client message id: %q", got)
	}
}

func TestTranscriptFrameRegistrationDrainWithoutAttemptRemainsRecoverable(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-registration-drain", "frame-registration-drain")
	if _, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "frame:frame-registration-drain", OwnerID: "local", ExternalID: "frame-registration-drain",
		SessionID: "frame-registration-drain", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "project-registration-drain", RootFrameID: "frame-registration-drain",
		FrameID: "frame-registration-drain", Epoch: 1,
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})

	result := SessionRunnerCycleResult{
		RunnerID:  "frame-resume:registration-drain",
		SessionID: "frame-registration-drain",
	}
	err := server.settleRunnerRegistrationDuringDrain(
		SessionRunnerChatOptions{RunnerID: result.RunnerID},
		&result,
		true,
		sessionstore.RunnerMutationClaim{},
		nil,
	)
	if err != nil || result.Claimed || result.Attempt != 0 || result.Status != "interrupted" {
		t.Fatalf("registration drain result=%#v err=%v", result, err)
	}
	entries, err := server.eventJournal.ReadAll(result.SessionID)
	if err != nil || len(entries) != 0 {
		t.Fatalf("legacy journal was mutated without a claim: entries=%#v err=%v", entries, err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", result.SessionID)
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	if state, found, err := repo.GetLatestRunnerRuntimeState(context.Background(), stream.UID, "local"); err != nil || found {
		t.Fatalf("runner attempt was created during registration drain: state=%#v found=%t err=%v", state, found, err)
	}
}

func TestFrameResumeDispatchContinuesFromTranscriptCheckpoint(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-resume", "frame-resume-transcript")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].Role != "user" ||
			request.Messages[len(request.Messages)-1].Content != "complete the interrupted evidence review" {
			t.Fatalf("provider messages=%#v", request.Messages)
		}
		if requestNumber == 1 {
			http.Error(w, "provider interrupted", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"resumed from durable checkpoint"}}]}`))
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-resume-transcript", MessageUUID: "message-resume", ClientMessageID: "client-resume",
		Text: "complete the interrupted evidence review",
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: "frame-resume-transcript", RunnerID: "runner-interrupted", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, MaxAttempts: 1,
		DisableSkillDiscovery: true,
	})
	if err != nil || failed.Status != "failed" || requests.Load() != 1 {
		t.Fatalf("failed=%#v requests=%d err=%v", failed, requests.Load(), err)
	}
	compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-resume-transcript/resume", "local", map[string]any{}, http.StatusOK)
	if session, found, err := server.sessionStore.Get("frame-resume-transcript"); err != nil || found {
		t.Fatalf("legacy resume session=%#v found=%t err=%v", session, found, err)
	}
	if entries, err := server.eventJournal.ReadAll("frame-resume-transcript"); err != nil || len(entries) != 0 {
		t.Fatalf("legacy resume journal=%#v err=%v", entries, err)
	}
	resumed, err := server.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "transcript-resume-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
			LeaseTTL: time.Minute, MaxAttempts: 1, DisableSkillDiscovery: true,
		},
	})
	if err != nil || !resumed.Claimed || resumed.Status != "completed" || requests.Load() != 2 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-resume-transcript")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, "local", 2)
	if err != nil || state.Status != "completed" || state.ResumeSource != transcriptstore.ResumeSourceCheckpoint ||
		state.ResumeCheckpoint <= 0 {
		t.Fatalf("runtime=%#v err=%v", state, err)
	}
	stream, err = repo.GetStream(context.Background(), stream.UID, "local")
	if err != nil {
		t.Fatal(err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminals := 0
	for _, event := range events {
		if event.Event.Type == "runner_finished" {
			terminals++
		}
	}
	if terminals != 2 {
		t.Fatalf("terminal events=%d events=%#v", terminals, events)
	}
	if session, found, err := server.sessionStore.Get("frame-resume-transcript"); err != nil || found {
		t.Fatalf("legacy completed session=%#v found=%t err=%v", session, found, err)
	}
	if entries, err := server.eventJournal.ReadAll("frame-resume-transcript"); err != nil || len(entries) != 0 {
		t.Fatalf("legacy completed journal=%#v err=%v", entries, err)
	}
}

func TestFrameRunnerDoesNotAutoReviewWithoutUserVerifier(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-reference-resume", "frame-reference-resume")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		content := "The selected structure is PDB 7ACK."
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}]}`, content)
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-reference-resume", MessageUUID: "message-reference-resume",
		ClientMessageID: "client-reference-resume", Text: "complete the evidence-backed task",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: "frame-reference-resume", RunnerID: "runner-reference-failed",
		Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
		LeaseTTL: time.Minute, MaxAttempts: 1, MaxToolRounds: 0,
		DisableSkillDiscovery: true,
	})
	if err != nil || result.Status != "completed" || requests.Load() != 1 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
}

func TestTranscriptRunnerContextPressureInterruptsCompactsAndResumes(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-context-pressure-resume", "frame-context-pressure-resume")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		if requestNumber == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"InvalidParameter","message":"Total tokens of image and text exceed max message tokens."}}`))
			return
		}
		var compactContext string
		for _, message := range request.Messages {
			if message.Role == "system" && strings.Contains(message.Content, "Synon compact handoff context:") {
				compactContext = message.Content
			}
		}
		if !strings.Contains(compactContext, "provider rejected the prior execution unit for context pressure") ||
			!strings.Contains(compactContext, "continue the biomedical task") {
			t.Fatalf("resumed request missing context-pressure compact handoff: %#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"completed after durable context recovery"}}]}`))
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-context-pressure-resume", MessageUUID: "message-context-pressure-resume",
		ClientMessageID: "client-context-pressure-resume",
		Text:            "continue the biomedical task " + strings.Repeat("evidence context ", 200),
	}); err != nil {
		t.Fatal(err)
	}
	options := SessionRunnerChatOptions{SessionID: "frame-context-pressure-resume", RunnerID: "context-pressure-runner-1",
		Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
		LeaseTTL: time.Minute, MaxAttempts: 1, MaxToolRounds: 0, ReplayLimit: 100,
		DisableSkillDiscovery: true,
	}
	interrupted, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || interrupted.Status != "interrupted" ||
		interrupted.InterruptionReasonCode != sessionRunnerProviderContextPressureReasonCode ||
		interrupted.FinishEventID != 0 || requests.Load() != 1 {
		t.Fatalf("interrupted=%#v requests=%d err=%v", interrupted, requests.Load(), err)
	}

	options.SessionID = ""
	options.RunnerID = "context-pressure-runner-2"
	resumed, err := server.RunSessionRunnerChatOnce(context.Background(), options)
	if err != nil || !resumed.Claimed || resumed.Status != "completed" || requests.Load() != 2 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}

	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-context-pressure-resume")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextInterruptions, compactCheckpoints, terminals := 0, 0, 0
	for _, projected := range events {
		switch projected.Event.Type {
		case "runner_checkpoint":
			var payload map[string]any
			if err := json.Unmarshal(projected.ResolvedPayloadJSON, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["reason_code"] == sessionRunnerProviderContextPressureReasonCode {
				contextInterruptions++
			}
			if payload["toolPhase"] == "auto_compact" && payload["reason"] == sessionRunnerProviderContextPressureReasonCode {
				compactCheckpoints++
			}
		case "runner_finished":
			terminals++
		}
	}
	if contextInterruptions != 1 || compactCheckpoints != 1 || terminals != 1 {
		t.Fatalf("context interruptions=%d compact checkpoints=%d terminals=%d events=%#v",
			contextInterruptions, compactCheckpoints, terminals, events)
	}
}

func TestGracefulRuntimeDrainResumesTranscriptRunnerWithoutBusinessCancellation(t *testing.T) {
	testRunnerInfrastructureResume(t, false)
}

func TestSupervisorInterruptionResumesSameTranscriptAttempt(t *testing.T) {
	testRunnerInfrastructureResume(t, true)
}

func testRunnerInfrastructureResume(t *testing.T, supervisor bool) {
	t.Helper()
	runCtx, cancelRun := context.WithCancelCause(context.Background())
	defer cancelRun(nil)
	reason := "runtime_draining"
	if supervisor {
		reason = sessionRunnerSupervisorInterruptedReasonCode
	}
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-runtime-drain", "frame-runtime-drain")
	requestStarted := make(chan struct{}, 1)
	releaseFirstRequest := make(chan struct{})
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			requestStarted <- struct{}{}
			select {
			case <-r.Context().Done():
			case <-releaseFirstRequest:
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"resumed after runtime drain"}}]}`))
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-runtime-drain", MessageUUID: "message-runtime-drain", ClientMessageID: "client-runtime-drain",
		Text: "keep this task resumable across a clean restart",
	}); err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan SessionRunnerCycleResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := server.RunSessionRunnerChatOnce(runCtx, SessionRunnerChatOptions{SessionID: "frame-runtime-drain", RunnerID: "runner-before-drain", Endpoint: provider.URL,
			APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, MaxAttempts: 1,
			DisableSkillDiscovery: true,
		})
		resultCh <- result
		errCh <- err
	}()
	select {
	case <-requestStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("provider request did not start")
	}
	if supervisor {
		cancelRun(newSessionRunnerInfrastructureInterruption(reason, errors.New("injected sibling infrastructure failure")))
	} else {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := server.Drain(drainCtx); err != nil {
			drainCancel()
			t.Fatalf("drain: %v", err)
		}
		drainCancel()
	}
	first := <-resultCh
	close(releaseFirstRequest)
	if err := <-errCh; err != nil || first.Status != "interrupted" || first.FinishEventID != 0 {
		t.Fatalf("interrupted result=%#v err=%v", first, err)
	}
	frame, found, err := store.GetFrame("frame-runtime-drain")
	if err != nil || !found || frame.Status != "processing" {
		t.Fatalf("frame after drain=%#v found=%t err=%v", frame, found, err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-runtime-drain")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	firstState, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, "local", 1)
	if err != nil || firstState.Status != "running" || firstState.FinishedEventID != 0 ||
		firstState.LastCheckpointSequence <= 0 || firstState.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("first runtime state=%#v err=%v", firstState, err)
	}
	if !supervisor {
		if oldRuntime, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "old-runtime-must-not-reclaim", Endpoint: provider.URL, Model: "test-model"}); err != nil || oldRuntime.Claimed || requests.Load() != 1 {
			t.Fatalf("drained runtime reclaimed work: result=%#v requests=%d err=%v", oldRuntime, requests.Load(), err)
		}
	}

	restarted := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = restarted.Close(ctx)
	})
	resumed, err := restarted.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "runner-after-drain", Endpoint: provider.URL, APIKey: "test-key", Model: "test-model",
		LeaseTTL: time.Minute, MaxAttempts: 1, DisableSkillDiscovery: true,
	})
	if err != nil || !resumed.Claimed || resumed.Attempt != 1 || resumed.Status != "completed" || requests.Load() != 2 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: 1000, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminals, interruptions := 0, 0
	for _, projected := range events {
		switch projected.Event.Type {
		case "runner_finished":
			terminals++
		case "runner_checkpoint":
			payload := map[string]any{}
			if err := json.Unmarshal(projected.ResolvedPayloadJSON, &payload); err != nil {
				t.Fatal(err)
			}
			if payload["reason_code"] == reason && payload["status"] == "interrupted" {
				interruptions++
			}
		}
	}
	if terminals != 1 || interruptions != 1 {
		t.Fatalf("terminal events=%d interruptions=%d events=%#v", terminals, interruptions, events)
	}
}

func TestRuntimeDrainCheckpointResumeRestoresAssistantSegmentAfterToolBoundary(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-segment-resume", "frame-segment-resume")
	if _, _, err := (&Server{workspaceStore: store, transcriptStore: repo}).submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-segment-resume", MessageUUID: "message-segment-resume", ClientMessageID: "client-segment-resume",
		Text: "resume the same attempt without reusing an assistant segment identity",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-segment-resume")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-before-segment-drain",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "segment-before-first-tool", "content_delta", map[string]any{
		"text": "first", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "segment-first-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-first", "toolName": "Read",
		"toolInput": map[string]any{"path": "first.txt"},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "segment-first-tool-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-first", "toolName": "Read",
		"toolInput": map[string]any{"path": "first.txt"}, "toolResult": map[string]any{"content": "first"},
	})
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "segment-before-drain", "content_delta", map[string]any{
		"text": "second", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "segment-final-tool-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "call-final", "toolName": "Read",
		"toolInput": map[string]any{"path": "final.txt"},
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "segment-final-tool-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "call-final", "toolName": "Read",
		"toolInput": map[string]any{"path": "final.txt"}, "toolResult": map[string]any{"content": "final"},
	})
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "segment-runtime-draining", ReasonCode: "runtime_draining",
		AutoResume: true,
	})
	if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-after-segment-drain",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(reclaimed.Claim.Attempt),
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: reclaimed.Claim},
	}
	server := &Server{transcriptStore: repo}
	if err := server.restoreTranscriptAssistantSegmentState(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if run.AssistantSegmentOrdinal != 3 || run.AssistantSegmentHasContent {
		t.Fatalf("restored assistant segment ordinal=%d has_content=%t", run.AssistantSegmentOrdinal, run.AssistantSegmentHasContent)
	}
}

func TestApprovalCheckpointResumeRestoresAssistantSegmentAfterToolBoundary(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-approval-segment-resume", "frame-approval-segment-resume")
	if _, _, err := (&Server{workspaceStore: store, transcriptStore: repo}).submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-approval-segment-resume", MessageUUID: "message-approval-segment-resume",
		ClientMessageID: "client-approval-segment-resume",
		Text:            "resume repeated approved tools without reusing an assistant segment identity",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-approval-segment-resume")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-before-approval-segment-resume",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "approval-segment-before", "content_delta", map[string]any{
		"text": "before approval", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	for index, callID := range []string{"approval-segment-one", "approval-segment-two"} {
		appendRunnerToolCheckpoint(t, repo, claimed.Claim, callID+"-waiting", map[string]any{
			"status": "waiting", "toolPhase": "waiting", "toolCallId": callID, "toolName": "software_runtime",
		})
		appendRunnerToolCheckpoint(t, repo, claimed.Claim, callID+"-resume", map[string]any{
			"status": "running", "stage": "resume_state",
			"detail": "restoring the durable assistant continuation state",
		})
		if index == 0 {
			appendRunnerPayloadEvent(t, repo, claimed.Claim, callID+"-content", "content_delta", map[string]any{
				"text": "after first approval", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
			})
		}
	}
	resumeClaim := claimed.Claim
	resumeClaim.ResumeSource = transcriptstore.ResumeSourceCheckpoint
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(resumeClaim.Attempt),
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: resumeClaim},
	}
	if err := (&Server{transcriptStore: repo}).restoreTranscriptAssistantSegmentState(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if run.AssistantSegmentOrdinal != 3 || run.AssistantSegmentHasContent {
		t.Fatalf("restored approval segment ordinal=%d has_content=%t", run.AssistantSegmentOrdinal, run.AssistantSegmentHasContent)
	}
}

func TestNewAttemptResumeIgnoresPriorAttemptAssistantSegmentState(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-attempt-segment-isolation", "frame-attempt-segment-isolation")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-attempt-segment-isolation", MessageUUID: "message-attempt-segment-isolation",
		ClientMessageID: "client-attempt-segment-isolation", Text: "exercise resume attempt isolation",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-attempt-segment-isolation")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	first, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-attempt-one",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	_, _, _, err = repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: first.Claim, ClientMessageID: "attempt-one-checkpoint", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"status":"running"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// A historical attempt may contain a segment shape that its own terminal
	// projection already rejected. A later attempt must not feed that foreign
	// state through the current attempt's segment normalizer.
	appendRunnerPayloadEvent(t, repo, first.Claim, "attempt-one-invalid-segment", "content_delta", map[string]any{
		"text": "old partial output", "assistant_segment": map[string]any{"version": 1, "ordinal": 0},
	})
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "attempt-one-finished", Status: "failed",
		PayloadJSON: []byte(`{"status":"failed","detail":"old attempt failed"}`),
	}); err != nil {
		t.Fatal(err)
	}
	secondClaim := transcriptstore.RunnerClaim{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-attempt-two",
		Attempt: 2, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpointAttempt: 1,
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(secondClaim.Attempt),
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: secondClaim},
	}
	if err := server.restoreTranscriptAssistantSegmentState(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if run.AssistantSegmentOrdinal != 0 || run.AssistantSegmentHasContent {
		t.Fatalf("foreign attempt contaminated restored state: %#v", run)
	}
}

func TestAskUserCheckpointResumeRestoresAssistantSegmentAfterToolBoundary(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-ask-segment-resume", "frame-ask-segment-resume")
	if _, _, err := (&Server{workspaceStore: store, transcriptStore: repo}).submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-ask-segment-resume", MessageUUID: "message-ask-segment-resume", ClientMessageID: "client-ask-segment-resume",
		Text: "resume after an AskUser clarification without reusing the prior assistant segment",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-ask-segment-resume")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-before-ask-segment-resume",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "ask-segment-before", "content_delta", map[string]any{
		"text": "before clarification", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "ask-segment-start", map[string]any{
		"status": "running", "toolPhase": "start", "toolCallId": "ask-segment", "toolName": "ask_user",
	})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "ask-segment-complete", map[string]any{
		"status": "completed", "toolPhase": "completed", "toolCallId": "ask-segment", "toolName": "ask_user",
		"toolResult": map[string]any{"status": "answered"},
	})
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "ask-segment-resume-checkpoint", ReasonCode: "runtime_draining", AutoResume: true,
	})
	if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable {
		t.Fatalf("interrupted=%#v err=%v", interrupted, err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-after-ask-segment-resume",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	run := &sessionRunnerChatRun{
		SessionID: stream.SessionID, Attempt: int(reclaimed.Claim.Attempt),
		Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: reclaimed.Claim},
	}
	server := &Server{transcriptStore: repo}
	if err := server.restoreTranscriptAssistantSegmentState(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if run.AssistantSegmentOrdinal != 2 || run.AssistantSegmentHasContent {
		t.Fatalf("restored AskUser assistant segment ordinal=%d has_content=%t", run.AssistantSegmentOrdinal, run.AssistantSegmentHasContent)
	}
}

func TestAskUserCheckpointResumeCountsConsecutiveBoundariesWithoutContent(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-ask-segment-chain", "frame-ask-segment-chain")
	if _, _, err := (&Server{workspaceStore: store, transcriptStore: repo}).submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-ask-segment-chain", MessageUUID: "message-ask-segment-chain", ClientMessageID: "client-ask-segment-chain",
		Text: "ask twice without merging the resumed response into an earlier assistant segment",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-ask-segment-chain")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-before-ask-segment-chain",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	appendRunnerPayloadEvent(t, repo, claimed.Claim, "ask-segment-chain-content", "content_delta", map[string]any{
		"text": "before questions", "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})

	for index, callID := range []string{"ask-segment-chain-one", "ask-segment-chain-two"} {
		appendRunnerToolCheckpoint(t, repo, claimed.Claim, callID+"-start", map[string]any{
			"status": "running", "toolPhase": "start", "toolCallId": callID, "toolName": "ask_user",
		})
		appendRunnerToolCheckpoint(t, repo, claimed.Claim, callID+"-complete", map[string]any{
			"status": "completed", "toolPhase": "completed", "toolCallId": callID, "toolName": "ask_user",
			"toolResult": map[string]any{"status": "answered"},
		})
		interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
			Claim: claimed.Claim, ClientMessageID: callID + "-checkpoint", ReasonCode: "runtime_draining", AutoResume: true,
		})
		if err != nil || !interrupted.Created || !interrupted.Checkpoint.Resumable {
			t.Fatalf("interrupt %d=%#v err=%v", index, interrupted, err)
		}
		reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: fmt.Sprintf("runner-after-ask-segment-chain-%d", index),
			TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
			ResumeCheckpoint: interrupted.Checkpoint.Sequence,
		})
		if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != claimed.Claim.Attempt {
			t.Fatalf("reclaim %d=%#v err=%v", index, reclaimed, err)
		}
		claimed = reclaimed
		run := &sessionRunnerChatRun{
			SessionID: stream.SessionID, Attempt: int(reclaimed.Claim.Attempt),
			Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: reclaimed.Claim},
		}
		if err := (&Server{transcriptStore: repo}).restoreTranscriptAssistantSegmentState(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		if want := int64(index + 2); run.AssistantSegmentOrdinal != want || run.AssistantSegmentHasContent {
			t.Fatalf("restore %d ordinal=%d want=%d has_content=%t", index, run.AssistantSegmentOrdinal, want, run.AssistantSegmentHasContent)
		}
	}
}

func TestCorrectionContinuationAdvancesWithoutResettingPublishedSegment(t *testing.T) {
	run := &sessionRunnerChatRun{
		AssistantSegmentOrdinal:    7,
		AssistantSegmentHasContent: true,
	}
	run.AssistantSegmentContent.WriteString("published progress")
	run.beginNextAssistantSegment()
	if run.AssistantSegmentOrdinal != 8 || run.AssistantSegmentHasContent ||
		run.AssistantSegmentContent.Len() != 0 {
		t.Fatalf("correction continuation did not open a fresh immutable segment: %#v", run)
	}
}

func TestFrameResumeRejectsMissingTranscriptBeforeWorkspaceMutation(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	if _, err := store.CreateProject(workspace.CreateProjectInput{
		ID: "project-missing-stream", UserID: "local", Name: "Missing stream",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateFrame(workspace.CreateFrameInput{
		ID: "frame-missing-stream", ProjectID: "project-missing-stream", AgentName: "synon",
		Status: "cancelled", ConversationType: "chat",
	}); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	compatJSONRequest(t, server.Handler(), http.MethodPost,
		"/api/frames/frame-missing-stream/resume", "local", map[string]any{}, http.StatusInternalServerError)
	frame, found, err := store.GetFrame("frame-missing-stream")
	if err != nil || !found || frame.Status != "cancelled" {
		t.Fatalf("frame=%#v found=%t err=%v", frame, found, err)
	}
	events, err := store.ListFrameEvents("frame-missing-stream", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "frame_resumed" {
			t.Fatalf("resume event committed without transcript stream: %#v", events)
		}
	}
}

func TestFrameResumeDispatchReconcilesTranscriptTerminalWithoutDuplicateModelCall(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-reconcile", "frame-reconcile")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestNumber := requests.Add(1)
		if requestNumber == 1 {
			http.Error(w, "provider interrupted", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"resume settled before dispatch recovery"}}]}`))
	}))
	defer provider.Close()
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-reconcile", MessageUUID: "message-reconcile", ClientMessageID: "client-reconcile",
		Text: "resume this run exactly once",
	}); err != nil {
		t.Fatal(err)
	}
	failed, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{SessionID: "frame-reconcile", RunnerID: "runner-interrupted", Endpoint: provider.URL + "/v1/chat/completions",
		APIKey: "test-key", Model: "test-model", LeaseTTL: time.Minute, MaxAttempts: 1,
		DisableSkillDiscovery: true,
	})
	if err != nil || failed.Status != "failed" || requests.Load() != 1 {
		t.Fatalf("failed=%#v requests=%d err=%v", failed, requests.Load(), err)
	}
	compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-reconcile/resume", "local", map[string]any{}, http.StatusOK)
	dispatch, found, err := store.GetCompatibilityFrameResumeDispatchByFrame("frame-reconcile")
	if err != nil || !found {
		t.Fatalf("dispatch=%#v found=%t err=%v", dispatch, found, err)
	}
	claimed, ok, err := store.ClaimNextCompatibilityFrameResumeDispatch("worker-before-crash", 10*time.Millisecond)
	if err != nil || !ok || claimed.ResumeEvent.ID != dispatch.ResumeEvent.ID {
		t.Fatalf("claim=%#v ok=%t err=%v", claimed, ok, err)
	}
	chat := SessionRunnerChatOptions{SessionID: "frame-reconcile", RunnerID: frameResumeRunnerID(dispatch.ResumeEvent.ID),
		Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
		LeaseTTL: time.Minute, MaxAttempts: 1, DisableSkillDiscovery: true,
	}
	if err := server.configureTranscriptFrameResume(context.Background(), dispatch, &chat); err != nil {
		t.Fatal(err)
	}
	completed, err := server.RunSessionRunnerChatOnce(context.Background(), chat)
	if err != nil || completed.Status != "completed" || requests.Load() != 2 {
		t.Fatalf("completed=%#v requests=%d err=%v", completed, requests.Load(), err)
	}
	if delay := time.Until(claimed.LeaseExpiresAt.Add(20 * time.Millisecond)); delay > 0 {
		time.Sleep(delay)
	}
	recovered, err := server.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "worker-after-crash", ClaimTTL: time.Second, Chat: chat,
	})
	if err != nil || !recovered.Claimed || !recovered.Recovered || recovered.Status != "completed" {
		t.Fatalf("recovered=%#v err=%v", recovered, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("duplicate model requests=%d", requests.Load())
	}
	if session, found, err := server.sessionStore.Get("frame-reconcile"); err != nil || found {
		t.Fatalf("legacy reconcile session=%#v found=%t err=%v", session, found, err)
	}
}

func TestFrameRunnerPollRecoversExpiredClaimFromDurableCheckpoint(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-restart", "frame-restart")
	submitter := &Server{workspaceStore: store, transcriptStore: repo}
	if _, _, err := submitter.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-restart", MessageUUID: "message-restart", ClientMessageID: "client-restart",
		Text: "continue the durable task after restart",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-restart")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	crashed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "runner-crashed", TTL: time.Minute,
		ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !crashed.Claimed {
		t.Fatalf("claim=%#v err=%v", crashed, err)
	}
	intent, found, err := repo.EnsureActiveFrameTaskIntent(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("task intent=%#v found=%t err=%v", intent, found, err)
	}
	snapshot, err := transcriptstore.SealRunnerTaskMemorySnapshot(transcriptstore.RunnerTaskMemorySnapshot{
		TaskIntentID: intent.ID, TaskIntentRevision: intent.Revision,
		InitialInputRevision: crashed.Claim.ClaimedInputRevision,
		PolicyVersion:        runnerTaskMemoryPolicyVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: crashed.Claim, ClientMessageID: "runner-task-memory-snapshot-v1", Phase: transcriptstore.RunnerPhasePlanning,
		Resumable: true, PayloadJSON: snapshotJSON,
	}); err != nil || !created {
		t.Fatalf("memory snapshot created=%t err=%v", created, err)
	}
	checkpoint, _, created, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: crashed.Claim, ClientMessageID: "checkpoint-before-restart", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"status":"running","detail":"source retrieval completed"}`),
		Destinations: []string{"ws"},
	})
	if err != nil || !created {
		t.Fatalf("checkpoint=%#v created=%t err=%v", checkpoint, created, err)
	}
	if _, err := db.Exec(`UPDATE transcript_runner_attempts SET expires_at=? WHERE stream_uid=? AND attempt=?`,
		time.Now().UTC().Add(-time.Minute), stream.UID, crashed.Claim.Attempt); err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})

	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode provider request: %v", err)
		}
		if len(request.Messages) == 0 || request.Messages[len(request.Messages)-1].Content != "continue the durable task after restart" {
			t.Fatalf("messages=%#v", request.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"recovered exactly once"}}]}`))
	}))
	defer provider.Close()
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{RunnerID: "runner-restarted", Endpoint: provider.URL + "/v1/chat/completions", APIKey: "test-key", Model: "test-model",
		LeaseTTL: time.Minute, MaxAttempts: 1, DisableSkillDiscovery: true,
	})
	if err != nil || !result.Claimed || result.SessionID != "frame-restart" || result.Attempt != 1 ||
		result.Status != "completed" || requests.Load() != 1 {
		t.Fatalf("result=%#v requests=%d err=%v", result, requests.Load(), err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, 1)
	if err != nil || state.ResumeSource != transcriptstore.ResumeSourceCheckpoint || state.ResumeCheckpoint != checkpoint.Sequence {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}
