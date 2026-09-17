package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"synon-go/internal/mcpdirectory"
	workspace "synon-go/internal/persistence/workspace"
	"synon-go/internal/tools/mcpstdio"
)

var compatMCPServerNamePattern = regexp.MustCompile(`^[a-z0-9-]+$`)

type compatOptionalString struct {
	Set   bool
	Value string
}

func (value *compatOptionalString) UnmarshalJSON(raw []byte) error {
	value.Set = true
	if string(raw) == "null" {
		value.Value = ""
		return nil
	}
	return json.Unmarshal(raw, &value.Value)
}

func (value compatOptionalString) Pointer() *string {
	if !value.Set {
		return nil
	}
	return &value.Value
}

func (s *Server) handleMCPServerCompatibility(w http.ResponseWriter, r *http.Request) {
	store, ok := s.agentProfileStore(w)
	if !ok {
		return
	}
	userID := compatAgentUserID(r)
	if r.URL.Path == "/api/mcp-servers" || r.URL.Path == "/api/mcp-servers/" {
		s.handleMCPServerCollectionCompatibility(w, r, store, userID)
		return
	}
	tail := strings.TrimPrefix(r.URL.Path, "/api/mcp-servers/")
	if tail == "attachment-counts" {
		s.handleMCPAttachmentCountsCompatibility(w, r, store, userID)
		return
	}
	segments := strings.Split(tail, "/")
	for index := range segments {
		decoded, err := url.PathUnescape(segments[index])
		if err != nil || strings.TrimSpace(decoded) == "" {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("invalid MCP server path"))
			return
		}
		segments[index] = decoded
	}
	switch {
	case len(segments) == 1:
		s.handleMCPServerRecordCompatibility(w, r, store, userID, segments[0])
	case len(segments) == 2 && segments[1] == "attach" && r.Method == http.MethodPost:
		s.handleMCPAttachSelectedCompatibility(w, r, store, userID, segments[0])
	case len(segments) == 2 && segments[1] == "attach-all" && r.Method == http.MethodPost:
		s.handleMCPAttachAllCompatibility(w, store, userID, segments[0])
	case len(segments) == 3 && segments[1] == "agents" && r.Method == http.MethodDelete:
		s.handleMCPDetachAgentCompatibility(w, store, userID, segments[0], segments[2])
	case len(segments) == 2 && segments[1] == "detach-all" && r.Method == http.MethodDelete:
		s.handleMCPDetachAllCompatibility(w, store, userID, segments[0])
	case len(segments) == 2 && segments[1] == "tool-grants":
		s.handleMCPToolGrantsCompatibility(w, r, store, userID, segments[0])
	case len(segments) == 2 && segments[1] == "tool-permissions":
		s.handleMCPToolPermissionsCompatibility(w, r, store, userID, segments[0])
	case len(segments) == 3 && segments[1] == "oauth" && segments[2] == "status":
		s.handleMCPOAuthStatusCompatibility(w, r, store, userID, segments[0])
	case len(segments) == 3 && segments[1] == "oauth" && segments[2] == "disconnect":
		s.handleMCPOAuthDisconnectCompatibility(w, r, store, userID, segments[0])
	default:
		writeAgentCompatError(w, http.StatusNotFound, errors.New("MCP server endpoint not found"))
	}
}

