package server

import (
	"net/http"
	"strconv"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityExecutionLog(
	w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, userID string,
) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		limit, err := strconv.Atoi(rawLimit)
		if err != nil {
			writeV11Detail(w, http.StatusBadRequest, "Execution log page limit is invalid")
			return
		}
		page, found, err := s.workspaceStore.ListCompatibilityExecutionLogPage(
			r.Context(), userID, frame.ID, strings.TrimSpace(r.URL.Query().Get("before")), limit,
		)
		if err != nil {
			if strings.Contains(err.Error(), "cursor is invalid") || strings.Contains(err.Error(), "page limit") {
				writeV11Detail(w, http.StatusBadRequest, err.Error())
				return
			}
			writeV11StoreError(w, err)
			return
		}
		if !found {
			writeV11Detail(w, http.StatusNotFound, "Frame "+frame.ID+" not found")
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	records, found, err := s.workspaceStore.ListCompatibilityExecutionLog(
		r.Context(), userID, frame.ID, strings.TrimSpace(r.URL.Query().Get("versionId")),
	)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeV11Detail(w, http.StatusNotFound, "Frame "+frame.ID+" not found")
		return
	}
	writeJSON(w, http.StatusOK, records)
}
