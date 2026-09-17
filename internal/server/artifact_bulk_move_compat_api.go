package server

import (
	"errors"
	"net/http"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityArtifactBulkMove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ArtifactIDs []string `json:"artifact_ids"`
		FolderID    *string  `json:"folder_id"`
	}
	if err := decodeAgentCompatJSON(r, &body); err != nil {
		writeV11Detail(w, http.StatusBadRequest, "Invalid arguments: "+err.Error())
		return
	}
	if len(body.ArtifactIDs) == 0 {
		writeV11Detail(w, http.StatusBadRequest, "artifact_ids is required (non-empty)")
		return
	}
	if len(body.ArtifactIDs) > 1000 {
		writeV11Detail(w, http.StatusBadRequest, "artifact_ids must contain at most 1000 entries")
		return
	}
	folderID := ""
	if body.FolderID != nil {
		folderID = strings.TrimSpace(*body.FolderID)
	}
	store, userID, ok := s.compatibilityArtifactControlContext(w, r)
	if !ok {
		return
	}
	count, err := store.BulkMoveCompatibilityArtifactsRealtime(r.Context(), userID, body.ArtifactIDs, folderID)
	if err != nil {
		switch {
		case errors.Is(err, workspace.ErrCompatibilityArtifactNotFound):
			writeV11Detail(w, http.StatusNotFound, "Artifact not found")
		case errors.Is(err, workspace.ErrCompatibilityFolderMoveTarget):
			writeV11Detail(w, http.StatusNotFound, "Folder "+folderID+" not found")
		default:
			writeV11StoreError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "count": count})
}
