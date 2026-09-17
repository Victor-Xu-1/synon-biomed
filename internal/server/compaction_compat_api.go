package server

import (
	"net/http"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityCompactionArchives(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if err := s.reconcileCompactionArchives(frame.ID); err != nil {
		writeV11StoreError(w, err)
		return
	}
	archives, err := s.workspaceStore.ListCompactionArchives(frame.ID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	response := make([]map[string]any, 0, len(archives))
	for _, archive := range archives {
		response = append(response, compatibilityCompactionArchiveResponse(archive, false))
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleCompatibilityCompactionArchive(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, rawIndex string) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	index, err := parseCompactionArchiveIndex(rawIndex)
	if err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.reconcileCompactionArchives(frame.ID); err != nil {
		writeV11StoreError(w, err)
		return
	}
	archive, found, err := s.workspaceStore.GetCompactionArchive(frame.ID, index)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Compaction archive "+rawIndex+" not found for frame "+frame.ID)
		return
	}
	writeJSON(w, http.StatusOK, compatibilityCompactionArchiveResponse(archive, true))
}
