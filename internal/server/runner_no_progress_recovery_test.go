package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

func TestRepeatedCompletionCorrectionIsBoundedByItsDurableFingerprint(t *testing.T) {
	detail := "runner completion reference integrity failed (missing_required_deliverables=1)"
	entries := []eventjournal.Entry{
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "reason_code": "artifact_reference_correction_required",
			"resume_detail": detail,
		}},
		{Message: eventjournal.Message{
			"type": "runner_checkpoint", "reason_code": "artifact_reference_correction_required",
			"resume_detail": detail + sessionRunnerNoProgressDetailMarker + `{"schema":"synon.runner_no_progress.v1"}`,
		}},
	}
	if got := runnerRepeatedCorrectionInterruptionCount(entries, transcriptstore.RunnerInterruptionCause{ReasonCode: "artifact_reference_correction_required", Detail: detail}); got != 2 {
		t.Fatalf("repeated correction count=%d, want 2", got)
	}
	var integrity *sessionRunnerReferenceIntegrityError
	integrity = &sessionRunnerReferenceIntegrityError{MissingRequiredDeliverables: []string{"at least one verified downloadable artifact"}}
	correction, ok := any(integrity).(sessionRunnerBoundedCorrection)
	if !ok {
		t.Fatal("reference integrity failure is not a bounded correction")
	}
	if cause := correction.runnerCorrection(); cause.ReasonCode != "artifact_reference_correction_required" || cause.Detail != integrity.Error() || cause.Condition == nil {
		t.Fatalf("correction=%#v, want typed integrity correction", cause)
	}
}

func TestUnavailableSourceDoesNotResetSemanticProgress(t *testing.T) {
	for _, test := range []struct {
		name     string
		result   map[string]any
		progress bool
	}{
		{"source unavailable", map[string]any{"sourceUnavailable": true, "complete": true, "bytesRead": 146}, false},
		{"native envelope", map[string]any{"ok": true, "result": map[string]any{"sourceUnavailable": true, "statusCode": 403}}, false},
		{"recoverable failure", map[string]any{"failure": map[string]any{"recoverable": true, "kind": "source_unavailable"}}, false},
		{"unavailable status", map[string]any{"status": "unavailable"}, false},
		{"all sources unavailable", map[string]any{"sources": []any{map[string]any{"status": "unavailable"}}}, false},
		{"failed result", map[string]any{"ok": false}, false},
		{"unchanged result", map[string]any{"ok": true, "effect": map[string]any{"state": "unchanged"}}, false},
		{"cached result", map[string]any{"ok": true, "reused": true}, false},
		{"successful read", map[string]any{"ok": true, "content": "new source content"}, true},
		{"partial usable output", map[string]any{"partial": true, "content": "usable source subset"}, true},
		{"body is not status", map[string]any{"ok": true, "body": map[string]any{"sourceUnavailable": true}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := runnerToolCompletionHasMaterialProgress("web_fetch", test.result); got != test.progress {
				t.Errorf("live progress=%t, want %t", got, test.progress)
			}
			entries := []eventjournal.Entry{
				{Message: eventjournal.Message{"type": "runner_checkpoint", "reason_code": sessionRunnerToolRoundNoProgressReasonCode}},
				{Message: eventjournal.Message{"type": "runner_checkpoint", "status": "completed", "toolPhase": "completed", "toolName": "web_fetch", "toolResult": test.result}},
			}
			wantCount := 1
			if test.progress {
				wantCount = 0
			}
			if got := runnerToolRoundNoProgressInterruptionCount(entries); got != wantCount {
				t.Errorf("replayed no-progress count=%d, want %d", got, wantCount)
			}
		})
	}
}

func TestNoProgressRecoveryPreviewPreservesIdentityAndProgressReset(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "complete the task", TaskIntentRevision: 1}
	run.restoreNoProgressRecovery(newSessionRunnerNoProgressRecovery(
		runnerRecoveryObligationFingerprint(nil, transcriptstore.RunnerInterruptionCause{}),
	))
	run.recordNoProgress([]agentruntime.ToolCall{{
		Name:      "update_step_status",
		Arguments: json.RawMessage(`{"step":"module-1","status":"in_progress","human_description":"first label"}`),
	}})
	blocked := run.NoProgressRecovery.containsFingerprint(agentruntime.ExecutionCallFingerprint(
		"update_step_status",
		json.RawMessage(`{"step":"module-1","status":"in_progress","human_description":"different label"}`),
	))
	if !blocked {
		t.Fatalf("presentation-only change reopened a closed route: %#v", blocked)
	}
	changed := run.NoProgressRecovery.containsFingerprint(agentruntime.ExecutionCallFingerprint(
		"update_step_status",
		json.RawMessage(`{"step":"module-1","status":"completed"}`),
	))
	if changed {
		t.Fatalf("materially changed action was quarantined: %#v", changed)
	}
	run.recordMaterialProgress()
	if blockedAfterProgress := run.NoProgressRecovery.containsFingerprint(agentruntime.ExecutionCallFingerprint(
		"update_step_status",
		json.RawMessage(`{"step":"module-1","status":"in_progress"}`),
	)); blockedAfterProgress {
		t.Fatalf("material progress did not release prior route: %#v", blockedAfterProgress)
	}
}

