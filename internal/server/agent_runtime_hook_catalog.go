package server

import (
	"context"
	"fmt"
	"strings"
	"synon-go/internal/agentruntime"
)

func (s *Server) agentRuntimePreToolHookResult(toolName string, call agentruntime.ToolCall, input map[string]any) map[string]any {
	_, result := s.applyAgentRuntimePreToolHooks(context.Background(), toolName, call, input)
	return result
}

func (s *Server) agentRuntimeHooksForEvent(eventName string, toolName string) []agentRuntimeHookConfig {
	if s == nil {
		return nil
	}
	hooks := []agentRuntimeHookConfig{}
	if s.runtimeStore != nil {
		entries, err := s.runtimeStore.List(agentRuntimeHookNamespace)
		if err == nil {
			for _, entry := range entries {
				raw := mapValue(entry.Value)
				if len(raw) == 0 {
					continue
				}
				hooks = append(hooks, agentRuntimeSimplifiedHook(entry.Key, raw, eventName, toolName)...)
				hooks = append(hooks, agentRuntimeOriginalShapeHooks("runtime:"+entry.Key, raw, eventName, toolName)...)
			}
		}
	}
	if s.settingsStore != nil {
		for _, key := range []string{"hooks", configStoreKey("hooks")} {
			setting, ok, err := s.settingsStore.Get(key)
			if err != nil || !ok {
				continue
			}
			raw := mapValue(setting.Value)
			if len(raw) == 0 {
				continue
			}
			hooks = append(hooks, agentRuntimeOriginalShapeHooks("settings:"+key, raw, eventName, toolName)...)
		}
	}
	if s.agentRuntimePluginHooksEnabled() && !s.agentRuntimeManagedHooksOnly() && s.plugins != nil {
		for _, plugin := range s.plugins.List() {
			if len(plugin.HooksConfig) == 0 {
				continue
			}
			hooks = append(hooks, agentRuntimeOriginalShapeHooksWithContext(
				"plugin:"+plugin.ID,
				plugin.HooksConfig,
				eventName,
				toolName,
				map[string]any{
					"pluginId":   plugin.ID,
					"pluginName": plugin.Name,
					"pluginRoot": plugin.Root(),
				},
			)...)
		}
	}
	return hooks
}

func (s *Server) agentRuntimePluginHooksEnabled() bool {
	if s == nil || s.settingsStore == nil {
		return true
	}
	for _, key := range []string{"agentRuntimePluginHooksEnabled", configStoreKey("agentRuntimePluginHooksEnabled")} {
		setting, ok, err := s.settingsStore.Get(key)
		if err == nil && ok {
			return boolValue(setting.Value, true)
		}
	}
	return true
}

func (s *Server) agentRuntimeManagedHooksOnly() bool {
	if s == nil || s.settingsStore == nil {
		return false
	}
	for _, key := range []string{"agentRuntimeManagedHooksOnly", configStoreKey("agentRuntimeManagedHooksOnly")} {
		setting, ok, err := s.settingsStore.Get(key)
		if err == nil && ok {
			return boolValue(setting.Value, false)
		}
	}
	return false
}

func agentRuntimeSimplifiedHook(key string, raw map[string]any, eventName string, toolName string) []agentRuntimeHookConfig {
	if !agentRuntimeHookEventMatches(stringValue(raw["event"]), eventName) {
		return nil
	}
	if !agentRuntimeHookToolMatches(agentRuntimeHookMatcher(raw), toolName) {
		return nil
	}
	return []agentRuntimeHookConfig{{Key: key, Raw: raw}}
}

func agentRuntimeOriginalShapeHooks(sourceKey string, raw map[string]any, eventName string, toolName string) []agentRuntimeHookConfig {
	return agentRuntimeOriginalShapeHooksWithContext(sourceKey, raw, eventName, toolName, nil)
}

func agentRuntimeOriginalShapeHooksWithContext(sourceKey string, raw map[string]any, eventName string, toolName string, context map[string]any) []agentRuntimeHookConfig {
	root := raw
	if nested := mapValue(raw["hooks"]); len(nested) > 0 {
		root = nested
	}
	blocks := arrayValue(root[eventName])
	if len(blocks) == 0 {
		return nil
	}
	result := []agentRuntimeHookConfig{}
	for blockIndex, blockValue := range blocks {
		block := mapValue(blockValue)
		if len(block) == 0 || !boolValue(block["enabled"], true) {
			continue
		}
		matcher := strings.TrimSpace(stringValue(block["matcher"]))
		if !agentRuntimeHookToolMatches(matcher, toolName) {
			continue
		}
		hookValues := arrayValue(block["hooks"])
		if len(hookValues) == 0 && strings.TrimSpace(stringValue(block["command"])) != "" {
			hookValues = []any{block}
		}
		for hookIndex, hookValue := range hookValues {
			hook := mapValue(hookValue)
			if len(hook) == 0 || !boolValue(hook["enabled"], true) {
				continue
			}
			expanded := mergeAgentRuntimeToolInput(block, hook)
			expanded["event"] = eventName
			expanded["matcher"] = matcher
			expanded["tool"] = matcher
			for key, value := range context {
				if value != nil {
					expanded[key] = value
				}
			}
			result = append(result, agentRuntimeHookConfig{
				Key: fmt.Sprintf("%s:%s:%d:%d", sourceKey, eventName, blockIndex, hookIndex),
				Raw: expanded,
			})
		}
	}
	return result
}

func agentRuntimeHookMatcher(raw map[string]any) string {
	if matcher := strings.TrimSpace(stringValue(raw["matcher"])); matcher != "" {
		return matcher
	}
	return stringValue(raw["tool"])
}

func arrayValue(value any) []any {
	switch typed := value.(type) {
	case []any:
		return typed
	case []map[string]any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, item)
		}
		return result
	default:
		return nil
	}
}

func mergeAgentRuntimeToolInput(input map[string]any, updates map[string]any) map[string]any {
	merged := make(map[string]any, len(input)+len(updates))
	for key, value := range input {
		merged[key] = value
	}
	for key, value := range updates {
		merged[key] = value
	}
	return merged
}

type agentRuntimeCommandHookResult struct {
	Decision          string
	Reason            string
	UpdatedInput      map[string]any
	AdditionalContext string
	SystemMessage     string
	Audit             map[string]any
}

func (s *Server) runAgentRuntimeHook(ctx context.Context, eventName string, hookKey string, raw map[string]any, payload map[string]any) *agentRuntimeCommandHookResult {
	hookType := strings.ToLower(strings.TrimSpace(stringValue(raw["type"])))
	if hookType == "" {
		hookType = "command"
	}
	switch hookType {
	case "command":
		return s.runAgentRuntimeCommandHook(ctx, eventName, hookKey, raw, payload)
	case "http":
		return s.runAgentRuntimeHTTPHook(ctx, eventName, hookKey, raw, payload)
	case "prompt":
		return s.runAgentRuntimePromptHook(ctx, eventName, hookKey, raw, payload)
	case "agent":
		return s.runAgentRuntimeAgentHook(ctx, eventName, hookKey, raw, payload)
	default:
		return nil
	}
}
