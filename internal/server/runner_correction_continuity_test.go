package server

import (
	"context"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func (fixture *noProgressReceiptFixture) resume(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	stream := fixture.run.Transcript.Stream
	checkpoint, found, err := fixture.repo.LatestResumableCheckpoint(ctx, stream.UID, stream.OwnerID)
	if err != nil || !found {
		t.Fatalf("checkpoint found=%t error=%v", found, err)
	}
	claim, err := fixture.repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: fixture.options.RunnerID, TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence})
	if err != nil || !claim.Claimed {
		t.Fatalf("resume claim=%#v error=%v", claim, err)
	}
	fixture.run = &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(claim.Claim.Attempt), ClaimToken: claim.Claim.ClaimToken, Transcript: &transcriptRunnerAuthority{Stream: stream, Claim: claim.Claim}}
	entries, err := fixture.server.loadTranscriptRunnerReplay(ctx, fixture.run.Transcript, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if correction, found := latestRunnerCorrection(entries); found {
		fixture.run.restoreCorrection(&correction)
	}
	state, _ := sessionRunnerNoProgressRecoveryFromReplay(entries)
	fixture.run.restoreNoProgressRecovery(state)
}

func TestCorrectionContinuityDoesNotExhaustLogicalTask(t *testing.T) {
	fixture := newNoProgressReceiptFixture(t)
	failure := sessionRunnerCompletionReviewCorrection{Summary: "The accepted obligation still needs work."}
	for attempt := 0; attempt < 4; attempt++ {
		entries, err := fixture.server.loadTranscriptRunnerReplay(context.Background(), fixture.run.Transcript, 20, 20)
		if err != nil {
			t.Fatal(err)
		}
		var chatErr error = failure
		result := &SessionRunnerCycleResult{SessionID: fixture.run.SessionID, Attempt: fixture.run.Attempt}
		handled, err := fixture.server.handleSessionRunnerChatInterruption(context.Background(), fixture.options, result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, fixture.run.Transcript, fixture.run, entries, &chatErr, nil)
		if err != nil || !handled || !result.InterruptionAutoResume {
			t.Fatalf("logical task stopped at correction %d: result=%#v handled=%t error=%v", attempt+1, result, handled, err)
		}
		fixture.resume(t)
	}
}

func TestPlanModePendingCannotReturnSuccessAfterPriorDenials(t *testing.T) {
	model := &planModeCompletionModel{}
	session := sessionstore.Session{ID: "pending-plan", Orchestration: map[string]any{"sessionConfig": map[string]any{"planMode": true}}}
	_, err := (&Server{}).runSessionAgentWithPlanMode(context.Background(), session, agentruntime.Engine{Model: model}, agentruntime.RunRequest{
		Messages: []agentruntime.Message{{Role: "user", Content: "Prepare the plan for approval."}},
	}, &sessionRunnerChatRun{PlanModeDenials: 3}, nil)
	if err == nil {
		t.Fatal("pending plan returned success after the historical denial count")
	}
}
