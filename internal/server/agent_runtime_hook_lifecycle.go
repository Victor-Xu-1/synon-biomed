package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"synon-go/internal/agentruntime"
	"synon-go/internal/toolcontract"
	"time"
)

func (s *Server) runAgentRuntimeLifecycleHooks(ctx context.Context, eventName string, matchValue string, payload map[string]any) []agentRuntimeCommandHookResult {
	if s == nil {
		return nil
	}
	hooks := s.agentRuntimeHooksForEvent(eventName, matchValue)
	results := make([]agentRuntimeCommandHookResult, 0, len(hooks))
	for _, hook := range hooks {
		raw := hook.Raw
		if !boolValue(raw["enabled"], true) {
			continue
		}
		result := s.runAgentRuntimeHook(ctx, eventName, hook.Key, raw, payload)
		if result == nil {
			continue
		}
		results = append(results, *result)
		s.auditAgentRuntimeLifecycleHook(eventName, hook.Key, matchValue, payload, *result)
	}
	return results
}

func (s *Server) auditAgentRuntimeLifecycleHook(eventName string, hookKey string, matchValue string, payload map[string]any, result agentRuntimeCommandHookResult) {
	if s == nil || s.runtimeStore == nil {
		return
	}
	auditID := agentRuntimeLifecycleHookAuditID(eventName, hookKey, matchValue)
	value := map[string]any{
		"id":        auditID,
		"hook":      hookKey,
		"event":     eventName,
		"match":     matchValue,
		"input":     payload,
		"command":   result.Audit,
		"createdAt": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if result.Decision != "" {
		value["decision"] = result.Decision
	}
	if result.Reason != "" {
		value["reason"] = result.Reason
	}
	if result.AdditionalContext != "" {
		value["additionalContext"] = result.AdditionalContext
	}
	_, _ = s.runtimeStore.Set(agentRuntimeHookAuditNamespace, auditID, value)
}

func agentRuntimeLifecycleHookAuditID(eventName string, hookKey string, matchValue string) string {
	raw, _ := json.Marshal(map[string]any{
		"event": eventName,
		"hook":  hookKey,
		"match": matchValue,
		"time":  time.Now().UTC().UnixNano(),
	})
	sum := sha256.Sum256(raw)
	return "ar-hook-audit-" + hex.EncodeToString(sum[:])[:24]
}

func agentRuntimeToolHookPayload(eventName string, toolName string, call agentruntime.ToolCall, input map[string]any, response any, status string) map[string]any {
	payload := map[string]any{
		"hook_event_name": eventName,
		"tool_name":       toolName,
		"tool_input":      input,
		"tool_use_id":     call.ID,
		"tool_call_id":    call.ID,
	}
	if response != nil {
		payload["tool_response"] = response
	}
	if status != "" {
		payload["status"] = status
	}
	return payload
}

func agentRuntimeFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func agentRuntimeHookEventMatches(raw string, event string) bool {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch strings.ToLower(strings.TrimSpace(event)) {
	case "pretool", "pretooluse":
		return normalized == "pretool" || normalized == "pre_tool" || normalized == "pre-tool" || normalized == "pretooluse" || normalized == "pre_tool_use" || normalized == "pre-tool-use"
	case "posttool", "posttooluse":
		return normalized == "posttool" || normalized == "post_tool" || normalized == "post-tool" || normalized == "posttooluse" || normalized == "post_tool_use" || normalized == "post-tool-use"
	default:
		return normalized == strings.ToLower(strings.TrimSpace(event))
	}
}

func agentRuntimeHookToolMatches(pattern string, toolName string) bool {
	trimmedPattern := strings.TrimSpace(pattern)
	if trimmedPattern == "" || trimmedPattern == "*" {
		return true
	}
	normalized := strings.NewReplacer("|", ",", ";", ",").Replace(pattern)
	for index, item := range strings.Split(normalized, ",") {
		if index > 0 {
			item = strings.TrimLeft(item, " \t\r\n")
		}
		item, ok := toolcontract.NormalizeRuntimeName(item)
		if !ok {
			continue
		}
		if strings.EqualFold(item, toolName) {
			return true
		}
	}
	return false
}

func (s *Server) auditAgentRuntimePreToolHook(ctx context.Context, hookKey string, toolName string, call agentruntime.ToolCall, input map[string]any, result agentRuntimeCommandHookResult) {
	if s == nil || s.runtimeStore == nil {
		return
	}
	auditID := agentRuntimeHookAuditID(hookKey, toolName, call.ID, "pre")
	value := map[string]any{
		"id":         auditID,
		"hook":       hookKey,
		"event":      "preTool",
		"tool":       toolName,
		"toolCallId": call.ID,
		"input":      input,
		"command":    result.Audit,
		"createdAt":  time.Now().UTC().Format(time.RFC3339Nano),
	}
	if result.Decision != "" {
		value["decision"] = result.Decision
	}
	if result.Reason != "" {
		value["reason"] = result.Reason
	}
	if len(result.UpdatedInput) > 0 {
		value["updatedInput"] = result.UpdatedInput
	}
	_, _ = s.runtimeStore.Set(agentRuntimeHookAuditNamespace, auditID, value)
}

func (s *Server) auditAgentRuntimePostToolHooks(ctx context.Context, toolName string, call agentruntime.ToolCall, input map[string]any, status string, result any) {
	if s == nil {
		return
	}
	eventName := "PostToolUse"
	auditEvent := "postTool"
	if status == "failed" {
		eventName = "PostToolUseFailure"
		auditEvent = "postToolFailure"
	}
	hooks := s.agentRuntimeHooksForEvent(eventName, toolName)
	for _, hook := range hooks {
		raw := hook.Raw
		if !boolValue(raw["enabled"], true) {
			continue
		}
		if s.runtimeStore == nil {
			continue
		}
		auditID := agentRuntimeHookAuditID(hook.Key, toolName, call.ID, status)
		value := map[string]any{
			"id":         auditID,
			"hook":       hook.Key,
			"event":      auditEvent,
			"tool":       toolName,
			"toolCallId": call.ID,
			"status":     status,
			"input":      input,
			"result":     result,
			"createdAt":  time.Now().UTC().Format(time.RFC3339Nano),
		}
		payload := agentRuntimeToolHookPayload(eventName, toolName, call, input, result, status)
		if agentRuntimePostHookRunsAsync(raw) {
			value["async"] = true
			value["asyncStatus"] = "queued"
			_, _ = s.runtimeStore.Set(agentRuntimeHookAuditNamespace, auditID, value)
			go s.runAsyncAgentRuntimePostToolHook(context.WithoutCancel(ctx), eventName, auditID, hook, payload, value)
			continue
		}
		commandResult := s.runAgentRuntimeHook(ctx, eventName, hook.Key, raw, payload)
		if commandResult != nil {
			value["command"] = commandResult.Audit
		}
		_, _ = s.runtimeStore.Set(agentRuntimeHookAuditNamespace, auditID, value)
	}
}

func agentRuntimePostHookRunsAsync(raw map[string]any) bool {
	if boolValue(raw["async"], false) || boolValue(raw["runInBackground"], false) || boolValue(raw["run_in_background"], false) {
		return true
	}
	if value, ok := raw["blocking"]; ok {
		return !boolValue(value, true)
	}
	return false
}

func (s *Server) runAsyncAgentRuntimePostToolHook(ctx context.Context, eventName string, auditID string, hook agentRuntimeHookConfig, payload map[string]any, queued map[string]any) {
	if s == nil || s.runtimeStore == nil {
		return
	}
	value := mergeAgentRuntimeToolInput(queued, map[string]any{
		"async":       true,
		"asyncStatus": "completed",
		"completedAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
	commandResult := s.runAgentRuntimeHook(ctx, eventName, hook.Key, hook.Raw, payload)
	if commandResult != nil {
		value["command"] = commandResult.Audit
		if exitCode := numberValue(commandResult.Audit["exitCode"]); exitCode != 0 {
			value["asyncStatus"] = "failed"
		}
	}
	_, _ = s.runtimeStore.Set(agentRuntimeHookAuditNamespace, auditID, value)
	s.recordAgentRuntimeHookRewake(auditID, value)
}

func (s *Server) recordAgentRuntimeHookRewake(auditID string, audit map[string]any) {
	if s == nil || s.runtimeStore == nil {
		return
	}
	status := strings.TrimSpace(stringValue(audit["asyncStatus"]))
	if status == "" {
		status = strings.TrimSpace(stringValue(audit["status"]))
	}
	rewakeID := agentRuntimeHookRewakeID(auditID)
	value := map[string]any{
		"id":         rewakeID,
		"auditId":    auditID,
		"hook":       stringValue(audit["hook"]),
		"event":      stringValue(audit["event"]),
		"tool":       stringValue(audit["tool"]),
		"toolCallId": stringValue(audit["toolCallId"]),
		"status":     status,
		"createdAt":  time.Now().UTC().Format(time.RFC3339Nano),
		"reason":     "async_hook_completed",
	}
	if command := mapValue(audit["command"]); len(command) > 0 {
		value["exitCode"] = numberValue(command["exitCode"])
	}
	_, _ = s.runtimeStore.Set(agentRuntimeHookRewakeNamespace, rewakeID, value)
}

func agentRuntimeHookRewakeID(auditID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(auditID)))
	return "ar-hook-rewake-" + hex.EncodeToString(sum[:])[:24]
}

func agentRuntimeHookAuditID(hookKey string, toolName string, toolCallID string, status string) string {
	raw, _ := json.Marshal(map[string]any{
		"hook":       hookKey,
		"tool":       toolName,
		"toolCallId": toolCallID,
		"status":     status,
	})
	sum := sha256.Sum256(raw)
	return "ar-hook-audit-" + hex.EncodeToString(sum[:])[:24]
}
