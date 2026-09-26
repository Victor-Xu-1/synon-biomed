package server

import (
	"fmt"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
)

// All producers retain their complete typed input. This display projection is
// bounded independently, and legacy data is never promoted into typed evidence.
func newRunnerCorrection(reason, display string, condition transcriptstore.RunnerCorrectionCondition) transcriptstore.RunnerInterruptionCause {
	condition.Schema = transcriptstore.RunnerCorrectionConditionSchema
	condition.ReasonCode = reason
	return transcriptstore.RunnerInterruptionCause{ReasonCode: reason,
		Detail: truncateSessionRunnerReferenceDiagnostic(display, maxRunnerCorrectionResumeDetailBytes), Condition: &condition}
}

func newRunnerTextCorrection(reason, detail string) transcriptstore.RunnerInterruptionCause {
	return newRunnerCorrection(reason, detail, transcriptstore.RunnerCorrectionCondition{Text: &transcriptstore.RunnerTextCondition{Detail: detail}})
}

func runnerCorrectionFingerprint(correction transcriptstore.RunnerInterruptionCause) string {
	if correction.Condition != nil {
		return correction.Condition.Fingerprint()
	}
	if correction.ConditionRef != nil {
		return correction.ConditionRef.Fingerprint
	}
	// Historical previews have only their historical identity. Do not claim
	// equality between them and a newly complete condition with hidden fields.
	return correctionRepetitionFingerprint(correction.ReasonCode, correction.Detail)
}

func (correction recoveredRunnerCorrection) cause() transcriptstore.RunnerInterruptionCause {
	return transcriptstore.RunnerInterruptionCause{ReasonCode: correction.ReasonCode, Detail: correction.Detail, Condition: correction.Condition}
}

func (run *sessionRunnerChatRun) correctionCause() transcriptstore.RunnerInterruptionCause {
	if run == nil {
		return transcriptstore.RunnerInterruptionCause{}
	}
	return transcriptstore.RunnerInterruptionCause{ReasonCode: run.CorrectionReason, Detail: run.CorrectionDetail, Condition: run.CorrectionCondition}
}

func (run *sessionRunnerChatRun) restoreCorrection(correction *recoveredRunnerCorrection) {
	if run == nil {
		return
	}
	run.CorrectionReason, run.CorrectionDetail, run.CorrectionCondition = "", "", nil
	if correction != nil {
		run.CorrectionReason = correction.ReasonCode
		run.CorrectionDetail = correction.repairDetail()
		run.CorrectionCondition = correction.Condition
	}
}

// repairDetail is a derived compatibility view for the existing deterministic
// repair classifiers. It is never persisted, hashed or placed unbounded in a
// prompt. Every diagnostic reaches the classifiers, not just a display prefix.
func (correction recoveredRunnerCorrection) repairDetail() string {
	condition := correction.Condition
	if condition == nil {
		return correction.Detail
	}
	switch {
	case condition.Reference != nil:
		reference := sessionRunnerReferenceIntegrityError(*condition.Reference)
		return reference.formatDiagnostics(func(values []string) string { return strings.Join(values, ", ") })
	case condition.Review != nil:
		parts := []string{"completion reviewer rejected the current candidate", condition.Review.Summary}
		for _, issue := range condition.Review.Issues {
			parts = append(parts, issue.Severity+": "+issue.Claim)
		}
		return strings.Join(parts, "; ")
	case condition.Plan != nil:
		values := make([]string, 0, len(condition.Plan.Steps))
		for _, step := range condition.Plan.Steps {
			values = append(values, step.ID+"="+step.Title)
		}
		return fmt.Sprintf("%s: %s", condition.ReasonCode, strings.Join(values, "; "))
	case condition.Visual != nil:
		values := append([]string(nil), condition.Visual.UnboundNames...)
		for _, artifact := range condition.Visual.Artifacts {
			values = append(values, artifact.Name+" version="+artifact.VersionID+" sha256="+artifact.SHA256)
		}
		return fmt.Sprintf("%s: %s", condition.ReasonCode, strings.Join(values, "; "))
	case condition.Text != nil:
		return condition.Text.Detail
	default:
		return correction.Detail
	}
}
