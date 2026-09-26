package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func TestUnchangedRecoveryWithRejectedActionsParksWithoutFailingTask(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	failure := sessionRunnerPlanStepsIncomplete{condition: transcriptstore.RunnerPlanCondition{
		ArtifactID: "plan", VersionID: "plan-version", Steps: []transcriptstore.RunnerPlanConditionStep{{ID: "compute", Title: "Compute result"}},
	}}
	cause := failure.runnerCorrection()
	payload, err := transcriptstore.RunnerInterruptionCausePayload(cause)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < sessionRunnerConsecutiveIdenticalToolRoundBudget; i++ {
		appendRunnerToolCheckpoint(t, f.repo, f.claim, fmt.Sprintf("rejected-route-%d", i), map[string]any{
			"status": "failed", "toolPhase": "prestart_failed", "toolName": "manage_environments",
			"toolResult": map[string]any{"ok": false, "executed": false, "code": "implementation_selection_required"},
		})
		appendRunnerToolCheckpoint(t, f.repo, f.claim, fmt.Sprintf("unchanged-condition-%d", i), map[string]any{
			"status": "interrupted", "reason_code": cause.ReasonCode, "resume_detail": cause.Detail,
			"recovery_contract_revision": sessionRunnerRecoveryContractRevision, transcriptstore.RunnerInterruptionCauseField: payload,
		})
	}
	appendRunnerToolCheckpoint(t, f.repo, f.claim, "latest-rejected-route", map[string]any{
		"status": "failed", "toolPhase": "prestart_failed", "toolName": "manage_environments",
		"toolResult": map[string]any{"ok": false, "executed": false, "code": "implementation_selection_required"},
	})
	authority := &transcriptRunnerAuthority{Stream: f.stream, Claim: f.claim}
	run := &sessionRunnerChatRun{SessionID: f.stream.SessionID, Attempt: int(f.claim.Attempt), Transcript: authority}
	entries, err := f.server.loadTranscriptRunnerReplay(context.Background(), authority, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	var chatErr error = failure
	result := &SessionRunnerCycleResult{SessionID: f.stream.SessionID, Attempt: int(f.claim.Attempt)}
	handled, err := f.server.handleSessionRunnerChatInterruption(context.Background(), SessionRunnerChatOptions{SessionID: f.stream.SessionID, RunnerID: f.claim.RunnerID}, result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, authority, run, entries, &chatErr, nil)
	if err != nil || !handled || result.Status != "interrupted" || result.InterruptionAutoResume {
		t.Fatalf("unchanged denied routes kept scheduling model calls: %#v handled=%t error=%v", result, handled, err)
	}
	if _, found, err := f.repo.LatestResumableCheckpoint(context.Background(), f.stream.UID, f.stream.OwnerID); err != nil || !found {
		t.Fatalf("parked recovery lost its resumable checkpoint: %t %v", found, err)
	}
	if _, eligible, err := f.repo.GetAutoResumeCandidate(context.Background(), f.stream.UID, f.stream.OwnerID); err != nil || eligible {
		t.Fatalf("parked recovery was automatically eligible without a state change: %t %v", eligible, err)
	}
}

func TestRecoveryConditionWaitUsesDurableChangesAndPreservesCancellation(t *testing.T) {
	for _, scenario := range []string{"unchanged", "administrative", "material", "user", "upgrade", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAgentSaveArtifactsFixture(t)
			interrupted, err := f.repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
				Claim: f.claim, ClientMessageID: "park-recovery", ReasonCode: sessionRunnerPlanStepsIncompleteReasonCode,
				Resumable: true, AutoResume: false, Destinations: []string{transcriptWebDestination},
			})
			if err != nil || !interrupted.Created {
				t.Fatalf("interrupt: %#v %v", interrupted, err)
			}
			checkpoint, found, err := f.repo.LatestResumableCheckpoint(context.Background(), f.stream.UID, f.stream.OwnerID)
			if err != nil || !found {
				t.Fatalf("checkpoint: %#v %v", checkpoint, err)
			}
			if _, err := f.store.CreateAutoResumeDispatch(f.stream.SessionID, f.stream.SessionID, f.stream.ProjectID, "OPERON", "initial_task"); err != nil {
				t.Fatal(err)
			}
			claim, claimed, err := f.store.ClaimNextCompatibilityFrameResumeDispatch("recovery-worker", time.Minute)
			if err != nil || !claimed {
				t.Fatalf("dispatch claim: %#v %v", claim, err)
			}
			revision := sessionRunnerRecoveryContractRevision
			if scenario == "upgrade" || scenario == "cancelled" {
				revision--
			}
			if _, _, err := f.store.RequeueCompatibilityFrameResumeDispatch(workspace.RequeueCompatibilityFrameResumeDispatchInput{
				ResumeEventID: claim.ResumeEvent.ID, ExpectedAttempt: claim.Attempt, ClaimToken: claim.ClaimToken,
				ReasonCode: sessionRunnerPlanStepsIncompleteReasonCode, RunnerAttempt: int(f.claim.Attempt),
				CheckpointEventID: checkpoint.Sequence, WaitingFor: workspace.CompatibilityFrameResumeDispatchWaitRecoveryCondition,
				RecoveryContractRevision: revision,
			}); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "user":
				if _, _, _, err := f.repo.AppendFrameUserEvent(context.Background(), transcriptstore.AppendFrameUserEventInput{
					StreamUID: f.stream.UID, OwnerID: f.stream.OwnerID, ClientMessageID: "changed-input",
					FrameEventID: "changed-input-event", MessageUUID: "changed-input-message", Text: "Continue with the corrected input.",
				}); err != nil {
					t.Fatal(err)
				}
			case "material", "administrative":
				resumed, err := f.repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
					StreamUID: f.stream.UID, OwnerID: f.stream.OwnerID, RunnerID: "external-evidence",
					TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceCheckpoint, ResumeCheckpoint: checkpoint.Sequence,
				})
				if err != nil || !resumed.Claimed {
					t.Fatalf("evidence claim: %#v %v", resumed, err)
				}
				tool := "python"
				if scenario == "administrative" {
					tool = updateStepStatusToolName
				}
				appendRunnerToolCheckpoint(t, f.repo, resumed.Claim, "new-evidence", map[string]any{
					"status": "completed", "toolPhase": "completed", "toolName": tool,
					"toolResult": map[string]any{"ok": true, "executed": true, "stdout": "new result"},
				})
			case "cancelled":
				cancelled := workspace.FrameStatusCancelled
				if _, err := f.store.UpdateFrame(f.stream.SessionID, workspace.UpdateFrameInput{Status: &cancelled}); err != nil {
					t.Fatal(err)
				}
			}
			for i := 0; i < 2; i++ {
				if err := f.server.wakeChangedFrameRecoveryWaits(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			current, found, err := f.store.GetCompatibilityFrameResumeDispatch(claim.ResumeEvent.ID)
			wantWake := scenario == "material" || scenario == "user" || scenario == "upgrade"
			if err != nil || !found || (current.WaitingFor == "") != wantWake || current.Attempt != claim.Attempt {
				t.Fatalf("same dispatch wake=%t: %#v %v", wantWake, current, err)
			}
			wakes := 0
			if wantWake {
				wakes = 1
			}
			assertFrameResumeDispatchEvents(t, f.store, f.stream.SessionID, map[string]int{"frame_resume_dispatch_woken": wakes})
		})
	}
}

