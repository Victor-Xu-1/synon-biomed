package server

import (
	"encoding/json"
	"math"
	"strings"
	"unicode/utf16"

	workspace "synon-go/internal/persistence/workspace"
)

var traceLeanContextKeys = []string{
	"_msg_base_idx", "_message_count", "_user_message_count", "_ws_seq", "_umc_ws_seq",
	"_branch_meta", "_plan_artifact_id", "_plan_version_id", "_plan_approved",
	"_step_statuses", "_plan_claims", "_tool_id_to_frame_id", "_running_children",
	"_running_executions", "_model", "_is_routine",
}

func traceLeanOutput(value map[string]any) any {
	output := copyMapAny(value)
	for _, key := range []string{"response", "thinking", "structured_output", "_completion_bullets"} {
		delete(output, key)
	}
	return traceNullableMap(output)
}

func traceLeanContext(value map[string]any, status string) map[string]any {
	lean := make(map[string]any)
	for _, key := range traceLeanContextKeys {
		if item, found := value[key]; found {
			lean[key] = item
		}
	}
	lean = traceBranchMessagesCleared(lean)
	if !traceTerminalStatus(status) {
		if latest, found := value["_latest_tool_block"]; found {
			lean["_latest_tool_block"] = latest
		}
	}
	return lean
}

func traceContextWithoutMessages(value any) any {
	contextData, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	contextData = copyMapAny(contextData)
	delete(contextData, "_messages")
	contextData = traceBranchMessagesCleared(contextData)
	return traceNullableMap(contextData)
}

func traceBranchMessagesCleared(contextData map[string]any) map[string]any {
	branchMeta, ok := contextData["_branch_meta"].(map[string]any)
	if !ok {
		return contextData
	}
	branches, ok := branchMeta["branches"].(map[string]any)
	if !ok {
		return contextData
	}
	cleanBranches := make(map[string]any, len(branches))
	for id, raw := range branches {
		branch, ok := raw.(map[string]any)
		if !ok {
			cleanBranches[id] = raw
			continue
		}
		clean := copyMapAny(branch)
		clean["messages"] = nil
		cleanBranches[id] = clean
	}
	cleanMeta := copyMapAny(branchMeta)
	cleanMeta["branches"] = cleanBranches
	contextData["_branch_meta"] = cleanMeta
	return contextData
}

func traceTerminalStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled", "canceled", "stopped":
		return true
	default:
		return false
	}
}

func traceContextInteger(contextData map[string]any, key string) (int, bool) {
	switch value := contextData[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func traceApplyContextMetrics(projection map[string]any, contextData map[string]any) {
	used, found := traceContextUsage(contextData)
	if !found {
		projection["context_used"] = nil
		projection["context_usage_percent"] = nil
		return
	}
	projection["context_used"] = used
	projection["context_usage_percent"] = math.Min(100,
		math.Round(used/float64(v11DefaultContextLimit)*1000)/10)
}

func traceContextUsage(contextData map[string]any) (float64, bool) {
	if used, found := traceNumber(contextData["_context_used"]); found {
		return used, true
	}
	messages, ok := contextData["_messages"].([]any)
	if !ok || len(messages) == 0 {
		return 0, false
	}
	lastAssistant := -1
	used := float64(0)
	for index := len(messages) - 1; index >= 0; index-- {
		message, ok := messages[index].(map[string]any)
		if !ok || stringValue(message["role"]) != "assistant" {
			continue
		}
		if serverTools, _ := message["_has_server_tools"].(bool); serverTools {
			continue
		}
		tokens, found := message["_tokens"]
		if !found {
			continue
		}
		tokenMap, _ := tokens.(map[string]any)
		used, _ = traceNumber(tokenMap["input"])
		lastAssistant = index
		break
	}
	if lastAssistant < 0 {
		return 0, false
	}
	compacted := float64(0)
	for _, raw := range messages[lastAssistant+1:] {
		message, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if boundary, _ := message["_compact_boundary"].(bool); boundary &&
			strings.TrimSpace(stringValue(message["_compact_from_uuid"])) != "" {
			if value, found := traceNumber(message["_pre_compact_token_count"]); found {
				compacted += value
			}
		}
		used += traceMessageTokenEstimate(message)
	}
	return math.Max(0, used-compacted), true
}

func traceMessageTokenEstimate(message map[string]any) float64 {
	content := message["content"]
	if value, ok := content.(string); ok {
		return float64(traceJSONStringLength(value) / 4)
	}
	items, ok := content.([]any)
	if !ok {
		return 0
	}
	total := 0
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch stringValue(item["type"]) {
		case "text":
			total += traceJSONStringLength(stringValue(item["text"])) / 4
		case "tool_result":
			if nested, ok := item["content"].([]any); ok {
				for _, child := range nested {
					if value, ok := child.(map[string]any); ok {
						switch stringValue(value["type"]) {
						case "document":
							total += traceVisionTokenEstimate(value, 50000)
						case "image":
							total += traceVisionTokenEstimate(value, 1600)
						default:
							total += traceJSONTokenEstimate(value)
						}
					} else {
						total += traceJSONTokenEstimate(child)
					}
				}
			} else {
				total += traceJSONTokenEstimate(item["content"])
			}
		case "tool_use":
			total += traceJSONTokenEstimate(item["input"])
		case "thinking":
			total += traceJSONStringLength(stringValue(item["thinking"])) / 4
		case "document":
			total += traceVisionTokenEstimate(item, 50000)
		case "image":
			total += traceVisionTokenEstimate(item, 1600)
		}
	}
	return float64(total)
}

func traceVisionTokenEstimate(value map[string]any, fallback int) int {
	if hint, found := traceNumber(value["_vision_token_hint"]); found && hint > 0 {
		return int(hint)
	}
	return fallback
}

func traceJSONTokenEstimate(value any) int {
	raw, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return traceJSONStringLength(string(raw)) / 4
}

func traceJSONStringLength(value string) int {
	return len(utf16.Encode([]rune(value)))
}

func traceNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func traceRootMentionedFiles(frame workspace.Frame) any {
	if frame.ParentFrameID != "" {
		return nil
	}
	return traceNullableStrings(frame.MentionedArtifactIDs)
}