func (s *Server) handleAgentMCPServersCompatibility(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, agentName string) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	agent, err := s.ensureAgentProfile(store, userID, agentName)
	if err != nil {
		writeAgentProfileLookupError(w, err)
		return
	}
	connectors, _, err := s.workspaceMCPRuntimeConnectors(userID, agent.Name, agent, true)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	includeTools := r.URL.Query().Get("include_tools") != "false"
	response := make([]map[string]any, 0, len(connectors))
	for _, connector := range connectors {
		tools := []mcpstdio.ToolProjection{}
		var discoveryError string
		if includeTools && connector.Enabled {
			tools, err = s.workspaceMCPRuntimeConnectorTools(r.Context(), userID, connector)
			if err != nil {
				discoveryError = err.Error()
				tools = []mcpstdio.ToolProjection{}
			}
		}
		var description, rawURL string
		transport := ""
		if connector.Custom != nil {
			description, rawURL, transport = connector.Custom.Description, connector.Custom.URL, compatMCPTransportForAPI(connector.Custom.Transport)
		} else if s.mcpDirectory != nil {
			resolved, found, resolveErr := s.mcpDirectory.ResolveRuntimeConnector(r.Context(), userID, connector.ID)
			if resolveErr != nil {
				discoveryError = resolveErr.Error()
			} else if found {
				description, rawURL = resolved.Description, resolved.Config.URL
				transport = resolved.Config.Type
				if transport == "" && resolved.Config.Command != "" {
					transport = "stdio"
				}
			}
		}
		item := map[string]any{
			"id": connector.ID, "name": connector.Name, "description": compatNilIfEmpty(description),
			"url": rawURL, "transport": compatMCPTransportForAPI(transport),
			"source": connector.Source, "is_connected": connector.Enabled, "tools": tools,
		}
		if discoveryError != "" {
			item["error"] = discoveryError
		}
		response = append(response, item)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleMCPToolGrantsCompatibility(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, serverID string) {
	_, custom, err := store.GetMCPServer(serverID, userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	if !custom {
		if s.mcpDirectory == nil {
			writeAgentCompatError(w, http.StatusNotFound, fmt.Errorf("MCP server %s not found", serverID))
			return
		}
		if _, found, err := s.mcpDirectory.ResolveRuntimeConnector(r.Context(), userID, serverID); err != nil || !found {
			if err == nil {
				err = fmt.Errorf("MCP server %s not found", serverID)
			}
			writeAgentCompatError(w, http.StatusNotFound, err)
			return
		}
	}
	if r.Method == http.MethodGet {
		response, err := listMCPToolGrantProjection(store, userID, serverID, custom)
		if err != nil {
			writeAgentCompatError(w, workspaceStatus(err), err)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	if r.Method == http.MethodPost {
		var input struct {
			ToolName string  `json:"toolName"`
			Decision *string `json:"decision"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeAgentCompatError(w, http.StatusBadRequest, err)
			return
		}
		input.ToolName = strings.TrimSpace(input.ToolName)
		if input.ToolName == "" {
			writeAgentCompatError(w, http.StatusBadRequest, errors.New("toolName is required"))
			return
		}
		if !custom {
			tools, err := s.mcpDirectory.ListUnifiedConnectorTools(r.Context(), userID, serverID)
			if err != nil {
				writeAgentCompatError(w, http.StatusBadGateway, err)
				return
			}
			found := false
			for _, tool := range tools {
				if tool.ToolName == input.ToolName {
					found = true
					break
				}
			}
			if !found {
				writeAgentCompatError(w, http.StatusBadRequest, fmt.Errorf("MCP tool %q was not advertised by connector %q", input.ToolName, serverID))
				return
			}
		}
		if custom {
			if err := updateCustomMCPToolGrant(store, userID, serverID, input.ToolName, input.Decision); err != nil {
				writeAgentCompatError(w, workspaceStatus(err), err)
				return
			}
		} else {
			var enabled *bool
			if input.Decision != nil {
				decision := strings.ToLower(strings.TrimSpace(*input.Decision))
				if decision != "allow" && decision != "deny" {
					writeAgentCompatError(w, http.StatusBadRequest, errors.New("decision must be allow, deny, or null"))
					return
				}
				value := decision == "allow"
				enabled = &value
			}
			if _, err := store.SetMCPConnectorToolPolicy(userID, serverID, input.ToolName, enabled); err != nil {
				writeAgentCompatError(w, workspaceStatus(err), err)
				return
			}
		}
		if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{
			"connector_id": serverID, "tool_name": input.ToolName, "action": "grant_updated",
		}); err != nil {
			writeDomainEventError(w, "MCP tool grant update", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func (s *Server) handleMCPToolPermissionsCompatibility(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, serverID string) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	server, custom, err := store.GetMCPServer(serverID, userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	var tools []mcpstdio.ToolProjection
	if custom {
		tools, err = s.discoverCompatMCPTools(r.Context(), server)
	} else if s.mcpDirectory != nil {
		tools, err = s.mcpDirectory.ListUnifiedConnectorTools(r.Context(), userID, serverID)
	} else {
		err = mcpdirectory.ErrConnectorNotFound
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"tools": []any{}, "skipApprovalsActive": false, "error": err.Error(),
		})
		return
	}
	grants, err := listMCPToolGrantProjection(store, userID, serverID, custom)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	decisions := make(map[string]string, len(grants))
	for _, grant := range grants {
		decisions[grant["toolName"].(string)] = grant["decision"].(string)
	}
	response := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		var state any
		if decision, ok := decisions[tool.ToolName]; ok {
			state = decision
		}
		response = append(response, map[string]any{
			"toolName": tool.ToolName, "title": tool.ToolName,
			"description": compatNilIfEmpty(tool.Description), "state": state,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": response, "skipApprovalsActive": false})
}

func listMCPToolGrantProjection(store *workspace.Store, userID, serverID string, custom bool) ([]map[string]any, error) {
	response := make([]map[string]any, 0)
	if custom {
		grants, err := store.ListGlobalMCPToolGrants(serverID, userID)
		if err != nil {
			return nil, err
		}
		for _, grant := range grants {
			decision := "deny"
			if grant.Enabled {
				decision = "allow"
			}
			response = append(response, map[string]any{"toolName": grant.ToolName, "decision": decision})
		}
		return response, nil
	}
	policies, err := store.ListMCPConnectorToolPolicies(userID, serverID)
	if err != nil {
		return nil, err
	}
	for _, policy := range policies {
		decision := "deny"
		if policy.Enabled {
			decision = "allow"
		}
		response = append(response, map[string]any{"toolName": policy.ToolName, "decision": decision})
	}
	return response, nil
}

func updateCustomMCPToolGrant(store *workspace.Store, userID, serverID, toolName string, decision *string) error {
	if decision == nil {
		_, err := store.DeleteGlobalMCPToolGrant(serverID, userID, toolName)
		return err
	}
	normalized := strings.ToLower(strings.TrimSpace(*decision))
	if normalized != "allow" && normalized != "deny" {
		return errors.New("decision must be allow, deny, or null")
	}
	_, err := store.SetMCPToolGrant(workspace.MCPToolGrantInput{
		ID: uuid.NewString(), MCPServerID: serverID, UserID: userID,
		AgentName: workspace.GlobalMCPToolGrantAgent, ToolName: toolName, Enabled: normalized == "allow",
	})
	return err
}

func (s *Server) handleMCPOAuthStatusCompatibility(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, serverID string) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	if _, found, err := store.GetMCPServer(serverID, userID); err != nil || !found {
		if err == nil {
			err = fmt.Errorf("MCP server %s not found", serverID)
		}
		writeAgentCompatError(w, http.StatusNotFound, err)
		return
	}
	status, connected, err := store.GetMCPOAuthStatus(serverID, userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	var expiresAt, scopes any
	if connected {
		expiresAt = status.ExpiresAt
		scopes = status.Scopes
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mcp_server_id": serverID, "is_connected": connected,
		"expires_at": expiresAt, "scopes": scopes,
	})
}

func (s *Server) handleMCPOAuthDisconnectCompatibility(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, serverID string) {
	if r.Method != http.MethodDelete {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	server, found, err := store.GetMCPServer(serverID, userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	if !found {
		writeAgentCompatError(w, http.StatusNotFound, fmt.Errorf("MCP server %s not found", serverID))
		return
	}
	deleted, err := store.DisconnectMCPOAuthCount(serverID, userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	if err := mcpstdio.DisconnectOAuth(s.fileRoot, server.Name); err != nil {
		writeAgentCompatError(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := s.publishUserEvent(userID, "connector_status", map[string]any{
		"connector_id": serverID, "status": "disconnected",
	}); err != nil {
		writeDomainEventError(w, "MCP OAuth disconnect", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "deleted": deleted})
}

func (s *Server) discoverCompatMCPTools(ctx context.Context, server workspace.MCPServer) ([]mcpstdio.ToolProjection, error) {
	config, err := s.runtimeMCPConfig(server)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(config.URL) != "" {
		client, err := mcpdirectory.SecureHTTPClient(ctx, config.URL, s.httpClient)
		if err != nil {
			return nil, err
		}
		ctx = mcpstdio.WithHTTPClient(ctx, client)
	}
	return mcpstdio.ListToolsForServer(ctx, s.fileRoot, server.Name, config)
}

func (s *Server) handleMCPServerCollectionCompatibility(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	switch r.Method {
	case http.MethodGet:
		servers, err := store.ListMCPServers(userID)
		if err != nil {
			writeAgentCompatError(w, workspaceStatus(err), err)
			return
		}
		response := make([]map[string]any, 0, len(servers))
		for _, server := range servers {
			projected, err := compatMCPServerProjection(store, server, userID)
			if err != nil {
				writeAgentCompatError(w, http.StatusInternalServerError, err)
				return
			}
			response = append(response, projected)
		}
		writeJSON(w, http.StatusOK, response)
	case http.MethodPost:
		var input struct {
			Name           string `json:"name"`
			Description    string `json:"description"`
			URL            string `json:"url"`
			Transport      string `json:"transport"`
			OAuthServerURL string `json:"oauth_server_url"`
			ClientID       string `json:"client_id"`
			Scopes         string `json:"scopes"`
			HeadersHelper  string `json:"headers_helper"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeAgentCompatError(w, http.StatusBadRequest, err)
			return
		}
		if err := validateCompatMCPServerInput(r.Context(), input.Name, input.URL, input.Transport, input.OAuthServerURL); err != nil {
			writeAgentCompatError(w, http.StatusBadRequest, err)
			return
		}
		server, err := store.CreateMCPServer(workspace.MCPServerInput{
			ID: uuid.NewString(), UserID: userID, Name: input.Name, Description: input.Description,
			URL: input.URL, Transport: compatMCPTransportForStore(input.Transport),
			OAuthServerURL: input.OAuthServerURL, ClientID: input.ClientID, Scopes: input.Scopes,
			HeadersHelper: input.HeadersHelper,
		})
		if err != nil {
			writeAgentCompatError(w, workspaceStatus(err), err)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": server.ID, "action": "created"}); err != nil {
			writeDomainEventError(w, "MCP server create", err)
			return
		}
		projected, err := compatMCPServerProjection(store, server, userID)
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusCreated, projected)
	default:
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (s *Server) handleMCPServerRecordCompatibility(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, serverID string) {
	server, found, err := store.GetMCPServer(serverID, userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	if !found {
		writeAgentCompatError(w, http.StatusNotFound, fmt.Errorf("MCP server %s not found", serverID))
		return
	}
	switch r.Method {
	case http.MethodGet:
		projected, err := compatMCPServerProjection(store, server, userID)
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, projected)
	case http.MethodPatch:
		var input struct {
			Name           compatOptionalString `json:"name"`
			Description    compatOptionalString `json:"description"`
			URL            compatOptionalString `json:"url"`
			Transport      compatOptionalString `json:"transport"`
			OAuthServerURL compatOptionalString `json:"oauth_server_url"`
			ClientID       compatOptionalString `json:"client_id"`
			Scopes         compatOptionalString `json:"scopes"`
			HeadersHelper  compatOptionalString `json:"headers_helper"`
		}
		if err := decodeAgentCompatJSON(r, &input); err != nil {
			writeAgentCompatError(w, http.StatusBadRequest, err)
			return
		}
		name, rawURL := server.Name, server.URL
		transport, oauthURL := compatMCPTransportForAPI(server.Transport), server.OAuthServerURL
		if input.Name.Set {
			name = input.Name.Value
		}
		if input.URL.Set {
			rawURL = input.URL.Value
		}
		if input.Transport.Set {
			transport = input.Transport.Value
		}
		if input.OAuthServerURL.Set {
			oauthURL = input.OAuthServerURL.Value
		}
		if err := validateCompatMCPServerInput(r.Context(), name, rawURL, transport, oauthURL); err != nil {
			writeAgentCompatError(w, http.StatusBadRequest, err)
			return
		}
		transportPointer := input.Transport.Pointer()
		if transportPointer != nil {
			normalized := compatMCPTransportForStore(*transportPointer)
			transportPointer = &normalized
		}
		server, err = store.UpdateMCPServer(serverID, userID, workspace.UpdateMCPServerInput{
			Name: input.Name.Pointer(), Description: input.Description.Pointer(), URL: input.URL.Pointer(),
			Transport: transportPointer, OAuthServerURL: input.OAuthServerURL.Pointer(),
			ClientID: input.ClientID.Pointer(), Scopes: input.Scopes.Pointer(), HeadersHelper: input.HeadersHelper.Pointer(),
		})
		if err != nil {
			writeAgentCompatError(w, workspaceStatus(err), err)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": server.ID, "action": "updated"}); err != nil {
			writeDomainEventError(w, "MCP server update", err)
			return
		}
		projected, err := compatMCPServerProjection(store, server, userID)
		if err != nil {
			writeAgentCompatError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, projected)
	case http.MethodDelete:
		assignments, err := store.ListMCPAssignments(serverID, userID)
		if err != nil {
			writeAgentCompatError(w, workspaceStatus(err), err)
			return
		}
		if err := store.DeleteMCPServer(serverID, userID); err != nil {
			writeAgentCompatError(w, workspaceStatus(err), err)
			return
		}
		if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": serverID, "action": "deleted"}); err != nil {
			writeDomainEventError(w, "MCP server delete", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mcp_server_id": serverID, "assignments_deleted": len(assignments)})
	default:
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func (s *Server) handleMCPAttachSelectedCompatibility(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID, serverID string) {
	if _, found, err := store.GetMCPServer(serverID, userID); err != nil || !found {
		if err == nil {
			err = fmt.Errorf("MCP server %s not found", serverID)
		}
		writeAgentCompatError(w, http.StatusNotFound, err)
		return
	}
	var input struct {
		AgentNames []string `json:"agent_names"`
	}
	if err := decodeAgentCompatJSON(r, &input); err != nil {
		writeAgentCompatError(w, http.StatusBadRequest, err)
		return
	}
	attached := make([]string, 0, len(input.AgentNames))
	skipped := make([]string, 0)
	seen := make(map[string]bool)
	for _, rawName := range input.AgentNames {
		name := normalizeBundledAgentName(rawName)
		if seen[name] {
			continue
		}
		seen[name] = true
		agent, err := s.ensureAgentProfile(store, userID, name)
		if err != nil {
			if errors.Is(err, errAgentProfileNotFound) {
				skipped = append(skipped, name)
				continue
			}
			writeAgentProfileLookupError(w, err)
			return
		}
		if _, err := store.AssignMCPServerToAgent(workspace.MCPAssignmentInput{
			ID: uuid.NewString(), MCPServerID: serverID, UserID: userID, AgentName: agent.Name,
		}); err != nil {
			writeAgentCompatError(w, workspaceStatus(err), err)
			return
		}
		attached = append(attached, agent.Name)
	}
	if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": serverID, "action": "assigned", "agents": attached}); err != nil {
		writeDomainEventError(w, "MCP assignment create", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcp_server_id": serverID, "attached": attached, "skipped": skipped})
}

func (s *Server) handleMCPAttachAllCompatibility(w http.ResponseWriter, store *workspace.Store, userID, serverID string) {
	if _, found, err := store.GetMCPServer(serverID, userID); err != nil || !found {
		if err == nil {
			err = fmt.Errorf("MCP server %s not found", serverID)
		}
		writeAgentCompatError(w, http.StatusNotFound, err)
		return
	}
	names := make([]string, 0)
	seen := make(map[string]bool)
	for _, definition := range s.agentCatalog.Agents() {
		agent, err := s.ensureAgentProfile(store, userID, definition.Name)
		if err != nil {
			writeAgentProfileLookupError(w, err)
			return
		}
		if !seen[agent.Name] {
			names = append(names, agent.Name)
			seen[agent.Name] = true
		}
	}
	agents, err := store.ListAgents(userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	customNames := make([]string, 0)
	for _, agent := range agents {
		if !seen[agent.Name] {
			customNames = append(customNames, agent.Name)
			seen[agent.Name] = true
		}
	}
	sort.Strings(customNames)
	names = append(names, customNames...)
	for _, name := range names {
		if _, err := store.AssignMCPServerToAgent(workspace.MCPAssignmentInput{
			ID: uuid.NewString(), MCPServerID: serverID, UserID: userID, AgentName: name,
		}); err != nil {
			writeAgentCompatError(w, workspaceStatus(err), err)
			return
		}
	}
	if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": serverID, "action": "assigned_all", "assignment_count": len(names)}); err != nil {
		writeDomainEventError(w, "MCP assign all", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcp_server_id": serverID, "attached": names, "skipped": []string{}})
}

func (s *Server) handleMCPDetachAgentCompatibility(w http.ResponseWriter, store *workspace.Store, userID, serverID, agentName string) {
	agentName = normalizeBundledAgentName(agentName)
	if _, found, err := store.GetMCPServer(serverID, userID); err != nil || !found {
		if err == nil {
			err = fmt.Errorf("MCP server %s not found", serverID)
		}
		writeAgentCompatError(w, http.StatusNotFound, err)
		return
	}
	if err := store.UnassignMCPServerFromAgent(userID, agentName, serverID); err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": serverID, "agent_name": agentName, "action": "detached"}); err != nil {
		writeDomainEventError(w, "MCP assignment detach", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcp_server_id": serverID, "agent_name": agentName, "success": true})
}

func (s *Server) handleMCPDetachAllCompatibility(w http.ResponseWriter, store *workspace.Store, userID, serverID string) {
	if _, found, err := store.GetMCPServer(serverID, userID); err != nil || !found {
		if err == nil {
			err = fmt.Errorf("MCP server %s not found", serverID)
		}
		writeAgentCompatError(w, http.StatusNotFound, err)
		return
	}
	count, err := store.DetachMCPServerFromAllAgents(serverID, userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	if _, err := s.publishUserEvent(userID, "connector_update", map[string]any{"connector_id": serverID, "action": "detached_all", "detached": count}); err != nil {
		writeDomainEventError(w, "MCP detach all", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mcp_server_id": serverID, "detached_count": count})
}

func (s *Server) handleMCPAttachmentCountsCompatibility(w http.ResponseWriter, r *http.Request, store *workspace.Store, userID string) {
	if r.Method != http.MethodGet {
		writeAgentCompatError(w, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		return
	}
	counts, err := store.MCPAttachmentCountsByAgent(userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	servers, err := store.ListMCPServers(userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	counts["OPERON"] = len(bundledAgentConnectorIDs) + len(servers)
	agents, err := store.ListAgents(userID)
	if err != nil {
		writeAgentCompatError(w, workspaceStatus(err), err)
		return
	}
	for _, agent := range agents {
		if agent.Name == "OPERON" {
			for _, connectorID := range agent.ConnectorTombstones {
				if isBundledAgentConnector(connectorID) || compatContainsMCPServer(servers, connectorID) {
					counts["OPERON"]--
				}
			}
			continue
		}
		if !agent.Unrestricted {
			continue
		}
		available := len(bundledAgentConnectorIDs) + len(servers)
		for _, connectorID := range agent.ConnectorTombstones {
			if isBundledAgentConnector(connectorID) || compatContainsMCPServer(servers, connectorID) {
				available--
			}
		}
		counts[agent.Name] += available
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

func compatContainsMCPServer(servers []workspace.MCPServer, id string) bool {
	for _, server := range servers {
		if server.ID == id {
			return true
		}
	}
	return false
}

func compatMCPServerProjection(store *workspace.Store, server workspace.MCPServer, userID string) (map[string]any, error) {
	assignments, err := store.ListMCPAssignments(server.ID, userID)
	if err != nil {
		return nil, err
	}
	agents := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		agents = append(agents, assignment.AgentName)
	}
	_, connected, err := store.GetMCPOAuthStatus(server.ID, userID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id": server.ID, "name": server.Name, "description": compatNilIfEmpty(server.Description),
		"url": server.URL, "transport": compatMCPTransportForAPI(server.Transport),
		"oauth_server_url": compatNilIfEmpty(server.OAuthServerURL), "client_id": compatNilIfEmpty(server.ClientID),
		"scopes": compatNilIfEmpty(server.Scopes), "headers_helper": compatNilIfEmpty(server.HeadersHelper),
		"headers_helper_status": nil, "is_connected": connected, "attached_agents": agents,
		"created_at": server.CreatedAt, "updated_at": server.UpdatedAt,
	}, nil
}

func compatNilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func compatMCPTransportForStore(value string) string {
	if value == "streamable_http" {
		return "streamable-http"
	}
	return value
}

func compatMCPTransportForAPI(value string) string {
	if value == "streamable-http" {
		return "streamable_http"
	}
	return value
}

func validateCompatMCPServerInput(ctx context.Context, name, rawURL, transport, oauthServerURL string) error {
	if !compatMCPServerNamePattern.MatchString(name) {
		return errors.New("name must contain only lowercase letters, numbers, and hyphens")
	}
	if transport != "sse" && transport != "streamable_http" {
		return errors.New("transport must be sse or streamable_http")
	}
	if err := validatePublicMCPURL(ctx, rawURL); err != nil {
		return err
	}
	if strings.TrimSpace(oauthServerURL) != "" {
		if err := validatePublicMCPURL(ctx, oauthServerURL); err != nil {
			return fmt.Errorf("invalid OAuth server URL: %w", err)
		}
	}
	return nil
}

func validatePublicMCPURL(ctx context.Context, raw string) error {
	return mcpdirectory.ValidatePublicHTTPSURL(ctx, raw)
}
