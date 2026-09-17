package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type richWebToolResult struct {
	Output  string
	IsError bool
}

type richWebSubagentEntry struct {
	callID       string
	childFrameID string
	projection   map[string]any
}

func (s *Server) loadRichWebFrameMessages(frameID string) ([]map[string]any, error) {
	messages := make([]map[string]any, 0)
	for len(messages) < maxTraceMessagesPerFrame {
		page, err := s.workspaceStore.CompatibilityFrameMessages(frameID, len(messages), 500)
		if err != nil {
			return nil, err
		}
		messages = append(messages, page.Messages...)
		if len(page.Messages) == 0 || len(messages) >= page.Total {
			return messages, nil
		}
	}
	return nil, fmt.Errorf("structured conversation exceeds the %d-message projection limit", maxTraceMessagesPerFrame)
}

func hasRichWebMessageBlocks(messages []map[string]any) bool {
	for _, message := range messages {
		if _, ok := message["content"].([]any); ok {
			return true
		}
	}
	return false
}

func (s *Server) projectRichWebConversationMessages(
	frameID string,
	rawMessages []map[string]any,
	branchID string,
) ([]map[string]any, error) {
	frame, found, err := s.workspaceStore.GetFrame(frameID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("conversation frame not found")
	}
	snapshot, err := s.workspaceStore.GetFrameTraceSnapshot(frame.RootFrameID, nil)
	if err != nil {
		return nil, err
	}
	frames := snapshot.Frames
	toolResults := collectRichWebToolResults(rawMessages)
	subagents := collectRichWebSubagents(frame, rawMessages, frames)
	subagentEvents := collectRichWebSubagentEvents(frame, rawMessages, frames)
	mapped := make([]map[string]any, 0, len(rawMessages))
	createdAt := frame.CreatedAt.UTC().UnixMilli()

	for messageIndex, rawMessage := range rawMessages {
		role := strings.ToLower(strings.TrimSpace(webString(rawMessage["role"])))
		if role != "user" && role != "assistant" {
			continue
		}
		blocks := richWebDisplayBlocks(rawMessage)
		sourceID := strings.TrimSpace(webString(rawMessage["_uuid"]))
		if sourceID == "" {
			sourceID = strings.TrimSpace(webString(rawMessage["uuid"]))
		}
		if sourceID == "" {
			sourceID = fmt.Sprintf("%s:%d", frame.ID, messageIndex)
		}
		position := "left"
		if role == "user" {
			position = "right"
		}
		for blockIndex, block := range blocks {
			blockType := strings.TrimSpace(webString(block["type"]))
			messageID := fmt.Sprintf("%s:%d", sourceID, blockIndex)
			messageCreatedAt := createdAt + int64(messageIndex*100+blockIndex)
			switch blockType {
			case "text":
				content := webString(block["text"])
				if content == "" {
					continue
				}
				var branch any
				if branchID != "" {
					branch = branchID
				}
				mapped = append(mapped, map[string]any{
					"id": messageID, "msg_id": sourceID, "conversation_id": frame.ID,
					"type": "text", "position": position, "status": "finish", "created_at": messageCreatedAt,
					"content": map[string]any{
						"content": content,
						"synonBiomed": map[string]any{
							"messageIndex": messageIndex, "blockIndex": blockIndex, "branchId": branch,
						},
					},
				})
			case "thinking":
				content := webString(block["thinking"])
				if content == "" {
					content = webString(block["text"])
				}
				if content == "" {
					continue
				}
				mapped = append(mapped, map[string]any{
					"id": messageID, "msg_id": sourceID, "conversation_id": frame.ID,
					"type": "thinking", "position": "left", "status": "finish", "created_at": messageCreatedAt,
					"content": map[string]any{"content": content, "status": "done"},
				})
			case "tool_use":
				callID := strings.TrimSpace(webString(block["id"]))
				name := strings.TrimSpace(webString(block["name"]))
				if callID == "" || name == "" || len(subagentEvents[callID]) > 0 {
					continue
				}
				input, _ := block["input"].(map[string]any)
				if input == nil {
					input = map[string]any{}
				}
				description := strings.TrimSpace(webString(input["human_description"]))
				if description == "" {
					description = strings.TrimSpace(webString(block["human_description"]))
				}
				result, hasResult := toolResults[callID]
				subagent := subagents[callID]
				isRunning := richWebSubagentRunning(subagent) || (!hasResult && richWebConversationRunning(frame.Status))
				content := map[string]any{
					"call_id": callID, "name": name, "args": input, "input": input,
				}
				if description != "" {
					content["description"] = description
				}
				if subagent != nil {
					content["subagent"] = subagent
				}
				messageStatus := "finish"
				switch {
				case hasResult && result.IsError:
					content["status"] = "error"
					content["error"] = result.Output
					if result.Output == "" {
						content["error"] = "Synon Biomed tool call failed"
					}
					messageStatus = "error"
				case isRunning:
					content["status"] = "running"
					messageStatus = "work"
				default:
					content["status"] = "completed"
					if hasResult {
						content["output"] = result.Output
					}
				}
				mapped = append(mapped, map[string]any{
					"id": callID, "msg_id": sourceID, "conversation_id": frame.ID,
					"type": "tool_call", "content": content, "created_at": messageCreatedAt,
					"position": "left", "status": messageStatus,
				})
			case "tool_result":
				callID := strings.TrimSpace(webString(block["tool_use_id"]))
				events := subagentEvents[callID]
				if callID == "" || len(events) == 0 {
					continue
				}
				mapped = append(mapped, map[string]any{
					"id":     fmt.Sprintf("%s:%d:subagent-events", sourceID, blockIndex),
					"msg_id": sourceID, "conversation_id": frame.ID, "type": "tool_call",
					"content": map[string]any{
						"call_id": callID + ":subagent-events", "name": "subagent_events",
						"args": map[string]any{}, "status": "completed", "subagentEvents": events,
					},
					"created_at": messageCreatedAt, "position": "left", "status": "finish",
				})
			case "image":
				content := richWebImageMarkdown(block)
				if content == "" {
					continue
				}
				mapped = append(mapped, map[string]any{
					"id": messageID, "msg_id": sourceID, "conversation_id": frame.ID,
					"type": "text", "content": map[string]any{"content": content},
					"created_at": messageCreatedAt, "position": position, "status": "finish",
				})
			}
		}
	}
	s.sanitizeTranscriptWebPublicMessages(mapped)
	return mapped, nil
}

