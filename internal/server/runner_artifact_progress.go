package server

import (
	"encoding/json"
	"strings"

	"synon-go/internal/agentruntime"
)

// runnerCorrectionResultMadeMutation distinguishes a successful mutating
// operation from an explicit no-op. Unknown legacy result shapes remain
// progress-capable so this detector cannot reject a real mutation merely
// because an older tool omitted the modern changed/unchanged markers.
func runnerCorrectionResultMadeMutation(result any) bool {
	object := runnerCorrectionResultObject(result)
	if object == nil {
		return true
	}
	if changed, recorded := object["changed"].(bool); recorded && changed {
		return true
	}
	if unchanged, recorded := object["unchanged"].(bool); recorded && !unchanged {
		return true
	}
	artifacts := anySliceValue(object["artifacts"])
	if len(artifacts) > 0 {
		for _, raw := range artifacts {
			artifact := mapValue(raw)
			unchanged, recorded := artifact["unchanged"].(bool)
			if !recorded || !unchanged {
				return true
			}
		}
		return false
	}
	if changed, recorded := object["changed"].(bool); recorded {
		return changed
	}
	if unchanged, recorded := object["unchanged"].(bool); recorded {
		return !unchanged
	}
	return true
}

func runnerArtifactSaveRequiresCorrection(result map[string]any) bool {
	return strings.TrimSpace(stringValue(result["code"])) == "artifact_save_requires_correction" ||
		boolValue(result["completion_pending"], false)
}

// sessionRunnerInlineArtifactRepairReadyForRevalidation closes an inline
// draft-correction window as soon as a later artifact publication succeeds
// without another correction contract. The immutable completion validator,
// not another provider-selected write, owns the next decision.
func sessionRunnerInlineArtifactRepairReadyForRevalidation(messages []agentruntime.Message) bool {
	calls := runnerToolCallsByCallID(messages)
	correctionObserved := false
	ready := false
	for _, message := range messages {
		if message.Role != "tool" || strings.TrimSpace(message.Content) == "" {
			continue
		}
		call, found := calls[message.ToolCallID]
		if !found {
			continue
		}
		var result any
		if json.Unmarshal([]byte(message.Content), &result) != nil || agentruntime.IsNonExecutingPreflight(result) {
			continue
		}
		normalized := normalizeAgentToolName(call.Name)
		if normalized != "saveartifacts" {
			if correctionObserved && runnerArtifactMutationTool(normalized) &&
				agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded &&
				runnerCorrectionResultMadeMutation(result) {
				ready = false
			}
			continue
		}
		object := runnerCorrectionResultObject(result)
		if object == nil {
			continue
		}
		if runnerArtifactSaveRequiresCorrection(object) {
			correctionObserved = true
			ready = false
			continue
		}
		if correctionObserved && agentruntime.ClassifyToolResult(result) == agentruntime.ToolResultSucceeded &&
			len(anySliceValue(object["artifacts"])) > 0 {
			ready = true
		}
	}
	return correctionObserved && ready
}

func runnerArtifactMutationTool(normalized string) bool {
	switch normalized {
	case "edit", "editfile", "filepatch", "filereplace", "patch", "write", "filewrite":
		return true
	default:
		return false
	}
}
