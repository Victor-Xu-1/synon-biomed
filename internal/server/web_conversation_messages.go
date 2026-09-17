package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
	"time"
)

func (s *Server) handleWebConversationMessages(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	segments []string,
) {
	if len(segments) == 4 && segments[3] == "tool-detail" {
		messageID, err := url.PathUnescape(segments[2])
		if err != nil || strings.TrimSpace(messageID) == "" {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid message id"})
			return
		}
		s.handleWebConversationToolDetail(w, r, frame, messageID)
		return
	}
	if len(segments) == 2 && r.Method == http.MethodPost {
		s.handleWebConversationMessageSubmission(w, r, frame)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, POST")
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	if len(segments) == 3 {
		messageID, err := url.PathUnescape(segments[2])
		if err != nil || strings.TrimSpace(messageID) == "" {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "invalid message id"})
			return
		}
		s.handleWebConversationMessageGet(w, r, frame, messageID)
		return
	}
	if len(segments) != 2 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "message endpoint not found"})
		return
	}
	limit := max(1, min(compatPositiveQuery(r, "limit", defaultWebMessagePageSize, maxWebMessagePageSize), maxWebMessagePageSize))
	historyProjectionStarted := time.Now()
	branchID := strings.TrimSpace(r.URL.Query().Get("branch_id"))
	if s.transcriptWebReadModel != nil {
		readModel, active, err := s.activatedTranscriptWebReadModel(
			r.Context(), compatAgentUserID(r), frame.ID, branchID,
		)
		if err != nil {
			if branchID != "" && errors.Is(err, transcriptstore.ErrBranchStateStale) {
				writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"message": "conversation branch changed; refresh and retry"})
			} else {
				writeWebConversationError(w, err)
			}
			return
		}
		if active {
			s.writeActivatedTranscriptWebReadModelPage(
				withWebServerTiming(w, "history_read_model", historyProjectionStarted),
				r, frame.ID, readModel, limit, compatAgentUserID(r),
			)
			return
		}
	}
	if branchID != "" {
		if s.transcriptStore != nil {
			view, active, err := s.loadActivatedTranscriptWebHistoryView(
				r.Context(), compatAgentUserID(r), frame.ID, branchID,
			)
			if err != nil {
				switch {
				case errors.Is(err, transcriptstore.ErrBranchTargetNotFound), errors.Is(err, transcriptstore.ErrOwnerMismatch):
					writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "conversation branch not found"})
				case errors.Is(err, transcriptstore.ErrBranchStateStale):
					writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"message": "conversation branch changed; refresh and retry"})
				case errors.Is(err, transcriptstore.ErrSchemaUnavailable):
					writeWebConversationError(w, transcriptWebAuthorityUnavailableError())
				default:
					writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"message": "internal workspace error"})
				}
				return
			}
			if !active {
				writeWebConversationError(w, transcriptWebAuthorityUnavailableError())
				return
			}
			s.writeTranscriptBranchMessagePage(
				withWebServerTiming(w, "history_projection", historyProjectionStarted),
				r, frame.ID, view.messages, limit, view.snapshot, view.stream, compatAgentUserID(r),
			)
			return
		}
		messages, found, err := s.workspaceStore.GetCompatibilityBranchMessages(frame.ID, branchID)
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		if !found {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "conversation branch not found"})
			return
		}
		if hasRichWebMessageBlocks(messages) {
			projected, err := s.projectRichWebConversationMessages(frame.ID, messages, branchID)
			if err != nil {
				writeWebConversationError(w, err)
				return
			}
			s.writeRichWebMessagePage(w, r, projected, limit)
			return
		}
		s.writeWebMessagePage(w, frame.ID, messages, limit, r)
		return
	}
	if view, active, err := s.loadActivatedTranscriptWebHistoryView(
		r.Context(), compatAgentUserID(r), frame.ID, "",
	); err != nil {
		writeWebConversationError(w, err)
		return
	} else if active {
		s.writeTranscriptBranchMessagePage(
			withWebServerTiming(w, "history_projection", historyProjectionStarted),
			r, frame.ID, view.messages, limit, view.snapshot, view.stream, compatAgentUserID(r),
		)
		return
	}
	if s.transcriptStore != nil {
		writeWebConversationError(w, transcriptWebAuthorityUnavailableError())
		return
	}
	totalPage, err := s.workspaceStore.CompatibilityFrameMessages(frame.ID, 0, 1)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	start, err := webMessagePageStart(r, totalPage.Total, limit, func(messageID string) (*int, error) {
		return s.workspaceStore.LocateCompatibilityFrameMessage(frame.ID, messageID)
	})
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	page, err := s.workspaceStore.CompatibilityFrameMessages(frame.ID, start, limit)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	if hasRichWebMessageBlocks(page.Messages) {
		messages, err := s.loadRichWebFrameMessages(frame.ID)
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		projected, err := s.projectRichWebConversationMessages(frame.ID, messages, "")
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		s.writeRichWebMessagePage(w, r, projected, limit)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, webMessagePage(frame.ID, page.Messages, start, page.Total))
}

