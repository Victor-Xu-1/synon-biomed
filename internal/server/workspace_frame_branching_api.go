package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	eventjournal "synon-go/internal/persistence/journal"
	sessionstore "synon-go/internal/persistence/sessions"
	workspace "synon-go/internal/persistence/workspace"
)

type workspaceFrameBranchRequest struct {
	ID                  string
	MessageIndex        *int
	MessageIndexSnake   *int
	EditedContent       string
	EditedContentSnake  string
	SourceBranchID      string
	SourceBranchIDSnake string
	ToolUseID           string
	ToolUseIDSnake      string
	Response            any
	Request             string
	Model               string
	IntentID            string
	IntentIDSnake       string
	AsSession           *bool
	AsSessionSnake      *bool
	TargetAgent         string
	TargetAgentSnake    string
	VerifierMode        json.RawMessage
	VerifierModeSnake   json.RawMessage
	MemoryMode          json.RawMessage
	MemoryModeSnake     json.RawMessage
	UltraMode           json.RawMessage
	UltraModeSnake      json.RawMessage
	PlanMode            json.RawMessage
	PlanModeSnake       json.RawMessage
}

func (input *workspaceFrameBranchRequest) UnmarshalJSON(data []byte) error {
	type wire struct {
		ID                  string          `json:"id"`
		MessageIndex        *int            `json:"messageIndex"`
		MessageIndexSnake   *int            `json:"message_index"`
		EditedContent       string          `json:"editedContent"`
		EditedContentSnake  string          `json:"edited_content"`
		SourceBranchID      string          `json:"sourceBranchId"`
		SourceBranchIDSnake string          `json:"source_branch_id"`
		ToolUseID           string          `json:"toolUseId"`
		ToolUseIDSnake      string          `json:"tool_use_id"`
		Response            any             `json:"response"`
		Request             string          `json:"request"`
		Model               string          `json:"model"`
		IntentID            string          `json:"intentId"`
		IntentIDSnake       string          `json:"intent_id"`
		AsSession           *bool           `json:"asSession"`
		AsSessionSnake      *bool           `json:"as_session"`
		TargetAgent         string          `json:"targetAgent"`
		TargetAgentSnake    string          `json:"target_agent"`
		VerifierMode        json.RawMessage `json:"verifierMode"`
		VerifierModeSnake   json.RawMessage `json:"verifier_mode"`
		MemoryMode          json.RawMessage `json:"memoryMode"`
		MemoryModeSnake     json.RawMessage `json:"memory_mode"`
		UltraMode           json.RawMessage `json:"ultraMode"`
		UltraModeSnake      json.RawMessage `json:"ultra_mode"`
		PlanMode            json.RawMessage `json:"planMode"`
		PlanModeSnake       json.RawMessage `json:"plan_mode"`
	}
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*input = workspaceFrameBranchRequest(decoded)
	return nil
}

