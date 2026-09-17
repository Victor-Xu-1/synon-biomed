package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) publishWebFrameEventProjection(
	frameContext workspace.FrameRealtimeContext,
	source workspace.FrameEvent,
) error {
	webFrame, err := s.isWebConversationFrame(frameContext.Frame.ID)
	if err != nil {
		return err
	}
	transcriptBacked := false
	if webFrame && s.transcriptStore != nil {
		authority, found, authorityErr := s.transcriptStore.GetFrameAuthorityBySession(
			context.Background(), frameContext.UserID, frameContext.Frame.ID,
		)
		if authorityErr != nil {
			return authorityErr
		}
		if !found || !authority.TranscriptPayloadActive() {
			return nil
		}
		transcriptBacked = true
	}
	return s.publishWebFrameEventProjectionSnapshot(frameContext, source, webFrame, transcriptBacked)
}

func (s *Server) publishWebFrameEventProjectionSnapshot(
	frameContext workspace.FrameRealtimeContext,
	source workspace.FrameEvent,
	webFrame bool,
	transcriptBacked bool,
) error {
	if !webFrame {
		return nil
	}
	conversationID := frameContext.Frame.ID
	if transcriptBacked {
		if s.transcriptStore == nil {
			return errors.New("transcript-backed realtime projection is unavailable")
		}
		if err := s.publishWebConfirmationProjection(frameContext, source); err != nil {
			return err
		}
		// Transcript delivery intents are the durable realtime authority. Runner
		// admission must not synchronously drain an owner's entire Web history:
		// a long scientific transcript can contain tens of thousands of events.
		// Wake the recoverable projector and let its ordered claim/ack loop make
		// progress independently of the task execution path.
		s.signalTranscriptWebDelivery()
		return nil
	}
	createdAt := source.CreatedAt.UnixMilli()
	switch source.Type {
	case "user_message":
		messageID := webMessageID(source.Payload, int(source.Sequence))
		content := webMessageText(source.Payload)
		if err := s.publishWebFrameEvent(frameContext, "web-user-created:"+source.ID, "message.userCreated", map[string]any{
			"conversation_id": conversationID, "msg_id": messageID, "content": content,
			"position": "right", "status": "finish", "hidden": false, "created_at": createdAt,
		}); err != nil {
			return err
		}
		if err := s.publishWebMessageStream(frameContext, "web-stream-start:"+source.ID, map[string]any{
			"type": "start", "data": map[string]any{}, "msg_id": messageID,
			"turn_id": conversationID, "conversation_id": conversationID, "created_at": createdAt, "status": "pending",
		}); err != nil {
			return err
		}
		return s.publishWebRuntimeStatus(frameContext, "web-runtime-start:"+source.ID, "waiting_for_lock", "")
	case "content_delta", "content_reset":
		messageID := s.webAssistantStreamMessageID(conversationID, source)
		streamType := "text"
		data := any(webMessageText(source.Payload))
		if firstNonEmpty(webString(source.Payload["block_type"]), webString(source.Payload["blockType"])) == "thinking" {
			streamType = "thinking"
			data = map[string]any{"content": webMessageText(source.Payload), "status": "thinking"}
		}
		return s.publishWebMessageStream(frameContext, "web-stream-delta:"+source.ID, map[string]any{
			"type": streamType, "data": data, "msg_id": messageID,
			"turn_id": conversationID, "conversation_id": conversationID, "created_at": createdAt,
			"position": "left", "status": "pending", "replace": source.Type == "content_reset",
		})
	case "assistant_message":
		messageID := s.webAssistantStreamMessageID(conversationID, source)
		return s.publishWebMessageStream(frameContext, "web-stream-assistant:"+source.ID, map[string]any{
			"type": "content", "data": webMessageText(source.Payload), "msg_id": messageID,
			"turn_id": conversationID, "conversation_id": conversationID, "created_at": createdAt,
			"position": "left", "status": "finish", "replace": true,
		})
	case "runner_finished":
		terminalStatus, streamType, phase, err := webTerminalEventMapping(source.Payload["status"])
		if err != nil {
			return err
		}
		messageID := s.webAssistantStreamMessageID(conversationID, source)
		if err := s.publishWebMessageStream(frameContext, "web-stream-finish:"+source.ID, map[string]any{
			"type": streamType, "terminal_status": terminalStatus, "data": webMessageText(source.Payload), "msg_id": messageID,
			"turn_id": conversationID, "conversation_id": conversationID, "created_at": createdAt,
			"position": "left", "status": map[bool]string{true: "error", false: "finish"}[terminalStatus == "failed"],
		}); err != nil {
			return err
		}
		if err := s.publishWebRuntimeStatus(frameContext, "web-runtime-finish:"+source.ID, phase, webMessageText(source.Payload), terminalStatus); err != nil {
			return err
		}
		return s.publishWebTurnCompleted(frameContext, source, terminalStatus, messageID)
	}
	if err := s.publishWebConfirmationProjection(frameContext, source); err != nil {
		return err
	}
	phase := "ready"
	message := ""
	switch strings.ToLower(strings.TrimSpace(frameContext.Frame.Status)) {
	case "processing", "running", "queued", "pending":
		phase = "waiting_for_lock"
	case "awaiting_user_response", "awaiting_plan_approval", "needs_input", "awaiting_input":
		// A legacy status string is not user-action evidence. Confirmation cards
		// are projected above from durable requests; absent that evidence this is
		// still an active checkpoint, not an implicit AskUser boundary.
		phase = "waiting_for_lock"
	case "failed", "cancelled", "canceled":
		phase = "failed"
		message = frameContext.Frame.StatusDescription
	}
	return s.publishWebRuntimeStatus(frameContext, "web-runtime:"+source.ID, phase, message)
}