func withWebServerTiming(w http.ResponseWriter, metric string, started time.Time) http.ResponseWriter {
	durationMilliseconds := float64(time.Since(started).Microseconds()) / 1000
	w.Header().Add("Server-Timing", fmt.Sprintf("%s;dur=%.3f", metric, durationMilliseconds))
	return w
}

func (s *Server) writeWebMessagePage(
	w http.ResponseWriter,
	frameID string,
	messages []map[string]any,
	limit int,
	r *http.Request,
) {
	start, err := webMessagePageStart(r, len(messages), limit, func(messageID string) (*int, error) {
		for index, message := range messages {
			if webMessageID(message, index) == messageID {
				return &index, nil
			}
		}
		return nil, nil
	})
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": err.Error()})
		return
	}
	end := min(len(messages), start+limit)
	writeWorkspaceJSON(w, http.StatusOK, webMessagePage(frameID, messages[start:end], start, len(messages)))
}

func webMessagePageStart(
	r *http.Request,
	total int,
	limit int,
	locate func(string) (*int, error),
) (int, error) {
	before := strings.TrimSpace(r.URL.Query().Get("before"))
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	anchor := strings.TrimSpace(r.URL.Query().Get("anchor_message_id"))
	if before != "" && after != "" {
		return 0, errors.New("before and after cursors are mutually exclusive")
	}
	if anchor != "" {
		index, err := locate(anchor)
		if err != nil {
			return 0, err
		}
		if index == nil {
			return 0, errors.New("anchor message was not found")
		}
		return max(0, min(*index-limit/2, max(0, total-limit))), nil
	}
	if before != "" {
		cursor, err := parseWebMessageCursor(before)
		if err != nil {
			index, locateErr := locate(before)
			if locateErr != nil {
				return 0, locateErr
			}
			if index == nil {
				return 0, err
			}
			cursor = *index
		}
		return max(0, min(cursor, total)-limit), nil
	}
	if after != "" {
		cursor, err := parseWebMessageCursor(after)
		if err != nil {
			index, locateErr := locate(after)
			if locateErr != nil {
				return 0, locateErr
			}
			if index == nil {
				return 0, err
			}
			cursor = *index
		}
		return min(total, cursor+1), nil
	}
	return max(0, total-limit), nil
}

func parseWebMessageCursor(value string) (int, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "idx:")
	index, err := strconv.Atoi(value)
	if err != nil || index < 0 {
		return 0, errors.New("invalid message cursor")
	}
	return index, nil
}

func webMessagePage(frameID string, messages []map[string]any, start, total int) map[string]any {
	items := make([]map[string]any, 0, len(messages))
	for index, message := range messages {
		items = append(items, webConversationMessage(frameID, message, start+index))
	}
	var oldest any
	var newest any
	if len(items) > 0 {
		oldest = "idx:" + strconv.Itoa(start)
		newest = "idx:" + strconv.Itoa(start+len(items)-1)
	}
	return map[string]any{
		"items": items, "oldest_cursor": oldest, "newest_cursor": newest,
		"has_more_before": start > 0, "has_more_after": start+len(items) < total,
	}
}

func webConversationMessage(frameID string, message map[string]any, index int) map[string]any {
	messageID := webMessageID(message, index)
	messageType := strings.TrimSpace(webString(message["type"]))
	role := strings.ToLower(strings.TrimSpace(webString(message["role"])))
	if messageType == "" || messageType == "message" || messageType == "user_message" || messageType == "assistant_message" {
		messageType = "text"
	}
	content := message["content"]
	if messageType == "text" {
		content = map[string]any{"content": webMessageText(message)}
	}
	position := "left"
	if role == "user" {
		position = "right"
	}
	result := map[string]any{
		"id": messageID, "msg_id": messageID, "conversation_id": frameID,
		"type": messageType, "content": content, "position": position, "status": "finish",
	}
	if createdAt := webMessageCreatedAt(message); createdAt > 0 {
		result["created_at"] = createdAt
	}
	if hidden, ok := message["hidden"].(bool); ok {
		result["hidden"] = hidden
	}
	if status := strings.TrimSpace(webString(message["status"])); status != "" {
		result["status"] = status
	}
	return result
}

