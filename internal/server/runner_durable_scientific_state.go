package server

import (
	"context"
	"encoding/json"
	"fmt"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

// trustedScientificTaskReplayReceipt compacts the immutable completed-tool
// evidence for one admitted input revision into a private in-memory receipt.
// Provider replay remains bounded, while deterministic completion state does
// not disappear merely because a long task produced more checkpoints than the
// model context window can retain.
func (s *Server) trustedScientificTaskReplayReceipt(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
) (eventjournal.Entry, bool, error) {
	if s == nil || s.transcriptStore == nil || authority == nil ||
		authority.Claim.ClaimedInputRevision <= 0 {
		return eventjournal.Entry{}, false, nil
	}
	projected, err := s.transcriptStore.ListRunnerTaskEvidence(ctx, transcriptstore.ListRunnerTaskEvidenceInput{
		StreamUID: authority.Stream.UID, OwnerID: authority.Stream.OwnerID,
		ClaimedInputRevision: authority.Claim.ClaimedInputRevision,
	})
	if err != nil {
		return eventjournal.Entry{}, false, fmt.Errorf("load durable runner task evidence: %w", err)
	}
	entries := make([]eventjournal.Entry, 0, len(projected))
	for _, item := range projected {
		message := eventjournal.Message{}
		if err := json.Unmarshal(item.ResolvedPayloadJSON, &message); err != nil {
			return eventjournal.Entry{}, false, fmt.Errorf(
				"decode durable runner task evidence event %d: %w", item.Event.EventID, err,
			)
		}
		message["type"] = "runner_checkpoint"
		if item.Event.RunnerAttempt != nil {
			message["runnerAttempt"] = *item.Event.RunnerAttempt
		}
		entries = append(entries, eventjournal.Entry{
			SessionID: authority.Stream.SessionID, EventID: item.Event.EventID,
			CreatedAt: item.Event.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
			Message:   message, SourceEventType: item.Event.Type,
		})
	}
	signals := trustedScientificReviewSignalsFromRunnerEntries(entries)
	for _, capability := range requiredScientificCapabilitiesFromRunnerEntries(entries) {
		signals = append(signals, trustedScientificRequiredCapabilitySignalPrefix+capability)
	}
	signals = normalizeTrustedScientificReviewSignals(signals)
	witnesses := trustedScientificCapabilityWitnessesFromRunnerEntries(entries)
	if len(signals) == 0 && len(witnesses) == 0 {
		return eventjournal.Entry{}, false, nil
	}
	message := eventjournal.Message{
		"type": runnerTrustedScientificEvidenceReplayType, "status": "completed",
		"runnerAttempt": authority.Claim.Attempt,
	}
	if len(signals) > 0 {
		message[trustedScientificReviewSignalsField] = signals
	}
	if len(witnesses) > 0 {
		message[trustedScientificCapabilityWitnessesField] = witnesses
	}
	return eventjournal.Entry{
		SessionID: authority.Stream.SessionID,
		Message:   message,
	}, true, nil
}
