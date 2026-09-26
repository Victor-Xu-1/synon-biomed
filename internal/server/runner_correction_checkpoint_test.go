package server

import (
	"context"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRetiredCorrectionRetainsAutomaticAndExplicitContinuationCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "correction-project", "correction-frame")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "correction-frame", MessageUUID: "correction-message",
		ClientMessageID: "correction-client", Text: "Continue the same task",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(ctx, "local", "correction-frame")
	if err != nil || !found {
		t.Fatalf("stream found=%t err=%v", found, err)
	}
	claim, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "correction-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	result := &SessionRunnerCycleResult{SessionID: "correction-frame"}
	err = server.interruptClaimedSessionRunnerLockedWithPolicy(ctx,
		SessionRunnerChatOptions{SessionID: "correction-frame", RunnerID: "correction-runner"},
		result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{},
		&transcriptRunnerAuthority{Stream: stream, Claim: claim.Claim},
		sessionRunnerCorrectionNoProgressExhaustedReasonCode,
		runnerInterruptionAutoResume(sessionRunnerCorrectionNoProgressExhaustedReasonCode),
		"Keep the prior artifacts and continue with a different repair.",
	)
	if err != nil || result.Status != "interrupted" || !result.InterruptionAutoResume {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	checkpoint, found, err := repo.LatestResumableCheckpoint(ctx, stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("explicit continuation checkpoint missing: found=%t err=%v", found, err)
	}
	candidates, err := repo.ListAllExpiredUnrecoverableRunnerCandidatesPage(ctx, 0, 100)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("recoverable correction selected for terminalization: candidates=%+v err=%v", candidates, err)
	}
	resumed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "explicit-continuation",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed {
		t.Fatalf("explicit continuation failed: resumed=%+v err=%v", resumed, err)
	}
}
