package server

import (
	"context"
	"fmt"
	"strings"

	sessionstore "synon-go/internal/persistence/sessions"
	transcriptstore "synon-go/internal/persistence/transcript"
)

// sessionRunnerCompletionRecoveryCandidate is an immutable final candidate
// reconstructed from a failed attempt's published stream. It intentionally
// contains no provider replay state: completion recovery may validate and
// finish this exact candidate, but it must never generate a replacement.
type sessionRunnerCompletionRecoveryCandidate struct {
	Content       string
	Detail        string
	ReasonCode    string
	SourceAttempt int64
	Segment       transcriptstore.AssistantSegmentV1
	SourceRun     *sessionRunnerChatRun
}

type sessionRunnerCompletionRecoveryDelta struct {
	index          int
	text           string
	segmentOrdinal int64
}

// loadTranscriptCompletionRecoveryCandidate recognizes only a complete,
// terminally rejected streamed candidate from the immediately preceding
// attempt. Any ambiguity deliberately falls back to ordinary checkpoint
// execution recovery, which is required for interrupted tools and partial
// streams.
func (s *Server) loadTranscriptCompletionRecoveryCandidate(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
) (sessionRunnerCompletionRecoveryCandidate, bool, error) {
	if s == nil || s.transcriptStore == nil || authority == nil ||
		authority.Claim.ResumeSource != transcriptstore.ResumeSourceCheckpoint ||
		authority.Claim.Attempt <= 1 {
		return sessionRunnerCompletionRecoveryCandidate{}, false, nil
	}
	// A correction interruption reclaims the same logical runner attempt. Its
	// checkpoint belongs to that current attempt, so ordinary checkpoint replay
	// must generate the repaired candidate. Completion-only recovery applies
	// only when a new attempt was opened from the immediately preceding
	// terminal attempt's checkpoint; looking farther back can mistake an older,
	// unrelated failed candidate for the current correction target.
	if authority.Claim.ResumeCheckpointAttempt == authority.Claim.Attempt {
		return sessionRunnerCompletionRecoveryCandidate{}, false, nil
	}
	if authority.Claim.ResumeCheckpointAttempt != authority.Claim.Attempt-1 {
		return sessionRunnerCompletionRecoveryCandidate{}, false, transcriptstore.ErrCheckpointUnavailable
	}
	previous, err := s.transcriptStore.GetRunnerRuntimeState(
		ctx, authority.Stream.UID, authority.Stream.OwnerID, authority.Claim.Attempt-1,
	)
	if err != nil {
		return sessionRunnerCompletionRecoveryCandidate{}, false, fmt.Errorf("load prior completion attempt: %w", err)
	}
	if !runnerResumeContinuesPriorInput(authority, previous) ||
		previous.Status != "failed" || previous.FinishedEventID <= 0 {
		return sessionRunnerCompletionRecoveryCandidate{}, false, nil
	}

	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, authority.Stream.UID, authority.Stream.OwnerID)
	if err != nil {
		return sessionRunnerCompletionRecoveryCandidate{}, false, fmt.Errorf("load completion recovery snapshot: %w", err)
	}
	var terminalPayload map[string]any
	deltas := make([]sessionRunnerCompletionRecoveryDelta, 0)
	resetSeen := false
	err = s.visitTranscriptWebProjectionSnapshotRaw(
		ctx, authority.Stream, authority.Stream.OwnerID, snapshot, true,
		func(projected transcriptstore.ProjectedEvent) error {
			if projected.Event.RunnerAttempt == nil || *projected.Event.RunnerAttempt != previous.Attempt {
				return nil
			}
			payload, err := transcriptPayloadObject(projected.ResolvedPayloadJSON)
			if err != nil {
				return err
			}
			switch projected.Event.Type {
			case "content_delta":
				// Localized reasoning/activity text is UI state, not a provider-
				// authored completion candidate. Every such projection uses the
				// reserved zero delta index; older and provider-neutral producers do
				// not all set synthetic_progress=true. The durable index is therefore
				// the authority: skip zero and fail closed on every other malformed
				// shape.
				if payload["synthetic_progress"] == true {
					return nil
				}
				segment, present, err := transcriptstore.ParseAssistantSegmentV1(payload)
				if err != nil || !present {
					return transcriptstore.ErrEventConflict
				}
				index, valid := exactPositiveInt(payload["delta_index"])
				if !valid {
					if zero, exact := transcriptWebNonnegativeSafeInteger(payload["delta_index"]); exact && zero == 0 {
						return nil
					}
					return transcriptstore.ErrEventConflict
				}
				text := stringValue(payload["text"])
				if strings.TrimSpace(text) == "" {
					// Provider streams may publish empty keep-alive deltas between
					// substantive segments. They carry no candidate content and must
					// not turn completion-only recovery into a terminal dispatch error.
					return nil
				}
				deltas = append(deltas, sessionRunnerCompletionRecoveryDelta{
					index: index, text: text, segmentOrdinal: segment.Ordinal,
				})
			case "content_reset":
				resetSeen = true
			case "runner_finished":
				if projected.Event.EventID != previous.FinishedEventID || terminalPayload != nil {
					return transcriptstore.ErrEventConflict
				}
				terminalPayload = payload
			}
			return nil
		},
	)
	if err != nil {
		return sessionRunnerCompletionRecoveryCandidate{}, false, fmt.Errorf("read completion recovery candidate: %w", err)
	}
	if resetSeen || terminalPayload == nil || strings.TrimSpace(stringValue(terminalPayload["status"])) != "failed" {
		return sessionRunnerCompletionRecoveryCandidate{}, false, nil
	}
	detail := strings.TrimSpace(stringValue(terminalPayload["detail"]))
	reasonCode := priorTerminalCorrectionReason(detail)
	if reasonCode == "" {
		return sessionRunnerCompletionRecoveryCandidate{}, false, nil
	}
	segment, present, err := transcriptstore.ParseAssistantSegmentV1(terminalPayload)
	if err != nil || !present || segment.Ordinal <= 0 {
		return sessionRunnerCompletionRecoveryCandidate{}, false, transcriptstore.ErrEventConflict
	}
	content, ok := contiguousCompletionRecoveryDeltaText(deltas, segment.Ordinal)
	if !ok {
		return sessionRunnerCompletionRecoveryCandidate{}, false, nil
	}
	sourceAuthority := &transcriptRunnerAuthority{Stream: authority.Stream, Claim: transcriptstore.RunnerClaim{
		StreamUID: authority.Stream.UID, OwnerID: authority.Stream.OwnerID, RunnerID: previous.RunnerID,
		Attempt: previous.Attempt,
	}}
	return sessionRunnerCompletionRecoveryCandidate{
		Content: content, Detail: detail, ReasonCode: reasonCode, SourceAttempt: previous.Attempt,
		Segment: segment, SourceRun: &sessionRunnerChatRun{
			SessionID: authority.Stream.SessionID, Attempt: int(previous.Attempt), Transcript: sourceAuthority,
		},
	}, true, nil
}