// enrichTranscriptWebConversationMessages restores delegation and
// notification semantics after a rich history crosses the typed Transcript
// bootstrap boundary. The read model remains the sole message authority; this
// response-only pass uses its canonical tool calls and durable Frame lineage.
func (s *Server) enrichTranscriptWebConversationMessages(
	ctx context.Context,
	frameID string,
	messages []map[string]any,
) error {
	if err := s.enrichTranscriptWebArtifactPresentation(ctx, frameID, messages); err != nil {
		return err
	}
	hasToolCall := false
	rawMessages := make([]map[string]any, 0, len(messages)*2)
	for _, message := range messages {
		if webString(message["type"]) != "tool_call" {
			continue
		}
		content, ok := message["content"].(map[string]any)
		if !ok || content == nil {
			continue
		}
		callID := strings.TrimSpace(webString(content["call_id"]))
		name := strings.TrimSpace(webString(content["name"]))
		if callID == "" || name == "" {
			continue
		}
		hasToolCall = true
		input, _ := content["args"].(map[string]any)
		if input == nil {
			input = map[string]any{}
		}
		rawMessages = append(rawMessages, map[string]any{
			"role": "assistant", "content": []any{map[string]any{
				"type": "tool_use", "id": callID, "name": name, "input": input,
			}},
		})
		if output, found := content["output"]; found {
			rawMessages = append(rawMessages, map[string]any{
				"role": "user", "content": []any{map[string]any{
					"type": "tool_result", "tool_use_id": callID,
					"content": richWebFormatValue(output), "is_error": content["status"] == "error",
				}},
			})
		}
	}
	if !hasToolCall {
		return nil
	}
	if s == nil || s.workspaceStore == nil {
		return errors.New("workspace runtime is not configured")
	}
	frame, found, err := s.workspaceStore.GetFrame(frameID)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("conversation frame not found")
	}
	snapshot, err := s.workspaceStore.GetFrameTraceSnapshot(frame.RootFrameID, nil)
	if err != nil {
		return err
	}
	subagents := collectRichWebSubagents(frame, rawMessages, snapshot.Frames)
	subagentEvents := collectRichWebSubagentEvents(frame, rawMessages, snapshot.Frames)
	for _, message := range messages {
		if webString(message["type"]) != "tool_call" {
			continue
		}
		content, ok := message["content"].(map[string]any)
		if !ok || content == nil {
			continue
		}
		callID := strings.TrimSpace(webString(content["call_id"]))
		if subagent := subagents[callID]; subagent != nil {
			content["subagent"] = subagent
		}
		if events := subagentEvents[callID]; len(events) > 0 {
			for _, key := range []string{"description", "input", "output", "subagent"} {
				delete(content, key)
			}
			content["call_id"] = callID + ":subagent-events"
			content["name"] = "subagent_events"
			content["args"] = map[string]any{}
			content["status"] = "completed"
			content["subagentEvents"] = events
		}
	}
	return nil
}

