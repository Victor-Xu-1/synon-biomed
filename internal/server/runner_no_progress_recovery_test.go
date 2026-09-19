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
	if got := runnerRepeatedCorrectionInterruptionCount(entries, "artifact_reference_correction_required", detail); got != 2 {
		t.Fatalf("repeated correction count=%d, want 2", got)
	}
	var integrity *sessionRunnerReferenceIntegrityError
	integrity = &sessionRunnerReferenceIntegrityError{MissingRequiredDeliverables: []string{"at least one verified downloadable artifact"}}
	correction, ok := any(integrity).(sessionRunnerBoundedCorrection)
	if !ok {
		t.Fatal("reference integrity failure is not a bounded correction")
	}
	if reason, gotDetail := correction.runnerCorrection(); reason != "artifact_reference_correction_required" || gotDetail != integrity.Error() {
		t.Fatalf("correction=(%q,%q), want typed integrity correction", reason, gotDetail)
	}
}

func TestNoProgressRouteQuarantineIgnoresPresentationLabelsAndClearsOnMaterialProgress(t *testing.T) {
	run := &sessionRunnerChatRun{TaskIntent: "complete the task", TaskIntentRevision: 1}
	run.restoreNoProgressRecovery(newSessionRunnerNoProgressRecovery(
		runnerRecoveryObligationFingerprint(nil, "", ""),
	))
	run.recordNoProgress([]agentruntime.ToolCall{{
		Name:      "update_step_status",
		Arguments: json.RawMessage(`{"step":"module-1","status":"in_progress","human_description":"first label"}`),
	}})
	blocked := run.noProgressRoutePreflight(
		"update_step_status",
		json.RawMessage(`{"step":"module-1","status":"in_progress","human_description":"different label"}`),
		"update_step_status",
		map[string]any{"step": "module-1", "status": "in_progress", "human_description": "different label"},
	)
	if stringValue(blocked["status"]) != "durable_no_progress_route_closed" {
		t.Fatalf("presentation-only change reopened a closed route: %#v", blocked)
	}
	changed := run.noProgressRoutePreflight(
		"update_step_status",
		json.RawMessage(`{"step":"module-1","status":"completed"}`),
		"update_step_status", map[string]any{"step": "module-1", "status": "completed"},
	)
	if changed != nil {
		t.Fatalf("materially changed action was quarantined: %#v", changed)
	}
	run.recordMaterialProgress()
	if blockedAfterProgress := run.noProgressRoutePreflight(
		"update_step_status",
		json.RawMessage(`{"step":"module-1","status":"in_progress"}`),
		"update_step_status", map[string]any{"step": "module-1", "status": "in_progress"},
	); blockedAfterProgress != nil {
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
		authority, "artifact_reference_correction_required", correctionDetail,
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
}
