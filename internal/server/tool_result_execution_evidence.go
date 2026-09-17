package server

import "synon-go/internal/agentruntime"

// Keep the outer transport result and its documented result envelope together.
// Success in a payload cannot erase a failed or non-executing wrapper. Only
// envelope fields are inspected; scientific records are not execution state.
func toolResultProvidesExecutedEvidence(result any) bool {
	if agentruntime.ClassifyToolResult(result) != agentruntime.ToolResultSucceeded {
		return false
	}
	outer, _ := result.(map[string]any)
	for _, envelope := range []map[string]any{outer, mapValue(outer["result"])} {
		if executed, present := envelope["executed"]; present && executed != true {
			return false
		}
	}
	return true
}