func TestRecoveryConditionWaitProjectsPausedLiveAndAfterReload(t *testing.T) {
	f := newAgentSaveArtifactsFixture(t)
	if _, err := f.repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{
		Claim: f.claim, ClientMessageID: "park-recovery", ReasonCode: sessionRunnerPlanStepsIncompleteReasonCode,
		Resumable: true, AutoResume: false, Destinations: []string{transcriptWebDestination},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.server.drainTranscriptWebDeliveries(context.Background()); err != nil {
		t.Fatal(err)
	}
	paused := false
	for _, event := range transcriptWebEvents(t, f.store, f.stream.OwnerID) {
		if event.Type == "runtime.statusChanged" && event.Payload["phase"] == "paused" {
			paused = true
		}
	}
	if !paused {
		t.Fatal("live recovery wait still appeared to be running")
	}
	frame, found, err := f.store.GetCompatibilityFrame(f.stream.SessionID)
	if err != nil || !found {
		t.Fatal(err)
	}
	snapshot, err := f.server.webConversationRuntimeSnapshot(frame)
	if err != nil || snapshot["state"] != "paused" || snapshot["is_processing"] != false || snapshot["can_send_message"] != true {
		t.Fatalf("reloaded recovery wait: %#v %v", snapshot, err)
	}
}

func TestRecoveryRepetitionDoesNotParkHealthyOrUpgradedWork(t *testing.T) {
	cause := sessionRunnerPlanStepsIncomplete{condition: transcriptstore.RunnerPlanCondition{
		ArtifactID: "plan", VersionID: "version", Steps: []transcriptstore.RunnerPlanConditionStep{{ID: "work", Title: "Analyze"}},
	}}.runnerCorrection()
	for _, scenario := range []string{"material", "old-runtime", "no-attempt"} {
		t.Run(scenario, func(t *testing.T) {
			state := runnerCorrectionRepetition{}
			for i := 0; i < 20; i++ {
				if scenario != "no-attempt" {
					state.observe(eventjournal.Entry{Message: eventjournal.Message{
						"type": "runner_checkpoint", "status": "completed", "toolName": "python", "toolPhase": "completed",
						"toolResult": map[string]any{"ok": scenario == "material", "executed": true},
					}})
				}
				revision := sessionRunnerRecoveryContractRevision
				if scenario == "old-runtime" {
					revision--
				}
				state.observe(eventjournal.Entry{SourceEventType: "runner_checkpoint", RuntimeProjection: cause, Message: eventjournal.Message{
					"type": "runner_checkpoint", "status": "interrupted", "reason_code": cause.ReasonCode,
					"resume_detail": cause.Detail, "recovery_contract_revision": revision,
				}})
			}
			if state.waitsForChangedCondition(cause) {
				t.Fatalf("%s incorrectly parked: %#v", scenario, state)
			}
		})
	}
}
