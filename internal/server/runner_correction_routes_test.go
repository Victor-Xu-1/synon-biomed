package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type correctionRouteFixture struct {
	*agentSaveArtifactsFixture
	run *sessionRunnerChatRun
}

func newCorrectionRouteFixture(t *testing.T) *correctionRouteFixture {
	fixture := newAgentSaveArtifactsFixture(t)
	return &correctionRouteFixture{agentSaveArtifactsFixture: fixture, run: &sessionRunnerChatRun{
		SessionID: fixture.stream.SessionID, Attempt: int(fixture.claim.Attempt), ClaimToken: fixture.claim.ClaimToken,
		Transcript: &transcriptRunnerAuthority{Stream: fixture.stream, Claim: fixture.claim},
	}}
}

func (fixture *correctionRouteFixture) gateway() serverAgentRuntimeToolGateway {
	return serverAgentRuntimeToolGateway{server: fixture.server, kernel: fixture.identity, sessionID: fixture.run.SessionID, taskRun: fixture.run,
		toolSchemas: []agentruntime.ToolSchema{agentWorkspaceReadFileToolSchema(), agentWorkspaceEditFileToolSchema()}, hasToolSnapshot: true, suppressHooks: true}
}

func correctionEditCall(id, path, value string) agentruntime.ToolCall {
	input, _ := json.Marshal(map[string]any{"file_path": path, "old_string": "", "new_string": value, "human_description": "Updating the current candidate"})
	return agentruntime.ToolCall{ID: id, Name: "edit_file", Arguments: input}
}

func (fixture *correctionRouteFixture) execute(t *testing.T, calls ...agentruntime.ToolCall) int {
	t.Helper()
	ctx := withTranscriptRunnerChatRun(context.Background(), fixture.run)
	gateway := fixture.gateway()
	options := SessionRunnerChatOptions{SessionID: fixture.run.SessionID, RunnerID: fixture.run.Transcript.Claim.RunnerID}
	starts := 0
	engine := agentruntime.Engine{Model: &receiptBatchModel{calls: calls}, Tools: gateway, OnEventError: func(event agentruntime.Event) error {
		if event.Type == agentruntime.EventModelResponse {
			return fixture.server.checkpointChatModelToolCalls(options, fixture.run, event.ToolCalls)
		}
		if event.Type == agentruntime.EventToolStarted {
			starts++
		}
		return fixture.server.checkpointSessionRunnerToolEvent(ctx, options, fixture.run, event)
	}}
	_, err := engine.Run(ctx, agentruntime.RunRequest{Messages: []agentruntime.Message{{Role: "user", Content: "Repair the current candidate."}}, Tools: gateway.toolSchemas})
	if err != nil {
		t.Fatal(err)
	}
	return starts
}

