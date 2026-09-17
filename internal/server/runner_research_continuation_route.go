package server

import (
	"strings"

	"synon-go/internal/agentruntime"
)

// researchContinuationModelTool translates only the retained compound
// WebResearch compatibility identity. Every new model continuation is an
// independently observable discovery or read action in the outer Engine loop.
func researchContinuationModelTool(action, explicitTool, sourceTool string) string {
	requested := strings.TrimSpace(explicitTool)
	if requested == "" {
		requested = strings.TrimSpace(sourceTool)
	}
	if normalizeAgentToolName(requested) != "webresearch" {
		return requested
	}
	return atomicWebContinuationTool(action)
}

func researchContinuationExpectedTool(action map[string]any) string {
	actionName := strings.ToLower(strings.TrimSpace(stringValue(action["action"])))
	requested := strings.TrimSpace(stringValue(action["tool"]))
	if requested != "" && normalizeAgentToolName(requested) != "webresearch" {
		return requested
	}
	if requested == "" || normalizeAgentToolName(requested) == "webresearch" {
		return atomicWebContinuationTool(actionName)
	}
	return ""
}

// researchContinuationRequiredToolChoice resolves a retained source frontier
// through the immutable capability snapshot. A URL requires a locator reader;
// other future target kinds may use their declared capability without adding a
// task-, source-, or tool-name branch here. The action's explicit tool remains
// a compatibility fallback for older persisted continuations.
func researchContinuationRequiredToolChoice(
	continuation map[string]any,
	tools []agentruntime.ToolSchema,
) any {
	action := researchContinuationFirstAction(continuation)
	if len(action) == 0 {
		return nil
	}
	capabilities := make([]string, 0, 2)
	if strings.TrimSpace(stringValue(action["url"])) != "" {
		capabilities = append(capabilities, "source-locator-read")
	}
	if required := strings.TrimSpace(stringValue(continuation["required_capability"])); required != "" {
		capabilities = append(capabilities, required)
	}
	for _, capability := range uniqueStrings(capabilities) {
		if choice := runnerCorrectionCapabilityToolChoice(tools, capability, nil); choice != nil {
			return choice
		}
	}
	expected := researchContinuationExpectedTool(action)
	if expected != "" && agentRuntimeToolSchemaNamed(tools, expected) {
		return map[string]any{"type": "tool", "name": expected}
	}
	return nil
}

func researchContinuationToolCanExecute(
	toolName string,
	capabilities []string,
	continuation map[string]any,
) bool {
	action := researchContinuationFirstAction(continuation)
	if len(action) == 0 {
		return false
	}
	if expected := researchContinuationExpectedTool(action); expected != "" &&
		normalizeAgentToolName(expected) == normalizeAgentToolName(toolName) {
		return true
	}
	if strings.TrimSpace(stringValue(action["url"])) != "" &&
		runtimeCapabilitiesContain(capabilities, "source-locator-read") {
		return true
	}
	required := strings.TrimSpace(stringValue(continuation["required_capability"]))
	return required != "" && runtimeCapabilitiesContain(capabilities, required)
}

func atomicWebContinuationTool(action string) string {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "search", "search_more", "continue_search", "research":
		return "web_search"
	case "fetch", "read_source":
		return "web_fetch"
	default:
		return ""
	}
}
