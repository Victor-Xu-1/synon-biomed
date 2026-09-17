package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os/exec"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"synon-go/internal/mcpdirectory"
	secretstore "synon-go/internal/persistence/secrets"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

const (
	webMCPConfigVersion      = 1
	webMCPMaxServers         = 100
	webMCPMaxOriginalJSON    = 256 << 10
	webMCPMaxDescription     = 8 << 10
	webMCPMaxArguments       = 256
	webMCPMaxEnvironment     = 256
	webMCPMaxHeaders         = 128
	webMCPConnectionTimeout  = 30 * time.Second
	webMCPConfigSecretPrefix = "mcp-config-"
)

type webMCPTransport struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

type webMCPMutationRequest struct {
	ID             string           `json:"id,omitempty"`
	Name           *string          `json:"name,omitempty"`
	Description    *string          `json:"description,omitempty"`
	Enabled        *bool            `json:"enabled,omitempty"`
	Transport      *webMCPTransport `json:"transport,omitempty"`
	OriginalJSON   *string          `json:"original_json,omitempty"`
	Builtin        *bool            `json:"builtin,omitempty"`
	RuntimeScopeID string           `json:"runtime_scope_id,omitempty"`
	Tools          json.RawMessage  `json:"tools,omitempty"`
	LastTestStatus string           `json:"last_test_status,omitempty"`
	LastConnected  int64            `json:"last_connected,omitempty"`
	CreatedAt      int64            `json:"created_at,omitempty"`
	UpdatedAt      int64            `json:"updated_at,omitempty"`
}

type webMCPStoredConfig struct {
	Version      int             `json:"version"`
	Transport    webMCPTransport `json:"transport"`
	OriginalJSON string          `json:"original_json"`
}

type webMCPConfigReference struct {
	Version       int    `json:"version"`
	SecretRef     string `json:"secret_ref"`
	TransportType string `json:"transport_type"`
}

type webMCPPreparedRecord struct {
	ServerInput workspace.MCPServerInput
	Config      webMCPStoredConfig
	SecretRef   string
}

func (s *Server) handleWebMCPBridge(w http.ResponseWriter, r *http.Request) {
	store, ok := s.agentProfileStore(w)
	if !ok {
		return
	}
	userID := compatAgentUserID(r)
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/mcp/"), "/")
	switch {
	case path == "servers":
		s.handleWebMCPServers(w, r, store, userID)
	case path == "servers/import":
		s.handleWebMCPServerImport(w, r, store, userID)
	case strings.HasPrefix(path, "servers/"):
		s.handleWebMCPServerMember(w, r, store, userID, strings.TrimPrefix(path, "servers/"))
	case path == "test-connection":
		s.handleWebMCPConnectionTest(w, r, store, userID)
	case path == "oauth/check-status":
		s.handleWebMCPOAuthStatus(w, r, store, userID)
	case path == "oauth/login":
		s.handleWebMCPOAuthLogin(w, r, store, userID)
	case path == "oauth/logout":
		s.handleWebMCPOAuthLogout(w, r, store, userID)
	case path == "oauth/authenticated":
		s.handleWebMCPAuthenticatedServers(w, r, store, userID)
	default:
		writeWebMCPError(w, http.StatusNotFound, "MCP_ENDPOINT_NOT_FOUND", "MCP endpoint not found", nil)
	}
}

func (s *Server) handleWebMCPServers(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	switch r.Method {
	case http.MethodGet:
		servers, err := store.ListMCPServers(userID)
		if err != nil {
			writeWebMCPError(w, workspaceStatus(err), "MCP_LIST_FAILED", err.Error(), nil)
			return
		}
		response := make([]map[string]any, 0, len(servers))
		for _, server := range servers {
			projected, err := s.webMCPProjection(server)
			if err != nil {
				writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), map[string]any{"id": server.ID})
				return
			}
			response = append(response, projected)
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPost:
		var input webMCPMutationRequest
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_CONFIG", err.Error(), nil)
			return
		}
		prepared, err := prepareWebMCPRecord(r.Context(), userID, uuid.NewString(), input)
		if err != nil {
			writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_CONFIG", err.Error(), nil)
			return
		}
		if err := s.createWebMCPConfigSecret(userID, prepared.SecretRef, prepared.Config); err != nil {
			writeWebMCPError(w, http.StatusInternalServerError, "MCP_SECRET_WRITE_FAILED", err.Error(), nil)
			return
		}
		server, err := store.CreateMCPServer(prepared.ServerInput)
		if err != nil {
			cleanupErr := s.deleteWebMCPConfigSecret(userID, prepared.SecretRef)
			writeWebMCPError(w, workspaceStatus(err), "MCP_CREATE_FAILED", combineWebMCPErrors(err, cleanupErr).Error(), nil)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": server.ID, "action": "created"}); err != nil {
			writeDomainEventError(w, "MCP server create", err)
			return
		}
		projected, err := s.webMCPProjection(server)
		if err != nil {
			writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusCreated, projected)
	default:
		writeWebMCPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
	}
}