func (s *Server) writeRichWebMessagePage(w http.ResponseWriter, r *http.Request, messages []map[string]any, limit int) {
	before := strings.TrimSpace(r.URL.Query().Get("before"))
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	anchor := strings.TrimSpace(r.URL.Query().Get("anchor_message_id"))
	if before != "" && after != "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "before and after cursors are mutually exclusive"})
		return
	}
	start, end := max(0, len(messages)-limit), len(messages)
	if before != "" {
		end = richWebMessageIndex(messages, before)
		if end < 0 {
			end = len(messages)
		}
		start = max(0, end-limit)
	} else if after != "" {
		index := richWebMessageIndex(messages, after)
		if index < 0 {
			start = len(messages)
		} else {
			start = index + 1
		}
		end = min(len(messages), start+limit)
	} else if anchor != "" {
		if index := richWebMessageIndex(messages, anchor); index >= 0 {
			start = max(0, index-limit/2)
			end = min(len(messages), start+limit)
			start = max(0, end-limit)
		}
	}
	items := compactWebConversationMessagePage(r, messages[start:end])
	var oldest any
	var newest any
	if len(items) > 0 {
		oldest = items[0]["id"]
		newest = items[len(items)-1]["id"]
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"items": items, "oldest_cursor": oldest, "newest_cursor": newest,
		"has_more_before": start > 0, "has_more_after": end < len(messages),
	})
}

type transcriptBranchMessageCursor struct {
	Version                    int    `json:"version"`
	BranchID                   string `json:"branch_id"`
	BranchGeneration           int64  `json:"branch_generation"`
	ThroughPublicationSequence int64  `json:"through_publication_sequence"`
	Index                      int    `json:"index"`
}

func (s *Server) writeTranscriptBranchMessagePage(
	w http.ResponseWriter,
	r *http.Request,
	frameID string,
	messages []map[string]any,
	limit int,
	snapshot transcriptstore.ProjectionSnapshot,
	stream transcriptstore.Stream,
	ownerID string,
) {
	before := strings.TrimSpace(r.URL.Query().Get("before"))
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	anchor := strings.TrimSpace(r.URL.Query().Get("anchor_message_id"))
	if before != "" && after != "" {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "before and after cursors are mutually exclusive"})
		return
	}
	start, end := max(0, len(messages)-limit), len(messages)
	if before != "" {
		cursor, status, err := s.resolveTranscriptBranchMessageCursor(r, frameID, before, snapshot, messages)
		if err != nil {
			writeWorkspaceJSON(w, status, map[string]any{"message": err.Error()})
			return
		}
		end = cursor.Index
		start = max(0, end-limit)
	} else if after != "" {
		cursor, status, err := s.resolveTranscriptBranchMessageCursor(r, frameID, after, snapshot, messages)
		if err != nil {
			writeWorkspaceJSON(w, status, map[string]any{"message": err.Error()})
			return
		}
		start = cursor.Index + 1
		end = min(len(messages), start+limit)
	} else if anchor != "" {
		index := richWebMessageIndex(messages, anchor)
		if index < 0 {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"message": "anchor message was not found"})
			return
		}
		start = max(0, index-limit/2)
		end = min(len(messages), start+limit)
		start = max(0, end-limit)
	}
	pageMessages := cloneTranscriptWebProjectionMessages(messages[start:end])
	if err := s.enrichTranscriptWebConversationMessages(r.Context(), frameID, pageMessages); err != nil {
		writeWebConversationError(w, err)
		return
	}
	if err := s.refreshCachedTranscriptWebArtifactAvailability(r.Context(), stream, ownerID, pageMessages); err != nil {
		writeWebConversationError(w, transcriptWebStorageError(err))
		return
	}
	items := compactWebConversationMessagePage(r, pageMessages)
	var oldest any
	var newest any
	if len(items) > 0 {
		oldest = encodeTranscriptBranchMessageCursor(snapshot, start)
		newest = encodeTranscriptBranchMessageCursor(snapshot, end-1)
	}
	payload := map[string]any{
		"items": items, "oldest_cursor": oldest, "newest_cursor": newest,
		"has_more_before": start > 0, "has_more_after": end < len(messages),
		"branch_id": snapshot.BranchID, "branch_generation": snapshot.BranchGeneration,
		"through_publication_sequence": snapshot.ThroughPublicationSequence,
	}
	writePrivateRevalidatedWorkspaceJSON(w, r, ownerID, payload)
}

