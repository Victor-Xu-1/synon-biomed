package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

const (
	maxTranscriptAnnotationIDLength   = 255
	maxTranscriptAnnotationTextLength = 256 << 10
	maxTranscriptAnchorLength         = 16 << 10
	maxTranscriptMessageIndex         = 10_000_000
)

type createTranscriptAnnotationRequest struct {
	ID           string  `json:"id"`
	MessageUUID  *string `json:"message_uuid"`
	MessageIndex *int    `json:"message_index"`
	BlockIndex   *int    `json:"block_index"`
	Source       string  `json:"source"`
	ToolName     *string `json:"tool_name"`
	AnchorText   string  `json:"anchor_text"`
	StartOffset  *int    `json:"start_offset"`
	EndOffset    *int    `json:"end_offset"`
	Kind         string  `json:"kind"`
	Note         *string `json:"note"`
}

func (s *Server) handleCompatibilityTranscriptAnnotations(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	segments []string,
) {
	rootFrameID := compatibilityRootFrameID(frame)
	if len(segments) == 2 {
		switch r.Method {
		case http.MethodGet:
			annotations, err := s.workspaceStore.ListTranscriptAnnotations(rootFrameID)
			if err != nil {
				writeV11StoreError(w, err)
				return
			}
			response := make([]map[string]any, 0, len(annotations))
			for _, annotation := range annotations {
				response = append(response, compatibilityTranscriptAnnotationResponse(annotation))
			}
			writeJSON(w, http.StatusOK, response)
		case http.MethodPost:
			s.createCompatibilityTranscriptAnnotation(w, r, frame, rootFrameID)
		default:
			writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		}
		return
	}

	if segments[2] == "drain" {
		s.drainCompatibilityTranscriptAnnotations(w, r, frame, rootFrameID)
		return
	}
	annotationID, err := url.PathUnescape(segments[2])
	if err != nil || strings.TrimSpace(annotationID) == "" {
		writeV11Detail(w, http.StatusBadRequest, "Invalid transcript annotation id")
		return
	}
	annotationID = strings.TrimSpace(annotationID)
	if len(annotationID) > maxTranscriptAnnotationIDLength {
		writeV11Detail(w, http.StatusBadRequest, "Transcript annotation id is too long")
		return
	}
	switch r.Method {
	case http.MethodPatch:
		var input struct {
			Note *string `json:"note"`
			Read *bool   `json:"read"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid transcript annotation update: "+err.Error())
			return
		}
		if input.Note != nil && len(*input.Note) > maxTranscriptAnnotationTextLength {
			writeV11Detail(w, http.StatusBadRequest, "Transcript annotation note is too long")
			return
		}
		annotation, found, err := s.workspaceStore.UpdateTranscriptAnnotation(rootFrameID, annotationID, workspace.UpdateTranscriptAnnotationInput{
			Note: input.Note, Read: input.Read,
		})
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if !found {
			writeV11Detail(w, http.StatusNotFound, "Transcript annotation "+annotationID+" not found")
			return
		}
		if err := s.publishTranscriptAnnotationUpdate(frame, rootFrameID); err != nil {
			writeV11StoreError(w, fmt.Errorf("transcript annotation updated but event delivery failed: %w", err))
			return
		}
		writeJSON(w, http.StatusOK, compatibilityTranscriptAnnotationResponse(annotation))
	case http.MethodDelete:
		deleted, err := s.workspaceStore.DeleteTranscriptAnnotation(rootFrameID, annotationID)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if deleted {
			if err := s.publishTranscriptAnnotationUpdate(frame, rootFrameID); err != nil {
				writeV11StoreError(w, fmt.Errorf("transcript annotation deleted but event delivery failed: %w", err))
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) createCompatibilityTranscriptAnnotation(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	rootFrameID string,
) {
	var input createTranscriptAnnotationRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid transcript annotation: "+err.Error())
		return
	}
	if err := validateCreateTranscriptAnnotationRequest(input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	blockIndex := 0
	if input.BlockIndex != nil {
		blockIndex = *input.BlockIndex
	}
	messageUUID := ""
	if input.MessageUUID != nil {
		messageUUID = strings.TrimSpace(*input.MessageUUID)
	}
	if messageUUID == "" {
		messageUUID = s.inferTranscriptAnnotationMessageUUID(rootFrameID, *input.MessageIndex, input.AnchorText)
	}
	toolName := ""
	if input.ToolName != nil {
		toolName = strings.TrimSpace(*input.ToolName)
	}
	note := ""
	if input.Note != nil {
		note = *input.Note
	}
	annotation, err := s.workspaceStore.CreateTranscriptAnnotation(workspace.CreateTranscriptAnnotationInput{
		ID: input.ID, RootFrameID: rootFrameID, MessageUUID: messageUUID,
		MessageIndex: *input.MessageIndex, BlockIndex: blockIndex, Source: input.Source,
		ToolName: toolName, AnchorText: input.AnchorText, StartOffset: input.StartOffset,
		EndOffset: input.EndOffset, Kind: input.Kind, Origin: "user", Note: note,
	})
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if err := s.publishTranscriptAnnotationUpdate(frame, rootFrameID); err != nil {
		writeV11StoreError(w, fmt.Errorf("transcript annotation created but event delivery failed: %w", err))
		return
	}
	writeJSON(w, http.StatusCreated, compatibilityTranscriptAnnotationResponse(annotation))
}

func (s *Server) drainCompatibilityTranscriptAnnotations(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
	rootFrameID string,
) {
	if r.Method != http.MethodPost {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var input struct {
		IDs []string `json:"ids"`
	}
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid transcript annotation drain request: "+err.Error())
		return
	}
	if input.IDs == nil {
		writeV11Detail(w, http.StatusBadRequest, "ids is required")
		return
	}
	for _, id := range input.IDs {
		if len(strings.TrimSpace(id)) > maxTranscriptAnnotationIDLength {
			writeV11Detail(w, http.StatusBadRequest, "Transcript annotation id is too long")
			return
		}
	}
	deleted, err := s.workspaceStore.DrainTranscriptAnnotations(rootFrameID, input.IDs)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if deleted > 0 {
		if err := s.publishTranscriptAnnotationUpdate(frame, rootFrameID); err != nil {
			writeV11StoreError(w, fmt.Errorf("transcript annotations drained but event delivery failed: %w", err))
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func validateCreateTranscriptAnnotationRequest(input createTranscriptAnnotationRequest) error {
	if input.MessageIndex == nil {
		return fmt.Errorf("message_index is required")
	}
	if *input.MessageIndex < 0 || *input.MessageIndex > maxTranscriptMessageIndex {
		return fmt.Errorf("message_index must be between 0 and %d", maxTranscriptMessageIndex)
	}
	if input.BlockIndex != nil && (*input.BlockIndex < 0 || *input.BlockIndex > maxTranscriptMessageIndex) {
		return fmt.Errorf("block_index must be between 0 and %d", maxTranscriptMessageIndex)
	}
	if input.Source != "assistant" && input.Source != "tool_input" && input.Source != "tool_result" {
		return fmt.Errorf("source must be assistant, tool_input, or tool_result")
	}
	if input.Kind != "annotation" && input.Kind != "bookmark" {
		return fmt.Errorf("kind must be annotation or bookmark")
	}
	if len(input.ID) > maxTranscriptAnnotationIDLength {
		return fmt.Errorf("transcript annotation id is too long")
	}
	if input.MessageUUID != nil && len(strings.TrimSpace(*input.MessageUUID)) > maxTranscriptAnnotationIDLength {
		return fmt.Errorf("message_uuid is too long")
	}
	if input.ToolName != nil && len(strings.TrimSpace(*input.ToolName)) > maxTranscriptAnnotationIDLength {
		return fmt.Errorf("tool_name is too long")
	}
	if len(input.AnchorText) > maxTranscriptAnchorLength {
		return fmt.Errorf("anchor_text is too long")
	}
	if input.Note != nil && len(*input.Note) > maxTranscriptAnnotationTextLength {
		return fmt.Errorf("note is too long")
	}
	if (input.StartOffset != nil && *input.StartOffset < 0) || (input.EndOffset != nil && *input.EndOffset < 0) {
		return fmt.Errorf("start_offset and end_offset must be non-negative")
	}
	if input.StartOffset != nil && input.EndOffset != nil && *input.StartOffset > *input.EndOffset {
		return fmt.Errorf("start_offset must not exceed end_offset")
	}
	return nil
}

func (s *Server) inferTranscriptAnnotationMessageUUID(rootFrameID string, messageIndex int, anchorText string) string {
	page, err := s.workspaceStore.CompatibilityFrameMessages(rootFrameID, messageIndex, 1)
	if err != nil || len(page.Messages) == 0 {
		return ""
	}
	message := page.Messages[0]
	if anchorText != "" {
		rawMessage, messageErr := json.Marshal(message)
		rawAnchor, anchorErr := json.Marshal(anchorText)
		if messageErr != nil || anchorErr != nil || len(rawAnchor) < 2 || !bytes.Contains(rawMessage, rawAnchor[1:len(rawAnchor)-1]) {
			return ""
		}
	}
	for _, key := range []string{"_uuid", "uuid", "message_uuid", "messageUuid", "msg_id", "id"} {
		if value, ok := message[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Server) publishTranscriptAnnotationUpdate(frame workspace.CompatibilityFrame, rootFrameID string) error {
	_, err := s.publishProjectEvent(frame.ProjectID, "transcript_annotations_update", map[string]any{
		"root_frame_id": rootFrameID,
	})
	return err
}

func compatibilityRootFrameID(frame workspace.CompatibilityFrame) string {
	if rootFrameID := strings.TrimSpace(frame.RootFrameID); rootFrameID != "" {
		return rootFrameID
	}
	return frame.ID
}

func compatibilityTranscriptAnnotationResponse(annotation workspace.TranscriptAnnotation) map[string]any {
	return map[string]any{
		"id": annotation.ID, "root_frame_id": annotation.RootFrameID,
		"message_uuid": nullableCompatibilityString(annotation.MessageUUID), "message_index": annotation.MessageIndex,
		"block_index": annotation.BlockIndex, "source": annotation.Source,
		"tool_name": nullableCompatibilityString(annotation.ToolName), "anchor_text": annotation.AnchorText,
		"start_offset": nullableTranscriptAnnotationInt(annotation.StartOffset),
		"end_offset":   nullableTranscriptAnnotationInt(annotation.EndOffset),
		"kind":         annotation.Kind, "origin": annotation.Origin,
		"read_at": nullableTranscriptAnnotationTime(annotation.ReadAt), "note": annotation.Note,
		"created_at": annotation.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updated_at": annotation.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func nullableTranscriptAnnotationInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableTranscriptAnnotationTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}