func (fixture *correctionRouteFixture) reject(t *testing.T, failure sessionRunnerBoundedCorrection) {
	t.Helper()
	entries, err := fixture.server.loadTranscriptRunnerReplay(context.Background(), fixture.run.Transcript, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	var chatErr error = failure
	result := &SessionRunnerCycleResult{SessionID: fixture.run.SessionID, Attempt: fixture.run.Attempt}
	options := SessionRunnerChatOptions{SessionID: fixture.run.SessionID, RunnerID: fixture.run.Transcript.Claim.RunnerID}
	handled, err := fixture.server.handleSessionRunnerChatInterruption(context.Background(), options, result, &activeSessionRun{}, sessionstore.RunnerMutationClaim{}, fixture.run.Transcript, fixture.run, entries, &chatErr, nil)
	if err != nil || !handled || !result.InterruptionAutoResume || result.Status != "interrupted" {
		t.Fatalf("rejection lost continuity: result=%#v handled=%t error=%v", result, handled, err)
	}
}

func (fixture *correctionRouteFixture) resume(t *testing.T) {
	t.Helper()
	state := noProgressReceiptFixture{server: fixture.server, repo: fixture.repo, run: fixture.run, options: SessionRunnerChatOptions{RunnerID: "route-resume"}}
	state.resume(t)
	fixture.run = state.run
}

func (fixture *correctionRouteFixture) reopen(t *testing.T) {
	t.Helper()
	fileRoot := fixture.server.fileRoot
	if err := fixture.server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.Open(fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := store.TranscriptRepository(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: fileRoot})
	fixture.store, fixture.repo, fixture.server = store, repo, server
	t.Cleanup(func() { _ = server.Close(context.Background()); _ = store.Close() })
}

func TestCorrectionRoutesContinueAcrossFourRejectionsAndDatabaseReopen(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	failure := sessionRunnerCompletionReviewCorrection{Issues: []sessionRunnerReviewIssue{{Severity: "major", Claim: "The current candidate has not satisfied the acceptance condition."}}}
	for version := 1; version <= 5; version++ {
		call := correctionEditCall(fmt.Sprintf("repair-%d", version), "candidate.txt", fmt.Sprintf("candidate-%d", version))
		if starts := fixture.execute(t, call); starts != 1 {
			t.Fatalf("new strategy %d did not execute: starts=%d", version, starts)
		}
		data, err := os.ReadFile(filepath.Join(fixture.projectPath, "candidate.txt"))
		if err != nil || string(data) != fmt.Sprintf("candidate-%d", version) {
			t.Fatalf("native edit was not durable: data=%q error=%v", data, err)
		}
		if version == 5 {
			// The test's deterministic validator accepts only the actual final
			// file, never a tool success or the count of prior corrections.
			if _, _, _, err := fixture.repo.FinishRunner(context.Background(), transcriptstore.FinishRunnerInput{Claim: fixture.run.Transcript.Claim, ClientMessageID: "accepted-candidate", Status: "completed", PayloadJSON: []byte(`{"status":"completed"}`)}); err != nil {
				t.Fatal(err)
			}
			break
		}
		fixture.reject(t, failure)
		if version == 3 {
			fixture.reopen(t)
		}
		fixture.resume(t)
		entries, err := fixture.server.loadTranscriptRunnerReplay(context.Background(), fixture.run.Transcript, 1, 1)
		if err != nil || runnerRepeatedCorrectionInterruptionCount(entries, failure.runnerCorrection()) != version {
			t.Fatalf("canonical rejection count lost at %d: error=%v", version, err)
		}
		call.ID += "-repeat"
		if starts := fixture.execute(t, call); starts != 0 {
			t.Fatalf("closed strategy %d repeated a native mutation: starts=%d", version, starts)
		}
	}
	t.Log("four durable corrections; SQLite close/reopen; all unchanged retries rejected before execution; fifth native strategy accepted")
}

func TestCorrectionRoutesIgnoreUnrelatedProgressButAcceptChangedMaterial(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	call := correctionEditCall("initial-edit", "candidate.txt", "original candidate")
	fixture.execute(t, call)
	failure := sessionRunnerCompletionReviewCorrection{Summary: "Repair the candidate."}
	fixture.reject(t, failure)
	fixture.resume(t)
	fixture.execute(t, correctionEditCall("unrelated-edit", "other.txt", "unrelated material"))
	call.ID = "unrelated-must-not-reopen"
	if fixture.execute(t, call) != 0 {
		t.Fatal("an unrelated successful mutation reopened the rejected strategy")
	}
	fixture.execute(t, correctionEditCall("relevant-edit", "candidate.txt", "materially changed input"))
	call.ID = "related-material-retry"
	if fixture.execute(t, call) != 1 {
		t.Fatal("verified change to this action's file did not permit re-evaluation")
	}
	// Returning to the old material state cannot forget its old membership.
	call.ID = "same-material-again"
	if fixture.execute(t, call) != 0 {
		t.Fatal("returning to the previously rejected material state erased route membership")
	}
	entries, err := fixture.server.loadTranscriptRunnerReplay(context.Background(), fixture.run.Transcript, 1, 1)
	if err != nil || runnerRepeatedCorrectionInterruptionCount(entries, failure.runnerCorrection()) != 1 {
		t.Fatal("tool progress falsely discharged the correction")
	}
}

func TestCorrectionRoutesDoNotCloseAnUnexecutedDecision(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	call := correctionEditCall("decision-preflight", "candidate.txt", "candidate")
	var input map[string]any
	if err := json.Unmarshal(call.Arguments, &input); err != nil {
		t.Fatal(err)
	}
	checkpoint := map[string]any{
		"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed",
		"toolName": call.Name, "toolInput": input,
		"toolResult": map[string]any{
			"ok": true, "executed": false, "decision_required": true,
			"status": "implementation_selection_required",
		},
	}
	if runnerCheckpointHasMaterialProgress(eventjournal.Message(checkpoint)) {
		t.Fatal("a non-executing decision was reported as material progress")
	}
	appendRunnerToolCheckpoint(t, fixture.repo, fixture.run.Transcript.Claim, "unexecuted-decision", checkpoint)
	appendRunnerToolCheckpoint(t, fixture.repo, fixture.run.Transcript.Claim, "legacy-derived-closure", map[string]any{
		"type": "runner_checkpoint", "status": "failed", "toolPhase": prestartToolFailurePhase,
		"toolName": call.Name, "toolInput": input, "rejectedBeforeExecution": true,
		"toolResult": map[string]any{"ok": false, "executed": false, "code": "correction_route_closed"},
	})
	fixture.reject(t, sessionRunnerCompletionReviewCorrection{Summary: "Continue the current task."})
	fixture.resume(t)
	call.ID = "retry-after-decision"
	diagnostics, err := fixture.gateway().correctionRoutePreflight(context.Background(), []agentruntime.ToolCall{call})
	if err != nil || diagnostics[0] != "" {
		t.Fatalf("unexecuted decision closed the route: diagnostics=%v err=%v", diagnostics, err)
	}
}

func TestDerivedClosureExemptionRequiresHostPrestartProvenance(t *testing.T) {
	entry := eventjournal.Entry{SourceEventType: "runner_checkpoint", Message: eventjournal.Message{
		"type": "runner_checkpoint", "status": "failed", "toolPhase": "failed",
		"toolName": "managed_tool", "rejectedBeforeExecution": false,
		"toolResult": map[string]any{"ok": false, "executed": false, "code": "correction_route_closed"},
	}}
	if !runnerCorrectionRouteReceipt(entry) {
		t.Fatal("tool-authored closed-route code erased an actual failure receipt")
	}
}

func TestCorrectionRouteQuarantineUsesRecoveryContractRevision(t *testing.T) {
	for _, revision := range []int{sessionRunnerRecoveryContractRevision - 1, sessionRunnerRecoveryContractRevision} {
		t.Run(fmt.Sprint(revision), func(t *testing.T) {
			f := newCorrectionRouteFixture(t)
			call := correctionEditCall("rejected-edit", "candidate.txt", "candidate")
			var input map[string]any
			if err := json.Unmarshal(call.Arguments, &input); err != nil {
				t.Fatal(err)
			}
			appendRunnerToolCheckpoint(t, f.repo, f.claim, "rejected-route", map[string]any{
				"status": "failed", "toolPhase": prestartToolFailurePhase, "toolName": call.Name,
				"toolInput": input, "rejectedBeforeExecution": true,
				"toolResult": map[string]any{"ok": false, "executed": false, "code": "invalid_tool_arguments"},
			})
			cause := sessionRunnerPlanStepsIncomplete{condition: transcriptstore.RunnerPlanCondition{
				ArtifactID: "plan", VersionID: "version", Steps: []transcriptstore.RunnerPlanConditionStep{{ID: "work", Title: "Analyze"}},
			}}.runnerCorrection()
			payload, err := transcriptstore.RunnerInterruptionCausePayload(cause)
			if err != nil {
				t.Fatal(err)
			}
			appendRunnerToolCheckpoint(t, f.repo, f.claim, "rejected-condition", map[string]any{
				"status": "interrupted", "reason_code": cause.ReasonCode, "resume_detail": cause.Detail,
				"recovery_contract_revision": revision, transcriptstore.RunnerInterruptionCauseField: payload,
			})
			f.run.CorrectionReason, f.run.CorrectionDetail, f.run.CorrectionCondition = cause.ReasonCode, cause.Detail, cause.Condition
			diagnostics, err := f.gateway().correctionRoutePreflight(context.Background(), []agentruntime.ToolCall{call})
			if err != nil || (diagnostics[0] != "") != (revision == sessionRunnerRecoveryContractRevision) {
				t.Fatalf("quarantine did not honor its admission contract revision: %#v %v", diagnostics, err)
			}
		})
	}
}

func TestCorrectionRoutesRespectCancellationOwnerBranchAndNewInput(t *testing.T) {
	for _, change := range []string{"cancel", "owner", "branch", "input"} {
		t.Run(change, func(t *testing.T) {
			fixture := newCorrectionRouteFixture(t)
			call := correctionEditCall("original-route", "candidate.txt", "candidate")
			fixture.execute(t, call)
			fixture.reject(t, sessionRunnerCompletionReviewCorrection{Summary: "Repair the candidate."})
			fixture.resume(t)
			ctx := context.Background()
			switch change {
			case "cancel":
				canceled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = canceled
			case "owner":
				fixture.run.Transcript.Claim.OwnerID = "foreign-owner"
			case "input":
				_, _, _, err := fixture.repo.AppendFrameUserEvent(ctx, transcriptstore.AppendFrameUserEventInput{StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, ClientMessageID: "new-task", FrameEventID: "new-task-event", MessageUUID: "new-task-message", Text: "Start a different task."})
				if err != nil {
					t.Fatal(err)
				}
			case "branch":
				branch, err := fixture.repo.GetBranchState(ctx, fixture.stream.UID, fixture.stream.OwnerID)
				if err != nil {
					t.Fatal(err)
				}
				_, err = fixture.repo.ForkFrameUserMessageBranch(ctx, transcriptstore.ForkFrameUserMessageBranchInput{StreamUID: fixture.stream.UID, OwnerID: fixture.stream.OwnerID, SourceBranchID: branch.ActiveBranchID, ExpectedActiveBranchID: branch.ActiveBranchID, ExpectedGeneration: branch.Generation, ClientMutationID: "branch-task", SourceClientMessageID: "save-user", SourceMessageIndex: 0, ReplacementText: "Work on the corrected task.", Destinations: []string{"ws"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := fixture.gateway().correctionRoutePreflight(ctx, []agentruntime.ToolCall{call})
			if err == nil || change == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("stale authority admitted a correction route: %v", err)
			}
		})
	}
}

func TestCorrectionRoutesPreserveCompleteConditionReadAndLegacyCause(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	call := correctionEditCall("legacy-route", "candidate.txt", "candidate")
	fixture.execute(t, call)
	cause := transcriptstore.RunnerInterruptionCause{ReasonCode: "completion_review_correction_required", Detail: "Historical exact correction"}
	_, err := fixture.repo.InterruptRunner(context.Background(), transcriptstore.InterruptRunnerInput{Claim: fixture.run.Transcript.Claim, ClientMessageID: "legacy-exhausted", ReasonCode: sessionRunnerCorrectionNoProgressExhaustedReasonCode, ResumeDetail: "Historical bounded attempt ended", Cause: &cause, Resumable: true})
	if err != nil {
		t.Fatal(err)
	}
	if candidate, found, err := fixture.repo.GetAutoResumeCandidate(context.Background(), fixture.stream.UID, fixture.stream.OwnerID); err != nil || !found || !runnerInterruptionAutoResume(candidate.ReasonCode) {
		t.Fatalf("retired policy still needs a manual continuation: candidate=%#v found=%t error=%v", candidate, found, err)
	}
	fixture.resume(t)
	if fixture.run.CorrectionCondition != nil || fixture.run.CorrectionDetail != cause.Detail {
		t.Fatal("legacy state was promoted into invented complete evidence")
	}
	diagnostics, err := fixture.gateway().correctionRoutePreflight(context.Background(), []agentruntime.ToolCall{call})
	if err != nil || !strings.Contains(diagnostics[0], "correction_route_closed") {
		t.Fatalf("legacy exact route lost membership: %v %v", diagnostics, err)
	}
	// A new full condition supersedes the historical preview. It remains
	// navigable through the existing authorized native read_file adapter.
	failure := sessionRunnerCompletionReviewCorrection{Summary: strings.Repeat("complete diagnostic 界 ", 1200)}
	fixture.reject(t, failure)
	fixture.resume(t)
	raw, _ := json.Marshal(map[string]any{"recovery_condition_id": fixture.run.CorrectionCondition.ContentID(), "offset": 1, "limit": 20, "human_description": "Reading the persisted condition"})
	read := agentruntime.ToolCall{ID: "condition-inspection", Name: "read_file", Arguments: raw}
	if fixture.execute(t, read) != 1 {
		t.Fatal("full condition became unavailable after route quarantine")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := fixture.gateway().correctionRoutePreflight(ctx, []agentruntime.ToolCall{read}); err != nil {
		t.Fatal(err)
	}
}

func TestCorrectionRoutesRetainMembershipBeyondReplayAndDisplayWindows(t *testing.T) {
	fixture := newCorrectionRouteFixture(t)
	calls := make([]agentruntime.ToolCall, 12)
	for index := range calls {
		calls[index] = correctionEditCall(fmt.Sprintf("candidate-%d", index), fmt.Sprintf("candidate-%d.txt", index), "candidate")
		if fixture.execute(t, calls[index]) != 1 {
			t.Fatal("initial strategy did not execute")
		}
	}
	for index := 0; index < 1040; index++ {
		appendRunnerToolCheckpoint(t, fixture.repo, fixture.run.Transcript.Claim, fmt.Sprintf("route-audit-%d", index), map[string]any{"status": "running", "audit": index})
	}
	fixture.reject(t, sessionRunnerCompletionReviewCorrection{Summary: "Correct the candidate set."})
	fixture.resume(t)
	var input map[string]any
	if err := json.Unmarshal(calls[0].Arguments, &input); err != nil {
		t.Fatal(err)
	}
	input["human_description"] = "A different presentation label for the same action"
	cosmetic, _ := json.Marshal(input)
	calls = append(calls, agentruntime.ToolCall{ID: "cosmetic-only", Name: "edit_file", Arguments: cosmetic})
	diagnostics, err := fixture.gateway().correctionRoutePreflight(context.Background(), calls)
	if err != nil || len(diagnostics) != len(calls) {
		t.Fatalf("full canonical route membership was clipped: closed=%d calls=%d error=%v", len(diagnostics), len(calls), err)
	}
	t.Logf("retained %d closed action identities beyond 1040 audit checkpoints and replay limit 20", len(calls))
}
