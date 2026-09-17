package server

import (
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) handleMCPOptional(w http.ResponseWriter, r *http.Request) {
	if s.mcpDirectory == nil {
		writeError(w, http.StatusServiceUnavailable, "MCP_DIRECTORY_UNAVAILABLE", "workspace MCP directory service is unavailable")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/mcp-servers/optional")
	if path == "" || path == "/" {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "optional MCP catalog is read-only at this path")
			return
		}
		items, err := s.mcpDirectory.ListOptionalConnectors(r.Context())
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
		return
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) != 2 || segments[1] != "install" || r.Method != http.MethodPost {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "optional MCP endpoint not found")
		return
	}
	id, err := url.PathUnescape(segments[0])
	if err != nil || strings.TrimSpace(id) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "optional MCP connector id is invalid")
		return
	}
	item, err := s.mcpDirectory.StartOptionalInstall(resolveMCPDirectoryUserID(r), id)
	if err != nil {
		writeMCPDirectoryError(w, err)
		return
	}
	if _, err := s.publishUserEvent(resolveMCPDirectoryUserID(r), "connector_status", map[string]any{
		"connector_id": item.ID, "status": item.Status, "action": "optional_install_started",
	}); err != nil {
		writeDomainEventError(w, "optional MCP installation", err)
		return
	}
	status := http.StatusOK
	if item.Status == "installing" {
		status = http.StatusAccepted
	}
	writeJSON(w, status, item)
}
