package server

import "testing"

func TestRepeatedSemanticCorrectionsRemainAutomaticallyResumable(t *testing.T) {
	for _, reason := range []string{
		sessionRunnerRealScientificEvidenceRequiredReasonCode,
		"artifact_reference_correction_required",
		"completion_review_correction_required",
		sessionRunnerRequiredToolChoiceUnsatisfiedReasonCode,
		sessionRunnerPlanStepsIncompleteReasonCode,
		sessionRunnerVisualArtifactValidationReasonCode,
		sessionRunnerToolRoundNoProgressExhaustedReasonCode,
		sessionRunnerCompletionReviewRecoveryReasonCode,
		sessionRunnerEmptyFinalResponseReasonCode,
		sessionRunnerContentDeltaPersistenceDeadlineReasonCode,
	} {
		if !runnerInterruptionMayContinueSameTask(reason) {
			t.Fatalf("reason %q must remain eligible for same-task continuation", reason)
		}
		if !runnerInterruptionAutoResume(reason) {
			t.Fatalf("reason %q must remain automatically resumable", reason)
		}
		if !runnerInterruptionNeedsRecoveryBackoff(reason) {
			t.Fatalf("reason %q must use bounded recovery backoff", reason)
		}
	}
}
