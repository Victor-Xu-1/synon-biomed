package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"synon-go/internal/agentruntime"
	"synon-go/internal/synonlink"
	"synon-go/internal/tools/mcpstdio"
	"time"
)

func (s *Server) agentRuntimePermissionResult(toolName string, call agentruntime.ToolCall, input map[string]any) map[string]any {
	return s.agentRuntimePermissionResultForSourceWithContext(context.Background(), toolName, call, input, "agent-runtime")
}

func (s *Server) agentRuntimePermissionResultForSource(toolName string, call agentruntime.ToolCall, input map[string]any, source string) map[string]any {
	return s.agentRuntimePermissionResultForSourceWithContext(context.Background(), toolName, call, input, source)
}

func (s *Server) agentRuntimePermissionResultForSourceWithContext(ctx context.Context, toolName string, call agentruntime.ToolCall, input map[string]any, source string) map[string]any {
	return s.agentRuntimePermissionResultForSessionAndSourceWithContext(ctx, "", toolName, call, input, source)
}

func (s *Server) agentRuntimePermissionResultForSession(sessionID string, toolName string, call agentruntime.ToolCall, input map[string]any) map[string]any {
	return s.agentRuntimePermissionResultForSessionWithContext(context.Background(), sessionID, toolName, call, input)
}

func (s *Server) agentRuntimePermissionResultForSessionWithContext(ctx context.Context, sessionID string, toolName string, call agentruntime.ToolCall, input map[string]any) map[string]any {
	return s.agentRuntimePermissionResultForSessionAndSourceWithContext(ctx, sessionID, toolName, call, input, "agent-runtime")
}

func (s *Server) agentRuntimePermissionResultForSessionAndSource(sessionID string, toolName string, call agentruntime.ToolCall, input map[string]any, source string) map[string]any {
	return s.agentRuntimePermissionResultForSessionAndSourceWithContext(context.Background(), sessionID, toolName, call, input, source)
}

func (s *Server) agentRuntimePermissionResultForSessionAndSourceWithContext(ctx context.Context, sessionID string, toolName string, call agentruntime.ToolCall, input map[string]any, source string, approvalMetadata ...map[string]any) map[string]any {
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		approvalMetadata = append(approvalMetadata, map[string]any{"sessionId": sessionID})
	}
	defaults := s.agentRuntimeApprovalDefaults()
	activeMode, err := s.agentRuntimeConversationApprovalMode(sessionID, defaults.Mode)
	if err != nil {
		return map[string]any{
			"ok": false, "error": "unable to resolve conversation permission policy", "tool": toolName, "decision": "denied",
		}
	}
	if policy, ok := s.workspaceMCPServerPermissionPolicyWithContext(ctx, sessionID, toolName, input); ok {
		return s.agentRuntimeMCPServerPermissionResult(policy, activeMode, toolName, call, input, source, approvalMetadata...)
	}
	if policy, ok := s.mcpServerPermissionPolicy(toolName, input); ok {
		return s.agentRuntimeMCPServerPermissionResult(policy, activeMode, toolName, call, input, source, approvalMetadata...)
	}
	if strings.EqualFold(strings.TrimSpace(activeMode), "deny") && agentRuntimeToolNeedsApproval(toolName) {
		return map[string]any{
			"ok": false, "error": fmt.Sprintf("permission denied by conversation policy for tool %s", toolName),
			"tool": toolName, "decision": "denied", "policySource": "conversation",
		}
	}
	if strings.EqualFold(strings.TrimSpace(toolName), manageEnvironmentsToolName) &&
		strings.EqualFold(strings.TrimSpace(stringValue(input["mode"])), "list") {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(toolName), managePackagesToolName) &&
		strings.EqualFold(strings.TrimSpace(stringValue(input["mode"])), "list") {
		return nil
	}
	if strings.TrimSpace(toolName) == computeDetailsToolName && strings.TrimSpace(stringValue(input["mode"])) == "read" {
		return nil
	}
	if strings.TrimSpace(toolName) == submitComputeJobToolName && publicComputeProviderName(stringValue(input["provider"])) == "modal" {
		// BYOC submission has its own tier card with the exact image, hardware,
		// timeout, volumes, and egress disclosure. A generic tool approval here
		// would create a duplicate competing decision before that authoritative
		// card.
		return nil
	}
	if strings.TrimSpace(toolName) == computeProviderToolName &&
		s.agentComputeProviderSessionActive(strings.TrimSpace(sessionID), strings.TrimSpace(stringValue(input["provider"]))) {
		return nil
	}
	if !agentRuntimeToolNeedsApproval(toolName) {
		return nil
	}
	if activeMode == "" {
		// Preserve the non-conversation direct-gateway default. Web conversations
		// always carry an explicit three-tier mode.
		activeMode = "allow"
	}
	defaults.Mode = activeMode
	decision, err := webPermissionDecision(activeMode, agentRuntimeSmartApprovalRisk(toolName, input))
	if err != nil {
		return map[string]any{
			"ok": false, "error": err.Error(), "tool": toolName, "decision": "denied",
		}
	}
	// A remembered allow can satisfy only a mode that would otherwise ask.
	// It must never bypass an explicit deny, while Smart routine work and Full
	// access are already admitted directly by the active conversation policy.
	if decision == "ask" && defaults.RememberDecisions {
		if _, ok := s.agentRuntimeRememberedApproval(toolName, input); ok {
			return nil
		}
	}
	switch decision {
	case "allow":
		return nil
	case "deny":
		return map[string]any{
			"ok":       false,
			"error":    fmt.Sprintf("permission denied by approval defaults for tool %s", toolName),
			"tool":     toolName,
			"decision": "denied",
		}
	case "ask":
		metadata := map[string]any{}
		for _, values := range approvalMetadata {
			for key, value := range values {
				metadata[key] = value
			}
		}
		if strings.TrimSpace(sessionID) != "" {
			metadata["sessionId"] = strings.TrimSpace(sessionID)
		}
		return s.queueAgentRuntimeApprovalForSourceWithMetadata(toolName, call, input, strings.ToLower(strings.TrimSpace(defaults.Mode)), defaults, source, metadata)
	default:
		return nil
	}
}

