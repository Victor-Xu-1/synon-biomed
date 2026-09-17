package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	frameControlDiscardPlan = "discard-plan"
)

var removedWorkspaceFrameControls = map[string]struct {
	code        string
	replacement string
}{
	"approve-plan": {
		code: "synon.frame_control.approve_plan_removed.v1", replacement: "/api/frames/{id}/approve-plan",
	},
	"resolve-input": {
		code: "synon.frame_control.resolve_input_removed.v1", replacement: "/api/frames/{id}/resolve-input",
	},
}

func isRemovedWorkspaceFrameControl(operation string) bool {
	_, removed := removedWorkspaceFrameControls[operation]
	return removed
}

func writeRemovedWorkspaceFrameControl(w http.ResponseWriter, operation string) {
	removed, found := removedWorkspaceFrameControls[operation]
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	writeWorkspaceJSON(w, http.StatusGone, map[string]any{
		"ok": false, "error": "Frame control endpoint has been retired",
		"error_code": removed.code, "replacement": removed.replacement,
	})
}

type workspaceFrameControlRequest struct {
	EditedPlan           string          `json:"editedPlan"`
	EditedPlanSnake      string          `json:"edited_plan"`
	Responses            any             `json:"responses"`
	ClientMessageID      string          `json:"clientMessageId"`
	ClientMessageIDSnake string          `json:"client_message_id"`
	TargetAgent          string          `json:"targetAgent"`
	TargetAgentSnake     string          `json:"target_agent"`
	VerifierMode         json.RawMessage `json:"verifierMode"`
	VerifierModeSnake    json.RawMessage `json:"verifier_mode"`
	MemoryMode           json.RawMessage `json:"memoryMode"`
	MemoryModeSnake      json.RawMessage `json:"memory_mode"`
	UltraMode            json.RawMessage `json:"ultraMode"`
	UltraModeSnake       json.RawMessage `json:"ultra_mode"`
	PlanMode             json.RawMessage `json:"planMode"`
	PlanModeSnake        json.RawMessage `json:"plan_mode"`
}

func (s *Server) handleWorkspaceFrameControl(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID, operation string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	if s.sessionStore == nil || s.eventJournal == nil || s.sessionSockets == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "session runtime is not configured"})
		return
	}
	var input workspaceFrameControlRequest
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	eventType, message, payload, err := frameControlPayload(operation, input)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	clientMessageID := firstNonEmpty(input.ClientMessageID, input.ClientMessageIDSnake)
	if clientMessageID == "" {
		clientMessageID = uuid.NewString()
	}
	payload["clientMessageId"] = clientMessageID
	message["clientMessageId"] = clientMessageID
	message["messageUuid"] = clientMessageID
	if operation == frameControlDiscardPlan && s.transcriptStore != nil {
		s.compatRequestMu.Lock()
		frame, event, idempotent, err := store.DiscardCompatibilityPlanWithTranscript(r.Context(), frameID, clientMessageID)
		s.compatRequestMu.Unlock()
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !idempotent {
			if err := s.publishWorkspaceEvent(event); err != nil {
				writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{
					"ok": false, "error": "control event persisted but its realtime event could not be persisted: " + err.Error(),
				})
				return
			}
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{
			"ok": true, "frame": frame, "event": event, "idempotent": idempotent,
		})
		return
	}

	s.sessionSubmissionMu.Lock()
	defer s.sessionSubmissionMu.Unlock()
	frame, event, idempotent, err := store.ApplyFrameControl(frameID, eventType, clientMessageID, payload)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	journaled, err := s.eventJournal.HasClientMessage(frameID, clientMessageID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !journaled {
		if err := s.appendClientWebSocketMessage(frameID, "user", message); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "control event persisted but runner queue failed: " + err.Error()})
			return
		}
		entries, readErr := s.eventJournal.ReadByClientMessage(frameID, clientMessageID)
		if readErr == nil && len(entries) > 0 {
			entry := entries[len(entries)-1]
			s.publishSessionEntry(&entry)
		}
	}
	if !idempotent {
		if err := s.publishWorkspaceEvent(event); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "control event persisted but its realtime event could not be persisted: " + err.Error()})
			return
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "frame": frame, "event": event,
		"idempotent": idempotent && journaled,
	})
}

func frameControlPayload(operation string, input workspaceFrameControlRequest) (string, map[string]any, map[string]any, error) {
	metadata, err := frameControlMetadata(input)
	if err != nil {
		return "", nil, nil, err
	}
	message := map[string]any{}
	payload := metadata
	switch operation {
	case frameControlDiscardPlan:
		message["type"] = "plan_discarded"
		message["text"] = "Plan discarded"
		return "plan_discarded", message, payload, nil
	default:
		return "", nil, nil, fmt.Errorf("unsupported frame control operation %q", operation)
	}
}

func frameControlMetadata(input workspaceFrameControlRequest) (map[string]any, error) {
	metadata := map[string]any{}
	if targetAgent := strings.TrimSpace(firstNonEmpty(input.TargetAgent, input.TargetAgentSnake)); targetAgent != "" {
		metadata["targetAgent"] = targetAgent
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
	return metadata, nil
}
