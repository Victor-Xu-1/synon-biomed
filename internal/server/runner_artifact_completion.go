package server

import (
	"context"

	"synon-go/internal/agentruntime"
	sessionstore "synon-go/internal/persistence/sessions"
)

// Completion validation never asks the model to create a replacement
// candidate in the same execution unit. The only local normalization allowed
// before validation removes malformed artifact-link presentation while
// preserving its visible label; it never invents an artifact identity or
// changes scientific content.
const maxSessionRunnerArtifactReferenceRepairs = 0

func (s *Server) runSessionAgentWithArtifactReferenceRepair(
	ctx context.Context,
	session sessionstore.Session,
	engine agentruntime.Engine,
	request agentruntime.RunRequest,
	run *sessionRunnerChatRun,
	onPlanModeCandidateRejected sessionRunnerPlanModeCandidateRejected,
) (agentruntime.RunResult, error) {
	if request.ToolRoundBudget == nil {
		request.ToolRoundBudget = agentruntime.NewToolRoundBudget(request.MaxToolRounds)
	}
	candidateStart := len(request.Messages)
	result, err := s.runSessionAgentWithPlanMode(
		ctx, session, engine, request, run, onPlanModeCandidateRejected,
	)
	if err != nil {
		return result, err
	}
	artifactCommits, err := s.sessionRunnerArtifactCommitReferences(ctx, run)
	if err != nil {
		return result, err
	}
	finalContent, err := s.sessionRunnerAttachRequiredDeliverables(run, artifactCommits, result.FinalMessage.Content)
	if err != nil {
		return result, err
	}
	if canonical, changed := normalizeSessionRunnerArtifactReferencesToCurrentVersions(finalContent, artifactCommits); changed {
		finalContent = canonical
	}
	replaceSessionRunnerFinalMessageContent(&result, finalContent)
	validationCommits, err := s.sessionRunnerActiveArtifactCommitReferences(
		ctx, run, artifactCommits, result.FinalMessage.Content,
	)
	if err != nil {
		return result, err
	}
	missingRequiredDeliverables, err := s.sessionRunnerMissingRequiredDeliverables(session, run, artifactCommits)
	if err != nil {
		return result, err
	}
	malformedArtifactReferences := sessionRunnerArtifactReferenceSyntaxFailures(result.FinalMessage.Content)
	if len(malformedArtifactReferences) > 0 {
		if normalized, changed := normalizeSessionRunnerMalformedArtifactReferences(result.FinalMessage.Content); changed {
			replaceSessionRunnerFinalMessageContent(&result, normalized)
			malformedArtifactReferences = sessionRunnerArtifactReferenceSyntaxFailures(normalized)
		}
	}
	// Resolve against the normalized candidate. A malformed or non-canonical
	// presentation that was safely reduced to its visible label no longer names
	// an artifact and must not keep a stale unresolved count. Canonical but
	// unknown UUIDs remain present and still fail closed below.
	unresolvedReferences, err := s.unresolvedSessionRunnerArtifactReferences(
		session, run, artifactCommits, result.FinalMessage.Content,
	)
	if err != nil {
		return result, err
	}
	unresolved := len(unresolvedReferences)
	missingLocalArtifacts, err := s.sessionRunnerMissingLocalArtifactDependencies(
		sessionRunnerProjectID(session), run, validationCommits, result.FinalMessage.Content,
	)
	if err != nil {
		return result, err
	}
	artifactCandidates, err := s.sessionRunnerCompletionArtifactCandidateReferences(
		session, run, validationCommits, result.FinalMessage.Content,
	)
	if err != nil {
		return result, err
	}
	durableEvidence, err := s.sessionRunnerDurableEvidenceMessages(ctx, run)
	if err != nil {
		return result, err
	}
	qualifiedEvidence := durableEvidence
	sourceMaterialsObserved := false
	if run != nil && run.Transcript != nil {
		materials, materialErr := s.sessionRunnerResearchMaterials(ctx, run)
		if materialErr != nil {
			return result, materialErr
		}
		if len(materials.Attempts) > 0 || len(materials.Receipts) > 0 {
			sourceMaterialsObserved = true
			qualifiedEvidence = researchQualifiedEvidenceMessages(durableEvidence, materials)
		}
	}
	sourceClaimFailures, err := s.validateSessionRunnerSourceClaimEvidence(
		ctx, sessionRunnerProjectID(session), validationCommits, qualifiedEvidence,
	)
	if err != nil {
		return result, err
	}
	evidenceDepthFailures, err := s.validateSessionRunnerEvidenceRecordDepth(
		ctx, sessionRunnerProjectID(session), validationCommits, qualifiedEvidence,
		run.trustedScientificReviewSignalsSnapshot(), sessionRunnerTaskIntent(run),
	)
	if err != nil {
		return result, err
	}
	evidencePrefix := sessionRunnerDurableEvidencePrefix(qualifiedEvidence, result.Messages)
	evidenceMessages := make([]agentruntime.Message, 0, len(evidencePrefix)+len(result.Messages))
	evidenceMessages = append(evidenceMessages, evidencePrefix...)
	evidenceMessages = append(evidenceMessages, result.Messages...)
	unsupportedCitationRefs := []string(nil)
	if sourceMaterialsObserved || s.sessionRunnerSourceWorkflowActive(run) || sessionRunnerCitationIntegrityRequired(run) {
		unsupportedCitationRefs = unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(
			evidenceMessages, candidateStart+len(evidencePrefix), result.FinalMessage.Content, nil, artifactCandidates,
			s.sessionRunnerEvidenceTool,
		)
	}
	sessionRunnerObserveUnsupportedCitationAdvisories(unsupportedCitationRefs)
	crossArtifactFailures, err := s.validateSessionRunnerCrossArtifactConsistency(
		ctx, sessionRunnerProjectID(session), validationCommits, result.FinalMessage.Content,
	)
	if err != nil {
		return result, err
	}
	crossArtifactFailures = append(
		crossArtifactFailures,
		sessionRunnerArtifactPublicationFreshnessFailures(result.Messages, result.FinalMessage.Content)...,
	)
	crossArtifactFailures = append(crossArtifactFailures, sourceClaimFailures...)
	crossArtifactFailures = append(crossArtifactFailures, evidenceDepthFailures...)
	scientificFailures, err := s.validateSessionRunnerScientificArtifacts(ctx, sessionRunnerProjectID(session), validationCommits, result.FinalMessage.Content)
	if err != nil {
		return result, err
	}
	researchValidation, err := s.validateSessionRunnerResearchArtifactSelection(ctx, sessionRunnerProjectID(session), validationCommits)
	if err != nil {
		return result, err
	}
	artifactCandidates, err = s.sessionRunnerCompletionArtifactCandidateReferencesWithSelection(
		session, run, validationCommits, result.FinalMessage.Content,
		researchValidation.SelectedVersions, researchValidation.ManifestFound,
	)
	if err != nil {
		return result, err
	}
	fatalCrossArtifactFailures := sessionRunnerFatalCrossArtifactFailures(run, crossArtifactFailures)
	if unresolved == 0 && len(malformedArtifactReferences) == 0 && len(artifactCandidates.contractFailures) == 0 &&
		len(missingLocalArtifacts) == 0 && len(fatalCrossArtifactFailures) == 0 && len(scientificFailures) == 0 &&
		len(researchValidation.Failures) == 0 && len(missingRequiredDeliverables) == 0 {
		if err := s.verifySessionRunnerVisualArtifactEvidence(session); err != nil {
			return result, err
		}
		if err := s.publishSessionRunnerCompletionArtifacts(
			ctx, session, run, validationCommits,
		); err != nil {
			return result, err
		}
		return result, nil
	}
	return result, &sessionRunnerReferenceIntegrityError{
		UnresolvedArtifacts:          unresolved,
		UnresolvedArtifactReferences: append([]string(nil), unresolvedReferences...),
		MalformedArtifactReferences:  malformedArtifactReferences,
		UnsupportedCitations:         nil,
		InvalidReferenceArtifacts:    append([]string(nil), artifactCandidates.contractFailures...),
		InvalidScientificArtifacts:   scientificFailures,
		CrossArtifactFailures:        fatalCrossArtifactFailures,
		InvalidResearchArtifacts:     researchValidation.Failures,
		MissingLocalArtifacts:        missingLocalArtifacts,
		MissingRequiredDeliverables:  missingRequiredDeliverables,
	}
}