func (s *Server) handleWorkspaceFrameBranch(w http.ResponseWriter, r *http.Request, store *workspace.Store, rootFrameID, mode string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if s.sessionStore == nil || s.eventJournal == nil || s.sessionSockets == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "session runtime is not configured"})
		return
	}
	var input workspaceFrameBranchRequest
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	branchID := strings.TrimSpace(input.ID)
	if branchID == "" {
		branchID = uuid.NewString()
	}
	sourceFrameID := firstNonEmpty(input.SourceBranchID, input.SourceBranchIDSnake, rootFrameID)
	messageIndex := -1
	if input.MessageIndex != nil {
		messageIndex = *input.MessageIndex
	} else if input.MessageIndexSnake != nil {
		messageIndex = *input.MessageIndexSnake
	}
	asSession := false
	if input.AsSession != nil {
		asSession = *input.AsSession
	} else if input.AsSessionSnake != nil {
		asSession = *input.AsSessionSnake
	}
	metadata, err := workspaceBranchMetadata(input)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if mode != workspace.FrameBranchAside {
		switch mode {
		case workspace.FrameBranchEdit:
			if messageIndex < 0 {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "message index must be non-negative"})
				return
			}
			if strings.TrimSpace(firstNonEmpty(input.EditedContent, input.EditedContentSnake)) == "" {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "edited content is required"})
				return
			}
		case workspace.FrameBranchAnswer:
			if strings.TrimSpace(firstNonEmpty(input.ToolUseID, input.ToolUseIDSnake)) == "" {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "tool use id is required"})
				return
			}
			if input.Response == nil {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "ask-user response is required"})
				return
			}
		default:
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": fmt.Sprintf("unsupported branch mode %q", mode)})
			return
		}
		writeWorkspaceJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": branchAuthorityUnavailableDetail})
		return
	}
	conversationType := "branch"
	if mode == workspace.FrameBranchAside {
		conversationType = "aside"
		if asSession {
			conversationType = "aside_session"
		}
	}
	result, err := store.CreateFrameBranch(workspace.CreateFrameBranchInput{
		ID: branchID, RootFrameID: rootFrameID, SourceFrameID: sourceFrameID, Mode: mode,
		MessageIndex:  messageIndex,
		EditedContent: firstNonEmpty(input.EditedContent, input.EditedContentSnake),
		ToolUseID:     firstNonEmpty(input.ToolUseID, input.ToolUseIDSnake),
		Response:      input.Response, Request: input.Request,
		AgentName:        firstNonEmpty(input.TargetAgent, input.TargetAgentSnake),
		ConversationType: conversationType, Metadata: metadata,
	})
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	runtimeEvents, err := s.prepareFrameBranchRuntime(result, input, mode)
	if err != nil {
		_ = store.DeleteFrame(result.Frame.ID)
		_ = s.eventJournal.Remove(result.Frame.ID)
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "branch runtime preparation failed: " + err.Error()})
		return
	}
	for _, event := range result.Events {
		if err := s.publishWorkspaceEvent(event); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "branch created but a realtime event could not be persisted: " + err.Error()})
			return
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "frame": result.Frame, "events": result.Events,
		"sourceFrameId": result.SourceFrameID, "runtimeEventCount": runtimeEvents,
	})
}

func workspaceBranchMetadata(input workspaceFrameBranchRequest) (map[string]any, error) {
	metadata := map[string]any{}
	for key, value := range map[string]string{
		"model":       input.Model,
		"intentId":    firstNonEmpty(input.IntentID, input.IntentIDSnake),
		"targetAgent": firstNonEmpty(input.TargetAgent, input.TargetAgentSnake),
	} {
		if strings.TrimSpace(value) != "" {
			metadata[key] = strings.TrimSpace(value)
		}
	}
	for _, option := range []struct {
		key     string
		primary json.RawMessage
		snake   json.RawMessage
	}{
		{key: "verifierMode", primary: input.VerifierMode, snake: input.VerifierModeSnake},
		{key: "memoryMode", primary: input.MemoryMode, snake: input.MemoryModeSnake},
		{key: "ultraMode", primary: input.UltraMode, snake: input.UltraModeSnake},
		{key: "planMode", primary: input.PlanMode, snake: input.PlanModeSnake},
	} {
		raw := option.primary
		if len(raw) == 0 {
			raw = option.snake
		}
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, fmt.Errorf("%s is invalid: %w", option.key, err)
		}
		metadata[option.key] = value
	}
	if input.AsSession != nil {
		metadata["asSession"] = *input.AsSession
	} else if input.AsSessionSnake != nil {
		metadata["asSession"] = *input.AsSessionSnake
	}
	return metadata, nil
}