func webMessageID(message map[string]any, index int) string {
	for _, key := range []string{"id", "msg_id", "messageUuid", "message_uuid", "uuid", "_uuid", "clientMessageId"} {
		if value := strings.TrimSpace(webString(message[key])); value != "" {
			return value
		}
	}
	return "message-" + strconv.Itoa(index)
}

func webMessageText(message map[string]any) string {
	if text := webString(message["text"]); text != "" {
		return text
	}
	switch content := message["content"].(type) {
	case string:
		return content
	case map[string]any:
		return webString(content["content"])
	case []any:
		parts := make([]string, 0, len(content))
		for _, item := range content {
			if record, ok := item.(map[string]any); ok {
				if text := webString(record["text"]); text != "" {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "")
	default:
		return ""
	}
}

func webMessageCreatedAt(message map[string]any) int64 {
	for _, key := range []string{"created_at", "createdAt", "timestamp"} {
		switch value := message[key].(type) {
		case float64:
			return int64(value)
		case int64:
			return value
		case json.Number:
			parsed, _ := value.Int64()
			return parsed
		case string:
			if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
				return parsed.UnixMilli()
			}
		}
	}
	return 0
}

func (s *Server) handleWebConversationMessageGet(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, messageID string) {
	branchID := strings.TrimSpace(r.URL.Query().Get("branch_id"))
	if s.transcriptWebReadModel != nil {
		readModel, active, err := s.activatedTranscriptWebReadModel(
			r.Context(), compatAgentUserID(r), frame.ID, branchID,
		)
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		if active {
			record, found, err := s.transcriptWebReadModel.GetTranscriptWebMessageByID(
				r.Context(), readModel.transcriptWebMessageIdentity(compatAgentUserID(r), messageID),
			)
			if err != nil {
				writeWebConversationError(w, transcriptWebReadModelServingError(err))
				return
			}
			if !found {
				writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "message not found"})
				return
			}
			messages, err := transcriptWebReadModelMessageMaps([]transcriptstore.TranscriptWebMessageRecord{record})
			if err != nil {
				writeWebConversationError(w, transcriptWebStorageError(err))
				return
			}
			if err := s.enrichTranscriptWebConversationMessages(r.Context(), frame.ID, messages); err != nil {
				writeWebConversationError(w, err)
				return
			}
			writeWorkspaceJSON(w, http.StatusOK, messages[0])
			return
		}
	}
	if view, active, err := s.loadActivatedTranscriptWebHistoryView(
		r.Context(), compatAgentUserID(r), frame.ID, branchID,
	); err != nil {
		writeWebConversationError(w, err)
		return
	} else if active {
		if index := exactTranscriptMessageIndexValue(view.messages, messageID); index >= 0 {
			message := cloneTranscriptWebProjectionMap(view.messages[index])
			if err := s.enrichTranscriptWebConversationMessages(r.Context(), frame.ID, []map[string]any{message}); err != nil {
				writeWebConversationError(w, err)
				return
			}
			if err := s.refreshCachedTranscriptWebArtifactAvailability(
				r.Context(), view.stream, compatAgentUserID(r), []map[string]any{message},
			); err != nil {
				writeWebConversationError(w, transcriptWebStorageError(err))
				return
			}
			writeWorkspaceJSON(w, http.StatusOK, message)
			return
		}
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "message not found"})
		return
	}
	if s.transcriptStore != nil {
		writeWebConversationError(w, transcriptWebAuthorityUnavailableError())
		return
	}
	if branchID != "" {
		writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"message": "conversation branch changed; refresh and retry"})
		return
	}
	messages, err := s.loadRichWebFrameMessages(frame.ID)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	if hasRichWebMessageBlocks(messages) {
		projected, err := s.projectRichWebConversationMessages(frame.ID, messages, "")
		if err != nil {
			writeWebConversationError(w, err)
			return
		}
		if index := richWebMessageIndex(projected, messageID); index >= 0 {
			writeWorkspaceJSON(w, http.StatusOK, projected[index])
			return
		}
	}
	index, err := s.workspaceStore.LocateCompatibilityFrameMessage(frame.ID, messageID)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	if index == nil {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "message not found"})
		return
	}
	page, err := s.workspaceStore.CompatibilityFrameMessages(frame.ID, *index, 1)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	if len(page.Messages) != 1 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"message": "message not found"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, webConversationMessage(frame.ID, page.Messages[0], *index))
}
