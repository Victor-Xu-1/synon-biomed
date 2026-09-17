package server

import (
	"strings"

	"synon-go/internal/agentruntime"
	workspace "synon-go/internal/persistence/workspace"
)

type bundledAgentCapabilities struct {
	Skills     []string
	Connectors []string
}

func capabilitiesFromAgent(agent agentruntime.AgentDefinition) (bundledAgentCapabilities, bool) {
	if strings.EqualFold(strings.TrimSpace(agent.Name), "OPERON") ||
		(len(agent.SkillNames) == 0 && len(agent.ConnectorIDs) == 0) {
		return bundledAgentCapabilities{}, false
	}
	return bundledAgentCapabilities{
		Skills: append([]string(nil), agent.SkillNames...), Connectors: append([]string(nil), agent.ConnectorIDs...),
	}, true
}

func (s *Server) bundledAgentCapabilityDefaults(name string) (bundledAgentCapabilities, bool) {
	if s == nil || s.agentCatalog == nil {
		return bundledAgentCapabilities{}, false
	}
	agent, found := s.agentCatalog.Agent(name)
	if !found {
		return bundledAgentCapabilities{}, false
	}
	return capabilitiesFromAgent(agent)
}

func (s *Server) bundledAgentDefaultSkillNames(name string) []string {
	capabilities, found := s.bundledAgentCapabilityDefaults(name)
	if !found {
		return nil
	}
	return capabilities.Skills
}

func (s *Server) bundledAgentDefaultConnectorIDs(name string) []string {
	capabilities, found := s.bundledAgentCapabilityDefaults(name)
	if !found {
		return nil
	}
	return capabilities.Connectors
}

func (s *Server) effectiveBundledAgentSkillNames(name string, stored, tombstones []string) []string {
	selected := append([]string(nil), stored...)
	if len(selected) == 0 && len(tombstones) == 0 {
		selected = s.bundledAgentDefaultSkillNames(name)
	}
	blocked := make(map[string]struct{}, len(tombstones))
	for _, value := range tombstones {
		if key := strings.ToLower(strings.TrimSpace(value)); key != "" {
			blocked[key] = struct{}{}
		}
	}
	result := make([]string, 0, len(selected))
	for _, value := range selected {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, excluded := blocked[strings.ToLower(value)]; excluded {
			continue
		}
		result = appendUniqueFolded(result, value)
	}
	return result
}

func (s *Server) bundledAgentConnectorIDsForProfile(userID, agentName string, profile workspace.Agent) ([]string, error) {
	ids := appendUniqueFolded(nil, s.bundledAgentDefaultConnectorIDs(agentName)...)
	if s != nil && s.workspaceStore != nil {
		attachments, err := s.workspaceStore.ListAgentConnectorAttachments(userID, agentName)
		if err != nil {
			return nil, err
		}
		for _, attachment := range attachments {
			ids = appendUniqueFolded(ids, attachment.ServerID)
		}
	}
	blocked := make(map[string]struct{}, len(profile.ConnectorTombstones))
	for _, value := range profile.ConnectorTombstones {
		if key := strings.ToLower(strings.TrimSpace(value)); key != "" {
			blocked[key] = struct{}{}
		}
	}
	filtered := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, excluded := blocked[strings.ToLower(strings.TrimSpace(id))]; excluded {
			continue
		}
		filtered = append(filtered, id)
	}
	return filtered, nil
}