func (s *Server) handleWebMCPServerImport(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodPost {
		writeWebMCPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	var input struct {
		Servers []webMCPMutationRequest `json:"servers"`
	}
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_IMPORT", err.Error(), nil)
		return
	}
	if len(input.Servers) == 0 || len(input.Servers) > webMCPMaxServers {
		writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_IMPORT", fmt.Sprintf("servers must contain 1-%d records", webMCPMaxServers), nil)
		return
	}
	prepared := make([]webMCPPreparedRecord, 0, len(input.Servers))
	for index, record := range input.Servers {
		item, err := prepareWebMCPRecord(r.Context(), userID, uuid.NewString(), record)
		if err != nil {
			writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_IMPORT", fmt.Sprintf("server %d: %v", index, err), nil)
			return
		}
		prepared = append(prepared, item)
	}
	createdSecrets := make([]string, 0, len(prepared))
	for _, item := range prepared {
		if err := s.createWebMCPConfigSecret(userID, item.SecretRef, item.Config); err != nil {
			cleanupErr := s.cleanupWebMCPConfigSecrets(userID, createdSecrets)
			writeWebMCPError(w, http.StatusInternalServerError, "MCP_SECRET_WRITE_FAILED", combineWebMCPErrors(err, cleanupErr).Error(), nil)
			return
		}
		createdSecrets = append(createdSecrets, item.SecretRef)
	}
	storeInputs := make([]workspace.MCPServerInput, 0, len(prepared))
	for _, item := range prepared {
		storeInputs = append(storeInputs, item.ServerInput)
	}
	servers, err := store.CreateMCPServers(storeInputs)
	if err != nil {
		cleanupErr := s.cleanupWebMCPConfigSecrets(userID, createdSecrets)
		writeWebMCPError(w, workspaceStatus(err), "MCP_IMPORT_FAILED", combineWebMCPErrors(err, cleanupErr).Error(), nil)
		return
	}
	if _, err := s.publishUserEvent(userID, "connector_snapshot", map[string]any{"action": "imported", "connector_count": len(servers)}); err != nil {
		writeDomainEventError(w, "MCP server import", err)
		return
	}
	response := make([]map[string]any, 0, len(servers))
	for _, server := range servers {
		projected, err := s.webMCPProjection(server)
		if err != nil {
			writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), nil)
			return
		}
		response = append(response, projected)
	}
	writeJSON(w, http.StatusCreated, response)
}

func (s *Server) handleWebMCPServerMember(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, tail string) {
	parts := strings.Split(strings.Trim(tail, "/"), "/")
	if len(parts) == 0 || len(parts) > 2 {
		writeWebMCPError(w, http.StatusNotFound, "MCP_ENDPOINT_NOT_FOUND", "MCP endpoint not found", nil)
		return
	}
	serverID, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(serverID) == "" {
		writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_ID", "invalid MCP server id", nil)
		return
	}
	server, found, err := store.GetMCPServer(serverID, userID)
	if err != nil {
		writeWebMCPError(w, workspaceStatus(err), "MCP_LOOKUP_FAILED", err.Error(), nil)
		return
	}
	if !found {
		writeWebMCPError(w, http.StatusNotFound, "MCP_NOT_FOUND", "MCP server not found", nil)
		return
	}
	if len(parts) == 2 {
		if parts[1] != "toggle" || r.Method != http.MethodPost {
			writeWebMCPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
			return
		}
		enabled := !server.Enabled
		server, err = store.UpdateMCPServer(server.ID, userID, workspace.UpdateMCPServerInput{Enabled: &enabled})
		if err != nil {
			writeWebMCPError(w, workspaceStatus(err), "MCP_UPDATE_FAILED", err.Error(), nil)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": server.ID, "action": "enabled_changed", "enabled": enabled}); err != nil {
			writeDomainEventError(w, "MCP server toggle", err)
			return
		}
		projected, err := s.webMCPProjection(server)
		if err != nil {
			writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), nil)
			return
		}
		writeJSON(w, http.StatusOK, projected)
		return
	}
	switch r.Method {
	case http.MethodPut, http.MethodPatch:
		s.updateWebMCPServer(w, r, store, userID, server)
	case http.MethodDelete:
		s.deleteWebMCPServer(w, store, userID, server)
	default:
		writeWebMCPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
	}
}

