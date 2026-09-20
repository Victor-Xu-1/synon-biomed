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
	refresh, valid := storageReadBoolean(w, r, "refresh", false)
	if !valid {
		return
	}
	usage, err := scanner.DiskUsage(r.Context(), refresh)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusRequestTimeout, map[string]any{"ok": false, "error": "storage scan cancelled"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, usage)
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
	refresh, valid := storageReadBoolean(w, r, "refresh", false)
	if !valid {
		return
	}
	usage, err := scanner.CondaDiskUsage(r.Context(), refresh)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusRequestTimeout, map[string]any{"ok": false, "error": "storage scan cancelled"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, usage)
}

func storageReadBoolean(w http.ResponseWriter, r *http.Request, name string, fallback bool) (bool, bool) {
	values, present := r.URL.Query()[name]
	if !present {
		return fallback, true
	}
	if len(values) != 1 || (values[0] != "true" && values[0] != "false") {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": name + " must be true or false"})
		return false, false
	}
	return values[0] == "true", true
}

func (s *Server) usageScannerForRequest(w http.ResponseWriter) (*runtimecontrol.Scanner, bool) {
	if s.usageScanner == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "runtime data directory is not configured"})
		return nil, false
	}
	return s.usageScanner, true
}
