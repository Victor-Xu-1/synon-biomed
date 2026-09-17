package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"synon-go/internal/mcpdirectory"
)

func (s *Server) handleMCPDirectory(w http.ResponseWriter, r *http.Request) {
	if s.mcpDirectory == nil {
		writeError(w, http.StatusServiceUnavailable, "MCP_DIRECTORY_UNAVAILABLE", "workspace MCP directory service is unavailable")
		return
	}
	userID := resolveMCPDirectoryUserID(r)
	switch {
	case r.URL.Path == "/api/mcp-servers/connectors" && r.Method == http.MethodGet:
		options, err := parseMCPConnectorListOptions(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		connectors, err := s.listMCPDirectoryAndCustomConnectors(r, userID)
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		if err := s.attachMCPConnectorUsage(userID, connectors); err != nil {
			writeError(w, http.StatusInternalServerError, "MCP_USAGE_UNAVAILABLE", "MCP usage statistics are unavailable")
			return
		}
		page, err := mcpdirectory.FilterConnectorPage(connectors, options)
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		w.Header().Set("X-Total-Count", strconv.Itoa(page.Total))
		w.Header().Set("X-Limit", strconv.Itoa(page.Limit))
		w.Header().Set("X-Offset", strconv.Itoa(page.Offset))
		writeJSON(w, http.StatusOK, page.Connectors)
	case r.URL.Path == "/api/mcp-servers/directory-health" && r.Method == http.MethodGet:
		health, err := s.mcpDirectory.DirectoryHealth(r.Context(), userID)
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		connectors, err := s.listMCPDirectoryAndCustomConnectors(r, userID)
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"directoryHealth": health, "runtimeSummary": mcpRuntimeSummary(connectors),
		})
	case r.URL.Path == "/api/mcp-servers/reconcile" && r.Method == http.MethodPost:
		results, err := s.mcpDirectory.Reconcile(r.Context(), userID)
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_snapshot", map[string]any{"action": "reconciled", "results": results}); err != nil {
			writeDomainEventError(w, "MCP connector reconciliation", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{})
	case r.URL.Path == "/api/mcp-servers/directory" && r.Method == http.MethodPost:
		var body map[string]any
		if err := decodeMCPDirectoryBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		input := mcpdirectory.AddDirectoryInput{
			Name:        stringMapValue(body, "name"),
			URL:         stringMapValue(body, "url"),
			CatalogUUID: firstMCPDirectoryValue(stringMapValue(body, "catalogUuid"), stringMapValue(body, "catalog_uuid")),
		}
		if input.Name == "" || input.URL == "" || input.CatalogUUID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid directory add body"})
			return
		}
		directory, result, err := s.mcpDirectory.AddDirectory(r.Context(), userID, input)
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_snapshot", map[string]any{"action": "directory_added", "directory_id": directory.ID}); err != nil {
			writeDomainEventError(w, "MCP directory add", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "status": http.StatusOK, "directory": directory,
			"connectorCount": result.ConnectorCount, "result": result,
		})
	case strings.HasPrefix(r.URL.Path, "/api/mcp-servers/connectors/"):
		s.handleMCPDirectoryConnector(w, r, userID)
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "unsupported MCP directory endpoint")
	}
}