// agentRuntimeConversationApprovalMode applies composer authority only to a
// compatibility/Web conversation. IM, task-runner, and direct runtime sessions
// do not own Web assistant metadata and must retain their existing policy
// defaults instead of being rejected while looking for a nonexistent composer.
func (s *Server) agentRuntimeConversationApprovalMode(sessionID, fallback string) (string, error) {
	frameID := strings.TrimSpace(sessionID)
	mode := strings.TrimSpace(fallback)
	if frameID == "" {
		return mode, nil
	}
	if _, found, err := s.workspaceStore.GetCompatibilityFrame(frameID); err != nil {
		if strings.EqualFold(strings.TrimSpace(err.Error()), "workspace store is closed") {
			return mode, nil
		}
		return "", err
	} else if !found {
		return mode, nil
	}
	selected, err := s.webSessionApprovalMode(frameID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(selected) != "" {
		mode = selected
	}
	return mode, nil
}

func (s *Server) queueAgentRuntimeApproval(toolName string, call agentruntime.ToolCall, input map[string]any, decision string, defaults synonlink.PolicyDefaults) map[string]any {
	return s.queueAgentRuntimeApprovalForSource(toolName, call, input, decision, defaults, "agent-runtime")
}

func (s *Server) queueAgentRuntimeApprovalForSource(toolName string, call agentruntime.ToolCall, input map[string]any, decision string, defaults synonlink.PolicyDefaults, source string) map[string]any {
	return s.queueAgentRuntimeApprovalForSourceWithMetadata(toolName, call, input, decision, defaults, source, nil)
}

func (s *Server) queueAgentRuntimeApprovalForSourceWithMetadata(toolName string, call agentruntime.ToolCall, input map[string]any, decision string, defaults synonlink.PolicyDefaults, source string, metadata map[string]any) map[string]any {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "agent-runtime"
	}
	approvalID := agentRuntimeApprovalID(toolName, call, input)
	value := map[string]any{
		"id":             approvalID,
		"status":         "pending",
		"decision":       decision,
		"tool":           toolName,
		"toolCallId":     call.ID,
		"input":          input,
		"requireReason":  defaults.RequireReason,
		"rememberable":   defaults.RememberDecisions,
		"createdAt":      time.Now().UTC().Format(time.RFC3339Nano),
		"approvalSource": source,
	}
	for key, item := range metadata {
		value[key] = item
	}
	if s != nil && s.runtimeStore != nil {
		if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value); err != nil {
			return map[string]any{
				"ok":       false,
				"error":    fmt.Sprintf("permission required before running tool %s; failed to persist approval request: %v", toolName, err),
				"tool":     toolName,
				"decision": "pending_approval",
			}
		}
	}
	response := map[string]any{
		"ok":           false,
		"error":        fmt.Sprintf("permission required before running tool %s", toolName),
		"tool":         toolName,
		"decision":     "pending_approval",
		"approvalId":   approvalID,
		"status":       "pending",
		"source":       source,
		"rememberable": defaults.RememberDecisions,
	}
	for key, item := range metadata {
		response[key] = item
	}
	return response
}

