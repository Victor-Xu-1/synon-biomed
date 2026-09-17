package server

import (
	"net/http"
	"net/url"

	workspace "synon-go/internal/persistence/workspace"
)

func (s *Server) handleWorkspaceMCPServerRecord(w http.ResponseWriter, r *http.Request, store *workspace.Store, serverID string) {
	switch r.Method {
	case http.MethodGet:
		server, found, err := store.GetMCPServer(serverID, r.URL.Query().Get("user_id"))
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if !found {
			writeWorkspaceJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "mcp server not found"})
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "server": server})
	case http.MethodPatch:
		var input struct {
			UserID         string  `json:"userId"`
			Name           *string `json:"name"`
			Description    *string `json:"description"`
			URL            *string `json:"url"`
			Transport      *string `json:"transport"`
			OAuthServerURL *string `json:"oauthServerUrl"`
			ClientID       *string `json:"clientId"`
			Scopes         *string `json:"scopes"`
			HeadersHelper  *string `json:"headersHelper"`
			Enabled        *bool   `json:"enabled"`
		}
		if err := decodeWorkspaceJSON(r, &input); err != nil {
			writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		server, err := store.UpdateMCPServer(serverID, input.UserID, workspace.UpdateMCPServerInput{
			Name: input.Name, Description: input.Description, URL: input.URL, Transport: input.Transport,
			OAuthServerURL: input.OAuthServerURL, ClientID: input.ClientID, Scopes: input.Scopes,
			HeadersHelper: input.HeadersHelper, Enabled: input.Enabled,
		})
		if err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if _, err := s.publishUserEvent(server.UserID, "connector_update", map[string]any{"connector_id": server.ID, "action": "updated"}); err != nil {
			writeDomainEventError(w, "MCP server update", err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "server": server})
	case http.MethodDelete:
		userID := r.URL.Query().Get("user_id")
		if err := store.DeleteMCPServer(serverID, userID); err != nil {
			writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": serverID, "action": "deleted"}); err != nil {
			writeDomainEventError(w, "MCP server delete", err)
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
	}
}

func (s *Server) handleWorkspaceMCPAssignmentMember(w http.ResponseWriter, r *http.Request, store *workspace.Store, serverID, member string) {
	member, err := url.PathUnescape(member)
	if err != nil {
		writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid assignment target"})
		return
	}
	if member == "all" {
		switch r.Method {
		case http.MethodPost:
			var input struct {
				UserID string `json:"userId"`
			}
			if err := decodeWorkspaceJSON(r, &input); err != nil {
				writeWorkspaceJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			assignments, err := store.AssignMCPServerToAllAgents(serverID, input.UserID)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			if _, err := s.publishUserEvent(input.UserID, "connector_update", map[string]any{"connector_id": serverID, "action": "assigned_all", "assignment_count": len(assignments)}); err != nil {
				writeDomainEventError(w, "MCP assign all", err)
				return
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "assignments": assignments})
		case http.MethodDelete:
			userID := r.URL.Query().Get("user_id")
			count, err := store.DetachMCPServerFromAllAgents(serverID, userID)
			if err != nil {
				writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
				return
			}
			if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": serverID, "action": "detached_all", "detached": count}); err != nil {
				writeDomainEventError(w, "MCP detach all", err)
				return
			}
			writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "detached": count})
		default:
			writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		}
		return
	}
	if r.Method != http.MethodDelete {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	userID := r.URL.Query().Get("user_id")
	if err := store.UnassignMCPServerFromAgent(userID, member, serverID); err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": serverID, "agent_name": member, "action": "detached"}); err != nil {
		writeDomainEventError(w, "MCP assignment detach", err)
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleWorkspaceMCPAttachmentCounts(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	counts, err := store.MCPAttachmentCountsByAgent(r.URL.Query().Get("user_id"))
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "counts": counts})
}

func (s *Server) handleWorkspaceMCPConnectors(w http.ResponseWriter, r *http.Request) {
	store, ok := s.workspaceForRequest(w)
	if !ok {
		return
	}
	if r.Method != http.MethodGet {
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "method not allowed"})
		return
	}
	userID := r.URL.Query().Get("user_id")
	servers, err := store.ListMCPServers(userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	counts, err := store.MCPAttachmentCountsByAgent(userID)
	if err != nil {
		writeWorkspaceJSON(w, workspaceStatus(err), map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"ok": true, "connectors": servers, "attachmentCounts": counts})
}
