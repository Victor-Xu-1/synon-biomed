package server

import (
	"context"
	"testing"
	"time"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRecoverableInterruptionsHaveNoAttemptCeiling(t *testing.T) {
	for _, reason := range []string{
		"artifact_reference_correction_required",
		sessionRunnerProviderOutputTokenLimitReasonCode,
		sessionRunnerKernelOperationPendingRecoveryReasonCode,
		sessionRunnerToolFailedReasonCode,
		"provider_stream_no_progress",
		sessionRunnerProviderTransportTemporaryReasonCode,
		sessionRunnerRealScientificEvidenceRequiredReasonCode,
	} {
		if !runnerInterruptionAutoResume(reason) {
			t.Fatalf("recoverable interruption %q lost automatic continuation", reason)
		}
	}
}

func TestSameInputNoProgressRecoveryUsesBackoffWithoutAttemptCeiling(t *testing.T) {
	now := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	for _, attempt := range []int64{1, 8, 1000} {
		notBefore := frameResumeDispatchAutoResumePolicy("provider_stream_no_progress", attempt, "frame-a", now)
		if delay := notBefore.Sub(now); delay < 2*time.Second || delay > 22*time.Second {
			t.Fatalf("attempt %d no-progress backoff=%s, want bounded 2s..22s", attempt, delay)
		}
	}
}

func TestProviderTransportRecoveryUsesReferenceBackoffWithoutAttemptCeiling(t *testing.T) {
	now := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		attempt      int64
		minimumDelay time.Duration
		maximumDelay time.Duration
	}{
		{attempt: 1, minimumDelay: 5 * time.Second, maximumDelay: 7500 * time.Millisecond},
		{attempt: 8, minimumDelay: 60 * time.Second, maximumDelay: 90 * time.Second},
		{attempt: 1000, minimumDelay: 60 * time.Second, maximumDelay: 90 * time.Second},
	} {
		notBefore := frameResumeDispatchAutoResumePolicy(
			sessionRunnerProviderTransportTemporaryReasonCode, test.attempt, "frame-provider", now,
		)
		if delay := notBefore.Sub(now); delay < test.minimumDelay || delay > test.maximumDelay {
			t.Fatalf("attempt %d provider transport backoff=%s, want %s..%s",
				test.attempt, delay, test.minimumDelay, test.maximumDelay)
		}
	}
}

func TestCollectFrameResumeRecoveryPagesContinuesPastPageSize(t *testing.T) {
	items := make([]int, 53)
	for index := range items {
		items[index] = index
	}
	loaded, err := collectFrameResumeRecoveryPages(
		context.Background(), 20,
		func(_ context.Context, offset, limit int) ([]int, error) {
			if offset >= len(items) {
				return nil, nil
			}
			end := offset + limit
			if end > len(items) {
				end = len(items)
			}
			return items[offset:end], nil
		},
	)
	if err != nil || len(loaded) != len(items) {
		t.Fatalf("loaded=%d want=%d err=%v", len(loaded), len(items), err)
	}
	for index, value := range loaded {
		if value != index {
			t.Fatalf("loaded[%d]=%d", index, value)
		}
	}
}

func TestInterruptionPersistenceTimeoutStartsAfterSettlementLock(t *testing.T) {
	activeRun := &activeSessionRun{}
	activeRun.settlement.Lock()
	done := make(chan error, 1)
	go func() {
		done <- withActiveRunSettlementContext(activeRun, 20*time.Millisecond, func(ctx context.Context) error {
			return ctx.Err()
		})
	}()
	time.Sleep(30 * time.Millisecond)
	activeRun.settlement.Unlock()
	if err := <-done; err != nil {
		t.Fatalf("settlement lock wait consumed persistence timeout: %v", err)
	}
}

func TestTranscriptKeepsRepeatedEvidenceCorrectionResumableWithoutAttemptCeiling(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-resume-budget", "frame-resume-budget")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		_ = server.Close(context.Background())
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-resume-budget", MessageUUID: "message-resume-budget",
		ClientMessageID: "client-resume-budget", Text: "继续修复当前科学报告",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-resume-budget")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "resume-budget-runner-1",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	reason := "real_scientific_evidence_required"
	const continuationCount = 12
	for bounce := 0; bounce < continuationCount; bounce++ {
		interrupted, interruptErr := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
			Claim: authority.Claim, ClientMessageID: "resume-budget-interrupt-" + string(rune('1'+bounce)),
			ReasonCode: reason, RecoveryContractRevision: sessionRunnerRecoveryContractRevision,
			ResumeDetail: "repair", AutoResume: true,
		})
		if interruptErr != nil || !interrupted.Created {
			t.Fatalf("bounce %d interruption=%#v err=%v", bounce+1, interrupted, interruptErr)
		}
		if bounce == continuationCount-1 {
			break
		}
		checkpoint, checkpointFound, checkpointErr := repo.LatestResumableCheckpoint(
			context.Background(), authority.Claim.StreamUID, authority.Claim.OwnerID,
		)
		if checkpointErr != nil || !checkpointFound {
			t.Fatalf("bounce %d checkpoint=%#v found=%t err=%v", bounce+1, checkpoint, checkpointFound, checkpointErr)
		}
		next, claimErr := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
			StreamUID: authority.Claim.StreamUID, OwnerID: authority.Claim.OwnerID,
			RunnerID: "resume-budget-runner-" + string(rune('2'+bounce)), TTL: time.Minute,
			ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
		})
		if claimErr != nil || !next.Claimed {
			t.Fatalf("bounce %d next claim=%#v err=%v", bounce+1, next, claimErr)
		}
		authority.Claim = next.Claim
	}
	candidate, eligible, candidateErr := repo.GetAutoResumeCandidate(
		context.Background(), stream.UID, stream.OwnerID,
	)
	if candidateErr != nil || !eligible || candidate.InputRevisionBounces != continuationCount {
		t.Fatalf("candidate=%#v eligible=%t err=%v", candidate, eligible, candidateErr)
	}
}

