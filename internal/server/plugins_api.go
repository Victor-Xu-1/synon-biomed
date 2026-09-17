package server

import (
	"net/http"
	"strings"
)

func (s *Server) handlePlugins(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/api/plugins")
	if tail == "" || tail == "/" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Plugin listing only supports GET")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"plugins": s.plugins.List()})
		return
	}

	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) == 2 && parts[1] == "manifest" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Plugin manifest only supports GET")
			return
		}
		plugin, ok := s.plugins.Get(parts[0])
		if !ok {
			writeError(w, http.StatusNotFound, "NOT_FOUND", "Plugin not found")
			return
		}
		writeJSON(w, http.StatusOK, plugin)
		return
	}

	writeError(w, http.StatusNotFound, "NOT_FOUND", "Unknown plugin route")
}
