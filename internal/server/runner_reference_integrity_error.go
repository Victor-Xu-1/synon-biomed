package server

import (
	"fmt"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
)

type sessionRunnerReferenceIntegrityError transcriptstore.RunnerReferenceCondition

func (err *sessionRunnerReferenceIntegrityError) Error() string {
	return truncateSessionRunnerReferenceDiagnostic(err.formatDiagnostics(formatSessionRunnerReferenceDiagnostics), maxRunnerCorrectionResumeDetailBytes)
}

func (err *sessionRunnerReferenceIntegrityError) formatDiagnostics(format func([]string) string) string {
	if err == nil {
		return "runner completion reference integrity failed"
	}
	details := make([]string, 0, 6)
	if len(err.MalformedArtifactReferences) > 0 {
		details = append(details, "malformed artifact references "+format(err.MalformedArtifactReferences))
	}
	if len(err.UnresolvedArtifactReferences) > 0 {
		details = append(details, "unresolved artifact references "+format(err.UnresolvedArtifactReferences))
	}
	if len(err.UnsupportedCitations) > 0 {
		details = append(details, "unsupported identifiers "+format(err.UnsupportedCitations))
	}
	if len(err.InvalidReferenceArtifacts) > 0 {
		details = append(details, "invalid rejected-reference artifacts "+format(err.InvalidReferenceArtifacts))
	}
	if len(err.InvalidScientificArtifacts) > 0 {
		details = append(details, "invalid scientific artifacts "+format(err.InvalidScientificArtifacts))
	}
	if len(err.CrossArtifactFailures) > 0 {
		details = append(details, "cross-artifact consistency failures "+format(err.CrossArtifactFailures))
	}
	if len(err.InvalidResearchArtifacts) > 0 {
		details = append(details, "invalid research artifacts "+format(err.InvalidResearchArtifacts))
	}
	if len(err.MissingLocalArtifacts) > 0 {
		details = append(details, "missing local artifacts "+format(err.MissingLocalArtifacts))
	}
	if len(err.MissingRequiredDeliverables) > 0 {
		details = append(details, "missing required deliverables "+format(err.MissingRequiredDeliverables))
	}
	detail := ""
	if len(details) > 0 {
		detail = ": " + strings.Join(details, "; ")
	}
	return fmt.Sprintf("runner completion reference integrity failed (unresolved_artifacts=%d malformed_artifact_references=%d unsupported_citations=%d invalid_reference_artifacts=%d invalid_scientific_artifacts=%d cross_artifact_failures=%d invalid_research_artifacts=%d missing_local_artifacts=%d missing_required_deliverables=%d)%s",
		err.UnresolvedArtifacts, len(err.MalformedArtifactReferences), len(err.UnsupportedCitations), len(err.InvalidReferenceArtifacts), len(err.InvalidScientificArtifacts), len(err.CrossArtifactFailures), len(err.InvalidResearchArtifacts), len(err.MissingLocalArtifacts), len(err.MissingRequiredDeliverables), detail)
}

// runnerCorrection keeps completion-integrity failures on the same durable
// correction path as every other bounded recovery. Relying on string parsing
// at settlement loses the machine-readable failure boundary and can cause a
// resumed task to regenerate the same final candidate without retaining the
// correction obligation.
func (err *sessionRunnerReferenceIntegrityError) runnerCorrection() transcriptstore.RunnerInterruptionCause {
	reference := transcriptstore.RunnerReferenceCondition{}
	if err != nil {
		reference = transcriptstore.RunnerReferenceCondition(*err)
	}
	return newRunnerCorrection("artifact_reference_correction_required", err.Error(), transcriptstore.RunnerCorrectionCondition{Reference: &reference})
}
