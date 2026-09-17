package server

import (
	"net/http"

	runtimecontrol "synon-go/internal/runtimecontrol"
)

func (s *Server) handleRunningFrameCount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	count, err := store.CountActiveFrames()
	if err != nil {
		writeWorkspaceJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{
		"count":          count,
		"activeStatuses": []string{"processing"},
	})
}

func (s *Server) handleDiskUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	scanner, ok := s.usageScannerForRequest(w)
	if !ok {
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, scanner.DiskUsage())
}

func (s *Server) handleCondaDiskUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	scanner, ok := s.usageScannerForRequest(w)
	if !ok {
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, scanner.CondaDiskUsage())
}

func (s *Server) usageScannerForRequest(w http.ResponseWriter) (*runtimecontrol.Scanner, bool) {
	if s.usageScanner == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "runtime data directory is not configured"})
		return nil, false
	}
	return s.usageScanner, true
}
