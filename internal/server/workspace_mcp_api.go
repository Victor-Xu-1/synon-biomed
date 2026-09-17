package server

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleWorkspaceMCPServers(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		servers, err := store.ListMCPServers(r.URL.Query().Get("user_id"))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "servers": servers})
	case http.MethodPost:
		var input struct {
			ID             string `json:"id"`
			UserID         string `json:"userId"`
			Name           string `json:"name"`
			Description    string `json:"description"`
			URL            string `json:"url"`
			Transport      string `json:"transport"`
			OAuthServerURL string `json:"oauthServerUrl"`
			ClientID       string `json:"clientId"`
			Scopes         string `json:"scopes"`
			HeadersHelper  string `json:"headersHelper"`
			Enabled        *bool  `json:"enabled"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		server, err := store.CreateMCPServer(workspace.MCPServerInput{
			ID: input.ID, UserID: input.UserID, Name: input.Name, Description: input.Description,
			URL: input.URL, Transport: input.Transport, OAuthServerURL: input.OAuthServerURL,
			ClientID: input.ClientID, Scopes: input.Scopes, HeadersHelper: input.HeadersHelper,
			Enabled: input.Enabled,
		})
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if _, err := s.publishUserEvent(server.UserID, "connector_update", map[string]any{"connector_id": server.ID, "action": "created"}); err != nil {
			writeDomainEventError(w, "MCP server create", err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "server": server})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceMCPServer(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	segments := workspacePathSegments(strings.TrimPrefix(r.URL.Path, "/api/go/mcp/servers/"))
	if len(segments) == 0 || len(segments) > 3 {
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	serverID, err := url.PathUnescape(segments[0])
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid mcp server id"})
		return
	}
	if len(segments) == 1 {
		s.handleWorkspaceMCPServerRecord(w, r, store, serverID)
		return
	}
	if len(segments) == 3 {
		if segments[1] == "assignments" {
			s.handleWorkspaceMCPAssignmentMember(w, r, store, serverID, segments[2])
			return
		}
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
		return
	}
	switch segments[1] {
	case "assignments":
		s.handleWorkspaceMCPAssignment(w, r, store, serverID)
	case "grants":
		s.handleWorkspaceMCPGrant(w, r, store, serverID)
	case "oauth":
		s.handleWorkspaceMCPOAuth(w, r, store, serverID)
	default:
		writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "workspace endpoint not found"})
	}
}

func (s *Server) handleWorkspaceMCPAssignment(w http.ResponseWriter, r *http.Request, store *workspace.Store, serverID string) {
	if r.Method == http.MethodGet {
		assignments, err := store.ListMCPAssignments(serverID, r.URL.Query().Get("user_id"))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "assignments": assignments})
		return
	}
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		ID        string `json:"id"`
		UserID    string `json:"userId"`
		AgentName string `json:"agentName"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	assignment, err := store.AssignMCPServerToAgent(workspace.MCPAssignmentInput{ID: input.ID, MCPServerID: serverID, UserID: input.UserID, AgentName: input.AgentName})
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := s.publishUserEvent(assignment.UserID, "connector_update", map[string]any{"connector_id": serverID, "agent_name": assignment.AgentName, "action": "assigned"}); err != nil {
		writeDomainEventError(w, "MCP assignment create", err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "assignment": assignment})
}

func (s *Server) handleWorkspaceMCPGrant(w http.ResponseWriter, r *http.Request, store *workspace.Store, serverID string) {
	if r.Method == http.MethodGet {
		grants, err := store.ListMCPToolGrants(serverID, r.URL.Query().Get("user_id"))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "grants": grants, "permissions": grants})
		return
	}
	if r.Method != http.MethodPost {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	var input struct {
		ID        string `json:"id"`
		UserID    string `json:"userId"`
		AgentName string `json:"agentName"`
		ToolName  string `json:"toolName"`
		Enabled   bool   `json:"enabled"`
	}
	if err := decodeWorkspaceJSON(r, &input); err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	grant, err := store.SetMCPToolGrant(workspace.MCPToolGrantInput{ID: input.ID, MCPServerID: serverID, UserID: input.UserID, AgentName: input.AgentName, ToolName: input.ToolName, Enabled: input.Enabled})
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := s.publishUserEvent(grant.UserID, "connector_update", map[string]any{"connector_id": serverID, "agent_name": grant.AgentName, "tool_name": grant.ToolName, "action": "grant_updated"}); err != nil {
		writeDomainEventError(w, "MCP tool grant update", err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "grant": grant})
}

func (s *Server) handleWorkspaceMCPOAuth(w http.ResponseWriter, r *http.Request, store *workspace.Store, serverID string) {
	switch r.Method {
	case http.MethodGet:
		status, connected, err := store.GetMCPOAuthStatus(serverID, r.URL.Query().Get("user_id"))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !connected {
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "connected": false})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "connected": true, "tokenType": status.TokenType, "expiresAt": status.ExpiresAt, "scopes": status.Scopes})
	case http.MethodPut:
		var input struct {
			UserID         string     `json:"userId"`
			AccessTokenRef string     `json:"accessTokenRef"`
			TokenType      string     `json:"tokenType"`
			ExpiresAt      *time.Time `json:"expiresAt"`
			Scopes         []string   `json:"scopes"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := store.UpsertMCPOAuthStatus(workspace.MCPOAuthStatusInput{MCPServerID: serverID, UserID: input.UserID, AccessTokenRef: input.AccessTokenRef, TokenType: input.TokenType, ExpiresAt: input.ExpiresAt, Scopes: input.Scopes}); err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if _, err := s.publishUserEvent(input.UserID, "connector_status", map[string]any{"connector_id": serverID, "status": "connected"}); err != nil {
			writeDomainEventError(w, "MCP OAuth connect", err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
	case http.MethodDelete:
		userID := r.URL.Query().Get("user_id")
		if err := store.DisconnectMCPOAuth(serverID, userID); err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_status", map[string]any{"connector_id": serverID, "status": "disconnected"}); err != nil {
			writeDomainEventError(w, "MCP OAuth disconnect", err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}