func (s *Server) isWebConversationFrame(frameID string) (bool, error) {
	metadata, found, err := s.workspaceStore.GetFrameRuntimeMetadata(frameID)
	if err != nil || !found {
		return false, err
	}
	_, hasExtra := metadata.ContextData["web_extra"]
	_, hasAssistant := metadata.ContextData["web_assistant"]
	return hasExtra || hasAssistant, nil
}

func (s *Server) publishWebFrameEvent(
	frameContext workspace.FrameRealtimeContext,
	id string,
	eventType string,
	payload map[string]any,
) error {
	_, err := s.publishCompatEvent(workspace.RealtimeEventInput{
		ID: id, UserID: frameContext.UserID, ProjectID: frameContext.Frame.ProjectID,
		RootFrameID: frameContext.Frame.RootFrameID, FrameID: frameContext.Frame.ID,
		Type: eventType, Payload: payload,
	})
	return err
}

func (s *Server) publishWebMessageStream(
	frameContext workspace.FrameRealtimeContext,
	id string,
	payload map[string]any,
) error {
	streamType := strings.TrimSpace(webString(payload["type"]))
	switch streamType {
	case "start", "thinking", "text", "content", "tool_call", "finish", "error":
	default:
		return fmt.Errorf("unsupported message stream type %q", streamType)
	}
	if declared, present := payload["stream_type"]; present && strings.TrimSpace(webString(declared)) != streamType {
		return errors.New("message stream subtype conflicts with payload type")
	}
	projected := make(map[string]any, len(payload)+1)
	for key, value := range payload {
		projected[key] = value
	}
	projected["stream_type"] = streamType
	return s.publishWebFrameEvent(frameContext, id, "message.stream", projected)
}

func (s *Server) publishWebRuntimeStatus(
	frameContext workspace.FrameRealtimeContext,
	id string,
	phase string,
	message string,
	terminalStatus ...string,
) error {
	payload := map[string]any{
		"resource": "acp_tool", "resource_id": frameContext.Frame.AgentName,
		"scope": map[string]any{"kind": "conversation", "id": frameContext.Frame.ID}, "phase": phase,
	}
	if strings.TrimSpace(message) != "" {
		payload["message"] = strings.TrimSpace(message)
	}
	if phase == "failed" {
		payload["failure_kind"] = "unknown"
	}
	if len(terminalStatus) == 1 {
		payload["terminal_status"] = terminalStatus[0]
	}
	return s.publishWebFrameEvent(frameContext, id, "runtime.statusChanged", payload)
}

