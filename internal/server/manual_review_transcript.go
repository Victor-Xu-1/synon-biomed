package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"synon-go/internal/agentruntime"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const (
	manualReviewMaxProjectedEvents = 100_000
	manualReviewMaxProjectedBytes  = 256 << 20
	manualReviewProjectionPageSize = 1_000
)

// loadManualReviewRootTranscript reads one immutable active-branch snapshot
// from message zero through the current terminal. Unlike provider replay, this
// audit projection must not discard earlier tasks at a compaction boundary.
func (s *Server) loadManualReviewRootTranscript(
	ctx context.Context,
	authority *transcriptRunnerAuthority,
) ([]eventjournal.Entry, transcriptstore.ProjectionSnapshot, error) {
	if s == nil || s.transcriptStore == nil || authority == nil {
		return nil, transcriptstore.ProjectionSnapshot{}, errors.New("manual review transcript authority is required")
	}
	snapshot, err := s.transcriptStore.GetProjectionSnapshot(
		ctx, authority.Stream.UID, authority.Stream.OwnerID,
	)
	if err != nil {
		return nil, transcriptstore.ProjectionSnapshot{}, err
	}
	entries := make([]eventjournal.Entry, 0)
	var after int64
	var projectedBytes int64
	projectedEvents := 0
	for after < snapshot.ThroughPublicationSequence {
		page, err := s.transcriptStore.ListProjectedEvents(ctx, transcriptstore.ListProjectedEventsInput{
			StreamUID: authority.Stream.UID, OwnerID: authority.Stream.OwnerID,
			BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
			AfterPublicationSequence: after, ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
			Limit: manualReviewProjectionPageSize,
		})
		if err != nil {
			return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf("load manual review transcript: %w", err)
		}
		if len(page) == 0 {
			return nil, transcriptstore.ProjectionSnapshot{}, errors.New("manual review transcript snapshot is incomplete")
		}
		for _, projected := range page {
			after = projected.Event.PublicationSeq
			projectedEvents++
			projectedBytes += int64(len(projected.ResolvedPayloadJSON))
			if projectedEvents > manualReviewMaxProjectedEvents || projectedBytes > manualReviewMaxProjectedBytes {
				return nil, transcriptstore.ProjectionSnapshot{}, fmt.Errorf(
					"manual review transcript exceeds the bounded audit budget: events=%d/%d bytes=%d/%d",
					projectedEvents, manualReviewMaxProjectedEvents, projectedBytes, manualReviewMaxProjectedBytes,
				)
			}
			entry, keep, err := manualReviewEntryFromProjectedEvent(authority, projected)
			if err != nil {
				return nil, transcriptstore.ProjectionSnapshot{}, err
			}
			if keep {
				entries = append(entries, entry)
			}
		}
	}
	return entries, snapshot, nil
}

func manualReviewEntryFromProjectedEvent(
	authority *transcriptRunnerAuthority,
	projected transcriptstore.ProjectedEvent,
) (eventjournal.Entry, bool, error) {
	message := eventjournal.Message{}
	if err := json.Unmarshal(projected.ResolvedPayloadJSON, &message); err != nil {
		return eventjournal.Entry{}, false, fmt.Errorf(
			"decode manual review transcript event %d: %w", projected.Event.EventID, err,
		)
	}
	if projected.Event.RunnerAttempt != nil {
		message["runnerAttempt"] = *projected.Event.RunnerAttempt
	}
	switch projected.Event.Type {
	case "user_message", "user_input_response", "history_user_message":
		message["type"] = "message"
		message["role"] = "user"
		if err := validateRunnerUserArtifactContext(message); err != nil {
			return eventjournal.Entry{}, false, fmt.Errorf(
				"validate manual review user event %d: %w", projected.Event.EventID, err,
			)
		}
	case "assistant_message", "history_assistant_message":
		message["type"] = "message"
		message["role"] = "assistant"
	case "history_system_message":
		message["type"] = "message"
		message["role"] = "system"
	case "runner_checkpoint", transcriptstore.TerminalToolRecoveryEventType:
		message["type"] = "runner_checkpoint"
	default:
		return eventjournal.Entry{}, false, nil
	}
	return eventjournal.Entry{
		SessionID:       authority.Stream.SessionID,
		EventID:         projected.Event.EventID,
		CreatedAt:       projected.Event.CreatedAt.UTC().Format(time.RFC3339Nano),
		RunID:           strings.TrimSpace(authority.Claim.RunnerID),
		ClientMessageID: projected.Event.ClientMessageID,
		Message:         message,
		SourceEventType: projected.Event.Type,
	}, true, nil
}

func manualReviewMessagesFromEntries(entries []eventjournal.Entry) ([]agentruntime.Message, error) {
	messages := make([]agentruntime.Message, 0, len(entries))
	for _, entry := range entries {
		eventType := strings.TrimSpace(stringValue(entry.Message["type"]))
		if eventType == "runner_checkpoint" {
			if isNestedKernelMCPReplayReceipt(entry.Message) {
				continue
			}
			calls, hasCalls, err := nativeProviderToolCalls(entry.Message["modelToolCalls"])
			if err != nil {
				return nil, fmt.Errorf("project manual review tool calls at event %d: %w", entry.EventID, err)
			}
			if hasCalls {
				messages = append(messages, agentruntime.Message{
					Role: "assistant", ToolCalls: agentRuntimeToolCallsFromChat(calls),
				})
				continue
			}
			phase := strings.TrimSpace(stringValue(entry.Message["toolPhase"]))
			if sessionRunnerToolContinuityTerminalPhase(phase) {
				callID := strings.TrimSpace(stringValue(entry.Message["toolCallId"]))
				result, present := entry.Message["toolResult"]
				if callID == "" || !present || result == nil {
					continue
				}
				encoded, err := json.Marshal(result)
				if err != nil {
					return nil, fmt.Errorf("project manual review tool result at event %d: %w", entry.EventID, err)
				}
				content := string(encoded)
				if text, ok := result.(string); ok {
					content = text
				}
				messages = append(messages, agentruntime.Message{
					Role: "tool", ToolCallID: callID, Content: content,
				})
				continue
			}
			if strings.EqualFold(phase, "auto_compact") {
				if summary := strings.TrimSpace(compactSummaryFromMessage(entry.Message)); summary != "" {
					messages = append(messages, agentruntime.Message{
						Role: "system", Content: "[Target compacted-history summary; orientation only]\n" + summary,
					})
				}
			}
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringValue(entry.Message["role"])))
		if role != "user" && role != "assistant" && role != "system" {
			continue
		}
		text := runnerMessageText(entry.Message)
		if role == "user" {
			text = runnerModelMessageText(entry.Message)
		}
		if text != "" {
			messages = append(messages, agentruntime.Message{Role: role, Content: text})
		}
	}
	return messages, nil
}
