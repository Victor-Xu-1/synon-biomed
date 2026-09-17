package server

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRuntimeDrainDuringPreparationPersistsResumableInterruption(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "prepare-drain-project", "prepare-drain-frame")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "prepare-drain-frame", MessageUUID: "prepare-drain-message",
		ClientMessageID: "prepare-drain-input", Text: "continue this task across deployment",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "prepare-drain-frame")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "prepare-drain-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	result := SessionRunnerCycleResult{
		Claimed: true, SessionID: stream.SessionID, RunnerID: claimed.Claim.RunnerID,
		Attempt: int(claimed.Claim.Attempt),
	}
	active := &activeSessionRun{}
	active.draining.Store(true)
	handled, err := server.settleSessionRunnerPreparationError(
		context.Background(), errors.New("preparation context canceled"), "model_resolution", time.Minute,
		SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID}, &result, active,
		sessionstore.RunnerMutationClaim{}, authority,
	)
	if err != nil || !handled || result.Status != "interrupted" ||
		result.InterruptionReasonCode != "runtime_draining" || !result.InterruptionAutoResume || !active.settled {
		t.Fatalf("handled=%t result=%#v settled=%t err=%v", handled, result, active.settled, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claimed.Claim.Attempt)
	if err != nil || state.Status != "running" || state.LastCheckpointSequence == 0 ||
		state.FinishedEventID != 0 || state.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("resumable state=%#v err=%v", state, err)
	}
}

func TestSessionRunnerPreparationTimeoutPersistsResumableChemistryInterruption(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "chem-owner", "chem-project", "chem-frame")
	stream, err := repo.CreateStream(context.Background(), transcriptstore.CreateStreamInput{
		UID: "chem-stream", OwnerID: "chem-owner", ExternalID: "chem-frame",
		SessionID: "chem-frame", Kind: transcriptstore.StreamKindFrameRef,
		ProjectID: "chem-project", RootFrameID: "chem-frame", FrameID: "chem-frame", Epoch: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, created, err := repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, ClientMessageID: "chem-input",
		FrameEventID: "chem-frame-input", MessageUUID: "chem-message", MessageOrigin: "task_intent",
		Text: "请用 RDKit 计算咖啡因的分子式和分子量。",
	}); err != nil || !created {
		t.Fatalf("append chemistry input created=%t err=%v", created, err)
	}

	server := New(Options{Workspace: store, Transcript: repo, FileRoot: filepath.Join(t.TempDir(), "runner")})
	result, err := server.RunSessionRunnerChatOnce(context.Background(), SessionRunnerChatOptions{
		SessionID: "chem-frame", RunnerID: "chem-preparation-timeout",
		Endpoint: BuiltinSessionRunnerChatEndpoint, Model: BuiltinSessionRunnerChatModel,
		LeaseTTL: time.Minute, PreparationTimeout: time.Nanosecond,
		DisableSkillDiscovery: true, DisableMCPDiscovery: true,
	})
	if err != nil {
		t.Fatalf("RunSessionRunnerChatOnce() error = %v", err)
	}
	if result.Status != "interrupted" || result.InterruptionReasonCode != sessionRunnerPreparationTimeoutReasonCode ||
		!result.InterruptionAutoResume || result.Attempt != 1 {
		t.Fatalf("runner result=%#v, want resumable preparation interruption", result)
	}

	candidates, err := repo.ListAutoResumeCandidates(context.Background(), stream.OwnerID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].StreamUID != stream.UID ||
		candidates[0].ReasonCode != sessionRunnerPreparationTimeoutReasonCode {
		t.Fatalf("auto-resume candidates=%#v", candidates)
	}
	checkpoint, found, err := repo.LatestResumableCheckpoint(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("resumable checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}
	state, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, 1)
	if err != nil || state.Status != "running" || state.FinishedEventID != 0 || state.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("interrupted runtime state=%#v err=%v", state, err)
	}

	resumed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "chem-preparation-resume",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed || resumed.Claim.Attempt != 1 ||
		resumed.Claim.ResumeSource != transcriptstore.ResumeSourceCheckpoint {
		t.Fatalf("same-task resume claim=%#v err=%v", resumed, err)
	}
}
