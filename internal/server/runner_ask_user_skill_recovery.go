package server

import (
	"encoding/json"
	"strings"
	"synon-go/internal/agentruntime"
)

func runnerPendingAskUserRequiredSkillChoice(
	pendingSkills []string,
	tools []agentruntime.ToolSchema,
) any {
	if len(pendingSkills) == 0 || !agentRuntimeToolSchemaNamed(tools, "skill") {
		return nil
	}
	return map[string]any{"type": "tool", "name": "skill"}
}

func runnerPendingAskUserRequiredSkillNames(messages []agentruntime.Message) []string {
	calls := runnerToolCallsByCallID(messages)
	boundary := -1
	required := []string(nil)
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		call, found := calls[message.ToolCallID]
		if message.Role != "tool" || !found || normalizeAgentToolName(call.Name) != "askuser" {
			continue
		}
		result := runnerCorrectionResultObjectFromContent(message.Content)
		// A later accepted question or answer supersedes repair requirements
		// from an older rejected proposal. A transport failure does not.
		var envelope any
		if json.Unmarshal([]byte(message.Content), &envelope) == nil &&
			toolResultProvidesExecutedEvidence(envelope) {
			switch strings.TrimSpace(stringValue(result["status"])) {
			case "answered", "awaiting_user_response":
				return nil
			}
		}
		if strings.TrimSpace(stringValue(result["status"])) != "implementation_decision_contract_incomplete" {
			continue
		}
		required = uniqueSortedFolded(stringArrayValue(result["required_skills"]))
		boundary = index
		break
	}
	if boundary < 0 || len(required) == 0 {
		return nil
	}
	loaded := make(map[string]struct{})
	for _, message := range messages[boundary+1:] {
		if message.Role != "tool" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || normalizeAgentToolName(call.Name) != "skill" || !runnerSkillLoadResultSucceeded(message.Content) {
			continue
		}
		input := map[string]any{}
		if json.Unmarshal(call.Arguments, &input) == nil {
			loaded[strings.ToLower(strings.TrimSpace(stringValue(input["skill"])))] = struct{}{}
		}
	}
	pending := make([]string, 0, len(required))
	for _, name := range required {
		if _, found := loaded[strings.ToLower(strings.TrimSpace(name))]; !found {
			pending = append(pending, name)
		}
	}
	return uniqueSortedFolded(pending)
}

func runnerSkillLoadResultSucceeded(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	var decoded any
	if json.Unmarshal([]byte(trimmed), &decoded) != nil {
		return false
	}
	switch value := decoded.(type) {
	case string:
		// A first Skill load returns the rendered Skill body as a JSON string.
		// A non-empty body is successful evidence that the exact Skill loaded.
		return strings.TrimSpace(value) != ""
	case map[string]any:
		return toolResultProvidesExecutedEvidence(value)
	default:
		return false
	}
}

func runnerCorrectionResultObjectFromContent(content string) map[string]any {
	result := map[string]any{}
	if json.Unmarshal([]byte(strings.TrimSpace(content)), &result) != nil {
		return nil
	}
	if nested := mapValue(result["result"]); len(nested) > 0 {
		return nested
	}
	return result
}
