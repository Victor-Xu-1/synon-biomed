package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const runnerTrustedScientificEvidenceReplayType = "runner_trusted_scientific_evidence_receipt"

func (s *Server) loadTranscriptRunnerReplay(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
	messageLimit, checkpointLimit int,
) ([]eventjournal.Entry, error) {
	if s == nil || s.transcriptStore == nil || authority == nil {
		return nil, errors.New("transcript runner authority is required")
	}
	correction, hasCorrection, err := s.loadPriorTerminalCorrection(ctx, authority)
	if err != nil {
		return nil, err
	}
	recoveryProjection, err := s.loadSessionRunnerRecoveryProjection(ctx, authority)
	if err != nil {
		return nil, err
	}
	projected, err := s.transcriptStore.ListRunnerReplay(ctx, transcriptstore.ListRunnerReplayInput{
		StreamUID: authority.Stream.UID, OwnerID: authority.Stream.OwnerID,
		MessageLimit: messageLimit, CheckpointLimit: checkpointLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("load transcript runner replay: %w", err)
	}
	entries := make([]eventjournal.Entry, 0, len(projected))
	artifactReferenceCorrection := hasCorrection &&
		strings.TrimSpace(stringValue(correction.Message["reason_code"])) == "artifact_reference_correction_required"
	for _, projectedEvent := range projected {
		if artifactReferenceCorrection && projectedEvent.Event.RunnerAttempt != nil && *projectedEvent.Event.RunnerAttempt > 0 {
			// A reference-integrity failure means the preceding runner output has
			// no complete durable evidence chain. Keep the immutable history for
			// audit, but do not project any prior assistant/tool/checkpoint event
			// into the new provider execution unit. The original user inputs and
			// canonical workspace remain the restart authority.
			//
			// Successful source/runtime checkpoints also contain server-generated,
			// validated scientific evidence signals. Preserve only those bounded
			// receipts so a link-only correction does not erase real computation or
			// source authority and enter an alternating evidence/reference loop.
			// The receipt has no tool call, result, or model-authored content and is
			// therefore invisible to provider protocol replay.
			receipt, ok, err := trustedScientificCorrectionReplayReceipt(authority, projectedEvent)
			if err != nil {
				return nil, err
			}
			if ok {
				entries = append(entries, receipt)
			}
			continue
		}
		message := eventjournal.Message{}
		if err := json.Unmarshal(projectedEvent.ResolvedPayloadJSON, &message); err != nil {
			return nil, fmt.Errorf("decode transcript event %d: %w", projectedEvent.Event.EventID, err)
		}
		// runner_attempt is authoritative event metadata, not a payload field.
		// Downstream logical-input scoping must see it for every replay event;
		// otherwise minimal interruption payloads (status/reason only) are
		// silently dropped and same-input repetition/backoff state resets on resume.
		if projectedEvent.Event.RunnerAttempt != nil {
			message["runnerAttempt"] = *projectedEvent.Event.RunnerAttempt
		}
		switch projectedEvent.Event.Type {
		case "user_message", "user_input_response", "history_user_message":
			message["type"] = "message"
			message["role"] = "user"
			if err := validateRunnerUserArtifactContext(message); err != nil {
				return nil, fmt.Errorf("validate transcript user event %d: %w", projectedEvent.Event.EventID, err)
			}
		case "assistant_message", "history_assistant_message":
			message["type"] = "message"
			message["role"] = "assistant"
		case "history_system_message":
			message["type"] = "message"
			message["role"] = "system"
		case "runner_checkpoint":
			message["type"] = "runner_checkpoint"
		case transcriptstore.TerminalToolRecoveryEventType:
			// A terminal recovery receipt closes a durable model tool-call
			// transaction after the original runner lost checkpoint authority.
			// Project it as a terminal checkpoint so provider replay receives the
			// matching tool result. The immutable event type is carried separately
			// as non-serialized in-memory provenance below.
			message["type"] = "runner_checkpoint"
		default:
			return nil, fmt.Errorf("unsupported transcript replay event type %q", projectedEvent.Event.Type)
		}
		entries = append(entries, eventjournal.Entry{
			SessionID:       authority.Stream.SessionID,
			EventID:         projectedEvent.Event.EventID,
			CreatedAt:       projectedEvent.Event.CreatedAt.UTC().Format(time.RFC3339Nano),
			RunID:           strings.TrimSpace(authority.Claim.RunnerID),
			ClientMessageID: projectedEvent.Event.ClientMessageID,
			Message:         message,
			SourceEventType: projectedEvent.Event.Type,
		})
	}
	taskEvidenceReceipt, hasTaskEvidence, err := s.trustedScientificTaskReplayReceipt(ctx, authority)
	if err != nil {
		return nil, err
	}
	if hasTaskEvidence {
		entries = append(entries, taskEvidenceReceipt)
	}
	if hasCorrection {
		entries = append(entries, correction)
	}
	if recoveryProjection.HasCorrection {
		current, found := latestRunnerCorrection(entries)
		if !found || current.ReasonCode != recoveryProjection.Correction.ReasonCode ||
			current.Detail != recoveryProjection.Correction.Detail ||
			current.RecoveryContractRevision != recoveryProjection.Correction.RecoveryContractRevision {
			entries = append(entries, eventjournal.Entry{Message: eventjournal.Message{
				"type": "runner_checkpoint", "status": "interrupted",
				"reason_code":                recoveryProjection.Correction.ReasonCode,
				"resume_detail":              recoveryProjection.Correction.Detail,
				"recovery_contract_revision": recoveryProjection.Correction.RecoveryContractRevision,
			}})
		}
	}
	if recoveryProjection.NoProgress.Consecutive > 0 || len(recoveryProjection.NoProgress.ClosedActions) > 0 {
		entries = append(entries, eventjournal.Entry{Message: eventjournal.Message{
			"type":                               sessionRunnerNoProgressRecoveryField,
			sessionRunnerNoProgressRecoveryField: recoveryProjection.NoProgress.payload(),
		}})
	}
	return entries, nil
}

func trustedScientificCorrectionReplayReceipt(
	authority *transcriptRunnerAuthority,
	projected transcriptstore.RunnerReplayEvent,
) (eventjournal.Entry, bool, error) {
	if authority == nil || projected.Event.Type != "runner_checkpoint" ||
		projected.Event.RunnerAttempt == nil || *projected.Event.RunnerAttempt <= 0 {
		return eventjournal.Entry{}, false, nil
	}
	message := eventjournal.Message{}
	if err := json.Unmarshal(projected.ResolvedPayloadJSON, &message); err != nil {
		return eventjournal.Entry{}, false, fmt.Errorf(
			"decode trusted scientific correction receipt event %d: %w", projected.Event.EventID, err,
		)
	}
	if stringValue(message["status"]) != "completed" || stringValue(message["toolPhase"]) != "completed" {
		return eventjournal.Entry{}, false, nil
	}
	// Re-derive checkpoint signals from the immutable result instead of
	// trusting an older process's projected signal list. This keeps recovery
	// safe across deployments that tighten evidence semantics.
	signals := trustedScientificReviewSignalsFromRunnerEntries([]eventjournal.Entry{{
		SessionID: authority.Stream.SessionID,
		EventID:   projected.Event.EventID,
		Message:   message,
	}})
	witnesses, witnessErr := decodeScientificCapabilityWitnesses(message[trustedScientificCapabilityWitnessesField])
	if witnessErr != nil {
		return eventjournal.Entry{}, false, witnessErr
	}
	if len(signals) == 0 && len(witnesses) == 0 {
		return eventjournal.Entry{}, false, nil
	}
	receipt := eventjournal.Message{
		"type": runnerTrustedScientificEvidenceReplayType, "status": "completed",
		"runnerAttempt": *projected.Event.RunnerAttempt,
	}
	if len(signals) > 0 {
		receipt[trustedScientificReviewSignalsField] = signals
	}
	if len(witnesses) > 0 {
		receipt[trustedScientificCapabilityWitnessesField] = witnesses
	}
	return eventjournal.Entry{
		SessionID:       authority.Stream.SessionID,
		EventID:         projected.Event.EventID,
		CreatedAt:       projected.Event.CreatedAt.UTC().Format(time.RFC3339Nano),
		RunID:           strings.TrimSpace(authority.Claim.RunnerID),
		ClientMessageID: projected.Event.ClientMessageID,
		Message:         receipt,
		SourceEventType: projected.Event.Type,
	}, true, nil
}

// appendPriorTerminalCorrection carries the exact durable failure that caused
// an explicitly resumed execution unit into the provider replay. The previous
// terminal remains authoritative; this read projection neither rewrites it nor
// treats its unverified evidence as valid. A new attempt must repair the named
// failure using current trusted tools before it can complete.
func (s *Server) loadPriorTerminalCorrection(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
) (eventjournal.Entry, bool, error) {
	if s == nil || s.transcriptStore == nil || authority == nil ||
		authority.Claim.ResumeSource != transcriptstore.ResumeSourceCheckpoint || authority.Claim.Attempt <= 1 {
		return eventjournal.Entry{}, false, nil
	}
	previous, err := s.transcriptStore.GetRunnerRuntimeState(
		ctx, authority.Stream.UID, authority.Stream.OwnerID, authority.Claim.Attempt-1,
	)
	if err != nil {
		return eventjournal.Entry{}, false, fmt.Errorf("load prior runner terminal state: %w", err)
	}
	if !runnerResumeContinuesPriorInput(authority, previous) ||
		previous.Status != "failed" || previous.FinishedEventID <= 0 {
		return eventjournal.Entry{}, false, nil
	}
	terminal, err := s.transcriptStore.GetTerminalProjection(
		ctx, authority.Stream.OwnerID, authority.Stream.UID, previous.FinishedEventID,
	)
	if err != nil {
		return eventjournal.Entry{}, false, fmt.Errorf("load prior runner terminal projection: %w", err)
	}
	reasonCode := priorTerminalCorrectionReason(terminal.Detail)
	if reasonCode == "" {
		return eventjournal.Entry{}, false, nil
	}
	detail := strings.TrimSpace(terminal.Detail)
	if detail == "" || len(detail) > maxRunnerCorrectionResumeDetailBytes {
		return eventjournal.Entry{}, false, errors.New("prior runner correction detail is invalid")
	}
	return eventjournal.Entry{
		SessionID: authority.Stream.SessionID,
		EventID:   terminal.EventID,
		CreatedAt: terminal.CreatedAt.UTC().Format(time.RFC3339Nano),
		RunID:     strings.TrimSpace(authority.Claim.RunnerID),
		Message: eventjournal.Message{
			"type": "runner_checkpoint", "status": "interrupted",
			"reason_code": reasonCode, "resume_detail": detail,
		},
	}, true, nil
}

func priorTerminalCorrectionReason(detail string) string {
	detail = strings.TrimSpace(detail)
	switch {
	case strings.HasPrefix(detail, "runner completion reference integrity failed"):
		return "artifact_reference_correction_required"
	case strings.HasPrefix(detail, "completion reviewer rejected the current candidate"):
		return "completion_review_correction_required"
	case strings.HasPrefix(detail, "artifact inventory changed while the independent completion review was running"):
		return "completion_review_correction_required"
	default:
		return ""
	}
}
