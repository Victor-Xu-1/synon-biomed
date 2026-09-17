package server

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	eventjournal "synon-go/internal/persistence/journal"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) persistCompactionArchive(frameID string, entries []eventjournal.Entry, boundary *eventjournal.Entry) (*workspace.CompactionArchive, error) {
	if s == nil || s.workspaceStore == nil || boundary == nil {
		return nil, nil
	}
	if _, found, err := s.workspaceStore.GetFrame(frameID); err != nil {
		return nil, err
	} else if !found {
		return nil, nil
	}
	messages := compactionMessagesAfterLastBoundary(entries)
	tokenCount := estimateCompactionMessageTokens(messages)
	sourceEventID := boundary.EventID
	createdAt := time.Now().UTC()
	if parsed, err := time.Parse(time.RFC3339Nano, boundary.CreatedAt); err == nil {
		createdAt = parsed.UTC()
	}
	archive, err := s.workspaceStore.CreateCompactionArchive(workspace.CreateCompactionArchiveInput{
		FrameID: frameID, MessageCount: len(messages), TokenCount: &tokenCount,
		Summary: compactSummaryFromMessage(boundary.Message), Messages: messages,
		SourceEventID: &sourceEventID, CreatedAt: createdAt,
	})
	if err != nil {
		return nil, err
	}
	return &archive, nil
}

func (s *Server) reconcileCompactionArchives(frameID string) error {
	if s == nil || s.workspaceStore == nil || s.eventJournal == nil {
		return nil
	}
	entries, err := s.eventJournal.ReadAll(frameID)
	if err != nil {
		return err
	}
	archives, err := s.workspaceStore.ListCompactionArchives(frameID)
	if err != nil {
		return err
	}
	existingIndexes := make(map[int]struct{}, len(archives))
	for _, archive := range archives {
		existingIndexes[archive.CompactionIndex] = struct{}{}
	}
	previousBoundary := int64(0)
	compactionIndex := 0
	for _, boundary := range entries {
		if !isCompactJournalEntry(boundary) {
			continue
		}
		compactedThrough := int64(numberValue(boundary.Message["compactedThroughEventId"]))
		if compactedThrough <= previousBoundary || compactedThrough >= boundary.EventID {
			compactedThrough = boundary.EventID - 1
		}
		if _, exists := existingIndexes[compactionIndex]; !exists {
			messages := compactionMessagesBetween(entries, previousBoundary, compactedThrough)
			tokenCount := estimateCompactionMessageTokens(messages)
			sourceEventID := boundary.EventID
			index := compactionIndex
			createdAt := time.Now().UTC()
			if parsed, err := time.Parse(time.RFC3339Nano, boundary.CreatedAt); err == nil {
				createdAt = parsed.UTC()
			}
			if _, err := s.workspaceStore.CreateCompactionArchive(workspace.CreateCompactionArchiveInput{
				FrameID: frameID, CompactionIndex: &index, MessageCount: len(messages),
				TokenCount: &tokenCount, Summary: compactSummaryFromMessage(boundary.Message),
				Messages: messages, SourceEventID: &sourceEventID, CreatedAt: createdAt,
			}); err != nil {
				return fmt.Errorf("reconcile compaction archive %d: %w", compactionIndex, err)
			}
		}
		previousBoundary = compactedThrough
		compactionIndex++
	}
	return nil
}

func compactionMessagesAfterLastBoundary(entries []eventjournal.Entry) []map[string]any {
	previousBoundary := int64(0)
	for _, entry := range entries {
		if isCompactJournalEntry(entry) {
			previousBoundary = entry.EventID
		}
	}
	return compactionMessagesBetween(entries, previousBoundary, int64(maxEventID(0, entries)))
}

func compactionMessagesBetween(entries []eventjournal.Entry, afterEventID, throughEventID int64) []map[string]any {
	messages := make([]map[string]any, 0)
	for _, entry := range entries {
		if entry.EventID <= afterEventID || entry.EventID > throughEventID || isCompactJournalEntry(entry) {
			continue
		}
		message := make(map[string]any, len(entry.Message))
		for key, value := range entry.Message {
			message[key] = value
		}
		messages = append(messages, message)
	}
	return messages
}

func estimateCompactionMessageTokens(messages []map[string]any) int {
	total := 0
	for _, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			continue
		}
		total += estimateTextTokens(string(raw))
	}
	return total
}

func compatibilityCompactionArchiveResponse(archive workspace.CompactionArchive, includeMessages bool) map[string]any {
	response := map[string]any{
		"id": archive.ID, "frame_id": archive.FrameID,
		"compaction_index": archive.CompactionIndex, "message_count": archive.MessageCount,
		"token_count": archive.TokenCount, "summary": archive.Summary,
		"created_at": archive.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if includeMessages {
		response["messages"] = archive.Messages
	}
	return response
}

func parseCompactionArchiveIndex(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	index, err := strconv.Atoi(raw)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("invalid compaction archive index %q", raw)
	}
	return index, nil
}
