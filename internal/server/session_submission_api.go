package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	transcriptstore "synon-go/internal/persistence/transcript"
	workspace "synon-go/internal/persistence/workspace"
)

type frameMessageSubmission struct {
	FrameID              string
	MessageUUID          string
	ClientMessageID      string
	Text                 string
	MessageOrigin        string
	InputData            map[string]any
	RuntimeConfig        map[string]any
	ArtifactReferences   []transcriptstore.UserArtifactReferenceInput
	MessageContext       string
	TargetBranchID       string
	ExpectedBranchID     string
	ExpectedGeneration   int64
	ClientMutationID     string
	DeferFrameActivation bool
}

func (s *Server) handleRuntimeRequestSubmission(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		FrameID         string `json:"frameId"`
		MessageUUID     string `json:"messageUuid"`
		ClientMessageID string `json:"clientMessageId"`
		Text            string `json:"text"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	event, idempotent, err := s.submitFrameMessage(store, frameMessageSubmission{
		FrameID: input.FrameID, MessageUUID: input.MessageUUID,
		ClientMessageID: input.ClientMessageID, Text: input.Text,
	})
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "event": event, "idempotent": idempotent})
}

func (s *Server) handleWorkspaceFrameMessageSubmission(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID string) {
	var input struct {
		MessageUUID     string `json:"messageUuid"`
		ClientMessageID string `json:"clientMessageId"`
		Text            string `json:"text"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	event, idempotent, err := s.submitFrameMessage(store, frameMessageSubmission{
		FrameID: frameID, MessageUUID: input.MessageUUID,
		ClientMessageID: input.ClientMessageID, Text: input.Text,
	})
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "event": event, "idempotent": idempotent})
}

func (s *Server) submitFrameMessage(store *workspace.Store, input frameMessageSubmission) (workspace.FrameEvent, bool, error) {
	input.FrameID = strings.TrimSpace(input.FrameID)
	input.MessageUUID = strings.TrimSpace(input.MessageUUID)
	input.ClientMessageID = strings.TrimSpace(input.ClientMessageID)
	input.Text = strings.TrimSpace(input.Text)
	input.TargetBranchID = strings.TrimSpace(input.TargetBranchID)
	input.ExpectedBranchID = strings.TrimSpace(input.ExpectedBranchID)
	input.ClientMutationID = strings.TrimSpace(input.ClientMutationID)
	if input.FrameID == "" || input.Text == "" {
		return workspace.FrameEvent{}, false, errors.New("frame id and message text are required")
	}
	if input.ClientMessageID == "" {
		input.ClientMessageID = uuid.NewString()
	}
	if input.MessageUUID == "" {
		input.MessageUUID = input.ClientMessageID
	}
	frameContext, found, err := store.GetFrameRealtimeContext(input.FrameID)
	if err != nil {
		return workspace.FrameEvent{}, false, err
	}
	if !found {
		return workspace.FrameEvent{}, false, errors.New("frame does not exist")
	}

	s.sessionSubmissionMu.Lock()
	defer s.sessionSubmissionMu.Unlock()
	if s.transcriptStore != nil {
		return s.submitTranscriptFrameMessage(context.Background(), frameContext, input)
	}
	if s.eventJournal == nil || s.sessionStore == nil || s.sessionSockets == nil {
		return workspace.FrameEvent{}, false, errors.New("session runtime is not configured")
	}
	alreadyJournaled, err := s.eventJournal.HasClientMessage(input.FrameID, input.ClientMessageID)
	if err != nil {
		return workspace.FrameEvent{}, false, err
	}
	if alreadyJournaled {
		event, found, err := store.LocateFrameMessage(input.FrameID, input.MessageUUID)
		if err != nil {
			return workspace.FrameEvent{}, false, err
		}
		if found {
			return event, true, nil
		}
		event, err = store.AppendFrameEvent(workspace.FrameEventInput{
			FrameID: input.FrameID, Type: "user_message",
			Payload: map[string]any{
				"messageUuid": input.MessageUUID, "clientMessageId": input.ClientMessageID,
				"text": input.Text, "role": "user", "_uuid": input.MessageUUID,
				"content": []any{map[string]any{"type": "text", "text": input.Text}},
			},
		})
		if err != nil {
			return workspace.FrameEvent{}, false, err
		}
		if err := s.publishWorkspaceEvent(event); err != nil {
			return workspace.FrameEvent{}, false, err
		}
		return event, true, nil
	}

	journalMessage := map[string]any{
		"type": "user_message", "messageUuid": input.MessageUUID,
		"clientMessageId": input.ClientMessageID, "text": input.Text, "_uuid": input.MessageUUID,
		"content": []any{map[string]any{"type": "text", "text": input.Text}},
	}
	if err := s.appendClientWebSocketMessage(input.FrameID, "user", journalMessage); err != nil {
		return workspace.FrameEvent{}, false, err
	}
	event, err := store.AppendFrameEvent(workspace.FrameEventInput{
		FrameID: input.FrameID, Type: "user_message",
		Payload: map[string]any{
			"messageUuid": input.MessageUUID, "clientMessageId": input.ClientMessageID,
			"text": input.Text, "role": "user", "_uuid": input.MessageUUID,
			"content": []any{map[string]any{"type": "text", "text": input.Text}},
		},
	})
	if err != nil {
		return workspace.FrameEvent{}, false, err
	}
	if err := s.publishWorkspaceEvent(event); err != nil {
		return workspace.FrameEvent{}, false, err
	}
	if entries, err := s.eventJournal.ReadByClientMessage(input.FrameID, input.ClientMessageID); err == nil && len(entries) > 0 {
		entry := entries[len(entries)-1]
		s.publishSessionEntry(&entry)
	}
	return event, false, nil
}