func TestLongReplayRestoresCorrectionAndNoProgressStateOutsideProviderWindow(t *testing.T) {
	store, repo, _ := newTranscriptWebFixture(t)
	seedTranscriptWebFrame(t, store, "local", "project-no-progress-replay", "frame-no-progress-replay")
	server := New(Options{Workspace: store, Transcript: repo, FileRoot: t.TempDir()})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Close(ctx)
	})
	if _, _, err := server.submitFrameMessage(store, frameMessageSubmission{
		FrameID: "frame-no-progress-replay", MessageUUID: "message-no-progress-replay",
		ClientMessageID: "client-no-progress-replay", Text: "Complete the source-backed report.",
	}); err != nil {
		t.Fatal(err)
	}
	stream, found, err := repo.GetFrameStreamBySession(context.Background(), "local", "frame-no-progress-replay")
	if err != nil || !found {
		t.Fatalf("stream=%#v found=%t err=%v", stream, found, err)
	}
	claimed, err := repo.ClaimRunner(context.Background(), transcriptstore.ClaimRunnerInput{
		StreamUID: stream.UID, OwnerID: stream.OwnerID, RunnerID: "no-progress-replay-runner",
		TTL: time.Minute, ResumeSource: transcriptstore.ResumeSourceFresh,
	})
	if err != nil || !claimed.Claimed {
		t.Fatalf("claim=%#v err=%v", claimed, err)
	}
	authority := &transcriptRunnerAuthority{Stream: stream, Claim: claimed.Claim}
	correctionDetail := "runner completion reference integrity failed (missing_required_deliverables=1)"
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "no-progress-correction", map[string]any{
		"status": "interrupted", "reason_code": "artifact_reference_correction_required",
		"resume_detail": correctionDetail,
	})
	state := newSessionRunnerNoProgressRecovery(runnerRecoveryObligationFingerprint(
		authority, transcriptstore.RunnerInterruptionCause{ReasonCode: "artifact_reference_correction_required", Detail: correctionDetail},
	))
	state.recordNoProgress([]agentruntime.ToolCall{{
		Name: "save_artifacts", Arguments: json.RawMessage(`{"files":["report.md"]}`),
	}})
	appendRunnerToolCheckpoint(t, repo, claimed.Claim, "no-progress-boundary", map[string]any{
		"status": "interrupted", "reason_code": sessionRunnerToolRoundNoProgressReasonCode,
		"resume_detail": state.resumeDetail("choose a materially different action"),
	})
	for index := 0; index < 240; index++ {
		appendRunnerToolCheckpoint(t, repo, claimed.Claim, fmt.Sprintf("no-progress-audit-%03d", index), map[string]any{
			"status": "running", "audit": index,
		})
	}
	// A completed transport carrying an unavailable source must not reopen the
	// previously closed action when restoring the durable, bounded history.
	run := &sessionRunnerChatRun{SessionID: stream.SessionID, Attempt: int(claimed.Claim.Attempt),
		ClaimToken: claimed.Claim.ClaimToken, Transcript: authority,
		CorrectionReason: "artifact_reference_correction_required", CorrectionDetail: correctionDetail}
	run.restoreNoProgressRecovery(state)
	options := SessionRunnerChatOptions{RunnerID: claimed.Claim.RunnerID, SessionID: stream.SessionID}
	call := agentruntime.ToolCall{ID: "unavailable-read", Name: "web_fetch", Arguments: json.RawMessage(`{"url":"https://example.org/source"}`)}
	if err := server.checkpointChatModelToolCalls(options, run, []agentruntime.ToolCall{call}); err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolStarted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments),
	}); err != nil {
		t.Fatal(err)
	}
	if err := server.checkpointSessionRunnerToolEvent(context.Background(), options, run, agentruntime.Event{
		Type: agentruntime.EventToolCompleted, ToolName: call.Name, ToolCallID: call.ID, Arguments: string(call.Arguments),
		Result: `{"ok":true,"result":{"sourceUnavailable":true,"statusCode":404,"complete":true,"bytesRead":127}}`,
	}); err != nil {
		t.Fatal(err)
	}
	if blocked := run.NoProgressRecovery.containsFingerprint(agentruntime.ExecutionCallFingerprint("save_artifacts", json.RawMessage(`{"files":["report.md"]}`))); !blocked {
		t.Fatalf("unavailable receipt reopened live action: %#v", blocked)
	}

	entries, err := server.loadTranscriptRunnerReplay(context.Background(), authority, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	correction, found := latestRunnerCorrection(entries)
	if !found || correction.ReasonCode != "artifact_reference_correction_required" || correction.Detail != correctionDetail {
		t.Fatalf("bounded replay lost durable correction: found=%t correction=%#v", found, correction)
	}
	restored, found := sessionRunnerNoProgressRecoveryFromReplay(entries)
	if !found || restored.Consecutive != 1 || len(restored.ClosedActions) != 1 ||
		restored.ClosedActions[0].Tool != "save_artifacts" {
		t.Fatalf("bounded replay lost no-progress state: found=%t state=%#v", found, restored)
	}
	runtimeState, err := repo.GetRunnerRuntimeState(context.Background(), stream.UID, stream.OwnerID, claimed.Claim.Attempt)
	if err != nil || runtimeState.Status != "running" {
		t.Fatalf("recoverable unavailable source terminated the runner: state=%#v err=%v", runtimeState, err)
	}
	history, found, err := server.loadTranscriptWebHistory(context.Background(), stream.OwnerID, stream.SessionID)
	if err != nil || !found || len(history) == 0 {
		t.Fatalf("history found=%t err=%v", found, err)
	}
	content, _ := history[len(history)-1]["content"].(map[string]any)
	if content["call_id"] != call.ID || content["status"] != "completed" {
		t.Fatalf("unavailable transport lifecycle was changed: %#v", content)
	}
}