type mcpServerPermissionPolicy struct {
	Server            string
	Tool              string
	Mode              string
	RequireReason     bool
	RememberDecisions bool
}

func (s *Server) agentRuntimeMCPServerPermissionResult(policy mcpServerPermissionPolicy, activeMode string, toolName string, call agentruntime.ToolCall, input map[string]any, source string, approvalMetadata ...map[string]any) map[string]any {
	metadata := map[string]any{
		"policySource": "mcp-server",
		"mcpServer":    policy.Server,
		"mcpTool":      policy.Tool,
	}
	for _, values := range approvalMetadata {
		for key, value := range values {
			metadata[key] = value
		}
	}
	if strings.EqualFold(strings.TrimSpace(activeMode), "deny") {
		return map[string]any{
			"ok": false, "error": fmt.Sprintf("permission denied by conversation policy for %s/%s", policy.Server, policy.Tool),
			"tool": toolName, "decision": "denied", "policySource": "conversation",
		}
	}
	switch policy.Mode {
	case "allow":
		return nil
	case "deny":
		return map[string]any{
			"ok":           false,
			"error":        fmt.Sprintf("permission denied by MCP server policy for %s/%s", policy.Server, policy.Tool),
			"tool":         toolName,
			"decision":     "denied",
			"policySource": "mcp-server",
			"mcpServer":    policy.Server,
			"mcpTool":      policy.Tool,
		}
	case "ask", "confirm":
		decision, err := webPermissionDecision(activeMode, true)
		if err != nil {
			return map[string]any{
				"ok": false, "error": err.Error(), "tool": toolName, "decision": "denied",
			}
		}
		if decision == "allow" {
			return nil
		}
		if decision == "deny" {
			return map[string]any{
				"ok": false, "error": fmt.Sprintf("permission denied by conversation policy for %s/%s", policy.Server, policy.Tool),
				"tool": toolName, "decision": "denied", "policySource": "conversation",
			}
		}
		defaults := s.agentRuntimeApprovalDefaults()
		defaults.Mode = "ask"
		if policy.RequireReason {
			defaults.RequireReason = true
		}
		if policy.RememberDecisions {
			defaults.RememberDecisions = true
		}
		if defaults.RememberDecisions {
			if _, ok := s.agentRuntimeRememberedApproval(toolName, input); ok {
				return nil
			}
		}
		return s.queueAgentRuntimeApprovalForSourceWithMetadata(toolName, call, input, policy.Mode, defaults, source, metadata)
	default:
		return nil
	}
}

func (s *Server) mcpServerPermissionPolicy(toolName string, input map[string]any) (mcpServerPermissionPolicy, bool) {
	serverName, mcpToolName, ok := mcpPolicyTarget(toolName, input)
	if !ok {
		return mcpServerPermissionPolicy{}, false
	}
	configs, err := mcpstdio.LoadServers(s.fileRoot)
	if err != nil {
		return mcpServerPermissionPolicy{}, false
	}
	config, canonical, ok := mcpServerConfigByName(configs, serverName)
	if !ok {
		return mcpServerPermissionPolicy{}, false
	}
	mode, ok := mcpServerPolicyMode(config)
	if !ok {
		return mcpServerPermissionPolicy{}, false
	}
	return mcpServerPermissionPolicy{
		Server:            canonical,
		Tool:              mcpToolName,
		Mode:              mode,
		RequireReason:     config.RequireReason || boolValue(config.Permissions["requireReason"], false),
		RememberDecisions: config.RememberDecisions || boolValue(config.Permissions["rememberDecisions"], false),
	}, true
}

