package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"synon-go/internal/memoryextract"
	eventjournal "synon-go/internal/persistence/journal"
	transcriptstore "synon-go/internal/persistence/transcript"
)

const memoryExtractionTranscriptPageSize = 1000

type memoryExtractionJournalSnapshot struct {
	Messages          []memoryextract.Message
	TerminalStatus    string
	FinalResponseText string
}

// readMemoryExtractionSnapshot enforces one authority per conversation kind.
// Web frames are projected only from immutable Transcript history. The legacy
// journal remains an isolated compatibility reader for sessions that have no
// Transcript frame stream; it is never merged with or used to complete a Web
// frame snapshot.
func (runtime *memoryExtractionRuntime) readMemoryExtractionSnapshot(
	ctx context.Context,
	ownerID string,
	sessionID string,
) (memoryExtractionJournalSnapshot, error) {
	if runtime == nil || runtime.server == nil {
		return memoryExtractionJournalSnapshot{}, errors.New("memory extraction snapshot reader is unavailable")
	}
	if runtime.server.transcriptStore != nil {
		stream, found, err := runtime.server.transcriptStore.GetFrameStreamBySession(ctx, ownerID, sessionID)
		if err != nil {
			return memoryExtractionJournalSnapshot{}, err
		}
		if found {
			entries, err := runtime.readMemoryExtractionTranscriptEntries(ctx, stream)
			if err != nil {
				return memoryExtractionJournalSnapshot{}, err
			}
			return projectMemoryExtractionSnapshot(entries), nil
		}
	}
	if runtime.server.eventJournal == nil {
		return memoryExtractionJournalSnapshot{}, errors.New("memory extraction compatibility journal is unavailable")
	}
	entries, err := runtime.server.eventJournal.ReadAllStrict(sessionID)
	if err != nil {
		return memoryExtractionJournalSnapshot{}, err
	}
	return projectMemoryExtractionSnapshot(entries), nil
}

func (runtime *memoryExtractionRuntime) readMemoryExtractionTranscriptEntries(
	ctx context.Context,
	stream transcriptstore.Stream,
) ([]eventjournal.Entry, error) {
	snapshot, err := runtime.server.transcriptStore.GetProjectionSnapshot(ctx, stream.UID, stream.OwnerID)
	if err != nil {
		return nil, err
	}
	if snapshot.ThroughPublicationSequence == 0 {
		return []eventjournal.Entry{}, nil
	}
	entries := make([]eventjournal.Entry, 0)
	after := int64(0)
	for after < snapshot.ThroughPublicationSequence {
		page, err := runtime.server.transcriptStore.ListProjectedCoordinateEvents(ctx, transcriptstore.ListProjectedEventsInput{
			StreamUID: stream.UID, OwnerID: stream.OwnerID,
			BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
			AfterPublicationSequence: after, ThroughPublicationSequence: snapshot.ThroughPublicationSequence,
			Limit: memoryExtractionTranscriptPageSize,
		})
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			return nil, fmt.Errorf(
				"memory extraction transcript projection stopped at publication sequence %d of %d",
				after, snapshot.ThroughPublicationSequence,
			)
		}
		for _, projected := range page {
			if entry, ok := memoryExtractionTranscriptEntry(stream.SessionID, projected); ok {
				entries = append(entries, entry)
			}
			after = projected.Event.PublicationSeq
		}
	}
	return entries, nil
}

func memoryExtractionTranscriptEntry(sessionID string, projected transcriptstore.ProjectedEvent) (eventjournal.Entry, bool) {
	eventType := strings.ToLower(strings.TrimSpace(projected.Event.Type))
	message := eventjournal.Message{}
	if len(projected.ResolvedPayloadJSON) > 0 {
		if err := json.Unmarshal(projected.ResolvedPayloadJSON, &message); err != nil {
			return eventjournal.Entry{}, false
		}
	}
	switch eventType {
	case "user_message", "user_input_response", "history_user_message":
		message["type"] = "message"
		message["role"] = "user"
	case "assistant_message", "history_assistant_message":
		message["type"] = "message"
		message["role"] = "assistant"
	case "history_system_message":
		message["type"] = "message"
		message["role"] = "system"
	case "runner_checkpoint", transcriptstore.TerminalToolRecoveryEventType:
		message["type"] = "runner_checkpoint"
	case "runner_finished":
		message["type"] = "runner_finished"
	default:
		return eventjournal.Entry{}, false
	}
	if projected.Event.RunnerAttempt != nil {
		message["runnerAttempt"] = *projected.Event.RunnerAttempt
	}
	runID := ""
	if projected.Event.RunnerAttempt != nil {
		runID = strconv.FormatInt(*projected.Event.RunnerAttempt, 10)
	}
	return eventjournal.Entry{
		SessionID: sessionID, EventID: projected.Event.PublicationSeq,
		CreatedAt: projected.Event.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z"),
		RunID:     runID, ClientMessageID: projected.Event.ClientMessageID,
		Message: message, SourceEventType: eventType,
	}, true
}

func projectMemoryExtractionSnapshot(entries []eventjournal.Entry) memoryExtractionJournalSnapshot {
	_, compactBoundary, hasCompact := latestCompactModelContext(entries)
	snapshot := memoryExtractionJournalSnapshot{Messages: make([]memoryextract.Message, 0, len(entries))}
	for _, entry := range entries {
		typeName := strings.ToLower(strings.TrimSpace(stringValue(entry.Message["type"])))
		if typeName == "runner_finished" {
			snapshot.TerminalStatus = strings.ToLower(strings.TrimSpace(stringValue(entry.Message["status"])))
			continue
		}
		if isCompactJournalEntry(entry) {
			continue
		}
		projected := memoryExtractionMessagesFromJournalEntry(entry)
		if hasCompact && entry.EventID <= compactBoundary {
			for index := range projected {
				projected[index].Ignore = true
			}
		}
		snapshot.Messages = append(snapshot.Messages, projected...)
	}
	snapshot.FinalResponseText = lastMemoryExtractionAssistantText(snapshot.Messages)
	return snapshot
}

func lastMemoryExtractionAssistantText(messages []memoryextract.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role != "assistant" || message.Ignore {
			continue
		}
		parts := make([]string, 0, len(message.Content))
		for _, block := range message.Content {
			if block.Type == memoryextract.BlockText && !block.HarnessNotice && strings.TrimSpace(block.Text) != "" {
				parts = append(parts, block.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
}
