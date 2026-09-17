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

	secretstore "synon-go/internal/persistence/secrets"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestRecoveredTranscriptCompletionRejectsMalformedArtifactLinkEnvelope(t *testing.T) {
	store, repo, db := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-completion-syntax", "frame-completion-syntax")
	artifact, version, err := store.SaveArtifactVersion(workspace.SaveArtifactVersionInput{
		ArtifactID: "artifact-completion-syntax", ProjectID: "project-completion-syntax", Name: "report.md",
		Kind: "markdown", Content: []byte("verified report"), CreatedBy: "runner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO artifact_runtime_metadata(artifact_id,root_frame_id,frame_id) VALUES(?,?,?)`,
		artifact.ID, "frame-completion-syntax", "frame-completion-syntax",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO artifact_version_provenance(version_id,frame_id,content_type) VALUES(?,?,?)`,
		version.ID, "frame-completion-syntax", "text/markdown",
	); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (&Server{workspaceStore: store, transcriptStore: repo}).submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-completion-syntax", MessageUUID: "message-completion-syntax",
		ClientMessageID: "client-completion-syntax", Text: "Create report.md",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-completion-syntax")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "completion-syntax-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	_, artifactSource, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "save-completion-syntax", Phase: transcriptstore.RunnerPhaseExecuting,
		Resumable: true, PayloadJSON: []byte(`{"toolCallId":"save-completion-syntax","toolName":"save_artifacts","toolPhase":"start"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO transcript_artifact_commits(
		stream_uid,runner_attempt,source_event_id,ordinal,artifact_id,version_id,relation,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, stream.UID, claimed.Claim.Attempt, artifactSource.EventID, 0,
		artifact.ID, version.ID, string(transcriptstore.ArtifactRelationProduced), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	content := "[report.md]({{artifact:" + version.ID + "}}))"
	payload, err := json.Marshal(map[string]any{
		"text": content, "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, refs, _, err := repo.AppendAssistantEventWithCommittedArtifacts(context.Background(), transcriptstore.AppendEventInput{
		Claim: claimed.Claim, ClientMessageID: "assistant-completion-syntax", Type: "assistant_message",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: payload,
	}); err != nil || len(refs) != 1 {
		t.Fatalf("assistant refs=%#v err=%v", refs, err)
	}
	server := &Server{workspaceStore: store, transcriptStore: repo}
	sourceRun := &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt), Transcript: &transcriptRunnerAuthority{
		Stream: stream, Claim: claimed.Claim,
	}}
	commits, err := server.sessionRunnerArtifactCommitReferences(context.Background(), sourceRun)
	if err != nil || len(commits) != 1 || commits[0].VersionID != version.ID {
		t.Fatalf("completion recovery commits=%#v err=%v", commits, err)
	}
	unresolved, err := server.unresolvedSessionRunnerArtifactReferenceCount(
		sessionstore.Session{ID: stream.SessionID, Project: &sessionstore.Project{ID: stream.ProjectID}},
		sourceRun, commits, content,
	)
	if err != nil || unresolved != 0 {
		t.Fatalf("known completion recovery reference unresolved=%d err=%v commits=%#v", unresolved, err, commits)
	}
	err = server.validateRecoveredTranscriptCompletionCandidate(
		context.Background(),
		sessionstore.Session{ID: stream.SessionID, Project: &sessionstore.Project{ID: stream.ProjectID}},
		sessionRunnerCompletionRecoveryCandidate{
			Content: content, SourceRun: sourceRun,
		},
	)
	var integrityErr *sessionRunnerReferenceIntegrityError
	if !errors.As(err, &integrityErr) || integrityErr.UnresolvedArtifacts != 0 ||
		len(integrityErr.MalformedArtifactReferences) != 1 {
		t.Fatalf("completion recovery accepted malformed link: error=%#v", err)
	}
}

func TestCompletionRecoveryIgnoresEmptyPriorSegmentDeltasAndAcceptsGlobalIndices(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-completion-deltas", "frame-completion-deltas")
	server := &Server{workspaceStore: store, transcriptStore: repo}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-completion-deltas", MessageUUID: "message-completion-deltas",
		ClientMessageID: "client-completion-deltas", Text: "Build the final evidence summary.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-completion-deltas")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "completion-delta-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	checkpoint, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: "completion-delta-checkpoint",
		Phase: transcriptstore.RunnerPhaseExecuting, Resumable: true,
		PayloadJSON: []byte(`{"status":"running"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	deltas := []struct {
		text      string
		index     int
		segment   int64
		synthetic bool
	}{
		{text: "Analyzing the task requirements…", index: 0, segment: 1, synthetic: true},
		{text: "Running the next tool…", index: 0, segment: 1, synthetic: false},
		{text: "", index: 1, segment: 1},
		{text: "Final evidence ", index: 2, segment: 2},
		{text: "summary", index: 3, segment: 2},
	}
	for ordinal, delta := range deltas {
		payload, err := json.Marshal(map[string]any{
			"text": delta.text, "delta_index": delta.index, "synthetic_progress": delta.synthetic,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(delta.segment, ""),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
			Claim: claimed.Claim, ClientMessageID: fmt.Sprintf("completion-delta-%d", ordinal),
			Type: "content_delta", Source: transcriptstore.EventSourcePayload, PayloadJSON: payload,
		}); err != nil {
			t.Fatal(err)
		}
	}
	finish, err := json.Marshal(map[string]any{
		"status": "failed", "detail": "runner completion reference integrity failed (unsupported_citations=1)",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(2, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: "completion-delta-finish", Status: "failed", PayloadJSON: finish,
	}); err != nil {
		t.Fatal(err)
	}

	authority := &transcriptRunnerAuthority{
		Stream: stream,
		Claim: transcriptstore.RunnerClaim{
			Attempt: 2, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
			ResumeCheckpoint: checkpoint.Sequence, ResumeCheckpointAttempt: claimed.Claim.Attempt,
			ClaimedInputRevision: claimed.Claim.ClaimedInputRevision,
		},
	}
	candidate, recoverable, err := server.loadTranscriptCompletionRecoveryCandidate(context.Background(), authority)
	if err != nil || !recoverable || candidate.Content != "Final evidence summary" || candidate.Segment.Ordinal != 2 {
		t.Fatalf("candidate=%#v recoverable=%t err=%v", candidate, recoverable, err)
	}
	authority.Claim.ClaimedInputRevision++
	if stale, recoverable, err := server.loadTranscriptCompletionRecoveryCandidate(context.Background(), authority); err != nil || recoverable {
		t.Fatalf("new logical input inherited prior completion candidate=%#v recoverable=%t err=%v", stale, recoverable, err)
	}
}

func TestFrameResumeCompletionRecoveryDoesNotRegeneratePublishedReferenceFailure(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-completion-recovery", "frame-completion-recovery")
	var requests atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&chatCompletionRequest{}); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if request := requests.Add(1); request != 1 {
			http.Error(w, "completion-only recovery must not call the provider", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"completion-recovery\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":\"stop\"}]}\n\n", "Published report: {{artifact:"+unresolvedArtifactVersionID+"}}")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer provider.Close()

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	if _, err := server.settingsStore.Set("model.activeProviderId", "completion-recovery-provider"); err != nil {
		t.Fatal(err)
	}
	if _, err := server.secretStore.Create(secretstore.Secret{
		ID: "completion-recovery-key", UserID: "local", Provider: "openai", Value: "test-key",
	}); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := store.RegisterModelProvider(workspace.ModelProviderInput{
		ID: "completion-recovery-provider", UserID: "local", Name: "Completion recovery provider", Type: "openai",
		BaseURL: provider.URL + "/v1", Model: "test-model", SecretRef: "secret://completion-recovery-key", Enabled: &enabled,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-completion-recovery", MessageUUID: "message-completion-recovery",
		ClientMessageID: "client-completion-recovery", Text: "Create the exact research deliverable.",
	}); err != nil {
		t.Fatal(err)
	}
	// Seed a terminally failed attempt with a published streamed candidate and
	// a resumable checkpoint. Reference-integrity failures normally become
	// resumable interruptions in the live runner; this fixture seeds the
	// terminal-failed shape that completion-only recovery is designed to
	// repair without re-calling the provider.
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-completion-recovery")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: "local", RunnerID: "completion-recovery-first",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	seedCheckpoint, err := json.Marshal(map[string]any{
		"kind": "runner-task-memory-snapshot-v1", "status": "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	seededCheckpoint, _, _, err := repo.AppendRunnerCheckpoint(context.Background(), transcriptstore.AppendRunnerCheckpointInput{
		Claim: claimed.Claim, ClientMessageID: transcriptRunnerClientMessageID(claimed.Claim, "seed-task-memory"),
		Phase: transcriptstore.RunnerPhasePlanning, Resumable: true, PayloadJSON: seedCheckpoint,
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, text := range []string{"Published report: ", "{{artifact:" + unresolvedArtifactVersionID + "}}"} {
		rawDelta, err := json.Marshal(map[string]any{
			"text": text, "delta_index": index + 1,
			"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
			Claim: claimed.Claim, ClientMessageID: transcriptRunnerClientMessageID(claimed.Claim, fmt.Sprintf("seed-delta-%d", index+1)),
			Type: "content_delta", Source: transcriptstore.EventSourcePayload, PayloadJSON: rawDelta,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rawAssistant, err := json.Marshal(map[string]any{
		"text":              "Published report: {{artifact:" + unresolvedArtifactVersionID + "}}",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: claimed.Claim, ClientMessageID: transcriptRunnerClientMessageID(claimed.Claim, "seed-assistant"),
		Type: "assistant_message", Source: transcriptstore.EventSourcePayload, PayloadJSON: rawAssistant,
	}); err != nil {
		t.Fatal(err)
	}
	rawFinish, err := json.Marshal(map[string]any{
		"status":            "failed",
		"detail":            "runner completion reference integrity failed (unresolved_artifacts=1)",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: claimed.Claim, ClientMessageID: transcriptRunnerClientMessageID(claimed.Claim, "runner-finished"),
		Status: "failed", PayloadJSON: rawFinish,
	}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 0 {
		t.Fatalf("seeded recovery fixture must not call the provider: requests=%d", requests.Load())
	}
	streamBeforeResume, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-completion-recovery")
	if err != nil || !found {
		t.Fatalf("stream before resume=%#v found=%t err=%v", streamBeforeResume, found, err)
	}
	candidate, recoverable, recoveryErr := server.loadTranscriptCompletionRecoveryCandidate(context.Background(), &transcriptRunnerAuthority{
		Stream: streamBeforeResume,
		Claim: transcriptstore.RunnerClaim{
			Attempt: 2, ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: seededCheckpoint.Sequence,
			ResumeCheckpointAttempt: 1, ClaimedInputRevision: claimed.Claim.ClaimedInputRevision,
		},
	})
	if recoveryErr != nil || !recoverable {
		t.Fatalf("candidate=%#v recoverable=%t err=%v", candidate, recoverable, recoveryErr)
	}

	compatJSONRequest(t, server.Handler(), http.MethodPost, "/api/frames/frame-completion-recovery/resume", "local", map[string]any{}, http.StatusOK)
	resumed, err := server.RunFrameResumeDispatchOnce(context.Background(), FrameResumeDispatchOptions{
		WorkerID: "completion-recovery-worker", ClaimTTL: time.Second,
		Chat: SessionRunnerChatOptions{
			LeaseTTL: time.Minute, MaxAttempts: 1, MaxToolRounds: 0, RequireSavedModel: true,
			DisableSkillDiscovery: true,
		},
	})
	if err != nil || !resumed.Claimed || resumed.Status != "failed" || resumed.Runner.Attempt != 2 || requests.Load() != 0 {
		t.Fatalf("resumed=%#v requests=%d err=%v", resumed, requests.Load(), err)
	}

	stream, found, err = repo.GetFrameStreamBySession(context.Background(), "local", "frame-completion-recovery")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	events, err := repo.ListProjectedEvents(context.Background(), transcriptstore.ListProjectedEventsInput{
		StreamUID: stream.UID, OwnerID: "local", ThroughPublicationSequence: stream.NextPublication - 1, Limit: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas strings.Builder
	segmentOrdinals := map[int64]int{}
	terminals := 0
	for _, projected := range events {
		var payload map[string]any
		if err := json.Unmarshal(projected.ResolvedPayloadJSON, &payload); err != nil {
			t.Fatal(err)
		}
		switch projected.Event.Type {
		case "content_delta":
			deltas.WriteString(stringValue(payload["text"]))
			segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
			if err != nil || !present {
				t.Fatalf("delta segment payload=%#v err=%v", payload, err)
			}
			segmentOrdinals[segment.Ordinal]++
		case "runner_finished":
			terminals++
		}
	}
	if deltas.String() != "Published report: {{artifact:"+unresolvedArtifactVersionID+"}}" || len(segmentOrdinals) != 1 || terminals != 2 {
		t.Fatalf("deltas=%q segments=%#v terminals=%d", deltas.String(), segmentOrdinals, terminals)
	}
}

func TestCompletionOnlyRecoveryDefersReviewerCorrectionToCheckpointExecution(t *testing.T) {
	if transcriptCompletionOnlyRecoveryHandles("completion_review_correction_required") {
		t.Fatal("review correction was replayed as completion-only terminal state")
	}
	if !transcriptCompletionOnlyRecoveryHandles("artifact_reference_correction_required") {
		t.Fatal("deterministic artifact-reference recovery was disabled")
	}
}

func TestCompletionRecoverySkipsPriorTerminalCandidateWhenReclaimingInterruptedAttempt(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-in-place-correction", "frame-in-place-correction")
	server := &Server{workspaceStore: store, transcriptStore: repo}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-in-place-correction", MessageUUID: "first-in-place-message",
		ClientMessageID: "first-in-place-message", Text: "first task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-in-place-correction")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	first, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "prior-terminal-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !first.Claimed {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	// A legacy terminal attempt may contain a delta that predates the assistant
	// segment envelope. Reclaiming the *current* interrupted attempt must not
	// inspect or try to recover this older terminal candidate.
	if _, _, err := repo.AppendRunnerEvent(context.Background(), transcriptstore.AppendEventInput{
		Claim: first.Claim, ClientMessageID: "legacy-unsegmented-delta", Type: "content_delta",
		Source: transcriptstore.EventSourcePayload, PayloadJSON: []byte(`{"text":"legacy candidate","delta_index":1}`),
	}); err != nil {
		t.Fatal(err)
	}
	finishPayload, err := json.Marshal(map[string]any{
		"status": "failed", "detail": "runner completion reference integrity failed (unsupported_citations=1)",
		"assistant_segment": transcriptstore.AssistantSegmentPayloadV1(1, ""),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{
		Claim: first.Claim, ClientMessageID: "prior-terminal-finish", Status: "failed", PayloadJSON: finishPayload,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-in-place-correction", MessageUUID: "second-in-place-message",
		ClientMessageID: "second-in-place-message", Text: "second task",
	}); err != nil {
		t.Fatal(err)
	}
	second, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "current-interrupted-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !second.Claimed || second.Claim.Attempt != first.Claim.Attempt+1 {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: second.Claim, ClientMessageID: "current-correction-checkpoint",
		ReasonCode: "artifact_reference_correction_required", ResumeDetail: "retrieve fresh evidence", AutoResume: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	reclaimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "current-correction-reclaim",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: interrupted.Checkpoint.Sequence,
	})
	if err != nil || !reclaimed.Claimed || reclaimed.Claim.Attempt != second.Claim.Attempt {
		t.Fatalf("reclaimed=%#v err=%v", reclaimed, err)
	}
	if reclaimed.Claim.ResumeCheckpointAttempt != second.Claim.Attempt {
		t.Fatalf("reclaimed checkpoint attempt=%d want=%d", reclaimed.Claim.ResumeCheckpointAttempt, second.Claim.Attempt)
	}
	candidate, recoverable, err := server.loadTranscriptCompletionRecoveryCandidate(context.Background(), &transcriptRunnerAuthority{
		Stream: stream, Claim: reclaimed.Claim,
	})
	if err != nil || recoverable {
		t.Fatalf("in-place resume inspected prior candidate=%#v recoverable=%t err=%v", candidate, recoverable, err)
	}
}
