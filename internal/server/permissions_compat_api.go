package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"synon-go/internal/synonlink"
)

type compatibilityPermissionGrant struct {
	Kind        string `json:"kind"`
	Key         string `json:"key"`
	Scope       string `json:"scope"`
	Tier        string `json:"tier"`
	ProjectID   string `json:"projectId,omitempty"`
	RootFrameID string `json:"rootFrameId,omitempty"`
}

func (s *Server) handleApprovalGrantsCompatibility(w http.ResponseWriter, r *http.Request) {
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	if s.settingsStore == nil || s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "permission storage is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		grants, err := s.loadCompatibilityPermissionGrants(userID)
		if err != nil {
			writeV11Detail(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"grants": grants})
	case http.MethodDelete:
		var grant compatibilityPermissionGrant
		if err := decodeWorkspaceJSON(r, &grant); err != nil {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := validateCompatibilityPermissionGrant(grant); err != nil {
			writeV11Detail(w, http.StatusBadRequest, err.Error())
			return
		}
		removed, err := s.revokeCompatibilityPermissionGrant(userID, grant)
		if err != nil {
			writeV11Detail(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeWorkspaceJSON(w, http.StatusOK, map[string]any{"removed": removed})
	default:
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleApprovalGrantsBulkCompatibility(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeV11Detail(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	userID, ok := attachmentUserID(w, r)
	if !ok {
		return
	}
	if s.settingsStore == nil || s.workspaceStore == nil {
		writeV11Detail(w, http.StatusServiceUnavailable, "permission storage is not configured")
		return
	}
	var filter compatibilityPermissionGrant
	if err := decodeWorkspaceJSON(r, &filter); err != nil {
		writeV11Detail(w, http.StatusBadRequest, err.Error())
		return
	}
	filter.Kind, filter.Tier = strings.TrimSpace(filter.Kind), strings.TrimSpace(filter.Tier)
	if filter.Kind == "" && strings.TrimSpace(filter.ProjectID) == "" && strings.TrimSpace(filter.Scope) == "" {
		writeV11Detail(w, http.StatusBadRequest, "kind, projectId, or scope filter required")
		return
	}
	if filter.Tier != "" && filter.Tier != "allow" && filter.Tier != "deny" && filter.Tier != "ask" {
		writeV11Detail(w, http.StatusBadRequest, "tier must be allow, deny, or ask")
		return
	}
	grants, err := s.loadCompatibilityPermissionGrants(userID)
	if err != nil {
		writeV11Detail(w, http.StatusInternalServerError, err.Error())
		return
	}
	removed := 0
	for _, grant := range grants {
		if !permissionGrantMatchesFilter(grant, filter) {
			continue
		}
		count, err := s.revokeCompatibilityPermissionGrant(userID, grant)
		if err != nil {
			writeV11Detail(w, http.StatusInternalServerError, err.Error())
			return
		}
		removed += count
	}
	writeWorkspaceJSON(w, http.StatusOK, map[string]any{"removed": removed})
}

func (s *Server) loadCompatibilityPermissionGrants(userID string) ([]compatibilityPermissionGrant, error) {
	grants := make([]compatibilityPermissionGrant, 0)
	hostGrants, err := s.loadHostGrants(userID)
	if err != nil {
		return nil, err
	}
	for _, grant := range hostGrants {
		mode := "ro"
		if grant.Mode == "read_write" {
			mode = "rw"
		}
		grants = append(grants, compatibilityPermissionGrant{Kind: "host", Key: mode + ":" + grant.Path, Scope: "always", Tier: "allow"})
	}
	if userID == "local" {
		domains, err := s.loadAllowedDomains()
		if err != nil {
			return nil, err
		}
		for _, domain := range domains {
			grants = append(grants, compatibilityPermissionGrant{Kind: "network", Key: domain, Scope: "always", Tier: "allow"})
		}
	}
	servers, err := s.workspaceStore.ListMCPServers(userID)
	if err != nil {
		return nil, err
	}
	for _, server := range servers {
		serverGrants, err := s.workspaceStore.ListGlobalMCPToolGrants(server.ID, userID)
		if err != nil {
			return nil, err
		}
		for _, grant := range serverGrants {
			tier := "deny"
			if grant.Enabled {
				tier = "allow"
			}
			grants = append(grants, compatibilityPermissionGrant{Kind: "mcp_tool", Key: server.ID + "\x00" + grant.ToolName, Scope: "always", Tier: tier})
		}
	}
	connectorPolicies, err := s.workspaceStore.ListAllMCPConnectorToolPolicies(userID)
	if err != nil {
		return nil, err
	}
	for _, policy := range connectorPolicies {
		tier := "deny"
		if policy.Enabled {
			tier = "allow"
		}
		grants = append(grants, compatibilityPermissionGrant{Kind: "mcp_tool", Key: policy.ConnectorID + "\x00" + policy.ToolName, Scope: "always", Tier: tier})
	}
	decisions, err := s.loadRememberedApprovalDecisions()
	if err != nil {
		return nil, err
	}
	for _, decision := range decisions {
		if !rememberedApprovalVisibleToUser(decision, userID) {
			continue
		}
		grants = append(grants, projectRememberedPermissionGrant(decision))
	}
	policyGrants, err := s.workspaceStore.ListApprovalPolicyGrants(context.Background(), userID)
	if err != nil {
		return nil, err
	}
	for _, grant := range policyGrants {
		projectID, rootFrameID := "", ""
		switch grant.Scope {
		case "project":
			projectID = grant.ScopeTargetID
		case "conversation":
			rootFrameID = grant.ScopeTargetID
		}
		grants = append(grants, compatibilityPermissionGrant{
			Kind: grant.Kind, Key: grant.Key, Scope: grant.Scope, Tier: grant.Tier,
			ProjectID: projectID, RootFrameID: rootFrameID,
		})
	}
	grants = deduplicateCompatibilityPermissionGrants(grants)
	sort.Slice(grants, func(i, j int) bool {
		left, right := permissionGrantCoordinate(grants[i]), permissionGrantCoordinate(grants[j])
		return left < right
	})
	return grants, nil
}

func (s *Server) revokeCompatibilityPermissionGrant(userID string, requested compatibilityPermissionGrant) (int, error) {
	grants, err := s.loadCompatibilityPermissionGrants(userID)
	if err != nil {
		return 0, err
	}
	matched := false
	for _, grant := range grants {
		if permissionGrantCoordinate(grant) == permissionGrantCoordinate(requested) {
			matched = true
			break
		}
	}
	if !matched {
		return 0, nil
	}
	switch requested.Kind {
	case "host":
		separator := strings.Index(requested.Key, ":")
		if separator < 1 {
			return 0, errors.New("host grant key must be ro:<path> or rw:<path>")
		}
		path := filepath.Clean(requested.Key[separator+1:])
		removed, err := s.revokeHostGrant(userID, path)
		if err != nil || !removed {
			return boolCount(removed), err
		}
		_, eventErr := s.publishUserEvent(userID, "host_access_granted", map[string]any{"path": path, "action": "revoked"})
		return 1, eventErr
	case "network":
		removed, err := s.revokeAllowedDomainPermission(requested.Key)
		return boolCount(removed), err
	case "mcp_tool":
		serverID, toolName, ok := splitPermissionMCPKey(requested.Key)
		if !ok {
			return 0, errors.New("mcp_tool key must be <server>\\0<tool>")
		}
		removed := 0
		if deleted, err := s.workspaceStore.DeleteGlobalMCPToolGrant(serverID, userID, toolName); err != nil {
			return 0, err
		} else if deleted {
			removed++
		}
		if deleted, err := s.workspaceStore.SetMCPConnectorToolPolicy(userID, serverID, toolName, nil); err != nil {
			return removed, err
		} else if deleted {
			removed++
		}
		removedRemembered, err := s.revokeProjectedRememberedApproval(userID, requested)
		return removed + removedRemembered, err
	case "local_exec":
		targetID := ""
		switch requested.Scope {
		case "project":
			targetID = strings.TrimSpace(requested.ProjectID)
		case "conversation":
			targetID = strings.TrimSpace(requested.RootFrameID)
		}
		deleted, err := s.workspaceStore.DeleteApprovalPolicyGrant(context.Background(), userID,
			requested.Kind, requested.Key, requested.Scope, requested.Tier, targetID)
		if err != nil {
			return 0, err
		}
		removedRemembered, err := s.revokeProjectedRememberedApproval(userID, requested)
		return boolCount(deleted) + removedRemembered, err
	default:
		return s.revokeProjectedRememberedApproval(userID, requested)
	}
}

func (s *Server) revokeAllowedDomainPermission(domain string) (bool, error) {
	s.allowedDomainsMu.Lock()
	defer s.allowedDomainsMu.Unlock()
	previous, err := s.loadAllowedDomains()
	if err != nil {
		return false, err
	}
	next := make([]string, 0, len(previous))
	removed := false
	for _, item := range previous {
		if item == domain {
			removed = true
			continue
		}
		next = append(next, item)
	}
	if !removed {
		return false, nil
	}
	if _, err := s.settingsStore.Set(allowedDomainsSettingKey, next); err != nil {
		return false, err
	}
	if err := s.publishAllowedDomainsEvent("revoked", next); err != nil {
		_, rollbackErr := s.settingsStore.Set(allowedDomainsSettingKey, previous)
		if rollbackErr != nil {
			return false, fmt.Errorf("publish allowed-domain revoke: %v; rollback failed: %w", err, rollbackErr)
		}
		return false, err
	}
	return true, nil
}

func (s *Server) revokeProjectedRememberedApproval(userID string, requested compatibilityPermissionGrant) (int, error) {
	if s.approvalDecisionMu != nil {
		s.approvalDecisionMu.Lock()
		defer s.approvalDecisionMu.Unlock()
	}
	decisions, err := s.loadRememberedApprovalDecisions()
	if err != nil {
		return 0, err
	}
	removed := 0
	for key, decision := range decisions {
		if !rememberedApprovalVisibleToUser(decision, userID) {
			continue
		}
		if permissionGrantCoordinate(projectRememberedPermissionGrant(decision)) == permissionGrantCoordinate(requested) {
			delete(decisions, key)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	_, err = s.settingsStore.Set(approvalRememberedSettingKey, rememberedApprovalDecisionsToSetting(decisions))
	return removed, err
}

func rememberedApprovalVisibleToUser(decision synonlink.RememberedApprovalDecision, userID string) bool {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return false
	}
	if decision.UserID == userID {
		return true
	}
	return userID == "local" && decision.UserID == agentRuntimeRememberedApprovalUser
}

func projectRememberedPermissionGrant(decision synonlink.RememberedApprovalDecision) compatibilityPermissionGrant {
	action := strings.TrimSpace(decision.Action)
	if strings.HasPrefix(action, "agent-runtime:") {
		rest := strings.TrimPrefix(action, "agent-runtime:")
		tool, suffix, _ := strings.Cut(rest, ":")
		if strings.HasPrefix(tool, "mcp__") {
			parts := strings.SplitN(strings.TrimPrefix(tool, "mcp__"), "__", 2)
			if len(parts) == 2 {
				return compatibilityPermissionGrant{Kind: "mcp_tool", Key: parts[0] + "\x00" + parts[1], Scope: "always", Tier: "allow"}
			}
		}
		if tool == "MCPTool" || strings.EqualFold(tool, "mcp") {
			return compatibilityPermissionGrant{Kind: "mcp_tool", Key: "remembered\x00" + action, Scope: "always", Tier: "allow"}
		}
		return compatibilityPermissionGrant{Kind: "local_exec", Key: tool + "\x00" + suffix, Scope: "always", Tier: "allow"}
	}
	return compatibilityPermissionGrant{Kind: "local_exec", Key: "synon_link\x00" + action, Scope: "always", Tier: "allow"}
}

func validateCompatibilityPermissionGrant(grant compatibilityPermissionGrant) error {
	if strings.TrimSpace(grant.Kind) == "" || strings.TrimSpace(grant.Key) == "" {
		return errors.New("kind and key are required")
	}
	if grant.Scope != "always" && grant.Scope != "project" && grant.Scope != "conversation" {
		return errors.New("scope must be always, project, or conversation")
	}
	if grant.Tier != "allow" && grant.Tier != "deny" && grant.Tier != "ask" {
		return errors.New("tier must be allow, deny, or ask")
	}
	if grant.Kind == "local_exec" {
		switch grant.Scope {
		case "conversation":
			if strings.TrimSpace(grant.RootFrameID) == "" {
				return errors.New("conversation local_exec grant requires rootFrameId")
			}
		case "project":
			if strings.TrimSpace(grant.ProjectID) == "" {
				return errors.New("project local_exec grant requires projectId")
			}
		}
	}
	return nil
}

func permissionGrantMatchesFilter(grant, filter compatibilityPermissionGrant) bool {
	return (filter.Kind == "" || grant.Kind == filter.Kind) &&
		(filter.Tier == "" || grant.Tier == filter.Tier) &&
		(filter.Scope == "" || grant.Scope == filter.Scope) &&
		(filter.ProjectID == "" || grant.ProjectID == filter.ProjectID) &&
		(filter.RootFrameID == "" || grant.RootFrameID == filter.RootFrameID)
}

func deduplicateCompatibilityPermissionGrants(values []compatibilityPermissionGrant) []compatibilityPermissionGrant {
	seen := make(map[string]struct{}, len(values))
	out := make([]compatibilityPermissionGrant, 0, len(values))
	for _, value := range values {
		coordinate := permissionGrantCoordinate(value)
		if _, exists := seen[coordinate]; exists {
			continue
		}
		seen[coordinate] = struct{}{}
		out = append(out, value)
	}
	return out
}

func permissionGrantCoordinate(grant compatibilityPermissionGrant) string {
	return strings.Join([]string{grant.Kind, grant.Key, grant.Scope, grant.Tier, grant.ProjectID, grant.RootFrameID}, "\x1f")
}

func splitPermissionMCPKey(key string) (string, string, bool) {
	serverID, toolName, ok := strings.Cut(key, "\x00")
	return serverID, toolName, ok && strings.TrimSpace(serverID) != "" && strings.TrimSpace(toolName) != ""
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