func (s *Server) prepareFrameBranchRuntime(result workspace.FrameBranchResult, input workspaceFrameBranchRequest, mode string) (int, error) {
	_, found, err := s.sessionStore.Get(result.SourceFrameID)
	if err != nil {
		return 0, err
	}
	if found {
		if _, err := s.forkSession(map[string]any{
			"sessionId": result.Frame.ID, "sourceSessionId": result.SourceFrameID,
			"title": result.Frame.Name, "resetRunner": true,
		}); err != nil {
			return 0, err
		}
	} else {
		if err := s.sessionStore.Save(sessionstore.Session{
			ID: result.Frame.ID, Title: result.Frame.Name, WorkDir: s.fileRoot,
			Project: &sessionstore.Project{ID: result.Frame.ProjectID, Name: result.Frame.ProjectID},
		}); err != nil {
			return 0, err
		}
	}

	if found && mode != workspace.FrameBranchAside {
		sourceEntries, err := s.eventJournal.ReadAll(result.SourceFrameID)
		if err != nil {
			return 0, err
		}
		keepCount := result.RuntimeKeepEventCount
		if mode == workspace.FrameBranchAnswer {
			toolUseID := firstNonEmpty(input.ToolUseID, input.ToolUseIDSnake)
			if index := branchJournalToolIndex(sourceEntries, toolUseID); index >= 0 {
				keepCount = index + 1
			}
		}
		if keepCount < 0 || keepCount > len(sourceEntries) {
			return 0, fmt.Errorf("runtime branch point %d is outside source journal of %d events", keepCount, len(sourceEntries))
		}
		afterEventID := int64(0)
		if keepCount > 0 {
			afterEventID = sourceEntries[keepCount-1].EventID
		}
		kept, err := s.eventJournal.TruncateAfter(result.Frame.ID, afterEventID)
		if err != nil {
			return 0, err
		}
		target, _, err := s.sessionStore.Get(result.Frame.ID)
		if err != nil {
			return 0, err
		}
		target = sessionWithJournalStats(target, kept)
		target.Runner = nil
		if err := s.sessionStore.Save(target); err != nil {
			return 0, err
		}
	}

	message := map[string]any{
		"clientMessageId": uuid.NewString(), "messageUuid": uuid.NewString(),
	}
	switch mode {
	case workspace.FrameBranchEdit:
		message["type"] = "user_message"
		message["text"] = strings.TrimSpace(firstNonEmpty(input.EditedContent, input.EditedContentSnake))
	case workspace.FrameBranchAnswer:
		message["type"] = "ask_user_answer"
		message["toolUseId"] = firstNonEmpty(input.ToolUseID, input.ToolUseIDSnake)
		message["response"] = input.Response
		message["text"] = branchResponseText(input.Response)
	case workspace.FrameBranchAside:
		message["type"] = "user_message"
		message["text"] = strings.TrimSpace(input.Request)
	default:
		return 0, errors.New("unsupported runtime branch mode")
	}
	if err := s.appendClientWebSocketMessage(result.Frame.ID, "user", message); err != nil {
		return 0, err
	}
	entries, err := s.eventJournal.ReadAll(result.Frame.ID)
	if err != nil {
		return 0, err
	}
	if len(entries) > 0 {
		entry := entries[len(entries)-1]
		s.publishSessionEntry(&entry)
	}
	return len(entries), nil
}

func branchJournalToolIndex(entries []eventjournal.Entry, toolUseID string) int {
	for index, entry := range entries {
		if branchRuntimeContainsString(entry.Message, toolUseID) {
			return index
		}
	}
	return -1
}

func branchRuntimeContainsString(value any, target string) bool {
	switch typed := value.(type) {
	case string:
		return typed == target
	case map[string]any:
		for key, item := range typed {
			if (key == "toolUseId" || key == "tool_use_id" || key == "id") && fmt.Sprint(item) == target {
				return true
			}
			if branchRuntimeContainsString(item, target) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if branchRuntimeContainsString(item, target) {
				return true
			}
		}
	}
	return false
}

func branchResponseText(response any) string {
	if text := strings.TrimSpace(stringValue(response)); text != "" {
		return text
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return fmt.Sprint(response)
	}
	return string(raw)
}