func (s *Server) publishWebTurnCompleted(
	frameContext workspace.FrameRealtimeContext,
	source workspace.FrameEvent,
	status string,
	messageID string,
) error {
	resultStatus := "finished"
	state := "ai_waiting_input"
	if status == "failed" {
		resultStatus = "error"
		state = "error"
	} else if status == "cancelled" {
		resultStatus = "cancelled"
	}
	lastMessage := map[string]any{
		"id": messageID, "type": "content", "content": nil,
		"status":     map[bool]string{true: "error", false: "finish"}[status == "failed"],
		"created_at": source.CreatedAt.UnixMilli(),
	}
	page, err := s.workspaceStore.CompatibilityFrameMessages(frameContext.Frame.ID, 0, 1)
	if err == nil && page.Total > 0 {
		lastPage, pageErr := s.workspaceStore.CompatibilityFrameMessages(frameContext.Frame.ID, page.Total-1, 1)
		if pageErr == nil && len(lastPage.Messages) == 1 {
			lastMessage["content"] = webMessageText(lastPage.Messages[0])
		}
	}
	runtime := map[string]any{
		"state": "idle", "can_send_message": true, "has_task": false, "task_status": resultStatus,
		"is_processing": false, "pending_confirmations": 0, "turn_id": nil,
	}
	payload := map[string]any{
		"session_id": frameContext.Frame.ID, "turn_id": frameContext.Frame.ID,
		"conversation_id": frameContext.Frame.ID, "status": resultStatus, "terminal_status": status, "state": state,
		"detail": webMessageText(source.Payload), "can_send_message": true, "runtime": runtime,
		"workspace": "", "model": map[string]any{"platform": "synon-go", "name": frameContext.Frame.Model, "use_model": frameContext.Frame.Model},
		"last_message": lastMessage,
	}
	return s.publishWebFrameEvent(frameContext, "web-turn-completed:"+source.ID, "turn.completed", payload)
}

func webTerminalEventMapping(value any) (terminalStatus, streamType, phase string, err error) {
	terminalStatus, streamType, err = transcriptstore.MapTerminalStatus(webString(value))
	if err != nil {
		return "", "", "", fmt.Errorf("unsupported runner terminal status: %w", err)
	}
	phase = "ready"
	if terminalStatus == "failed" {
		phase = "failed"
	}
	return terminalStatus, streamType, phase, nil
}

func (s *Server) publishWebConfirmationProjection(
	frameContext workspace.FrameRealtimeContext,
	source workspace.FrameEvent,
) error {
	confirmations, err := s.webConversationPendingConfirmations(frameContext.Frame.ID)
	if err != nil || len(confirmations) == 0 {
		return err
	}
	for _, confirmation := range confirmations {
		callID := webString(confirmation["id"])
		addID := "web-confirmation-add:" + frameContext.Frame.ID + ":" + webEventComponent(callID)
		payload := copyMapAny(confirmation)
		payload["conversation_id"] = frameContext.Frame.ID
		if existing, found, err := s.workspaceStore.GetRealtimeEventByID(addID); err != nil {
			return err
		} else if !found {
			if err := s.publishWebFrameEvent(frameContext, addID, "confirmation.add", payload); err != nil {
				return err
			}
			continue
		} else {
			_ = existing
		}
		raw, _ := json.Marshal(payload)
		sum := sha256.Sum256(raw)
		updateID := "web-confirmation-update:" + frameContext.Frame.ID + ":" + webEventComponent(callID) + ":" + hex.EncodeToString(sum[:])[:16]
		if err := s.publishWebFrameEvent(frameContext, updateID, "confirmation.update", payload); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) webAssistantStreamMessageID(frameID string, source workspace.FrameEvent) string {
	attempt := int64(0)
	if value, ok := source.Payload["runnerAttempt"].(float64); ok {
		attempt = int64(value)
	}
	if attempt == 0 && s.sessionStore != nil {
		if session, found, err := s.sessionStore.Get(frameID); err == nil && found && session.Runner != nil {
			attempt = int64(session.Runner.Attempt)
		}
	}
	if attempt == 0 {
		attempt = source.Sequence
	}
	return fmt.Sprintf("assistant-%s-%d", frameID, attempt)
}

func webEventComponent(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])[:20]
}
