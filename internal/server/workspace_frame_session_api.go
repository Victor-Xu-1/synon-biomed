package server

import (
	"net/http"
	"net/url"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleWorkspaceFrameMove(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, frameID string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		ProjectID string `json:"projectId"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !workspaceProjectOwned(w, store, input.ProjectID, userID) {
		return
	}
	frame, event, err := store.MoveFrameToProject(frameID, input.ProjectID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if event != nil {
		if err := s.publishWorkspaceEvent(*event); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "frame moved but its realtime event could not be persisted: " + err.Error()})
			return
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "frame": frame, "alreadyInProject": event == nil})
}

func (s *Server) handleWorkspaceFrameResume(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	frame, event, err := store.ResumeFrame(frameID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if event != nil {
		if err := s.publishWorkspaceEvent(*event); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "frame resumed but its realtime event could not be persisted: " + err.Error()})
			return
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "frame": frame, "alreadyRunning": event == nil})
}

func (s *Server) handleWorkspaceFrameMessages(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID string, segments []string) {
	if len(segments) == 2 {
		if r.Method == http.MethodPost {
			s.handleWorkspaceFrameMessageSubmission(w, r, store, frameID)
			return
		}
		if r.Method != http.MethodGet {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		after, err := workspaceEventCursor(r)
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		page, err := store.GetFrameMessagesPage(frameID, after, workspaceEventLimit(r))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{
			"ok": true, "messages": page.Messages,
			"nextSequence": page.NextSequence, "hasMore": page.HasMore,
		})
		return
	}
	if len(segments) == 4 && segments[3] == "retract" {
		if r.Method != http.MethodPost {
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
			return
		}
		messageUUID, err := url.PathUnescape(segments[2])
		if err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid message uuid"})
			return
		}
		event, err := store.RetractQueuedMessage(frameID, messageUUID)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := s.publishWorkspaceEvent(event); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "message retracted but its realtime event could not be persisted: " + err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "event": event})
		return
	}
	if len(segments) != 3 || r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	messageUUID, err := url.PathUnescape(segments[2])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid message uuid"})
		return
	}
	message, found, err := store.LocateFrameMessage(frameID, messageUUID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "message not found"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "message": message})
}

func (s *Server) handleWorkspaceFrameReadCursor(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID string) {
	frame, found, err := store.GetCompatibilityFrame(frameID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "frame not found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		cursor, found, err := s.getCompatibilityFrameReadCursor(r.Context(), frame)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !found {
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "cursor": nil})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "cursor": cursor})
	case http.MethodPut:
		var input struct {
			MessageUUID          string `json:"messageUuid"`
			MessageIndex         int    `json:"messageIndex"`
			ObservedMessageUUID  string `json:"observedMessageUuid"`
			ObservedMessageIndex *int   `json:"observedMessageIndex"`
			Repair               bool   `json:"repair"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		cursor, err := s.putCompatibilityFrameReadCursor(
			r.Context(), frame, input.MessageUUID, input.MessageIndex,
			input.ObservedMessageUUID, input.ObservedMessageIndex, input.Repair,
		)
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "cursor": cursor})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceFrameCancel(w http.ResponseWriter, r *http.Request, store *workspace.Store, frameID string) {
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var frame workspace.Frame
	var event *workspace.FrameEvent
	err := s.commitSessionRunCancellation([]string{frameID}, func(_ map[string]*transcriptRunnerAuthority) error {
		var cancelErr error
		if s.transcriptStore != nil {
			frame, event, cancelErr = store.CancelFrameWithTranscript(r.Context(), frameID)
		} else {
			frame, event, cancelErr = store.CancelFrame(frameID)
		}
		return cancelErr
	})
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if event != nil {
		if err := s.publishWorkspaceEvent(*event); err != nil {
			writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "frame cancelled but its realtime event could not be persisted: " + err.Error()})
			return
		}
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"ok": true, "frame": frame, "alreadyCancelled": event == nil,
	})
}
