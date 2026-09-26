package server

import (
	"context"
	"encoding/json"
	"fmt"

	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

// Choice replay reads the same immutable question/result transaction as the
// answer API. A bounded conversational preview is not the selection authority.
func (s *Server) runnerAskUserSelectionEntries(ctx context.Context, run *sessionRunnerChatRun) ([]eventjournal.Entry, error) {
	if run == nil || run.Transcript == nil || s.transcriptStore == nil {
		return nil, nil
	}
	authority := run.Transcript
	boundary, required, err := s.sessionRunnerDurableTaskBoundary(ctx, run)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(ctx, authority.Stream.UID, authority.Stream.OwnerID)
	if err != nil {
		return nil, err
	}
	reached := !required
	var entries []eventjournal.Entry
	err = s.scanTranscriptProjection(ctx, snapshot, authority.Stream.OwnerID, snapshot.ThroughPublicationSequence, func(projected transcriptstore.ProjectedEvent) error {
		if !reached && sessionRunnerProjectedEventMatchesTaskBoundary(projected.Event, boundary) {
			reached = true
		}
		if !reached {
			return nil
		}
		raw := projected.ResolvedPayloadJSON
		if len(raw) == 0 {
			raw = projected.Event.PayloadJSON
		}
		if projected.Event.Type == "runner_checkpoint" {
			var message eventjournal.Message
			if err := json.Unmarshal(raw, &message); err != nil {
				return err
			}
			message["type"] = "runner_checkpoint"
			entry := eventjournal.Entry{EventID: projected.Event.EventID, SourceEventType: projected.Event.Type, Message: message}
			if len(registrySelectedImplementationsFromRunnerEntry(entry)) > 0 {
				entries = append(entries, entry)
			}
			return nil
		}
		if projected.Event.Type != transcriptstore.AskUserResultEventType {
			return nil
		}
		result, err := transcriptstore.DecodeAskUserResultEventV1(raw)
		if err != nil {
			return err
		}
		if string(result.Result.Status) != "answered" && string(result.Result.Status) != "delegated" {
			return nil
		}
		continuation := result.ModelContinuation
		if string(result.Result.Status) == "answered" {
			prompt, err := s.transcriptStore.GetFrameAskUserPrompt(ctx, transcriptstore.GetFrameAskUserPromptInput{
				OwnerID: authority.Stream.OwnerID, FrameID: authority.Stream.FrameID, Origin: result.Origin,
			})
			if err != nil {
				return err
			}
			encoded, err := json.Marshal(prompt.Questions)
			if err != nil {
				return err
			}
			var questions []any
			if err := json.Unmarshal(encoded, &questions); err != nil {
				return err
			}
			continuation, err = s.reconcileAnsweredAskUserSelection(map[string]any{"questions": questions}, result.Result.Answers, continuation)
			if err != nil {
				return err
			}
		}
		entries = append(entries, eventjournal.Entry{
			EventID: projected.Event.EventID, SourceEventType: "user_input_response",
			Message: eventjournal.Message{"type": "message", "role": "user", "messageOrigin": "input_response",
				"text": fmt.Sprintf("Resolved input requests:\n%s: %s", result.ToolUseID, continuation)},
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("restore answered implementation authority: %w", err)
	}
	return entries, nil
}
