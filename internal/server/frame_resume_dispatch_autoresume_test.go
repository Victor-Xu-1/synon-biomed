package server

import (
	"testing"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func TestFrameResumeTranscriptHonorsPersistedNonResumableDecision(t *testing.T) {
	result := SessionRunnerCycleResult{
		Status:                 "interrupted",
		InterruptionReasonCode: "artifact_reference_correction_required",
		InterruptionAutoResume: false,
	}
	if frameResumeRunnerShouldAutoResume(true, result, result.InterruptionReasonCode) {
		t.Fatal("canonical Transcript task ignored its persisted non-resumable decision")
	}
	if !frameResumeRunnerShouldAutoResume(false, result, result.InterruptionReasonCode) {
		t.Fatal("legacy non-Transcript task lost its reason-code compatibility fallback")
	}
	result.InterruptionAutoResume = true
	if !frameResumeRunnerShouldAutoResume(true, result, result.InterruptionReasonCode) {
		t.Fatal("canonical Transcript task rejected an explicitly resumable interruption")
	}
}

func TestFrameResumeAlwaysRecoversDurablePendingKernelOperation(t *testing.T) {
	result := SessionRunnerCycleResult{
		Status:                 "interrupted",
		InterruptionReasonCode: sessionRunnerKernelOperationPendingRecoveryReasonCode,
		InterruptionAutoResume: false,
	}
	if !frameResumeRunnerShouldAutoResume(true, result, result.InterruptionReasonCode) {
		t.Fatal("canonical Transcript task terminalized a durable pending kernel operation")
	}
	if notBefore := frameResumeDispatchAutoResumePolicy(
		result.InterruptionReasonCode, 100, "frame-kernel", time.Now().UTC(),
	); notBefore.IsZero() {
		t.Fatalf("pending kernel recovery policy notBefore=%v", notBefore)
	}
}

func TestFrameResumeKernelOperationReadyOnlyForConsumableDurableStates(t *testing.T) {
	tests := map[string]bool{
		workspace.KernelLocalOperationStatePendingApproval: false,
		workspace.KernelLocalOperationStatePrepared:        false,
		workspace.KernelLocalOperationStateStarted:         false,
		workspace.KernelLocalOperationStateApproved:        true,
		workspace.KernelLocalOperationStateCompleted:       true,
		workspace.KernelLocalOperationStateFailed:          true,
		workspace.KernelLocalOperationStateCancelled:       true,
		workspace.KernelLocalOperationStateOutcomeUnknown:  true,
	}
	for state, want := range tests {
		if got := frameResumeKernelOperationReady(state); got != want {
			t.Fatalf("state %q ready=%t want=%t", state, got, want)
		}
	}
}
