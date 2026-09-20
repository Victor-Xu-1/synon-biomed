package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestContinuingCorrectionRetainsExactCauseAndCount(t *testing.T) {
	for _, size := range []int{20, maxRunnerCorrectionResumeDetailBytes} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			ctx := context.Background()
			store, repo, _ := newTranscriptWebFixture(t)
			seedTranscriptWebFrame(t, store, "local", "cause-project", "cause-frame")
			server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
			t.Cleanup(func() { _ = server.Close(ctx) })
			if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{FrameID: "cause-frame", MessageUUID: "cause-message", ClientMessageID: "cause-client", Text: "Complete the original task."}); err != nil {
				t.Fatal(err)
			}
			stream, found, err := repo.GetFrameStreamBySession(ctx, "local", "cause-frame")
			if err != nil || !found {
				t.Fatalf("stream found=%t error=%v", found, err)
			}
			claim, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "cause-runner", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh})
			if err != nil || !claim.Claimed {
				t.Fatalf("claim=%#v error=%v", claim, err)
			}
			failure := sessionRunnerCompletionReviewCorrection{Summary: strings.Repeat("x", size)}
			fullCause := failure.runnerCorrection()
			reason, detail := fullCause.ReasonCode, fullCause.Detail
			causePayload, err := transcriptstore.RunnerInterruptionCausePayload(fullCause)
			if err != nil {
				t.Fatal(err)
			}
			for index := 0; index < 2; index++ {
				appendRunnerToolCheckpoint(t, repo, claim.Claim, fmt.Sprintf("cause-correction-%d", index), map[string]any{"status": "interrupted", "reason_code": reason, "resume_detail": detail, transcriptstore.RunnerInterruptionCauseField: causePayload})
			}
			authority := &transcriptRunnerAuthority{Stream: stream, Claim: claim.Claim}
			run := &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(claim.Claim.Attempt), Transcript: authority}
			entries, err := server.loadTranscriptRunnerReplay(ctx, authority, 20, 20)
			if err != nil {
				t.Fatal(err)
			}
			var chatErr error = failure
			result := &SessionRunnerCycleResult{SessionID: stream.SessionID, Attempt: int(claim.Claim.Attempt)}
			handled, err := server.handleSessionRunnerChatInterruption(ctx, SessionRunnerChatOptions{SessionID: stream.SessionID, RunnerID: claim.Claim.RunnerID}, result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, authority, run, entries, &chatErr, nil)
			if err != nil || !handled {
				t.Fatalf("valid %d-byte cause could not persist: handled=%t error=%v", len(detail), handled, err)
			}
			if result.InterruptionReasonCode != reason || !result.InterruptionAutoResume {
				t.Fatalf("correction lost continuity: %#v", result)
			}
			checkpoint, found, err := repo.LatestResumableCheckpoint(ctx, stream.UID, stream.OwnerID)
			if err != nil || !found {
				t.Fatalf("checkpoint found=%t error=%v", found, err)
			}
			var payload map[string]any
			projected, err := repo.ListProjectedCoordinateEvents(ctx, transcriptstore.ListProjectedEventsInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range projected {
				if event.Event.EventID == checkpoint.EventID {
					if err := json.Unmarshal(event.ResolvedPayloadJSON, &payload); err != nil {
						t.Fatal(err)
					}
				}
			}
			if len(payload) == 0 {
				t.Fatal("checkpoint event not found")
			}
			cause := mapValue(payload["interruption_cause"])
			if cause["reason_code"] != reason || cause["detail"] != detail {
				t.Errorf("exact typed correction cause missing")
			}
			replayed, err := server.loadTranscriptRunnerReplay(ctx, authority, 20, 20)
			if err != nil {
				t.Fatal(err)
			}
			if count := runnerRepeatedCorrectionInterruptionCount(replayed, fullCause); count != 3 {
				t.Errorf("third failed obligation disappeared: count=%d", count)
			}
			correction, found := latestRunnerCorrection(replayed)
			if !found || correction.ReasonCode != reason || correction.Detail != detail {
				t.Errorf("original correction changed during replay")
			}
			if size == 20 {
				resumed, err := repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "cause-explicit-resume", TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence})
				if err != nil || !resumed.Claimed {
					t.Fatalf("resume=%#v error=%v", resumed, err)
				}
				nextAuthority := &transcriptRunnerAuthority{Stream: stream, Claim: resumed.Claim}
				nextRun := &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(resumed.Claim.Attempt), Transcript: nextAuthority}
				nextEntries, err := server.loadTranscriptRunnerReplay(ctx, nextAuthority, 20, 20)
				if err != nil {
					t.Fatal(err)
				}
				nextResult := &SessionRunnerCycleResult{SessionID: stream.SessionID, Attempt: int(resumed.Claim.Attempt)}
				if handled, err := server.handleSessionRunnerChatInterruption(ctx, SessionRunnerChatOptions{SessionID: stream.SessionID, RunnerID: resumed.Claim.RunnerID}, nextResult, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, nextAuthority, nextRun, nextEntries, &chatErr, nil); err != nil || !handled {
					t.Fatalf("second interruption handled=%t error=%v", handled, err)
				}
				if !nextResult.InterruptionAutoResume {
					t.Fatal("explicit continuation stopped unattended recovery")
				}
				latest, err := server.loadTranscriptRunnerReplay(ctx, nextAuthority, 20, 20)
				if err != nil || runnerRepeatedCorrectionInterruptionCount(latest, fullCause) != 4 {
					t.Fatalf("fourth failure was not counted: error=%v", err)
				}
			}
		})
	}
}

