package server

import (
	"net/http"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleCompatibilityFrameTokenClasses(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame, userID string) {
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	session, found, err := s.workspaceStore.TokenClassBreakdownForRoot(frame.RootFrameID, userID)
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, map[string]any{
			"root_frame_id": frame.RootFrameID, "classes": map[string]any{},
			"unattributed": workspace.TokenClassUsage{}, "totals": workspace.TokenTotals{},
			"attributed_frames": 0, "total_frames": 0,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"root_frame_id": session.RootFrameID, "classes": session.Classes,
		"unattributed": session.Unattributed, "totals": session.Totals,
		"attributed_frames": session.AttributedFrames, "total_frames": session.TotalFrames,
	})
}

func (s *Server) handleCompatibilityTokenClasses(w http.ResponseWriter, r *http.Request) {
	if s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "Workspace runtime is not configured")
		return
	}
	if r.Method != http.MethodGet {
		writeV11Detail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	window := strings.TrimSpace(r.URL.Query().Get("window"))
	if window == "" {
		window = "24h"
	}
	hours := 0
	switch window {
	case "24h":
		hours = 24
	case "7d":
		hours = 7 * 24
	case "30d":
		hours = 30 * 24
	default:
		writeV11Detail(w, http.StatusBadRequest, "window must be one of 24h|7d|30d, got "+window)
		return
	}
	sessions, err := s.workspaceStore.TokenClassBreakdownForWindow(compatAgentUserID(r), time.Now().UTC().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		writeV11StoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"window": window, "sessions": sessions})
}