func (s *Server) handleMCPDirectoryConnector(w http.ResponseWriter, r *http.Request, userID string) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/mcp-servers/connectors/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || strings.TrimSpace(parts[0]) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "connector id is required")
		return
	}
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case action == "enabled" && r.Method == http.MethodPut:
		var body map[string]any
		if err := decodeMCPDirectoryBody(r, &body); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
		enabled, ok := body["enabled"].(bool)
		if !ok {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "enabled must be boolean")
			return
		}
		var connector mcpdirectory.Connector
		server, custom, err := s.workspaceStore.GetMCPServer(id, userID)
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		if custom {
			config, configErr := s.runtimeMCPConfig(server)
			if configErr != nil {
				writeMCPDirectoryError(w, configErr)
				return
			}
			connector, err = s.mcpDirectory.SetCustomEnabledWithConfig(r.Context(), userID, server, enabled, config)
		} else {
			connector, err = s.mcpDirectory.SetUnifiedEnabled(r.Context(), userID, id, enabled)
		}
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": id, "enabled": enabled, "action": "enabled_changed"}); err != nil {
			writeDomainEventError(w, "MCP connector update", err)
			return
		}
		writeJSON(w, http.StatusOK, connector)
	case action == "authorize" && r.Method == http.MethodPost:
		if !strings.HasPrefix(id, "bundled:") && uuid.Validate(id) != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"detail": "Invalid arguments: id: Invalid uuid"})
			return
		}
		result, err := s.mcpDirectory.AuthorizeUnified(r.Context(), userID, id)
		if err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_status", map[string]any{"connector_id": id, "status": "authorization_started"}); err != nil {
			writeDomainEventError(w, "MCP connector authorization", err)
			return
		}
		writeJSON(w, http.StatusOK, result)
	case action == "credential" && r.Method == http.MethodPut:
		s.handleBundledMCPAPIKey(w, r, userID, id)
	case action == "disconnect" && r.Method == http.MethodPost:
		if strings.HasPrefix(id, "bundled:") {
			if err := s.deleteBundledMCPAPIKey(userID, id); err != nil {
				writeMCPDirectoryError(w, err)
				return
			}
		}
		if err := s.mcpDirectory.DisconnectUnified(r.Context(), userID, id); err != nil {
			writeMCPDirectoryError(w, err)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_status", map[string]any{"connector_id": id, "status": "disconnected"}); err != nil {
			writeDomainEventError(w, "MCP connector disconnect", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	default:
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "unsupported MCP connector endpoint")
	}
}

func decodeMCPDirectoryBody(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 16*1024+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid JSON request")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	return nil
}

func resolveMCPDirectoryUserID(r *http.Request) string {
	if userID := strings.TrimSpace(resolveUserID(r, nil)); userID != "" {
		return userID
	}
	if userID := strings.TrimSpace(r.URL.Query().Get("user_id")); userID != "" {
		return userID
	}
	return "local"
}

func parseMCPConnectorListOptions(r *http.Request) (mcpdirectory.ConnectorListOptions, error) {
	query := r.URL.Query()
	options := mcpdirectory.ConnectorListOptions{
		Source: strings.TrimSpace(query.Get("source")),
		Status: strings.TrimSpace(query.Get("status")),
		Query:  strings.TrimSpace(query.Get("q")),
	}
	if raw, exists := query["limit"]; exists {
		if len(raw) != 1 {
			return mcpdirectory.ConnectorListOptions{}, errors.New("limit must be specified once")
		}
		value, err := strconv.Atoi(raw[0])
		if err != nil || value < 1 || value > 500 {
			return mcpdirectory.ConnectorListOptions{}, errors.New("limit must be an integer between 1 and 500")
		}
		options.Limit = value
	}
	if raw, exists := query["offset"]; exists {
		if len(raw) != 1 {
			return mcpdirectory.ConnectorListOptions{}, errors.New("offset must be specified once")
		}
		value, err := strconv.Atoi(raw[0])
		if err != nil || value < 0 {
			return mcpdirectory.ConnectorListOptions{}, errors.New("offset must be a non-negative integer")
		}
		options.Offset = value
	}
	if raw, exists := query["enabled"]; exists {
		if len(raw) != 1 {
			return mcpdirectory.ConnectorListOptions{}, errors.New("enabled must be specified once")
		}
		value, err := strconv.ParseBool(raw[0])
		if err != nil {
			return mcpdirectory.ConnectorListOptions{}, errors.New("enabled must be true or false")
		}
		options.Enabled = &value
	}
	return options, nil
}

func writeMCPDirectoryError(w http.ResponseWriter, err error) {
	message := err.Error()
	status := http.StatusBadRequest
	switch {
	case strings.Contains(strings.ToLower(message), "not found"):
		status = http.StatusNotFound
	case strings.Contains(strings.ToLower(message), "disabled"):
		status = http.StatusConflict
	}
	writeError(w, status, "MCP_DIRECTORY_ERROR", message)
}

func stringMapValue(body map[string]any, key string) string {
	value, _ := body[key].(string)
	return strings.TrimSpace(value)
}

func firstMCPDirectoryValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
