package server

import (
	"fmt"
	"strings"
)

type sessionRunnerReferenceIntegrityError struct {
	UnresolvedArtifacts          int
	UnresolvedArtifactReferences []string
	MalformedArtifactReferences  []string
	UnsupportedCitations         []string
	InvalidReferenceArtifacts    []string
	InvalidScientificArtifacts   []string
	CrossArtifactFailures        []string
	InvalidResearchArtifacts     []string
	MissingLocalArtifacts        []string
	MissingRequiredDeliverables  []string
}

func (err *sessionRunnerReferenceIntegrityError) Error() string {
	if err == nil {
		return "runner completion reference integrity failed"
	}
	details := make([]string, 0, 6)
	if len(err.MalformedArtifactReferences) > 0 {
		details = append(details, "malformed artifact references "+formatSessionRunnerReferenceDiagnostics(err.MalformedArtifactReferences))
	}
	if len(err.UnresolvedArtifactReferences) > 0 {
		details = append(details, "unresolved artifact references "+formatSessionRunnerReferenceDiagnostics(err.UnresolvedArtifactReferences))
	}
	if len(err.UnsupportedCitations) > 0 {
		details = append(details, "unsupported identifiers "+formatSessionRunnerReferenceDiagnostics(err.UnsupportedCitations))
	}
	if len(err.InvalidReferenceArtifacts) > 0 {
		details = append(details, "invalid rejected-reference artifacts "+formatSessionRunnerReferenceDiagnostics(err.InvalidReferenceArtifacts))
	}
	if len(err.InvalidScientificArtifacts) > 0 {
		details = append(details, "invalid scientific artifacts "+formatSessionRunnerReferenceDiagnostics(err.InvalidScientificArtifacts))
	}
	if len(err.CrossArtifactFailures) > 0 {
		details = append(details, "cross-artifact consistency failures "+formatSessionRunnerReferenceDiagnostics(err.CrossArtifactFailures))
	}
	if len(err.InvalidResearchArtifacts) > 0 {
		details = append(details, "invalid research artifacts "+formatSessionRunnerReferenceDiagnostics(err.InvalidResearchArtifacts))
	}
	if len(err.MissingLocalArtifacts) > 0 {
		details = append(details, "missing local artifacts "+formatSessionRunnerReferenceDiagnostics(err.MissingLocalArtifacts))
	}
	if len(err.MissingRequiredDeliverables) > 0 {
		details = append(details, "missing required deliverables "+formatSessionRunnerReferenceDiagnostics(err.MissingRequiredDeliverables))
	}
	detail := ""
	if len(details) > 0 {
		detail = ": " + strings.Join(details, "; ")
	}
	return fmt.Sprintf("runner completion reference integrity failed (unresolved_artifacts=%d malformed_artifact_references=%d unsupported_citations=%d invalid_reference_artifacts=%d invalid_scientific_artifacts=%d cross_artifact_failures=%d invalid_research_artifacts=%d missing_local_artifacts=%d missing_required_deliverables=%d)%s",
		err.UnresolvedArtifacts, len(err.MalformedArtifactReferences), len(err.UnsupportedCitations), len(err.InvalidReferenceArtifacts), len(err.InvalidScientificArtifacts), len(err.CrossArtifactFailures), len(err.InvalidResearchArtifacts), len(err.MissingLocalArtifacts), len(err.MissingRequiredDeliverables), detail)
}