func mcpPolicyTarget(toolName string, input map[string]any) (string, string, bool) {
	switch {
	case strings.TrimSpace(toolName) == "MCPTool":
		serverName := strings.TrimSpace(firstNonEmpty(stringValue(input["server"]), stringValue(input["serverName"])))
		mcpToolName := strings.TrimSpace(firstNonEmpty(stringValue(input["toolName"]), stringValue(input["tool"])))
		if serverName == "" || mcpToolName == "" {
			return "", "", false
		}
		return serverName, mcpToolName, true
	case strings.HasPrefix(strings.TrimSpace(toolName), "mcp__"):
		parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(toolName), "mcp__"), "__", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return "", "", false
		}
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

func mcpServerConfigByName(configs map[string]mcpstdio.ServerConfig, name string) (mcpstdio.ServerConfig, string, bool) {
	if config, ok := configs[name]; ok {
		return config, name, true
	}
	normalized := mcpstdio.NormalizeName(name)
	for serverName, config := range configs {
		if mcpstdio.NormalizeName(serverName) == normalized {
			return config, serverName, true
		}
	}
	return mcpstdio.ServerConfig{}, "", false
}

func mcpServerPolicyMode(config mcpstdio.ServerConfig) (string, bool) {
	raw := strings.TrimSpace(firstNonEmpty(
		config.PermissionPolicy,
		config.ApprovalMode,
		stringValue(config.Permissions["permissionPolicy"]),
		stringValue(config.Permissions["approvalMode"]),
		stringValue(config.Permissions["mode"]),
		stringValue(config.Permissions["policy"]),
	))
	if raw == "" && (config.RequireApproval || boolValue(config.Permissions["requireApproval"], false)) {
		raw = "confirm"
	}
	switch strings.ToLower(strings.ReplaceAll(raw, "-", "_")) {
	case "allow", "allowed", "trusted", "trust":
		return "allow", true
	case "deny", "denied", "block", "blocked", "forbid", "forbidden":
		return "deny", true
	case "ask", "prompt":
		return "ask", true
	case "confirm", "require_approval", "approval_required", "manual":
		return "confirm", true
	default:
		return "", false
	}
}

func agentRuntimeApprovalID(toolName string, call agentruntime.ToolCall, input map[string]any) string {
	raw, _ := json.Marshal(map[string]any{
		"tool":       toolName,
		"toolCallId": call.ID,
		"input":      input,
	})
	sum := sha256.Sum256(raw)
	return "ar-approval-" + hex.EncodeToString(sum[:])[:24]
}

func (s *Server) agentRuntimeRememberedApproval(toolName string, input map[string]any) (synonlink.RememberedApprovalDecision, bool) {
	if s == nil || s.settingsStore == nil {
		return synonlink.RememberedApprovalDecision{}, false
	}
	setting, ok, err := s.settingsStore.Get(approvalRememberedSettingKey)
	if err != nil || !ok {
		return synonlink.RememberedApprovalDecision{}, false
	}
	action := agentRuntimeRememberedApprovalAction(toolName, input)
	decisions := rememberedApprovalDecisionsFromSetting(setting.Value)
	decision, ok := decisions[synonlink.RememberedApprovalKey(agentRuntimeRememberedApprovalUser, agentRuntimeRememberedApprovalClient, action)]
	return decision, ok
}

