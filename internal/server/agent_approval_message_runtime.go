package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"synon-go/internal/agentruntime"
	taskruns "synon-go/internal/persistence/taskruns"
	"time"
)

type agentToolExecutionContextKey struct{}

func isAgentRuntimeApprovalMessage(to string, message map[string]any) bool {
	messageType := strings.ToLower(strings.TrimSpace(stringValue(message["type"])))
	switch messageType {
	case "agent_runtime_approval_response", "agent-runtime-approval-response", "tool_approval_response", "approval_response":
		return true
	default:
		return strings.HasPrefix(strings.TrimSpace(to), "ar-approval-")
	}
}

func (s *Server) resolveAgentRuntimeApprovalMessage(ctx context.Context, to string, message map[string]any) (any, error) {
	if ctx != nil && ctx.Value(agentToolExecutionContextKey{}) == true {
		return nil, errors.New("tool execution cannot resolve user approval requests")
	}
	if s == nil || s.runtimeStore == nil {
		return nil, errors.New("agent runtime approval store is not configured")
	}
	s.agentToolApprovalMu.Lock()
	locked := true
	defer func() {
		if locked {
			s.agentToolApprovalMu.Unlock()
		}
	}()
	approvalID := strings.TrimSpace(firstNonEmpty(
		stringValue(message["approvalId"]),
		stringValue(message["approval_id"]),
		stringValue(message["request_id"]),
	))
	if approvalID == "" && strings.HasPrefix(strings.TrimSpace(to), "ar-approval-") {
		approvalID = strings.TrimSpace(to)
	}
	if approvalID == "" {
		return nil, errors.New("agent runtime approval response requires approvalId or request_id")
	}
	entry, ok, err := s.runtimeStore.Get(agentRuntimeApprovalNamespace, approvalID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("agent runtime approval not found: %s", approvalID)
	}
	value := mapValue(entry.Value)
	if strings.TrimSpace(stringValue(value["status"])) != "pending" {
		return map[string]any{
			"success":    false,
			"approvalId": approvalID,
			"status":     stringValue(value["status"]),
			"message":    "Approval request is no longer pending.",
		}, nil
	}
	approvalSource := strings.TrimSpace(stringValue(value["approvalSource"]))
	if approvalSource == kernelArtifactDeleteApprovalSource && !kernelArtifactApprovalUserAuthorized(ctx) {
		return nil, errors.New("artifact deletion approval requires an authenticated user response")
	}
	approved, explicit := approvalMessageDecision(message)
	if !explicit {
		return nil, errors.New("agent runtime approval response requires approve=true/false or decision")
	}
	reason := strings.TrimSpace(firstNonEmpty(
		stringValue(message["reason"]),
		stringValue(message["approvalReason"]),
		stringValue(message["approval_reason"]),
		stringValue(message["message"]),
	))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	toolName, err := canonicalRuntimeToolName(stringValue(value["tool"]))
	if err != nil {
		return nil, fmt.Errorf("agent runtime approval %s is missing tool", approvalID)
	}
	value["tool"] = toolName
	if !approved {
		value["status"] = "denied"
		value["deniedAt"] = now
		value["reason"] = reason
		if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value); err != nil {
			return nil, err
		}
		return map[string]any{
			"success":    true,
			"approvalId": approvalID,
			"status":     "denied",
			"reason":     reason,
		}, nil
	}
	if boolValue(value["requireReason"], false) && reason == "" {
		return nil, errors.New("agent runtime approval requires reason")
	}
	input := mapValue(value["input"])
	call := agentruntime.ToolCall{
		ID:   strings.TrimSpace(stringValue(value["toolCallId"])),
		Name: toolName,
	}
	if call.ID == "" {
		call.ID = toolName
	}
	runnerOwned := false
	if s.workspaceStore != nil {
		runnerOwned, err = s.workspaceStore.HasFrameToolCallBatch(ctx, stringValue(value["sessionId"]), call.ID, toolName)
		if err != nil {
			return nil, err
		}
	}
	if runnerOwned || isKernelHostWaitOnlyApprovalSource(approvalSource) {
		value["status"] = "approved"
		value["approvedAt"] = now
		value["reason"] = reason
		remembered := false
		if (runnerOwned || approvalSource == "kernel-host-mcp") && approvalMessageRememberDecision(message) && boolValue(value["rememberable"], false) {
			decision, rememberErr := s.rememberAgentRuntimeApproval(toolName, input, reason, time.Now().UTC())
			if rememberErr != nil {
				value["rememberError"] = rememberErr.Error()
			} else {
				remembered = true
				value["rememberedApproval"] = map[string]any{
					"userId": decision.UserID, "clientId": decision.ClientID,
					"action": decision.Action, "reason": decision.Reason,
					"createdAt": decision.CreatedAt.UTC().Format(time.RFC3339Nano),
				}
			}
		}
		if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value); err != nil {
			return nil, err
		}
		return map[string]any{
			"success": true, "approvalId": approvalID, "status": "approved",
			"tool": toolName, "remembered": remembered,
		}, nil
	}
	value["status"] = "executing"
	value["approvedAt"] = now
	value["reason"] = reason
	if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value); err != nil {
		return nil, err
	}
	approvalAuditExtra := map[string]any{
		"approvalId": approvalID,
	}
	// The executing state already excludes duplicate decisions. Do not hold
	// the decision lock while a direct/non-runner tool performs slow work.
	s.agentToolApprovalMu.Unlock()
	locked = false
	if source := strings.TrimSpace(stringValue(value["approvalSource"])); source != "" {
		approvalAuditExtra["approvalSource"] = source
	}
	response, executedInput, status, err := s.executeApprovedAgentRuntimeTool(
		ctx, stringValue(value["sessionId"]), toolName, call, input, approvalAuditExtra,
	)
	value["executedInput"] = executedInput
	if status == "blocked" {
		value["status"] = "blocked"
		value["blockedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		value["result"] = response
		if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value); err != nil {
			return nil, err
		}
		return map[string]any{
			"success":    false,
			"approvalId": approvalID,
			"status":     "blocked",
			"tool":       toolName,
			"result":     response,
		}, nil
	}
	if err != nil {
		value["status"] = "failed"
		value["failedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		value["error"] = err.Error()
		_, _ = s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value)
		return nil, err
	}
	if status == "failed" {
		failure := agentruntime.ToolFailureEventMessage(response)
		value["status"] = "failed"
		value["failedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
		value["error"] = failure
		value["result"] = response
		if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value); err != nil {
			return nil, err
		}
		return map[string]any{
			"success": false, "approvalId": approvalID, "status": "failed",
			"tool": toolName, "result": response,
		}, nil
	}
	value["status"] = "completed"
	value["completedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	value["result"] = response
	remembered := false
	if approvalMessageRememberDecision(message) && boolValue(value["rememberable"], false) {
		decision, err := s.rememberAgentRuntimeApproval(toolName, executedInput, reason, time.Now().UTC())
		if err != nil {
			value["rememberError"] = err.Error()
		} else {
			remembered = true
			value["rememberedApproval"] = map[string]any{
				"userId":    decision.UserID,
				"clientId":  decision.ClientID,
				"action":    decision.Action,
				"reason":    decision.Reason,
				"createdAt": decision.CreatedAt.UTC().Format(time.RFC3339Nano),
			}
		}
	}
	if _, err := s.runtimeStore.Set(agentRuntimeApprovalNamespace, approvalID, value); err != nil {
		return nil, err
	}
	s.auditAgentRuntimePostToolHooks(ctx, toolName, call, input, "completed", response)
	return map[string]any{
		"success":    true,
		"approvalId": approvalID,
		"status":     "completed",
		"tool":       toolName,
		"result":     response,
		"input":      executedInput,
		"remembered": remembered,
	}, nil
}

func approvalMessageDecision(message map[string]any) (bool, bool) {
	if value, ok := message["approve"].(bool); ok {
		return value, true
	}
	if value, ok := message["approved"].(bool); ok {
		return value, true
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(message["decision"]))) {
	case "approve", "approved", "allow", "allowed":
		return true, true
	case "deny", "denied", "reject", "rejected", "block", "blocked":
		return false, true
	default:
		return false, false
	}
}

func approvalMessageRememberDecision(message map[string]any) bool {
	for _, key := range []string{"remember", "rememberDecision", "rememberApproval", "alwaysAllow", "permanent"} {
		if boolValue(message[key], false) {
			return true
		}
	}
	switch strings.ToLower(strings.TrimSpace(stringValue(message["remember"]))) {
	case "true", "yes", "y", "1", "always", "forever":
		return true
	default:
		return false
	}
}

func isTerminalTaskRunStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "failed", "cancelled", "verified":
		return true
	default:
		return false
	}
}

func isAgentDelegationTaskRun(run taskruns.Record) bool {
	switch strings.TrimSpace(stringValue(run.Orchestration["sourceTool"])) {
	case "Agent":
		return true
	default:
		return false
	}
}