func (s *Server) updateWebMCPServer(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string, server workspace.MCPServer) {
	if server.Builtin {
		writeWebMCPError(w, http.StatusForbidden, "MCP_BUILTIN_READ_ONLY", "built-in MCP servers cannot be edited", nil)
		return
	}
	var input webMCPMutationRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_CONFIG", err.Error(), nil)
		return
	}
	current, ref, err := s.loadWebMCPStoredConfig(server)
	if err != nil {
		writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), nil)
		return
	}
	merged := webMCPMutationRequest{
		Name: &server.Name, Description: &server.Description, Enabled: &server.Enabled,
		Transport: &current.Transport, OriginalJSON: &current.OriginalJSON, Builtin: &server.Builtin,
	}
	if input.Name != nil {
		merged.Name = input.Name
	}
	if input.Description != nil {
		merged.Description = input.Description
	}
	if input.Transport != nil {
		merged.Transport = input.Transport
	}
	if input.OriginalJSON != nil {
		merged.OriginalJSON = input.OriginalJSON
	}
	if input.Builtin != nil && *input.Builtin {
		writeWebMCPError(w, http.StatusForbidden, "MCP_BUILTIN_READ_ONLY", "clients cannot mark MCP servers as built-in", nil)
		return
	}
	prepared, err := prepareWebMCPRecord(r.Context(), userID, server.ID, merged)
	if err != nil {
		writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_CONFIG", err.Error(), nil)
		return
	}
	oldSecretValue, created, err := s.replaceWebMCPConfigSecret(userID, ref, prepared.SecretRef, prepared.Config)
	if err != nil {
		writeWebMCPError(w, http.StatusInternalServerError, "MCP_SECRET_WRITE_FAILED", err.Error(), nil)
		return
	}
	name, description := prepared.ServerInput.Name, prepared.ServerInput.Description
	configJSON, rawURL, transport := prepared.ServerInput.ConfigJSON, prepared.ServerInput.URL, prepared.ServerInput.Transport
	updated, err := store.UpdateMCPServer(server.ID, userID, workspace.UpdateMCPServerInput{
		Name: &name, Description: &description, URL: &rawURL, Transport: &transport, ConfigJSON: &configJSON,
	})
	if err != nil {
		rollbackErr := s.rollbackWebMCPConfigSecret(userID, prepared.SecretRef, oldSecretValue, created)
		writeWebMCPError(w, workspaceStatus(err), "MCP_UPDATE_FAILED", combineWebMCPErrors(err, rollbackErr).Error(), nil)
		return
	}
	if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": server.ID, "action": "updated"}); err != nil {
		writeDomainEventError(w, "MCP server update", err)
		return
	}
	projected, err := s.webMCPProjection(updated)
	if err != nil {
		writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), nil)
		return
	}
	writeJSON(w, http.StatusOK, projected)
}

