package server

import (
	"errors"
	"net/http"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityArtifactDelete(w http.ResponseWriter, r *http.Request, artifactID string) {
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	result, err := store.DeleteCompatibilityArtifactRealtime(r.Context(), userID, artifactID)
	if err != nil {
		writeCompatibilityArtifactControlError(w, err, artifactID)
		return
	}
	if err := store.RemoveArtifactBlobs(result.BlobPaths); err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCompatibilityArtifactRename(w http.ResponseWriter, r *http.Request, artifactID string) {
	var body struct {
		Filename *string `json:"filename"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid arguments: "+err.Error())
		return
	}
	filename := ""
	if body.Filename != nil {
		filename = sanitizeCompatibilityArtifactFilename(*body.Filename)
	}
	if filename == "" {
		writeV11Detail(w, http.StatusBadRequest, "filename is required")
		return
	}
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	result, err := store.RenameCompatibilityArtifactRealtime(r.Context(), userID, artifactID, filename)
	if err != nil {
		writeCompatibilityArtifactControlError(w, err, artifactID)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleCompatibilityArtifactPriority(w http.ResponseWriter, r *http.Request, artifactID string) {
	var body struct {
		Priority string `json:"priority"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid arguments: "+err.Error())
		return
	}
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	artifact, err := store.UpdateCompatibilityArtifactPriorityRealtime(r.Context(), userID, artifactID, body.Priority)
	if err != nil {
		if errors.Is(err, workspace.ErrCompatibilityArtifactPriority) {
			writeV11Detail(w, http.StatusBadRequest,
				"Invalid arguments: priority: Invalid enum value. Expected 'user_starred' | 'user_hidden' | 'user_no_priority' | 'unknown', received '"+body.Priority+"'")
			return
		}
		writeCompatibilityArtifactControlError(w, err, artifactID)
		return
	}
	writeJSON(w, http.StatusOK, artifact)
}

func (s *Server) compatibilityArtifactControlContext(w http.ResponseWriter, r *http.Request) (*workspace.Store, string, bool) {
	if s == nil || s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return nil, "", false
	}
	return s.workspaceStore, compatAgentUserID(r), true
}

func writeCompatibilityArtifactControlError(w http.ResponseWriter, err error, artifactID string) {
	switch {
	case errors.Is(err, workspace.ErrCompatibilityArtifactNotFound):
		writeV11Detail(w, http.StatusNotFound, "Artifact "+artifactID+" not found")
	default:
		writeV11StoreError(w, err)
	}
}

// v1.1 trims the input, removes control and bidi-control characters, then
// keeps at most 255 UTF-16 code units. Avoiding a split surrogate is a stricter
// serialization guarantee while preserving every valid v1.1 filename.
func sanitizeCompatibilityArtifactFilename(value string) string {
	return workspace.SanitizeCompatibilityArtifactFilename(value)
}