func (s *Server) resolveTranscriptBranchMessageCursor(
	r *http.Request,
	frameID, value string,
	snapshot transcriptstore.ProjectionSnapshot,
	messages []map[string]any,
) (transcriptBranchMessageCursor, int, error) {
	cursor, status, err := parseTranscriptBranchMessageCursor(value, snapshot, len(messages))
	if err == nil || s == nil || s.transcriptStore == nil {
		return cursor, status, err
	}
	if cursor.Version != 1 || cursor.BranchID == "" || cursor.BranchGeneration <= 0 ||
		cursor.ThroughPublicationSequence < 0 || cursor.Index < 0 {
		return cursor, status, err
	}
	translated, found, translateErr := s.transcriptStore.ResolveActivatedLegacyCursor(
		r.Context(), compatAgentUserID(r), frameID, cursor.BranchID, cursor.BranchGeneration,
		cursor.ThroughPublicationSequence, cursor.Index,
	)
	if translateErr != nil {
		return transcriptBranchMessageCursor{}, http.StatusInternalServerError, errors.New("unable to resolve conversation cursor")
	}
	if !found || translated.TargetBranchID != snapshot.BranchID ||
		translated.TargetMessageIndex < 0 || translated.TargetMessageIndex >= len(messages) ||
		(webString(messages[translated.TargetMessageIndex]["id"]) != translated.StableMessageID &&
			webString(messages[translated.TargetMessageIndex]["msg_id"]) != translated.StableMessageID) {
		return cursor, status, err
	}
	return transcriptBranchMessageCursor{
		Version: 1, BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Index: translated.TargetMessageIndex,
	}, http.StatusOK, nil
}

