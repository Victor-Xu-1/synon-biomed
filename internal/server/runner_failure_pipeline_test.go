package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRunnerSettlementPersistsSafeFailureReasonThroughFrameAndHistory(t *testing.T) {
	fixture := newAgentSaveArtifactsFixture(t)
	authority := &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim}
	run := &sessionRunnerChatRun{SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), Transcript: authority, ResponseLanguage: "zh"}
	if err := ensureSessionRunnerExecutionPhaseMachine(run); err != nil {
		t.Fatal(err)
	}
	result := &SessionRunnerCycleResult{SessionID: run.SessionID, Attempt: run.Attempt}
	err := fixture.server.settleSessionRunnerChatOutcome(context.Background(), SessionRunnerChatOptions{RunnerID: fixture.claim.RunnerID}, result, &activeSessionRun{},
		sessionstore.RunnerMutationClaim{}, authority, run, sessionstore.Session{ID: run.SessionID}, nil, "", errors.New("internal computation failure at /private/path?token=SECRET"), "")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := fixture.repo.GetTerminalProjection(context.Background(), fixture.stream.OwnerID, fixture.stream.UID, result.FinishEventID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.ReasonCode != "runner_execution_failed" || strings.Contains(projection.Detail, "SECRET") || strings.Contains(projection.Detail, "/private") {
		t.Fatalf("lost reason or leaked detail: %#v", projection)
	}
	frame := compatJSONRequest(t, fixture.server.Handler(), http.MethodGet, "/api/frames/"+fixture.stream.FrameID, fixture.stream.OwnerID, nil, http.StatusOK)
	if frame["runtime_failure_reason"] != "runner_execution_failed" {
		t.Fatalf("frame reason=%#v", frame["runtime_failure_reason"])
	}
	history := compatJSONRequest(t, fixture.server.Handler(), http.MethodGet, "/api/conversations/"+fixture.stream.FrameID+"/messages?limit=10", fixture.stream.OwnerID, nil, http.StatusOK)
	raw, err := json.Marshal(history)
	if err != nil || !strings.Contains(string(raw), "\"terminal_reason_code\":\"runner_execution_failed\"") || strings.Contains(string(raw), "SECRET") {
		t.Fatalf("history lost safe reason: %s err=%v", raw, err)
	}
	compatJSONRequest(t, fixture.server.Handler(), http.MethodGet, "/api/frames/"+fixture.stream.FrameID, "foreign-owner", nil, http.StatusNotFound)
	frameContext, found, err := fixture.store.GetFrameRealtimeContext(fixture.stream.FrameID)
	if err != nil || !found {
		t.Fatalf("frame context: found=%t err=%v", found, err)
	}
	if err := fixture.server.publishTranscriptTerminal(frameContext, "failure-pipeline-test", projection, "failure-pipeline-message"); err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(transcriptWebEvents(t, fixture.store, fixture.stream.OwnerID))
	if err != nil || strings.Count(string(raw), "\"reason_code\":\"runner_execution_failed\"") < 2 || !strings.Contains(string(raw), "\"terminal_reason_code\":\"runner_execution_failed\"") || strings.Contains(string(raw), "SECRET") {
		t.Fatalf("live event reasons missing/leaked: %s err=%v", raw, err)
	}
}

func TestPresentationCorrectionRetainsCompletedReceiptAndBoundedRecovery(t *testing.T) {
	for _, failure := range []sessionRunnerBoundedCorrection{sessionRunnerResponseLanguageMismatch{}, sessionRunnerFinalPresentationCorrection{}} {
		t.Run(failure.runnerCorrection().ReasonCode, func(t *testing.T) {
			fixture := newAgentSaveArtifactsFixture(t)
			ctx := context.Background()
			appendRunnerToolCheckpoint(t, fixture.repo, fixture.claim, "verified-download", map[string]any{
				"toolName": "download_public_scientific_file", "toolPhase": "completed", "status": "completed", "toolCallId": "completed-download",
				"toolResult": map[string]any{"ok": true, "download": map[string]any{"filename": "resource.txt", "size_bytes": 128}},
			})
			claim := fixture.claim
			for attempt := 0; attempt < 3; attempt++ {
				authority := &transcriptRunnerAuthority{Stream: fixture.stream, Claim: claim}
				run := &sessionRunnerChatRun{SessionID: fixture.stream.SessionID, Attempt: int(claim.Attempt), Transcript: authority}
				entries, err := fixture.server.loadTranscriptRunnerReplay(ctx, authority, 20, 20)
				if err != nil {
					t.Fatal(err)
				}
				result := &SessionRunnerCycleResult{SessionID: run.SessionID, Attempt: run.Attempt}
				var callErr error = failure
				handled, err := fixture.server.handleSessionRunnerChatInterruption(ctx, SessionRunnerChatOptions{RunnerID: claim.RunnerID, SessionID: run.SessionID}, result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, authority, run, entries, &callErr, nil)
				if err != nil || !handled {
					t.Fatalf("presentation became terminal: handled=%t err=%v", handled, err)
				}
				if !result.InterruptionAutoResume {
					t.Fatalf("retry policy=%#v", result)
				}
				checkpoint, found, err := fixture.repo.LatestResumableCheckpoint(ctx, fixture.stream.UID, fixture.stream.OwnerID)
				if err != nil || !found {
					t.Fatalf("checkpoint found=%t err=%v", found, err)
				}
				replayed, err := fixture.server.loadTranscriptRunnerReplay(ctx, authority, 20, 20)
				if err != nil {
					t.Fatal(err)
				}
				kept := false
				for _, entry := range replayed {
					if eventjournal.Message(entry.Message)["toolCallId"] == "completed-download" {
						kept = true
					}
				}
				if !kept {
					t.Fatal("completed acquisition receipt lost")
				}
				if attempt < 2 {
					next, err := fixture.repo.ClaimRunner(ctx, transcriptstore.ClaimRunnerInput{StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, RunnerID: fmt.Sprintf("presentation-recovery-%d", attempt), TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence})
					if err != nil || !next.Claimed {
						t.Fatalf("resume=%#v err=%v", next, err)
					}
					claim = next.Claim
				}
			}
		})
	}
}
