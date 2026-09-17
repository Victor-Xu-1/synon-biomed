package server

import (
	"net/http"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityConversationArtifacts(
	w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, userID string,
) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	excludeIntermediate := r.URL.Query().Get("exclude_intermediate") != "false"
	artifacts, err := s.workspaceStore.ListCompatibilityConversationArtifacts(
		r.Context(), userID, frame.ProjectID, frame.ID, excludeIntermediate,
	)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, artifacts)
}