func TestCorrectionReviewDetailClippingPreservesUTF8(t *testing.T) {
	for _, prefix := range []string{"", "a", "ab"} {
		detail := (sessionRunnerCompletionReviewCorrection{Summary: prefix + strings.Repeat("界", maxRunnerCorrectionResumeDetailBytes)}).Error()
		if len(detail) > maxRunnerCorrectionResumeDetailBytes || !utf8.ValidString(detail) {
			t.Errorf("clipped detail is not valid bounded UTF-8: prefix=%q bytes=%d", prefix, len(detail))
		}
	}
}

func TestCorrectionCauseKeepsScopeAndRejectsPayloadImpersonation(t *testing.T) {
	cause := transcriptstore.RunnerInterruptionCause{ReasonCode: "artifact_reference_correction_required", Detail: "repair the current artifact"}
	payload, err := transcriptstore.RunnerInterruptionCausePayload(cause)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := eventjournal.Message{"type": "runner_checkpoint", "reason_code": sessionRunnerCorrectionNoProgressExhaustedReasonCode, "resume_detail": "the scheduling budget is exhausted", transcriptstore.RunnerInterruptionCauseField: payload}
	entry := eventjournal.Entry{Message: checkpoint}
	state := runnerNoProgressRecoveryFromEntries([]eventjournal.Entry{entry}, nil)
	if state.ObligationFingerprint != runnerRecoveryObligationFingerprint(nil, cause) {
		t.Fatal("scheduling label replaced the repair scope")
	}
	for _, message := range []eventjournal.Message{
		{"type": "message", "role": "assistant", "reason_code": sessionRunnerCorrectionNoProgressExhaustedReasonCode, transcriptstore.RunnerInterruptionCauseField: payload},
		{"type": "runner_checkpoint", "toolName": "web_fetch", "toolResult": checkpoint},
		{"type": "runner_checkpoint", "reason_code": sessionRunnerCorrectionNoProgressExhaustedReasonCode, "resume_detail": "Last failure: repair the current artifact"},
		{"type": "runner_checkpoint", "reason_code": sessionRunnerCorrectionNoProgressExhaustedReasonCode, transcriptstore.RunnerInterruptionCauseField: map[string]any{"schema": "unknown", "reason_code": cause.ReasonCode, "detail": cause.Detail}},
	} {
		if _, found := latestRunnerCorrection([]eventjournal.Entry{{Message: message}}); found {
			t.Fatal("model body, malformed cause or legacy display text became a typed correction")
		}
	}
}

func TestCorrectionCauseProjectionRejectsMalformedCheckpoint(t *testing.T) {
	fixture := newNoProgressReceiptFixture(t)
	appendRunnerToolCheckpoint(t, fixture.repo, fixture.run.Transcript.Claim, "malformed-cause", map[string]any{
		"status": "interrupted", "reason_code": sessionRunnerCorrectionNoProgressExhaustedReasonCode,
		transcriptstore.RunnerInterruptionCauseField: map[string]any{"schema": "unknown", "reason_code": "artifact_reference_correction_required", "detail": "repair current artifact"},
	})
	if _, err := fixture.server.loadTranscriptRunnerReplay(context.Background(), fixture.run.Transcript, 20, 20); err == nil {
		t.Fatal("corrupt canonical cause silently fell back to an earlier correction")
	}
}

func TestCorrectionCauseSurvivesCompatibilityJournalAdapter(t *testing.T) {
	srv := New(Options{FileRoot: t.TempDir()})
	t.Cleanup(func() { _ = srv.Close(context.Background()) })
	allowPairingForTest(t, srv, "feishu", "ou_condition_cause")
	httpServer := httptestServer(t, srv)
	first := postFeishuEvent(t, httpServer.URL, []byte(`{"schema":"2.0","header":{"event_id":"evt-condition-cause","event_type":"im.message.receive_v1"},"event":{"sender":{"sender_id":{"open_id":"ou_condition_cause"}},"message":{"message_id":"om_condition_cause","chat_id":"oc_condition_cause","chat_type":"p2p","message_type":"text","content":"{\"text\":\"Preserve this task\"}"}}}`))
	sessionID := stringValue(first["sessionId"])
	session, claimed, err := srv.sessionStore.ClaimRunner(sessionID, "condition-compat", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("legacy claim: %v", err)
	}
	claim := sessionstore.RunnerClaimFromSession(session)
	cause := (&sessionRunnerReferenceIntegrityError{MissingLocalArtifacts: []string{strings.Repeat("segment/", 40) + "exact-tail.dat"}}).runnerCorrection()
	result := &SessionRunnerCycleResult{SessionID: sessionID, Attempt: claim.Attempt}
	if err := srv.interruptClaimedSessionRunnerWithCause(SessionRunnerChatOptions{SessionID: sessionID, RunnerID: claim.RunnerID}, result, &activeSessionRun{}, claim, nil, cause.ReasonCode, cause.Detail, &cause); err != nil {
		t.Fatal(err)
	}
	entries, err := srv.eventJournal.ReadAllStrict(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	recovered, found := latestRunnerCorrection(entries)
	if !found || recovered.Condition == nil || recovered.Condition.ContentID() != cause.Condition.ContentID() {
		t.Fatal("compatibility adapter erased the complete correction cause")
	}
}