func TestCompletionQualityCorrectionsRemainResumableAcrossReasonChanges(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-completion-family", "frame-completion-family")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-completion-family", MessageUUID: "message-completion-family",
		ClientMessageID: "client-completion-family", Text: "完成并验证当前报告",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-completion-family")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "completion-family-1",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	interrupted, err := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: authority.Claim, ClientMessageID: "completion-family-interrupt-1",
		ReasonCode:               "artifact_reference_correction_required",
		RecoveryContractRevision: sessionRunnerRecoveryContractRevision,
		ResumeDetail:             "refresh validation", AutoResume: true,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("interruption=%#v err=%v", interrupted, err)
	}
	checkpoint, found, err := repo.LatestResumableCheckpoint(
		context.Background(), authority.Claim.StreamUID, authority.Claim.OwnerID,
	)
	if err != nil || !found {
		t.Fatalf("checkpoint=%#v found=%t err=%v", checkpoint, found, err)
	}
	resumed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "completion-family-2",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed {
		t.Fatalf("resumed=%#v err=%v", resumed, err)
	}
	authority.Claim = resumed.Claim
	interrupted, err = repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: authority.Claim, ClientMessageID: "completion-family-interrupt-2",
		ReasonCode:               "completion_review_correction_required",
		RecoveryContractRevision: sessionRunnerRecoveryContractRevision,
		ResumeDetail:             "publish corrected validation", AutoResume: true,
	})
	if err != nil || !interrupted.Created {
		t.Fatalf("second interruption=%#v err=%v", interrupted, err)
	}
	checkpoint = interrupted.Checkpoint
	resumed, err = repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "completion-family-3",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint,
		ResumeCheckpoint: checkpoint.Sequence,
	})
	if err != nil || !resumed.Claimed {
		t.Fatalf("second resumed=%#v err=%v", resumed, err)
	}
	authority.Claim = resumed.Claim
	if !runnerInterruptionAutoResume("artifact_reference_correction_required") ||
		!runnerInterruptionAutoResume("completion_review_correction_required") {
		t.Fatal("completion-quality correction reasons lost automatic continuation")
	}
}

func TestRepeatedEvidenceCorrectionRemainsAutomaticallyResumable(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-exhausted-terminal", "frame-exhausted-terminal")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-exhausted-terminal", MessageUUID: "message-exhausted-terminal",
		ClientMessageID: "client-exhausted-terminal", Text: "完成并验证报告",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-exhausted-terminal")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "exhausted-runner-1",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	reason := "real_scientific_evidence_required"
	const seededContinuations = 12
	for bounce := int64(0); bounce < seededContinuations; bounce++ {
		interrupted, interruptErr := repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
			Claim: authority.Claim, ClientMessageID: "exhausted-seed-" + string(rune('1'+bounce)),
			ReasonCode: reason, RecoveryContractRevision: sessionRunnerRecoveryContractRevision,
			ResumeDetail: "repair", AutoResume: true,
		})
		if interruptErr != nil || !interrupted.Created {
			t.Fatalf("seed interruption %d=%#v err=%v", bounce+1, interrupted, interruptErr)
		}
		next, claimErr := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			RunnerID: "exhausted-runner-" + string(rune('2'+bounce)), TTL: time.Minute,
			ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: interrupted.Checkpoint.Sequence,
		})
		if claimErr != nil || !next.Claimed {
			t.Fatalf("resume %d=%#v err=%v", bounce+1, next, claimErr)
		}
		authority.Claim = next.Claim
	}
	result := SessionRunnerCycleResult{Claimed: true, SessionID: stream.SessionID, Attempt: int(authority.Claim.Attempt)}
	active := &activeSessionRun{}
	if err := server.interruptClaimedSessionRunnerLockedWithPolicy(
		context.Background(), SessionRunnerChatOptions{}, &result, active,
		sessionstore.RunnerMutationClaim{}, authority, reason, true, "validation still failed",
	); err != nil {
		t.Fatal(err)
	}
	if result.Status != "interrupted" || result.FinishEventID != 0 || !result.InterruptionAutoResume || !active.settled {
		t.Fatalf("unlimited correction result=%#v settled=%t", result, active.settled)
	}
	candidate, eligible, err := repo.GetAutoResumeCandidate(context.Background(), stream.UID, stream.OwnerID)
	if err != nil || !eligible || candidate.InputRevisionBounces != seededContinuations+1 {
		t.Fatalf("unlimited candidate=%#v eligible=%t err=%v", candidate, eligible, err)
	}
}
