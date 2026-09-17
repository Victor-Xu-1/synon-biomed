package server

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"synon-go/internal/synonlink"
	"time"
)

func (s *Server) executeRememberedApprovalTool(toolName string, input map[string]any) (any, error) {
	if err := s.validateRegisteredTool(toolName, input); err != nil {
		return nil, err
	}
	if s == nil || s.settingsStore == nil {
		return nil, errors.New("approval remembered decision store is not configured")
	}
	switch toolName {
	case "approval_remembered_list":
		return s.listRememberedApprovals(input)
	case "approval_remembered_revoke":
		return s.revokeRememberedApproval(input)
	default:
		return nil, fmt.Errorf("unsupported remembered approval tool: %s", toolName)
	}
}

func (s *Server) listRememberedApprovals(input map[string]any) (any, error) {
	sourceFilter := strings.ToLower(strings.TrimSpace(stringValue(input["source"])))
	if sourceFilter == "" {
		sourceFilter = "all"
	}
	if sourceFilter != "all" && sourceFilter != "agent-runtime" && sourceFilter != "synon-link" && sourceFilter != "synon-web" {
		return nil, fmt.Errorf("approval remembered source is unsupported: %s", sourceFilter)
	}
	decisions, err := s.loadRememberedApprovalDecisions()
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(decisions))
	keys := make([]string, 0, len(decisions))
	for key := range decisions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		decision := decisions[key]
		source := rememberedApprovalSource(decision)
		if sourceFilter != "all" && sourceFilter != source {
			continue
		}
		rows = append(rows, rememberedApprovalDecisionRow(key, source, decision))
	}
	return map[string]any{
		"total":     len(rows),
		"source":    sourceFilter,
		"decisions": rows,
	}, nil
}

func (s *Server) revokeRememberedApproval(input map[string]any) (any, error) {
	if s.approvalDecisionMu != nil {
		s.approvalDecisionMu.Lock()
		defer s.approvalDecisionMu.Unlock()
	}
	decisions, err := s.loadRememberedApprovalDecisions()
	if err != nil {
		return nil, err
	}
	key, err := resolveRememberedApprovalRevokeKey(decisions, input)
	if err != nil {
		return nil, err
	}
	decision, ok := decisions[key]
	if !ok {
		return nil, fmt.Errorf("remembered approval not found: %s", key)
	}
	delete(decisions, key)
	if _, err := s.settingsStore.Set(approvalRememberedSettingKey, rememberedApprovalDecisionsToSetting(decisions)); err != nil {
		return nil, err
	}
	return map[string]any{
		"revoked":   true,
		"key":       key,
		"remaining": len(decisions),
		"decision":  rememberedApprovalDecisionRow(key, rememberedApprovalSource(decision), decision),
	}, nil
}

func (s *Server) loadRememberedApprovalDecisions() (map[string]synonlink.RememberedApprovalDecision, error) {
	if s == nil || s.settingsStore == nil {
		return nil, errors.New("approval remembered decision store is not configured")
	}
	setting, ok, err := s.settingsStore.Get(approvalRememberedSettingKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return map[string]synonlink.RememberedApprovalDecision{}, nil
	}
	return rememberedApprovalDecisionsFromSetting(setting.Value), nil
}

func resolveRememberedApprovalRevokeKey(decisions map[string]synonlink.RememberedApprovalDecision, input map[string]any) (string, error) {
	key := strings.TrimSpace(stringValue(input["key"]))
	if key != "" {
		return key, nil
	}
	action := strings.TrimSpace(stringValue(input["action"]))
	if action == "" {
		return "", errors.New("approval_remembered_revoke requires key or action")
	}
	source := strings.ToLower(strings.TrimSpace(stringValue(input["source"])))
	userID := strings.TrimSpace(stringValue(input["userId"]))
	clientID := strings.TrimSpace(stringValue(input["clientId"]))
	if source == "agent-runtime" {
		userID = agentRuntimeRememberedApprovalUser
		clientID = agentRuntimeRememberedApprovalClient
	}
	matches := make([]string, 0, 1)
	for key, decision := range decisions {
		if decision.Action != action {
			continue
		}
		if source != "" && source != "all" && rememberedApprovalSource(decision) != source {
			continue
		}
		if userID != "" && decision.UserID != userID {
			continue
		}
		if clientID != "" && decision.ClientID != clientID {
			continue
		}
		matches = append(matches, key)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("remembered approval action not found: %s", action)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("remembered approval action is ambiguous; provide key, userId, or clientId")
	}
	return matches[0], nil
}

func rememberedApprovalSource(decision synonlink.RememberedApprovalDecision) string {
	if decision.UserID == agentRuntimeRememberedApprovalUser && decision.ClientID == agentRuntimeRememberedApprovalClient {
		return "agent-runtime"
	}
	if decision.ClientID == webApprovalRememberedClient {
		return "synon-web"
	}
	return "synon-link"
}

func rememberedApprovalDecisionRow(key string, source string, decision synonlink.RememberedApprovalDecision) map[string]any {
	row := map[string]any{
		"key":       key,
		"source":    source,
		"userId":    decision.UserID,
		"clientId":  decision.ClientID,
		"action":    decision.Action,
		"reason":    decision.Reason,
		"createdAt": decision.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if strings.TrimSpace(decision.DeviceID) != "" {
		row["deviceId"] = decision.DeviceID
	}
	return row
}
