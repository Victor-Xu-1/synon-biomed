package server

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"synon-go/internal/agentruntime"
)

// A rejected native edit proves that choosing the editor alone cannot advance
// the repair. Permit another admitted tool (including inspection/acquisition)
// while retaining mandatory action, native validation and failed-call fences.
// This changes only tool selection: no receipt is promoted to material progress.
func runnerCorrectionFailedEditChoice(choice any, messages []agentruntime.Message, tools []agentruntime.ToolSchema) any {
	selected := strings.TrimSpace(stringValue(mapValue(choice)["name"]))
	if selected == "" || !runtimeCapabilitiesContain(agentRuntimeToolCapabilities(tools, selected), "artifact-edit") {
		return choice
	}
	window := messages[runnerCorrectionToolBoundaryIndex(messages)+1:]
	calls := runnerToolCallsByCallID(window)
	failed := make(map[string]bool)
	for _, message := range window {
		if message.Role != "tool" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found || normalizeAgentToolName(call.Name) != normalizeAgentToolName(selected) {
			continue
		}
		var input, result map[string]any
		if json.Unmarshal(call.Arguments, &input) != nil || json.Unmarshal([]byte(message.Content), &result) != nil {
			continue
		}
		path := strings.TrimSpace(firstNonEmpty(stringValue(input["file_path"]), stringValue(input["path"])))
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if nativeEditRecoverableCondition(result) {
			failed[path] = true
		} else if agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded &&
			!agentruntime.ToolResultDidNotExecute(result) && runnerCorrectionResultMadeMutation(result) {
			delete(failed, path)
		}
	}
	if len(failed) > 0 {
		return "required"
	}
	return choice
}
