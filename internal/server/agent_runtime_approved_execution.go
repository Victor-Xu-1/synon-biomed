package server

import (
	"context"
	"fmt"
	"strings"
	"synon-go/internal/agentruntime"
)

func (s *Server) executeApprovedAgentRuntimeTool(
	ctx context.Context,
	sessionID, toolName string,
	call agentruntime.ToolCall,
	input map[string]any,
	auditExtra map[string]any,
) (any, map[string]any, string, error) {
	canonical, err := canonicalRuntimeToolName(toolName)
	if err != nil {
		return nil, input, "failed", err
	}
	receipt := s.executeExactToolGateway(
		ctx, "approved-deferred", sessionID, call.ID, canonical, input,
		exactServerToolGatewayOptions{ResumeAfterApproval: true, AuditExtra: auditExtra},
	)
	return receipt.Value, receipt.Input, receipt.Status, receipt.Err
}

func (s *Server) resolveApprovedAgentToolContext(ctx context.Context, sessionID string) *agentKernelContext {
	if identity := s.resolveAgentKernelContext(ctx, sessionID); identity != nil {
		return identity
	}
	if s == nil || s.workspaceStore == nil || ctx == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	access, found, err := s.workspaceStore.GetKernelFrameAccessContext(ctx, strings.TrimSpace(sessionID))
	if err != nil || !found {
		return nil
	}
	return &agentKernelContext{access: access, workspaceDir: strings.TrimSpace(access.ProjectPath)}
}

func (s *Server) applyAgentRuntimePreToolHooks(ctx context.Context, toolName string, call agentruntime.ToolCall, input map[string]any) (map[string]any, map[string]any) {
	if s == nil {
		return input, nil
	}
	hooks := s.agentRuntimeHooksForEvent("PreToolUse", toolName)
	updated := input
	for _, hook := range hooks {
		raw := hook.Raw
		if !boolValue(raw["enabled"], true) {
			continue
		}
		commandResult := s.runAgentRuntimeHook(ctx, "PreToolUse", hook.Key, raw, agentRuntimeToolHookPayload("PreToolUse", toolName, call, updated, nil, ""))
		if commandResult != nil {
			s.auditAgentRuntimePreToolHook(ctx, hook.Key, toolName, call, updated, *commandResult)
			if len(commandResult.UpdatedInput) > 0 {
				updated = mergeAgentRuntimeToolInput(updated, commandResult.UpdatedInput)
			}
			if commandResult.Decision == "deny" || commandResult.Decision == "block" {
				reason := strings.TrimSpace(commandResult.Reason)
				if reason == "" {
					reason = fmt.Sprintf("tool %s blocked by PreToolUse command hook %s", toolName, hook.Key)
				}
				return updated, map[string]any{
					"ok":         false,
					"error":      reason,
					"tool":       toolName,
					"toolCallId": call.ID,
					"decision":   "blocked_by_hook",
					"hook":       hook.Key,
					"event":      "preTool",
					"input":      updated,
					"command":    commandResult.Audit,
				}
			}
			if commandResult.Decision == "ask" {
				return updated, map[string]any{
					"ok":         false,
					"error":      fmt.Sprintf("tool %s requires confirmation from PreToolUse command hook %s", toolName, hook.Key),
					"tool":       toolName,
					"toolCallId": call.ID,
					"decision":   "ask_by_hook",
					"hook":       hook.Key,
					"event":      "preTool",
					"input":      updated,
					"command":    commandResult.Audit,
				}
			}
		}
		decision := strings.ToLower(strings.TrimSpace(stringValue(raw["decision"])))
		if decision == "" {
			decision = strings.ToLower(strings.TrimSpace(stringValue(raw["permissionDecision"])))
		}
		switch decision {
		case "block", "deny":
			reason := strings.TrimSpace(stringValue(raw["reason"]))
			if reason == "" {
				reason = strings.TrimSpace(stringValue(raw["message"]))
			}
			if reason == "" {
				reason = fmt.Sprintf("tool %s blocked by PreToolUse hook %s", toolName, hook.Key)
			}
			return updated, map[string]any{
				"ok":         false,
				"error":      reason,
				"tool":       toolName,
				"toolCallId": call.ID,
				"decision":   "blocked_by_hook",
				"hook":       hook.Key,
				"event":      "preTool",
				"input":      updated,
			}
		}
		if next := mapValue(raw["updatedInput"]); len(next) > 0 {
			updated = mergeAgentRuntimeToolInput(updated, next)
		}
	}
	return updated, nil
}