func (s *Server) rememberAgentRuntimeApproval(toolName string, input map[string]any, reason string, createdAt time.Time) (synonlink.RememberedApprovalDecision, error) {
	if s == nil || s.settingsStore == nil {
		return synonlink.RememberedApprovalDecision{}, errors.New("settings store is unavailable")
	}
	if s.approvalDecisionMu != nil {
		s.approvalDecisionMu.Lock()
		defer s.approvalDecisionMu.Unlock()
	}
	action := agentRuntimeRememberedApprovalAction(toolName, input)
	decision := synonlink.RememberedApprovalDecision{
		UserID:    agentRuntimeRememberedApprovalUser,
		ClientID:  agentRuntimeRememberedApprovalClient,
		Action:    action,
		Reason:    reason,
		CreatedAt: createdAt.UTC(),
	}
	setting, ok, err := s.settingsStore.Get(approvalRememberedSettingKey)
	if err != nil {
		return synonlink.RememberedApprovalDecision{}, err
	}
	decisions := map[string]synonlink.RememberedApprovalDecision{}
	if ok {
		decisions = rememberedApprovalDecisionsFromSetting(setting.Value)
	}
	decisions[synonlink.RememberedApprovalKey(decision.UserID, decision.ClientID, decision.Action)] = decision
	if _, err := s.settingsStore.Set(approvalRememberedSettingKey, rememberedApprovalDecisionsToSetting(decisions)); err != nil {
		return synonlink.RememberedApprovalDecision{}, err
	}
	return decision, nil
}

func agentRuntimeRememberedApprovalAction(toolName string, input map[string]any) string {
	normalizedTool := strings.TrimSpace(toolName)
	raw, _ := json.Marshal(map[string]any{
		"tool":  normalizedTool,
		"input": input,
	})
	sum := sha256.Sum256(raw)
	return "agent-runtime:" + normalizedTool + ":" + hex.EncodeToString(sum[:])[:24]
}

func (s *Server) agentRuntimeApprovalDefaults() synonlink.PolicyDefaults {
	if s == nil || s.settingsStore == nil {
		return synonlink.PolicyDefaults{}
	}
	setting, ok, err := s.settingsStore.Get(approvalDefaultsSettingKey)
	if err != nil || !ok {
		return synonlink.PolicyDefaults{}
	}
	defaults, err := synonlink.NormalizePolicyDefaults(setting.Value)
	if err != nil {
		return synonlink.PolicyDefaults{}
	}
	return defaults
}

func agentRuntimeToolNeedsApproval(toolName string) bool {
	name := strings.TrimSpace(toolName)
	if strings.HasPrefix(name, "mcp__") {
		return true
	}
	switch name {
	case "edit_file", "file_write", "Write", "Edit", "Patch", "file_patch", "file_replace", "json_patch", "NotebookEdit",
		"file_delete", "file_move", "file_copy", "file_mkdir",
		"shell_exec", "bash", "Shell", "powershell", manageEnvironmentsToolName, managePackagesToolName,
		"python", "r", "repl",
		"MCPTool", "synon_link",
		computeDetailsToolName, sshComputeToolName, scpComputeToolName, submitComputeJobToolName,
		computeProviderToolName,
		cancelComputeJobToolName, setComputeConcurrencyToolName,
		requestHostAccessToolName, requestNetworkAccessToolName, deleteHostFilesToolName:
		return true
	default:
		return false
	}
}

// agentRuntimeSmartApprovalRisk separates the second permission tier from the
// first. Task-local edits and sandboxed execution are routine and may proceed
// automatically. Operations that expand authority, remove data, mutate shared
// software, reach remote compute, or cross an MCP/browser boundary remain
// explicit risk decisions in Smart mode.
func agentRuntimeSmartApprovalRisk(toolName string, input map[string]any) bool {
	name := strings.TrimSpace(toolName)
	if strings.HasPrefix(name, "mcp__") {
		return true
	}
	switch name {
	case "edit_file", "file_write", "Write", "Edit", "Patch", "file_patch", "file_replace", "json_patch", "NotebookEdit",
		"file_copy", "file_mkdir",
		"shell_exec", "bash", "Shell", "powershell",
		"python", "r", "repl":
		return false
	case "file_delete", "file_move", deleteHostFilesToolName,
		requestHostAccessToolName, requestNetworkAccessToolName,
		manageEnvironmentsToolName, managePackagesToolName,
		"MCPTool", "synon_link",
		sshComputeToolName, scpComputeToolName, submitComputeJobToolName,
		computeProviderToolName, cancelComputeJobToolName, setComputeConcurrencyToolName:
		return true
	case computeDetailsToolName:
		return !strings.EqualFold(strings.TrimSpace(stringValue(input["mode"])), "read")
	default:
		// New governed tools must be explicitly reviewed before Smart mode may
		// classify them as routine. Failing safe prevents a future approval-
		// required tool from silently inheriting automatic execution.
		return true
	}
}