func (s *Server) submitTranscriptFrameMessage(
	ctx context.Context,
	frameContext workspace.FrameRealtimeContext,
	input frameMessageSubmission,
) (workspace.FrameEvent, bool, error) {
	var stream transcriptstore.Stream
	var err error
	if input.TargetBranchID == "" {
		stream, err = s.ensureTranscriptFrameStream(ctx, frameContext.UserID, input.FrameID)
	} else {
		var found bool
		stream, found, err = s.transcriptStore.GetFrameStreamBySession(ctx, frameContext.UserID, input.FrameID)
		if err == nil && !found {
			err = transcriptstore.ErrBranchTargetNotFound
		}
	}
	if err != nil {
		return workspace.FrameEvent{}, false, err
	}
	frameEventID := "frame-message:" + input.FrameID + ":" + input.MessageUUID
	if input.TargetBranchID != "" {
		frameEventID = "frame-branch-continue:" + input.FrameID + ":" + input.TargetBranchID + ":" + input.ClientMutationID
	}
	appendInput := transcriptstore.AppendFrameUserEventInput{
		StreamUID: stream.UID, OwnerID: frameContext.UserID, ClientMessageID: input.ClientMessageID,
		FrameEventID: frameEventID,
		MessageUUID:  input.MessageUUID, Text: input.Text, MessageOrigin: input.MessageOrigin,
		InputData:          input.InputData,
		RuntimeConfig:      input.RuntimeConfig,
		ArtifactReferences: input.ArtifactReferences, MessageContext: input.MessageContext,
		Destinations:         []string{transcriptWebDestination},
		DeferFrameActivation: input.DeferFrameActivation,
	}
	var transcriptEvent transcriptstore.Event
	var reference transcriptstore.FrameReferenceEvent
	var created bool
	if input.TargetBranchID == "" {
		transcriptEvent, reference, created, err = s.transcriptStore.AppendFrameUserEvent(ctx, appendInput)
	} else {
		appendInput.MessageOrigin = "task_intent"
		var result transcriptstore.AppendFrameUserEventToBranchResult
		result, err = s.transcriptStore.AppendFrameUserEventToBranch(ctx, transcriptstore.AppendFrameUserEventToBranchInput{
			AppendFrameUserEventInput: appendInput,
			TargetBranchID:            input.TargetBranchID,
			ExpectedActiveBranchID:    input.ExpectedBranchID,
			ExpectedGeneration:        input.ExpectedGeneration,
			ClientMutationID:          input.ClientMutationID,
		})
		transcriptEvent, reference, created = result.Event, result.FrameEvent, result.Created
	}
	if err != nil {
		if errors.Is(err, transcriptstore.ErrArtifactMissing) || errors.Is(err, transcriptstore.ErrArtifactMismatch) ||
			errors.Is(err, transcriptstore.ErrOwnerMismatch) {
			return workspace.FrameEvent{}, false, &webConversationRequestError{
				Status: http.StatusNotFound, Detail: "artifact reference not found", Cause: err,
			}
		}
		return workspace.FrameEvent{}, false, transcriptWebStorageError(err)
	}
	if created {
		s.signalTranscriptWebDelivery()
	}
	var payload map[string]any
	if err := json.Unmarshal(reference.PayloadJSON, &payload); err != nil {
		return workspace.FrameEvent{}, false, transcriptWebStorageError(err)
	}
	frameEvent := workspace.FrameEvent{
		ID: reference.ID, FrameID: reference.FrameID, Sequence: reference.Sequence,
		Type: reference.Type, Payload: payload, CreatedAt: reference.CreatedAt,
	}
	if transcriptEvent.Source == transcriptstore.EventSourceFrameRef {
		if transcriptEvent.FrameEventID == nil || *transcriptEvent.FrameEventID != frameEvent.ID {
			return workspace.FrameEvent{}, false, transcriptWebStorageError(transcriptstore.ErrEventConflict)
		}
	} else if transcriptEvent.Source != transcriptstore.EventSourcePayload || transcriptEvent.FrameEventID != nil {
		return workspace.FrameEvent{}, false, transcriptWebStorageError(transcriptstore.ErrEventConflict)
	}
	return frameEvent, !created, nil
}

func (s *Server) handleRuntimeSessionConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireLegacySessionSurface(w, r) {
		return
	}
	if s.sessionStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "session store is not configured"})
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/sessions/"))
	if len(segments) != 2 || segments[1] != "config" {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "runtime endpoint not found"})
		return
	}
	sessionID, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid session id"})
		return
	}
	session, found, err := s.sessionStore.Get(sessionID)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "session not found"})
		return
	}
	if r.Method == http.MethodGet {
		config := map[string]any{}
		if session.Orchestration != nil {
			if stored, ok := session.Orchestration["sessionConfig"].(map[string]any); ok {
				config = stored
			}
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "config": config})
		return
	}
	if r.Method != http.MethodPut {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		Config map[string]any `json:"config"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if input.Config == nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "config is required"})
		return
	}
	orchestration := make(map[string]any, len(session.Orchestration)+1)
	for key, value := range session.Orchestration {
		orchestration[key] = value
	}
	orchestration["sessionConfig"] = input.Config
	session.Orchestration = orchestration
	if err := s.sessionStore.Save(session); err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "config": input.Config})
}