func (s *Server) deleteWebMCPServer(w http.ResponseWriter, store *workspace.Store, userID string, server workspace.MCPServer) {
	if server.Builtin {
		writeWebMCPError(w, http.StatusForbidden, "MCP_BUILTIN_READ_ONLY", "built-in MCP servers cannot be deleted", nil)
		return
	}
	_, ref, err := s.loadWebMCPStoredConfig(server)
	if err != nil {
		writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), nil)
		return
	}
	var backup secretstore.Secret
	if ref != "" {
		var found bool
		backup, found, err = s.secretStore.ResolveForUser(ref, userID)
		if err != nil || !found {
			if err == nil {
				err = errors.New("MCP config secret is missing")
			}
			writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), nil)
			return
		}
		if err := s.deleteWebMCPConfigSecret(userID, ref); err != nil {
			writeWebMCPError(w, http.StatusInternalServerError, "MCP_SECRET_DELETE_FAILED", err.Error(), nil)
			return
		}
	}
	if err := store.DeleteMCPServer(server.ID, userID); err != nil {
		var restoreErr error
		if ref != "" {
			_, restoreErr = s.secretStore.Create(backup)
		}
		writeWebMCPError(w, workspaceStatus(err), "MCP_DELETE_FAILED", combineWebMCPErrors(err, restoreErr).Error(), nil)
		return
	}
	_ = mcpstdio.DisconnectOAuth(s.fileRoot, server.Name)
	if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": server.ID, "action": "deleted"}); err != nil {
		writeDomainEventError(w, "MCP server delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWebMCPConnectionTest(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodPost {
		writeWebMCPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	var input webMCPMutationRequest
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_CONFIG", err.Error(), nil)
		return
	}
	var config mcpstdio.ServerConfig
	var serverName string
	if id := strings.TrimSpace(firstNonEmptyString(input.RuntimeScopeID, input.ID)); id != "" {
		if server, found, err := store.GetMCPServer(id, userID); err != nil {
			writeWebMCPError(w, workspaceStatus(err), "MCP_LOOKUP_FAILED", err.Error(), nil)
			return
		} else if found {
			config, err = s.runtimeMCPConfig(server)
			if err != nil {
				writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), nil)
				return
			}
			serverName = server.Name
		}
	}
	if serverName == "" {
		prepared, err := prepareWebMCPRecord(r.Context(), userID, uuid.NewString(), input)
		if err != nil {
			writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_CONFIG", err.Error(), nil)
			return
		}
		config = s.applyMCPX509Posture(runtimeMCPConfigFromStored(prepared.ServerInput.Name, true, prepared.Config))
		serverName = prepared.ServerInput.Name
	}
	ctx, cancel := context.WithTimeout(r.Context(), webMCPConnectionTimeout)
	defer cancel()
	if strings.TrimSpace(config.URL) != "" {
		client, err := mcpdirectory.SecureHTTPClient(ctx, config.URL, s.httpClient)
		if err != nil {
			writeWebMCPConnectionFailure(w, config, err)
			return
		}
		ctx = mcpstdio.WithHTTPClient(ctx, client)
	}
	tools, err := mcpstdio.ListToolsForServer(ctx, s.fileRoot, serverName, config)
	if err != nil {
		writeWebMCPConnectionFailure(w, config, err)
		return
	}
	responseTools := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		responseTools = append(responseTools, map[string]any{
			"name": tool.ToolName, "description": tool.Description,
			"input_schema": tool.InputSchema,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "tools": responseTools})
}

