package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestCorrectionCountIsIndependentOfReplayWindow(t *testing.T) {
	ctx := context.Background()
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "correction-count-project", "correction-count-frame")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = server.Close(context.Background()) })
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "correction-count-frame", MessageUUID: "correction-count-message",
		ClientMessageID: "correction-count-client", Text: "Complete the source-backed report.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(ctx, "local", "correction-count-frame")
	if err != nil || !found {
		t.Fatalf("stream found=%t err=%v", found, err)
	}
	claim, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "correction-count-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claim.Claimed {
		t.Fatalf("claim=%#v err=%v", claim, err)
	}
	failure := &sessionRunnerReferenceIntegrityError{MissingRequiredDeliverables: []string{"at least one verified downloadable artifact"}}
	cause := failure.runnerCorrection()
	reason, detail := cause.ReasonCode, cause.Detail
	causePayload, err := transcriptstore.RunnerInterruptionCausePayload(cause)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		appendRunnerToolCheckpoint(t, repo, claim.Claim, fmt.Sprintf("correction-count-%d", i), map[string]any{
			"status": "interrupted", "reason_code": reason, "resume_detail": detail, transcriptstore.RunnerInterruptionCauseField: causePayload,
		})
	}
	for i := 0; i < 1240; i++ {
		appendRunnerToolCheckpoint(t, repo, claim.Claim, fmt.Sprintf("correction-audit-%d", i), map[string]any{"status": "running", "audit": i})
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claim.Claim}
	run := &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(claim.Claim.Attempt), Transcript: authority}
	for _, limit := range []int{20, 1000} {
		entries, err := server.loadTranscriptRunnerReplay(ctx, authority, limit, limit)
		if err != nil {
			t.Fatal(err)
		}
		if got := runnerRepeatedCorrectionInterruptionCount(entries, cause); got != 2 {
			t.Errorf("replay limit=%d count=%d want2", limit, got)
		}
		scoped, err := server.runnerEntriesForCurrentLogicalInput(ctx, entries, run)
		if err != nil || runnerRepeatedCorrectionInterruptionCount(scoped, cause) != 2 {
			t.Fatalf("input scoping lost repetition: err=%v", err)
		}
		legacyScoped := filterRunnerEntriesByInputRevision(entries,
			map[int]int64{run.Attempt: claim.Claim.ClaimedInputRevision}, claim.Claim.ClaimedInputRevision)
		if runnerRepeatedCorrectionInterruptionCount(legacyScoped, cause) != 2 {
			t.Fatal("input-revision fallback lost the claim-bound repetition projection")
		}
		for _, message := range sessionEntriesToChatMessages("", scoped) {
			if strings.Contains(message.Content, runnerCorrectionRepetitionProjectionType) {
				t.Fatal("internal repetition projection leaked into model text")
			}
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := server.loadTranscriptRunnerReplay(canceled, authority, 20, 20); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled replay error=%v", err)
	}
	foreign := *authority
	foreign.Stream.OwnerID = "another-owner"
	if _, err := server.loadTranscriptRunnerReplay(ctx, &foreign, 20, 20); err == nil {
		t.Fatal("foreign owner received repetition history")
	}
	entries, err := server.loadTranscriptRunnerReplay(ctx, authority, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	var chatErr error = failure
	result := &SessionRunnerCycleResult{SessionID: stream.SessionID, Attempt: int(claim.Claim.Attempt)}
	handled, err := server.handleSessionRunnerChatInterruption(ctx,
		SessionRunnerChatOptions{SessionID: stream.SessionID, RunnerID: claim.Claim.RunnerID},
		result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, authority, run, entries, &chatErr, nil)
	if err != nil || !handled || result.InterruptionReasonCode != cause.ReasonCode || !result.InterruptionAutoResume {
		t.Fatalf("correction count became a task lifetime: handled=%t result=%#v err=%v", handled, result, err)
	}
	if _, found, err := repo.LatestResumableCheckpoint(ctx, stream.UID, stream.OwnerID); err != nil || !found {
		t.Fatalf("correction checkpoint lost: found=%t err=%v", found, err)
	}
}

func TestCorrectionCountRejectsPayloadOnlyProjection(t *testing.T) {
	const reason, detail = "artifact_reference_correction_required", "repair report"
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": reason, "resume_detail": detail}},
		{Message: eventjournal.Message{"type": runnerCorrectionRepetitionProjectionType,
			runnerCorrectionRepetitionProjectionType: runnerCorrectionRepetition{ReasonCode: reason, Fingerprint: correctionRepetitionFingerprint(reason, detail), Count: 99}}},
	}
	if got := runnerRepeatedCorrectionInterruptionCount(entries, transcriptstore.RunnerInterruptionCause{ReasonCode: reason, Detail: detail}); got != 1 {
		t.Fatalf("payload-only aggregate changed authoritative count to %d", got)
	}
}

func TestCorrectionCountTracksCurrentObligationNotUnrelatedProgress(t *testing.T) {
	const reason = "artifact_reference_correction_required"
	checkpoint := func(detail string) eventjournal.Entry {
		return eventjournal.Entry{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": reason, "resume_detail": detail}}
	}
	for _, test := range []struct {
		name    string
		between eventjournal.Entry
		want    int
	}{
		{"different obligation", checkpoint("repair a different artifact"), 1},
		{"successful unrelated tool", eventjournal.Entry{Message: eventjournal.Message{"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolName": "read_file", "toolResult": map[string]any{"ok": true}}}, 2},
		{"user clarification", eventjournal.Entry{SourceEventType: "user_input_response", Message: eventjournal.Message{"type": "message", "role": "user", "text": "use the original method"}}, 2},
		{"new task", eventjournal.Entry{Message: eventjournal.Message{"type": "message", "role": "user", "text": "a different task"}}, 1},
		{"protocol correction", eventjournal.Entry{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode, "resume_detail": "protocol repair"}}, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			entries := []eventjournal.Entry{checkpoint("repair current artifact"), test.between, checkpoint("repair current artifact")}
			if got := runnerRepeatedCorrectionInterruptionCount(entries, transcriptstore.RunnerInterruptionCause{ReasonCode: reason, Detail: "repair current artifact"}); got != test.want {
				t.Fatalf("count=%d want%d", got, test.want)
			}
		})
	}
}