func encodeTranscriptBranchMessageCursor(snapshot transcriptstore.ProjectionSnapshot, index int) string {
	encoded, _ := json.Marshal(transcriptBranchMessageCursor{
		Version: 1, BranchID: snapshot.BranchID, BranchGeneration: snapshot.BranchGeneration,
		ThroughPublicationSequence: snapshot.ThroughPublicationSequence, Index: index,
	})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func parseTranscriptBranchMessageCursor(
	value string,
	snapshot transcriptstore.ProjectionSnapshot,
	messageCount int,
) (transcriptBranchMessageCursor, int, error) {
	if len(value) == 0 || len(value) > 1024 {
		return transcriptBranchMessageCursor{}, http.StatusBadRequest, errors.New("invalid conversation branch cursor")
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(raw) == 0 || len(raw) > 512 {
		return transcriptBranchMessageCursor{}, http.StatusBadRequest, errors.New("invalid conversation branch cursor")
	}
	var cursor transcriptBranchMessageCursor
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 ||
		cursor.BranchID == "" || cursor.Index < 0 {
		return transcriptBranchMessageCursor{}, http.StatusBadRequest, errors.New("invalid conversation branch cursor")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return transcriptBranchMessageCursor{}, http.StatusBadRequest, errors.New("invalid conversation branch cursor")
	}
	if cursor.Index >= messageCount {
		return cursor, http.StatusBadRequest, errors.New("invalid conversation branch cursor")
	}
	if cursor.BranchID != snapshot.BranchID {
		return cursor, http.StatusBadRequest, errors.New("conversation branch cursor belongs to another branch")
	}
	if cursor.BranchGeneration != snapshot.BranchGeneration ||
		cursor.ThroughPublicationSequence != snapshot.ThroughPublicationSequence {
		return cursor, http.StatusConflict, errors.New("conversation branch cursor is stale")
	}
	return cursor, http.StatusOK, nil
}

func richWebMessageIndex(messages []map[string]any, id string) int {
	if strings.HasPrefix(id, "idx:") {
		if index, err := strconv.Atoi(strings.TrimPrefix(id, "idx:")); err == nil && index >= 0 && index < len(messages) {
			return index
		}
	}
	for index, message := range messages {
		if webString(message["id"]) == id || webString(message["msg_id"]) == id {
			return index
		}
	}
	return -1
}

func collectRichWebToolResults(messages []map[string]any) map[string]richWebToolResult {
	results := map[string]richWebToolResult{}
	for _, message := range messages {
		for _, block := range richWebRawBlocks(message["content"]) {
			if webString(block["type"]) != "tool_result" {
				continue
			}
			callID := strings.TrimSpace(webString(block["tool_use_id"]))
			if callID == "" {
				continue
			}
			results[callID] = richWebToolResult{Output: richWebFormatValue(block["content"]), IsError: block["is_error"] == true}
		}
	}
	return results
}

func collectRichWebSubagents(
	parent workspace.Frame,
	messages []map[string]any,
	frames []workspace.Frame,
) map[string]map[string]any {
	frameByID := make(map[string]workspace.Frame, len(frames))
	for _, frame := range frames {
		frameByID[frame.ID] = frame
	}
	contextMapping, _ := parent.ContextData["_tool_id_to_frame_id"].(map[string]any)
	resultMapping := collectRichWebResultChildIDs(messages)
	entries := make([]richWebSubagentEntry, 0)
	ordinal := 0
	for _, message := range messages {
		if message["_harness_notice"] == true {
			continue
		}
		for _, block := range richWebRawBlocks(message["content"]) {
			if webString(block["type"]) != "tool_use" {
				continue
			}
			callID := strings.TrimSpace(webString(block["id"]))
			name := richWebNormalizedToolName(webString(block["name"]))
			input, _ := block["input"].(map[string]any)
			if input == nil {
				input = map[string]any{}
			}
			inputChildID := strings.TrimSpace(webString(input["child_frame_id"]))
			delegation := name == "delegate" || (name == "agent" && strings.TrimSpace(webString(input["delegate_name"])) != "")
			childMessage := name == "send_message" && inputChildID != ""
			if callID == "" || (!delegation && !childMessage) {
				continue
			}
			ordinal++
			childID := strings.TrimSpace(webString(contextMapping[callID]))
			if childID == "" {
				childID = inputChildID
			}
			if childID == "" {
				childID = resultMapping[callID]
			}
			child, hasChild := frameByID[childID]
			fallbackName := strings.TrimSpace(webString(input["delegate_name"]))
			if fallbackName == "" {
				fallbackName = strings.TrimSpace(webString(input["name"]))
			}
			if fallbackName == "" {
				fallbackName = strings.TrimSpace(webString(input["agent_name"]))
			}
			projectionOrdinal := ordinal
			if hasChild && child.RootSequence > 0 {
				projectionOrdinal = int(child.RootSequence)
			}
			projection := map[string]any{"ordinal": projectionOrdinal, "superseded": false}
			if childID != "" {
				projection["frameId"] = childID
			}
			if hasChild {
				projection["rootFrameId"] = child.RootFrameID
				if child.ParentFrameID != "" {
					projection["parentFrameId"] = child.ParentFrameID
				}
				if child.AgentName != "" {
					projection["agentName"] = child.AgentName
				}
				delegateName := child.DelegateName
				if delegateName == "" {
					delegateName = fallbackName
				}
				if delegateName != "" {
					projection["delegateName"] = delegateName
				}
				projection["status"] = child.Status
				projection["messageCount"] = child.MessageCount
				if child.StatusDescription != "" {
					projection["statusDescription"] = child.StatusDescription
				}
				if child.TaskSummary != "" {
					projection["taskSummary"] = child.TaskSummary
				}
				if latestAction := richWebLatestSubagentAction(child); latestAction != "" {
					projection["latestAction"] = latestAction
				}
			} else {
				projection["status"] = "processing"
				if fallbackName != "" {
					projection["delegateName"] = fallbackName
				}
			}
			entries = append(entries, richWebSubagentEntry{callID: callID, childFrameID: childID, projection: projection})
		}
	}
	latestToolByChild := map[string]string{}
	for _, entry := range entries {
		if entry.childFrameID != "" {
			latestToolByChild[entry.childFrameID] = entry.callID
		}
	}
	result := map[string]map[string]any{}
	for _, entry := range entries {
		if entry.childFrameID != "" && latestToolByChild[entry.childFrameID] != entry.callID {
			entry.projection["superseded"] = true
		}
		result[entry.callID] = entry.projection
	}
	return result
}

func collectRichWebResultChildIDs(messages []map[string]any) map[string]string {
	result := map[string]string{}
	for _, message := range messages {
		for _, block := range richWebRawBlocks(message["content"]) {
			if webString(block["type"]) != "tool_result" {
				continue
			}
			callID := strings.TrimSpace(webString(block["tool_use_id"]))
			childID := strings.TrimSpace(webString(block["child_frame_id"]))
			if childID == "" {
				childID = richWebChildFrameID(block["content"])
			}
			if callID != "" && childID != "" {
				result[callID] = childID
			}
		}
	}
	return result
}

func collectRichWebSubagentEvents(
	parent workspace.Frame,
	messages []map[string]any,
	frames []workspace.Frame,
) map[string][]map[string]any {
	children := make([]workspace.Frame, 0)
	for _, frame := range frames {
		if frame.ParentFrameID == parent.ID && !frame.IsHidden {
			children = append(children, frame)
		}
	}
	sort.Slice(children, func(i, j int) bool {
		if children[i].CreatedAt.Equal(children[j].CreatedAt) {
			return children[i].ID < children[j].ID
		}
		return children[i].CreatedAt.Before(children[j].CreatedAt)
	})
	childByID := make(map[string]workspace.Frame, len(children))
	ordinalByID := make(map[string]int, len(children))
	for index, child := range children {
		childByID[child.ID] = child
		ordinal := index + 1
		if child.RootSequence > 0 {
			ordinal = int(child.RootSequence)
		}
		ordinalByID[child.ID] = ordinal
	}
	result := map[string][]map[string]any{}
	for _, message := range messages {
		for _, block := range richWebRawBlocks(message["content"]) {
			if webString(block["type"]) != "tool_result" {
				continue
			}
			callID := strings.TrimSpace(webString(block["tool_use_id"]))
			if callID == "" {
				continue
			}
			events := make([]map[string]any, 0)
			for _, envelope := range richWebNotificationEnvelopes(block["content"]) {
				if notifications, ok := envelope["notifications"].([]any); ok {
					for _, notification := range notifications {
						if parsed := richWebSubagentNotification(notification, childByID, ordinalByID); parsed != nil {
							events = append(events, parsed)
						}
					}
				}
				if collected, ok := envelope["_collected"].([]any); ok {
					for _, completion := range collected {
						if parsed := richWebCollectedCompletion(completion, childByID, ordinalByID); parsed != nil {
							events = append(events, parsed)
						}
					}
				}
			}
			if len(events) > 0 {
				result[callID] = events
			}
		}
	}
	return result
}

func richWebSubagentNotification(
	value any,
	childByID map[string]workspace.Frame,
	ordinalByID map[string]int,
) map[string]any {
	notification, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	payload, _ := notification["payload"].(map[string]any)
	if payload == nil {
		payload = map[string]any{}
	}
	frameID := strings.TrimSpace(webString(payload["sender_frame_id"]))
	if frameID == "" {
		frameID = strings.TrimSpace(webString(notification["sender_frame_id"]))
	}
	if frameID == "" {
		return nil
	}
	child := childByID[frameID]
	childName := strings.TrimSpace(webString(payload["name"]))
	if childName == "" {
		childName = strings.TrimSpace(webString(payload["agent_name"]))
	}
	if childName == "" {
		childName = child.DelegateName
	}
	if childName == "" {
		childName = child.AgentName
	}
	if childName == "" {
		childName = "Synon Biomed"
	}
	ordinal := ordinalByID[frameID]
	if ordinal == 0 {
		ordinal = 1
	}
	if webString(notification["notification_type"]) == "completion" {
		if webString(payload["status"]) != "completed" {
			return nil
		}
		result := map[string]any{
			"kind": "completion", "frameId": frameID, "ordinal": ordinal,
			"childName": childName, "bullets": richWebStringSlice(payload["_completion_bullets"]),
		}
		if wall, ok := richWebNumber(payload["wall_s"]); ok {
			result["wallSeconds"] = wall
		}
		return result
	}
	if webString(notification["notification_type"]) != "child_message" {
		return nil
	}
	text := strings.TrimSpace(webString(payload["text"]))
	if text == "" {
		return nil
	}
	kind := "info"
	if webString(payload["kind"]) == "question" {
		kind = "question"
	}
	return map[string]any{
		"kind": kind, "frameId": frameID, "ordinal": ordinal,
		"childName": childName, "text": text,
	}
}

func richWebCollectedCompletion(
	value any,
	childByID map[string]workspace.Frame,
	ordinalByID map[string]int,
) map[string]any {
	completion, ok := value.(map[string]any)
	if !ok || webString(completion["status"]) != "completed" {
		return nil
	}
	frameID := strings.TrimSpace(webString(completion["sender_frame_id"]))
	if frameID == "" {
		return nil
	}
	child := childByID[frameID]
	childName := child.DelegateName
	if childName == "" {
		childName = child.AgentName
	}
	if childName == "" {
		childName = "Synon Biomed"
	}
	ordinal := ordinalByID[frameID]
	if ordinal == 0 {
		ordinal = 1
	}
	result := map[string]any{
		"kind": "completion", "frameId": frameID, "ordinal": ordinal,
		"childName": childName, "bullets": richWebStringSlice(completion["_completion_bullets"]),
	}
	if wall, ok := richWebNumber(completion["wall_s"]); ok {
		result["wallSeconds"] = wall
	}
	return result
}

func richWebDisplayBlocks(message map[string]any) []map[string]any {
	hidden := richWebShouldHideMessage(message)
	content := richWebMessageContent(message)
	if text, ok := content.(string); ok {
		if hidden || text == "" {
			return nil
		}
		return []map[string]any{{"type": "text", "text": text}}
	}
	blocks := richWebRawBlocks(content)
	if hidden {
		filtered := make([]map[string]any, 0)
		for _, block := range blocks {
			if webString(block["type"]) == "tool_result" {
				filtered = append(filtered, block)
			}
		}
		return filtered
	}
	filtered := make([]map[string]any, 0, len(blocks))
	skipNextAttachment := false
	for _, block := range blocks {
		if skipNextAttachment {
			skipNextAttachment = false
			continue
		}
		if webString(block["type"]) == "text" && strings.HasPrefix(webString(block["text"]), "[System]") {
			text := webString(block["text"])
			if richWebFailedAttachment(text) || block["_harness_notice"] == true {
				filtered = append(filtered, block)
				continue
			}
			if strings.HasPrefix(text, "[System] Attached file:") {
				skipNextAttachment = true
			}
			continue
		}
		filtered = append(filtered, block)
	}
	return filtered
}

func richWebShouldHideMessage(message map[string]any) bool {
	if message["_harness_notice"] == true || richWebTruthy(message["_task_boundary"]) ||
		richWebTruthy(message["_rolling_summary"]) || richWebTruthy(message["_harness_prompt"]) ||
		richWebTruthy(message["_from_agent"]) || richWebTruthy(message["_from_aside"]) {
		return true
	}
	content := richWebMessageContent(message)
	if text, ok := content.(string); ok {
		return strings.HasPrefix(text, "[Auditor]") || richWebInternalText(text)
	}
	blocks := richWebRawBlocks(content)
	if len(blocks) == 0 {
		return true
	}
	allToolResults := true
	textCount := 0
	allTextInternal := true
	for _, block := range blocks {
		if webString(block["type"]) != "tool_result" {
			allToolResults = false
		}
		if webString(block["type"]) == "text" {
			textCount++
			text := webString(block["text"])
			if !strings.HasPrefix(text, "[Auditor]") && !richWebInternalText(text) {
				allTextInternal = false
			}
		}
	}
	return allToolResults || (textCount > 0 && allTextInternal)
}

func richWebMessageContent(message map[string]any) any {
	if content, found := message["content"]; found && content != nil {
		return content
	}
	return message["text"]
}

func richWebRawBlocks(value any) []map[string]any {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	blocks := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if block, ok := item.(map[string]any); ok {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func richWebNotificationEnvelopes(value any) []map[string]any {
	if text, ok := value.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) != nil {
			return nil
		}
		return richWebNotificationEnvelopes(decoded)
	}
	if items, ok := value.([]any); ok {
		result := make([]map[string]any, 0)
		for _, item := range items {
			result = append(result, richWebNotificationEnvelopes(item)...)
		}
		return result
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	if _, notifications := record["notifications"].([]any); notifications {
		return []map[string]any{record}
	}
	if _, collected := record["_collected"].([]any); collected {
		return []map[string]any{record}
	}
	if text, ok := record["text"].(string); ok {
		return richWebNotificationEnvelopes(text)
	}
	return nil
}

func richWebChildFrameID(value any) string {
	if text, ok := value.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(text), &decoded) != nil {
			return ""
		}
		return richWebChildFrameID(decoded)
	}
	if items, ok := value.([]any); ok {
		for _, item := range items {
			if id := richWebChildFrameID(item); id != "" {
				return id
			}
		}
		return ""
	}
	record, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if id := strings.TrimSpace(webString(record["child_frame_id"])); id != "" {
		return id
	}
	if id := strings.TrimSpace(webString(record["_frame_id"])); id != "" {
		return id
	}
	if text, ok := record["text"].(string); ok {
		return richWebChildFrameID(text)
	}
	return ""
}

func richWebLatestSubagentAction(frame workspace.Frame) string {
	latest, _ := frame.ContextData["_latest_tool_block"].(map[string]any)
	if latest == nil {
		return frame.StatusDescription
	}
	input, _ := latest["input"].(map[string]any)
	for _, value := range []string{
		strings.TrimSpace(webString(input["human_description"])),
		strings.TrimSpace(webString(latest["human_description"])),
		strings.TrimSpace(frame.StatusDescription),
		strings.TrimSpace(webString(latest["name"])),
	} {
		if value != "" {
			return value
		}
	}
	return ""
}

func richWebSubagentRunning(subagent map[string]any) bool {
	if subagent == nil || subagent["superseded"] == true {
		return false
	}
	switch strings.ToLower(webString(subagent["status"])) {
	case "processing", "running", "executing", "in_progress", "in-progress", "queued", "pending":
		return true
	default:
		return false
	}
}

func richWebConversationRunning(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "processing", "running", "executing", "in_progress", "in-progress":
		return true
	default:
		return false
	}
}

