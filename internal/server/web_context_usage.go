package server

import (
	"errors"
	"log"
	"net/http"

	workspace "synon-go/internal/persistence/workspace"
)

// Access is checked by handleWebConversation before this read-only endpoint.
func (s *Server) handleWebContextUsage(w http.ResponseWriter, r *http.Request, frame workspace.CompatibilityFrame) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	if s.runtimeStore == nil {
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "context usage storage unavailable"})
		return
	}
	entry, found, err := s.runtimeStore.Get(contextUsageNamespace(frame.ID), "latest")
	if err != nil {
		log.Printf("context_usage_read_failed frame=%s: %v", frame.ID, err)
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "context usage temporarily unavailable"})
		return
	}
	if !found {
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"status": "unavailable"})
		return
	}
	snapshot, err := decodeRunnerContextUsage(entry)
	if errors.Is(err, errLegacyRunnerContextUsage) && snapshot.SessionID == frame.ID {
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"status": "unavailable"})
		return
	}
	if err != nil || snapshot.SessionID != frame.ID {
		log.Printf("context_usage_record_invalid frame=%s", frame.ID)
		writeWorkspaceJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "context usage record unavailable"})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"status": "available", "snapshot": snapshot})
}