func (s *Server) handleWebMCPOAuthStatus(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodPost {
		writeWebMCPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	server, ok := s.webMCPServerFromOAuthRequest(w, r, store, userID)
	if !ok {
		return
	}
	authenticated := mcpstdio.OAuthConnected(s.fileRoot, server.Name)
	_, recorded, err := store.GetMCPOAuthStatus(server.ID, userID)
	if err != nil {
		writeWebMCPError(w, workspaceStatus(err), "MCP_OAUTH_STATUS_FAILED", err.Error(), nil)
		return
	}
	if authenticated && !recorded {
		if err := store.UpsertMCPOAuthStatus(workspace.MCPOAuthStatusInput{
			MCPServerID: server.ID, UserID: userID, AccessTokenRef: "vault://mcp/oauth/" + server.ID, TokenType: "Bearer",
		}); err != nil {
			writeWebMCPError(w, workspaceStatus(err), "MCP_OAUTH_STATUS_FAILED", err.Error(), nil)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_status", map[string]any{"connector_id": server.ID, "status": "connected"}); err != nil {
			writeDomainEventError(w, "MCP OAuth status", err)
			return
		}
	}
	if !authenticated && recorded {
		if _, err := store.DisconnectMCPOAuthCount(server.ID, userID); err != nil {
			writeWebMCPError(w, workspaceStatus(err), "MCP_OAUTH_STATUS_FAILED", err.Error(), nil)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"authenticated": authenticated})
}

func (s *Server) handleWebMCPOAuthLogin(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodPost {
		writeWebMCPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	server, ok := s.webMCPServerFromOAuthRequest(w, r, store, userID)
	if !ok {
		return
	}
	config, err := s.runtimeMCPConfig(server)
	if err != nil {
		writeWebMCPError(w, http.StatusInternalServerError, "MCP_CONFIG_CORRUPT", err.Error(), nil)
		return
	}
	if strings.TrimSpace(config.URL) == "" {
		writeWebMCPError(w, http.StatusBadRequest, "MCP_OAUTH_UNSUPPORTED", "OAuth requires an HTTP MCP transport", nil)
		return
	}
	client, err := mcpdirectory.SecureHTTPClient(r.Context(), config.URL, s.httpClient)
	if err != nil {
		writeWebMCPError(w, http.StatusBadRequest, "MCP_OAUTH_UNAVAILABLE", err.Error(), nil)
		return
	}
	result, err := mcpstdio.AuthenticateServer(mcpstdio.WithHTTPClient(r.Context(), client), s.fileRoot, server.Name, config)
	if err != nil {
		writeWebMCPError(w, http.StatusBadGateway, "MCP_OAUTH_FAILED", redactWebMCPError(err.Error(), config), nil)
		return
	}
	if result.Status != "auth_url" || strings.TrimSpace(result.AuthURL) == "" {
		writeWebMCPError(w, http.StatusBadRequest, "MCP_OAUTH_UNSUPPORTED", result.Message, nil)
		return
	}
	if _, err := s.publishUserEvent(userID, "connector_status", map[string]any{"connector_id": server.ID, "status": "authorization_started"}); err != nil {
		writeDomainEventError(w, "MCP OAuth login", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "pending": true, "auth_url": result.AuthURL, "redirect_uri": result.RedirectURI,
	})
}

func (s *Server) handleWebMCPOAuthLogout(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodPost {
		writeWebMCPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	server, ok := s.webMCPServerFromOAuthRequest(w, r, store, userID)
	if !ok {
		return
	}
	if err := mcpstdio.DisconnectOAuth(s.fileRoot, server.Name); err != nil {
		writeWebMCPError(w, http.StatusInternalServerError, "MCP_OAUTH_LOGOUT_FAILED", err.Error(), nil)
		return
	}
	if _, err := store.DisconnectMCPOAuthCount(server.ID, userID); err != nil {
		writeWebMCPError(w, workspaceStatus(err), "MCP_OAUTH_LOGOUT_FAILED", err.Error(), nil)
		return
	}
	if _, err := s.publishUserEvent(userID, "connector_status", map[string]any{"connector_id": server.ID, "status": "disconnected"}); err != nil {
		writeDomainEventError(w, "MCP OAuth logout", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleWebMCPAuthenticatedServers(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodGet {
		writeWebMCPError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", nil)
		return
	}
	servers, err := store.ListMCPServers(userID)
	if err != nil {
		writeWebMCPError(w, workspaceStatus(err), "MCP_LIST_FAILED", err.Error(), nil)
		return
	}
	urls := make([]string, 0)
	for _, server := range servers {
		if strings.TrimSpace(server.URL) != "" && mcpstdio.OAuthConnected(s.fileRoot, server.Name) {
			urls = append(urls, server.URL)
		}
	}
	sort.Strings(urls)
	writeJSON(w, http.StatusOK, urls)
}

func (s *Server) webMCPServerFromOAuthRequest(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) (workspace.MCPServer, bool) {
	var input struct {
		ServerURL string `json:"server_url"`
	}
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_OAUTH_REQUEST", err.Error(), nil)
		return workspace.MCPServer{}, false
	}
	wanted := strings.TrimSpace(input.ServerURL)
	if wanted == "" {
		writeWebMCPError(w, http.StatusBadRequest, "INVALID_MCP_OAUTH_REQUEST", "server_url is required", nil)
		return workspace.MCPServer{}, false
	}
	servers, err := store.ListMCPServers(userID)
	if err != nil {
		writeWebMCPError(w, workspaceStatus(err), "MCP_LIST_FAILED", err.Error(), nil)
		return workspace.MCPServer{}, false
	}
	matches := make([]workspace.MCPServer, 0, 1)
	for _, server := range servers {
		if strings.TrimSpace(server.URL) == wanted && server.Transport != "stdio" {
			matches = append(matches, server)
		}
	}
	if len(matches) == 0 {
		writeWebMCPError(w, http.StatusNotFound, "MCP_NOT_FOUND", "MCP server URL not found", nil)
		return workspace.MCPServer{}, false
	}
	if len(matches) > 1 {
		writeWebMCPError(w, http.StatusConflict, "MCP_URL_AMBIGUOUS", "multiple MCP servers use this URL", nil)
		return workspace.MCPServer{}, false
	}
	return matches[0], true
}

func prepareWebMCPRecord(ctx context.Context, userID, serverID string, input webMCPMutationRequest) (webMCPPreparedRecord, error) {
	if input.Name == nil || input.Transport == nil {
		return webMCPPreparedRecord{}, errors.New("name and transport are required")
	}
	name := strings.TrimSpace(*input.Name)
	if err := validateWebMCPName(name); err != nil {
		return webMCPPreparedRecord{}, err
	}
	description := ""
	if input.Description != nil {
		description = strings.TrimSpace(*input.Description)
	}
	if len(description) > webMCPMaxDescription {
		return webMCPPreparedRecord{}, fmt.Errorf("description exceeds %d bytes", webMCPMaxDescription)
	}
	if input.Builtin != nil && *input.Builtin {
		return webMCPPreparedRecord{}, errors.New("clients cannot create built-in MCP servers")
	}
	transport, rawURL, storedTransport, err := normalizeWebMCPTransport(ctx, serverID, *input.Transport)
	if err != nil {
		return webMCPPreparedRecord{}, err
	}
	originalJSON := "{}"
	if input.OriginalJSON != nil && strings.TrimSpace(*input.OriginalJSON) != "" {
		originalJSON = *input.OriginalJSON
	}
	if len(originalJSON) > webMCPMaxOriginalJSON {
		return webMCPPreparedRecord{}, fmt.Errorf("original_json exceeds %d bytes", webMCPMaxOriginalJSON)
	}
	if !json.Valid([]byte(originalJSON)) {
		return webMCPPreparedRecord{}, errors.New("original_json must contain valid JSON")
	}
	secretRef := webMCPConfigSecretPrefix + serverID
	referenceJSON, err := json.Marshal(webMCPConfigReference{
		Version: webMCPConfigVersion, SecretRef: secretRef, TransportType: transport.Type,
	})
	if err != nil {
		return webMCPPreparedRecord{}, fmt.Errorf("encode MCP config reference: %w", err)
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return webMCPPreparedRecord{
		ServerInput: workspace.MCPServerInput{
			ID: serverID, UserID: userID, Name: name, Description: description,
			URL: rawURL, Transport: storedTransport, ConfigJSON: string(referenceJSON), Enabled: &enabled,
		},
		Config:    webMCPStoredConfig{Version: webMCPConfigVersion, Transport: transport, OriginalJSON: originalJSON},
		SecretRef: secretRef,
	}, nil
}

func normalizeWebMCPTransport(ctx context.Context, serverID string, input webMCPTransport) (webMCPTransport, string, string, error) {
	input.Type = strings.ToLower(strings.TrimSpace(input.Type))
	switch input.Type {
	case "stdio":
		input.Command = strings.TrimSpace(input.Command)
		if input.Command == "" {
			return webMCPTransport{}, "", "", errors.New("stdio transport requires command")
		}
		if len(input.Command) > 4096 || strings.IndexByte(input.Command, 0) >= 0 {
			return webMCPTransport{}, "", "", errors.New("stdio command is invalid")
		}
		if input.URL != "" || len(input.Headers) > 0 {
			return webMCPTransport{}, "", "", errors.New("stdio transport cannot include URL or headers")
		}
		if len(input.Args) > webMCPMaxArguments || len(input.Env) > webMCPMaxEnvironment {
			return webMCPTransport{}, "", "", errors.New("stdio args or environment exceed configured limits")
		}
		for _, argument := range input.Args {
			if len(argument) > 8192 || strings.IndexByte(argument, 0) >= 0 {
				return webMCPTransport{}, "", "", errors.New("stdio argument is invalid")
			}
		}
		for key, value := range input.Env {
			if !validWebMCPEnvironmentName(key) || len(value) > 64<<10 || strings.IndexByte(value, 0) >= 0 {
				return webMCPTransport{}, "", "", fmt.Errorf("stdio environment entry %q is invalid", key)
			}
		}
		return input, "stdio://local/" + serverID, "stdio", nil
	case "sse", "http", "streamable_http":
		input.URL = strings.TrimSpace(input.URL)
		if input.Command != "" || len(input.Args) > 0 || len(input.Env) > 0 {
			return webMCPTransport{}, "", "", errors.New("HTTP MCP transport cannot include command, args, or environment")
		}
		if err := validatePublicMCPURL(ctx, input.URL); err != nil {
			return webMCPTransport{}, "", "", err
		}
		if len(input.Headers) > webMCPMaxHeaders {
			return webMCPTransport{}, "", "", fmt.Errorf("MCP headers exceed %d entries", webMCPMaxHeaders)
		}
		for key, value := range input.Headers {
			if !validWebMCPHeaderName(key) || len(value) > 64<<10 || strings.ContainsAny(value, "\r\n") {
				return webMCPTransport{}, "", "", fmt.Errorf("MCP header %q is invalid", key)
			}
		}
		storedTransport := "streamable-http"
		if input.Type == "sse" {
			storedTransport = "sse"
		}
		return input, input.URL, storedTransport, nil
	default:
		return webMCPTransport{}, "", "", errors.New("transport type must be stdio, sse, http, or streamable_http")
	}
}

func validateWebMCPName(value string) error {
	if value == "" || len(value) > 128 {
		return errors.New("name must contain 1-128 characters")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("name cannot contain control characters")
		}
	}
	return nil
}

func validWebMCPEnvironmentName(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for index, character := range value {
		if !(character == '_' || unicode.IsLetter(character) || (index > 0 && unicode.IsDigit(character))) {
			return false
		}
	}
	return true
}

func validWebMCPHeaderName(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	const token = "!#$%&'*+-.^_`|~"
	for _, character := range value {
		if !(unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune(token, character)) {
			return false
		}
	}
	return true
}

func (s *Server) createWebMCPConfigSecret(userID, secretRef string, config webMCPStoredConfig) error {
	if s.secretStore == nil {
		return errors.New("encrypted secret store is unavailable")
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		return fmt.Errorf("encode MCP config secret: %w", err)
	}
	_, err = s.secretStore.Create(secretstore.Secret{
		ID: secretRef, UserID: userID, Provider: "mcp", Name: secretRef,
		Value: string(encoded), Description: "Encrypted MCP transport configuration",
	})
	return err
}

func (s *Server) deleteWebMCPConfigSecret(userID, secretRef string) error {
	if secretRef == "" {
		return nil
	}
	if s.secretStore == nil {
		return errors.New("encrypted secret store is unavailable")
	}
	deleted, err := s.secretStore.DeleteForUser(secretRef, userID)
	if err != nil {
		return err
	}
	if !deleted {
		return fmt.Errorf("MCP config secret %q does not exist", secretRef)
	}
	return nil
}

func (s *Server) cleanupWebMCPConfigSecrets(userID string, refs []string) error {
	var cleanupErr error
	for index := len(refs) - 1; index >= 0; index-- {
		if err := s.deleteWebMCPConfigSecret(userID, refs[index]); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return cleanupErr
}

func (s *Server) loadWebMCPStoredConfig(server workspace.MCPServer) (webMCPStoredConfig, string, error) {
	var reference webMCPConfigReference
	if json.Unmarshal([]byte(server.ConfigJSON), &reference) == nil && strings.TrimSpace(reference.SecretRef) != "" {
		if s.secretStore == nil {
			return webMCPStoredConfig{}, "", errors.New("encrypted secret store is unavailable")
		}
		secret, found, err := s.secretStore.ResolveForUser(reference.SecretRef, server.UserID)
		if err != nil {
			return webMCPStoredConfig{}, "", fmt.Errorf("resolve MCP config secret: %w", err)
		}
		if !found {
			return webMCPStoredConfig{}, "", fmt.Errorf("MCP config secret %q is missing", reference.SecretRef)
		}
		var config webMCPStoredConfig
		if err := json.Unmarshal([]byte(secret.Value), &config); err != nil {
			return webMCPStoredConfig{}, "", fmt.Errorf("decode MCP config secret: %w", err)
		}
		if config.Version != webMCPConfigVersion {
			return webMCPStoredConfig{}, "", fmt.Errorf("unsupported MCP config version %d", config.Version)
		}
		return config, reference.SecretRef, nil
	}
	return legacyWebMCPStoredConfig(server), "", nil
}

func legacyWebMCPStoredConfig(server workspace.MCPServer) webMCPStoredConfig {
	transportType := compatMCPTransportForAPI(server.Transport)
	transport := webMCPTransport{Type: transportType, URL: server.URL}
	if server.Transport == "stdio" {
		transport.Type = "stdio"
		transport.Command = server.URL
		transport.URL = ""
	}
	if server.Transport == "websocket" {
		transport.Type = "streamable_http"
	}
	return webMCPStoredConfig{Version: webMCPConfigVersion, Transport: transport, OriginalJSON: "{}"}
}

func (s *Server) replaceWebMCPConfigSecret(userID, oldRef, newRef string, config webMCPStoredConfig) (string, bool, error) {
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", false, fmt.Errorf("encode MCP config secret: %w", err)
	}
	if oldRef == "" {
		return "", true, s.createWebMCPConfigSecret(userID, newRef, config)
	}
	secret, found, err := s.secretStore.ResolveForUser(oldRef, userID)
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", false, fmt.Errorf("MCP config secret %q is missing", oldRef)
	}
	oldValue := secret.Value
	_, err = s.secretStore.UpdateForUser(oldRef, userID, func(secret *secretstore.Secret) error {
		secret.Value = string(encoded)
		return nil
	})
	return oldValue, false, err
}

func (s *Server) rollbackWebMCPConfigSecret(userID, ref, oldValue string, created bool) error {
	if created {
		return s.deleteWebMCPConfigSecret(userID, ref)
	}
	_, err := s.secretStore.UpdateForUser(ref, userID, func(secret *secretstore.Secret) error {
		secret.Value = oldValue
		return nil
	})
	return err
}

func (s *Server) runtimeMCPConfig(server workspace.MCPServer) (mcpstdio.ServerConfig, error) {
	stored, _, err := s.loadWebMCPStoredConfig(server)
	if err != nil {
		return mcpstdio.ServerConfig{}, err
	}
	return s.applyMCPX509Posture(runtimeMCPConfigFromStored(server.Name, server.Enabled, stored)), nil
}

func (s *Server) applyMCPX509Posture(config mcpstdio.ServerConfig) mcpstdio.ServerConfig {
	posture := mcpstdio.TLSPosture{Strict: true}
	if s != nil && s.mcpX509Posture != nil {
		posture = s.mcpX509Posture()
	}
	return mcpstdio.ApplyTLSPosture(config, posture)
}

func runtimeMCPConfigFromStored(name string, enabled bool, stored webMCPStoredConfig) mcpstdio.ServerConfig {
	return mcpstdio.ServerConfig{
		Type: stored.Transport.Type, Name: name, Command: stored.Transport.Command,
		Args: append([]string(nil), stored.Transport.Args...), Env: cloneWebMCPMap(stored.Transport.Env),
		URL: stored.Transport.URL, Headers: cloneWebMCPMap(stored.Transport.Headers), Disabled: !enabled,
	}
}

func (s *Server) webMCPProjection(server workspace.MCPServer) (map[string]any, error) {
	stored, _, err := s.loadWebMCPStoredConfig(server)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id": server.ID, "name": server.Name, "description": server.Description,
		"enabled": server.Enabled, "transport": stored.Transport,
		"created_at": server.CreatedAt.UnixMilli(), "updated_at": server.UpdatedAt.UnixMilli(),
		"original_json": stored.OriginalJSON, "builtin": server.Builtin,
	}, nil
}

func cloneWebMCPMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func writeWebMCPConnectionFailure(w http.ResponseWriter, config mcpstdio.ServerConfig, err error) {
	code := "MCP_CONNECTION_FAILED"
	details := map[string]any{}
	message := redactWebMCPError(err.Error(), config)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		code = "MCP_TIMEOUT"
		details["timeout_seconds"] = int(webMCPConnectionTimeout / time.Second)
	case errors.Is(err, exec.ErrNotFound):
		code = "MCP_COMMAND_NOT_FOUND"
		details["command"] = config.Command
		details["runtime"] = webMCPRuntimeLabel(config.Command)
	case strings.Contains(strings.ToLower(message), "permission denied"):
		code = "MCP_COMMAND_PERMISSION_DENIED"
		details["command"] = config.Command
	case strings.Contains(message, "HTTP 401") || strings.Contains(strings.ToLower(message), "unauthorized"):
		writeJSON(w, http.StatusOK, map[string]any{
			"success": false, "error": message, "code": code,
			"needsAuth": true, "needs_auth": true, "authMethod": "oauth", "auth_method": "oauth",
		})
		return
	case strings.Contains(message, "HTTP "):
		code = "MCP_HTTP_ERROR"
	case strings.Contains(strings.ToLower(message), "json-rpc"):
		code = "MCP_RPC_ERROR"
	case strings.Contains(strings.ToLower(message), "protocol") || strings.Contains(strings.ToLower(message), "decode"):
		code = "MCP_PROTOCOL_ERROR"
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": false, "error": message, "code": code, "details": details})
}

func redactWebMCPError(message string, config mcpstdio.ServerConfig) string {
	for _, values := range []map[string]string{config.Env, config.Headers} {
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				message = strings.ReplaceAll(message, value, "[REDACTED]")
			}
		}
	}
	return message
}

func webMCPRuntimeLabel(command string) string {
	name := strings.ToLower(strings.TrimSpace(command))
	if parsed := strings.LastIndexAny(name, `/\\`); parsed >= 0 {
		name = name[parsed+1:]
	}
	name = strings.TrimSuffix(name, ".exe")
	switch name {
	case "node", "npx":
		return "node"
	case "bun", "bunx":
		return "bun"
	case "uv", "uvx":
		return "uv"
	case "python", "python3", "py":
		return "python"
	case "deno":
		return "deno"
	default:
		return ""
	}
}

func writeWebMCPError(w http.ResponseWriter, status int, code, message string, details any) {
	payload := map[string]any{"error": message, "message": message, "code": code}
	if details != nil {
		payload["details"] = details
	}
	writeJSON(w, status, payload)
}

func combineWebMCPErrors(primary, cleanup error) error {
	if cleanup == nil {
		return primary
	}
	return fmt.Errorf("%w; cleanup failed: %v", primary, cleanup)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
