package server

import (
	"net/http"
	"strings"

	workspace "synon-go/internal/persistence/workspace"
)

// webConversationComposerCapabilities returns the exact capability identities
// that the conversation may select for a single turn. It deliberately shares
// the same assistant and connector authority used by send validation instead
// of maintaining a second frontend-only catalog.
func (s *Server) webConversationComposerCapabilities(
	userID string,
	frame workspace.CompatibilityFrame,
) (map[string]any, error) {
	record, err := s.webConversationAssistantRecord(userID, frame.ID, frame.AgentName)
	if err != nil {
		return nil, err
	}
	configured, constrained, err := s.webAssistantConfiguredMCPSelection(userID, frame.ID)
	if err != nil {
		return nil, err
	}
	return s.webAssistantComposerCapabilities(userID, record, configured, constrained)
}

// webAssistantComposerCapabilities is the single projection for both a new
// conversation draft and an existing conversation. Existing conversations may
// add a configuration fence; drafts expose the full assistant-authorized set.
func (s *Server) webAssistantComposerCapabilities(
	userID string,
	record webAssistantRecord,
	configured []string,
	constrained bool,
) (map[string]any, error) {
	runtimeAgent, err := s.webAssistantRuntimeAgentForRecord(userID, record)
	if err != nil {
		return nil, err
	}
	agent, agentFound, err := s.workspaceStore.GetAgent(userID, runtimeAgent)
	if err != nil {
		return nil, err
	}
	connectors, _, err := s.workspaceMCPRuntimeConnectors(userID, runtimeAgent, agent, agentFound)
	if err != nil {
		return nil, err
	}
	configuredSet := normalizedSkillNameSet(configured)

	statuses := make([]map[string]any, 0, len(connectors))
	serverIDs := make([]string, 0, len(connectors))
	serverNames := make([]string, 0, len(connectors))
	seen := make(map[string]struct{}, len(connectors))
	for _, connector := range connectors {
		id := strings.TrimSpace(connector.ID)
		if id == "" || !connector.Enabled {
			continue
		}
		key := strings.ToLower(id)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		if constrained {
			if _, selected := configuredSet[key]; !selected {
				continue
			}
		}
		seen[key] = struct{}{}
		name := strings.TrimSpace(connector.Name)
		if name == "" {
			name = id
		}
		statuses = append(statuses, map[string]any{
			"id": id, "name": name, "status": "loaded",
		})
		serverIDs = append(serverIDs, id)
		serverNames = append(serverNames, name)
	}

	return map[string]any{
		"skills":         append([]string(nil), s.webAssistantAllowedSkillNames(record)...),
		"mcp_server_ids": serverIDs,
		"mcp_servers":    serverNames,
		"mcp_statuses":   statuses,
	}, nil
}

func (s *Server) handleWebAssistantComposerCapabilities(
	w http.ResponseWriter,
	r *http.Request,
	userID string,
	record webAssistantRecord,
) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	capabilities, err := s.webAssistantComposerCapabilities(userID, record, nil, false)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	writePrivateRevalidatedWorkspaceJSON(w, r, userID, capabilities)
}

func (s *Server) handleWebConversationComposerCapabilities(
	w http.ResponseWriter,
	r *http.Request,
	frame workspace.CompatibilityFrame,
) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWorkspaceJSON(w, http.StatusMethodNotAllowed, map[string]any{"message": "method not allowed"})
		return
	}
	userID := strings.TrimSpace(r.Header.Get("X-Synon-User-Id"))
	capabilities, err := s.webConversationComposerCapabilities(userID, frame)
	if err != nil {
		writeWebConversationError(w, err)
		return
	}
	writePrivateRevalidatedWorkspaceJSON(w, r, userID, capabilities)
}