// contiguousCompletionRecoveryDeltaText verifies the terminal segment is a
// contiguous persisted stream, excluding any preceding tool-round segment.
func contiguousCompletionRecoveryDeltaText(
	deltas []sessionRunnerCompletionRecoveryDelta,
	segmentOrdinal int64,
) (string, bool) {
	if len(deltas) == 0 || segmentOrdinal <= 0 {
		return "", false
	}
	selected := make([]sessionRunnerCompletionRecoveryDelta, 0, len(deltas))
	for _, delta := range deltas {
		if delta.segmentOrdinal == segmentOrdinal {
			selected = append(selected, delta)
		}
	}
	if len(selected) == 0 {
		return "", false
	}
	var content strings.Builder
	firstIndex := selected[0].index
	if firstIndex <= 0 {
		return "", false
	}
	for index, delta := range selected {
		if delta.index != firstIndex+index {
			return "", false
		}
		content.WriteString(delta.text)
	}
	return content.String(), strings.TrimSpace(content.String()) != ""
}

// validateRecoveredTranscriptCompletionCandidate repeats only deterministic
// completion gates over immutable durable evidence. It does not construct an
// agent engine and therefore cannot contact a provider or execute a tool.
func (s *Server) validateRecoveredTranscriptCompletionCandidate(
	ctx context.Context,
	session sessionstore.Session,
	candidate sessionRunnerCompletionRecoveryCandidate,
) error {
	if candidate.SourceRun == nil || strings.TrimSpace(candidate.Content) == "" {
		return transcriptstore.ErrEventConflict
	}
	commits, err := s.sessionRunnerArtifactCommitReferences(ctx, candidate.SourceRun)
	if err != nil {
		return err
	}
	validationCommits, err := s.sessionRunnerActiveArtifactCommitReferences(
		ctx, candidate.SourceRun, commits, candidate.Content,
	)
	if err != nil {
		return err
	}
	missingRequiredDeliverables, err := s.sessionRunnerMissingRequiredDeliverables(
		session, candidate.SourceRun, commits,
	)
	if err != nil {
		return err
	}
	unresolvedReferences, err := s.unresolvedSessionRunnerArtifactReferences(session, candidate.SourceRun, commits, candidate.Content)
	if err != nil {
		return err
	}
	unresolved := len(unresolvedReferences)
	malformedArtifactReferences := sessionRunnerArtifactReferenceSyntaxFailures(candidate.Content)
	scientificFailures, err := s.validateSessionRunnerScientificArtifacts(ctx, sessionRunnerProjectID(session), validationCommits, candidate.Content)
	if err != nil {
		return err
	}
	crossArtifactFailures, err := s.validateSessionRunnerCrossArtifactConsistency(
		ctx, sessionRunnerProjectID(session), validationCommits, candidate.Content,
	)
	if err != nil {
		return err
	}
	researchValidation, err := s.validateSessionRunnerResearchArtifactSelection(ctx, sessionRunnerProjectID(session), validationCommits)
	if err != nil {
		return err
	}
	missingLocalArtifacts, err := s.sessionRunnerMissingLocalArtifactDependencies(
		sessionRunnerProjectID(session), candidate.SourceRun, validationCommits, candidate.Content,
	)
	if err != nil {
		return err
	}
	artifactCandidates, err := s.sessionRunnerCompletionArtifactCandidateReferencesWithSelection(
		session, candidate.SourceRun, validationCommits, candidate.Content,
		researchValidation.SelectedVersions, researchValidation.ManifestFound,
	)
	if err != nil {
		return err
	}
	durableEvidence, err := s.sessionRunnerDurableEvidenceMessages(ctx, candidate.SourceRun)
	if err != nil {
		return err
	}
	unsupported := []string(nil)
	if sessionRunnerCitationIntegrityRequired(candidate.SourceRun) {
		unsupported = unsupportedSessionRunnerCitationReferencesWithArtifactCandidatesUsing(
			durableEvidence, len(durableEvidence), candidate.Content, nil, artifactCandidates, s.sessionRunnerEvidenceTool,
		)
	}
	sessionRunnerObserveUnsupportedCitationAdvisories(unsupported)
	fatalCrossArtifactFailures := sessionRunnerFatalCrossArtifactFailures(candidate.SourceRun, crossArtifactFailures)
	if unresolved == 0 && len(malformedArtifactReferences) == 0 && len(artifactCandidates.contractFailures) == 0 &&
		len(scientificFailures) == 0 && len(fatalCrossArtifactFailures) == 0 && len(researchValidation.Failures) == 0 && len(missingLocalArtifacts) == 0 &&
		len(missingRequiredDeliverables) == 0 {
		return nil
	}
	return &sessionRunnerReferenceIntegrityError{
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

func (s *Server) recoverTranscriptCompletionOnly(
	ctx context.Context,
	session sessionstore.Session,
	run *sessionRunnerChatRun,
) (status, detail string, segment transcriptstore.AssistantSegmentV1, handled bool, err error) {
	if run == nil || run.Transcript == nil {
		return "", "", transcriptstore.AssistantSegmentV1{}, false, nil
	}
	candidate, found, err := s.loadTranscriptCompletionRecoveryCandidate(ctx, run.Transcript)
	if err != nil || !found {
		return "", "", transcriptstore.AssistantSegmentV1{}, false, err
	}
	if !transcriptCompletionOnlyRecoveryHandles(candidate.ReasonCode) {
		return "", "", transcriptstore.AssistantSegmentV1{}, false, nil
	}
	if candidate.ReasonCode == "artifact_reference_correction_required" {
		if validationErr := s.validateRecoveredTranscriptCompletionCandidate(ctx, session, candidate); validationErr != nil {
			return "failed", validationErr.Error(), candidate.Segment, true, nil
		}
		if err := s.appendTranscriptAssistantEvent(context.WithoutCancel(ctx), run.Transcript, "completion-recovery-assistant", map[string]any{
			"text": candidate.Content, "assistant_segment": transcriptstore.AssistantSegmentPayloadV1(candidate.Segment.Ordinal, ""),
		}); err != nil {
			return "failed", fmt.Sprintf("append recovered transcript assistant message failed: %v", err), candidate.Segment, true, nil
		}
		return "completed", "runner completion validation recovered immutable published candidate", candidate.Segment, true, nil
	}
	// Reviewer verdicts are intentionally not replayed: an independent review
	// can invoke a provider and read-only tools, both forbidden on this recovery
	// path. An explicit capsule restart must nevertheless enter the ordinary
	// checkpoint correction path so the model can inspect and repair the rejected
	// artifact. Returning "not handled" here does exactly that; unattended review
	// auto-resume remains disabled, so a repeated rejection cannot form a loop.
	return "", "", transcriptstore.AssistantSegmentV1{}, false, nil
}

func transcriptCompletionOnlyRecoveryHandles(reasonCode string) bool {
	return strings.TrimSpace(reasonCode) == "artifact_reference_correction_required"
}
