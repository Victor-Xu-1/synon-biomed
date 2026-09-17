package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityProjectNotes(w http.ResponseWriter, r *http.Request, projectID string) {
	if s == nil || s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	userID := compatAgentUserID(r)
	switch r.Method {
	case http.MethodGet:
		options := workspace.CompatibilityNoteListOptions{
			Query:         strings.TrimSpace(r.URL.Query().Get("q")),
			TargetType:    strings.TrimSpace(r.URL.Query().Get("target_type")),
			TargetFrameID: strings.TrimSpace(r.URL.Query().Get("target_frame_id")),
		}
		if options.TargetType != "" && !workspace.ValidCompatibilityNoteTargetType(options.TargetType) {
			writeV11Detail(w, http.StatusBadRequest, "Invalid target_type")
			return
		}
		notes, found, err := s.workspaceStore.ListCompatibilityNotes(r.Context(), userID, projectID, options)
		if err != nil {
			writeV11StoreError(w, err)
			return
		}
		if !found {
			writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
			return
		}
		writeJSON(w, http.StatusOK, notes)
	case http.MethodPost:
		var body struct {
			TargetType         string  `json:"target_type"`
			TargetFrameID      string  `json:"target_frame_id"`
			TargetMessageIndex *int    `json:"target_message_index"`
			TargetArtifactID   *string `json:"target_artifact_id"`
			Content            string  `json:"content"`
		}
		if err := decodeCompatibilityNoteJSON(r, &body); err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Invalid arguments: "+err.Error())
			return
		}
		if !workspace.ValidCompatibilityNoteTargetType(body.TargetType) {
			writeV11Detail(w, http.StatusBadRequest, "Invalid target_type")
			return
		}
		artifactID := ""
		if body.TargetArtifactID != nil {
			artifactID = *body.TargetArtifactID
		}
		note, err := s.workspaceStore.CreateCompatibilityNoteRealtime(r.Context(), userID, workspace.CreateProjectNoteInput{
			ID: uuid.NewString(), ProjectID: projectID, UserID: userID,
			TargetType: body.TargetType, TargetFrameID: body.TargetFrameID,
			TargetMessageIndex: body.TargetMessageIndex, TargetArtifactID: artifactID, Content: body.Content,
		})
		if err != nil {
			writeCompatibilityNoteError(w, err, projectID, "")
			return
		}
		writeJSON(w, http.StatusOK, note)
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

func (s *Server) handleCompatibilityNoteMutation(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	noteID, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/api/notes/"))
	if err != nil || strings.TrimSpace(noteID) == "" || strings.Contains(noteID, "/") {
		writeV11Detail(w, http.StatusBadRequest, "Invalid note id")
		return
	}
	switch r.Method {
	case http.MethodDelete:
		if err := s.workspaceStore.DeleteCompatibilityNoteRealtime(r.Context(), compatAgentUserID(r), noteID); err != nil {
			writeCompatibilityNoteError(w, err, "", noteID)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "deleted", "note_id": noteID})
		return
	case http.MethodPatch:
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := decodeCompatibilityNoteJSON(r, &body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid arguments: "+err.Error())
		return
	}
	note, err := s.workspaceStore.UpdateCompatibilityNoteRealtime(r.Context(), compatAgentUserID(r), noteID, body.Content)
	if err != nil {
		writeCompatibilityNoteError(w, err, "", noteID)
		return
	}
	writeJSON(w, http.StatusOK, note)
}

func decodeCompatibilityNoteJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func writeCompatibilityNoteError(w http.ResponseWriter, err error, projectID, noteID string) {
	switch {
	case errors.Is(err, workspace.ErrCompatibilityNoteProjectNotFound):
		writeV11Detail(w, http.StatusNotFound, "Project "+projectID+" not found")
	case errors.Is(err, workspace.ErrCompatibilityNoteFrameNotFound):
		writeV11Detail(w, http.StatusNotFound, err.Error())
	case errors.Is(err, workspace.ErrCompatibilityNoteNotFound):
		writeV11Detail(w, http.StatusNotFound, "Note "+noteID+" not found")
	case errors.Is(err, workspace.ErrCompatibilityNoteContent),
		errors.Is(err, workspace.ErrCompatibilityNoteMessageIndex),
		errors.Is(err, workspace.ErrCompatibilityNoteArtifact):
		writeV11Detail(w, http.StatusBadRequest, err.Error())
	default:
		writeV11StoreError(w, err)
	}
}