func richWebNormalizedToolName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == ':' || r == '/' })
	if len(parts) == 0 {
		return ""
	}
	return strings.ToLower(parts[len(parts)-1])
}

func richWebFormatValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	if items, ok := value.([]any); ok {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok {
				parts = append(parts, text)
				continue
			}
			if record, ok := item.(map[string]any); ok {
				if text, ok := record["text"].(string); ok {
					parts = append(parts, text)
					continue
				}
			}
			if text := richWebJSON(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return richWebJSON(value)
}

func richWebJSON(value any) string {
	if value == nil {
		return ""
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(raw)
}

func richWebImageMarkdown(block map[string]any) string {
	if direct := strings.TrimSpace(webString(block["url"])); direct != "" {
		return "![Synon Biomed image](" + direct + ")"
	}
	source, _ := block["source"].(map[string]any)
	if source == nil {
		return ""
	}
	if direct := strings.TrimSpace(webString(source["url"])); direct != "" {
		return "![Synon Biomed image](" + direct + ")"
	}
	data := strings.TrimSpace(webString(source["data"]))
	mediaType := strings.TrimSpace(webString(source["media_type"]))
	if data != "" && strings.HasPrefix(mediaType, "image/") {
		return "![Synon Biomed image](data:" + mediaType + ";base64," + data + ")"
	}
	return ""
}

func richWebInternalText(text string) bool {
	return (strings.HasPrefix(text, "[System]") && !richWebFailedAttachment(text)) ||
		strings.HasPrefix(text, "[image:") || strings.HasPrefix(text, "[document:") ||
		strings.HasPrefix(text, "[compute]") || strings.HasPrefix(text, "[Memory]") ||
		strings.HasPrefix(text, "You are reviewing work an agent did in frame ")
}

func richWebFailedAttachment(text string) bool {
	return strings.HasPrefix(text, "[System] Attached file ") && strings.Contains(text, "could not be auto-attached")
}

func richWebTruthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.TrimSpace(typed) != ""
	case nil:
		return false
	default:
		return true
	}
}

func richWebStringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func richWebNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
