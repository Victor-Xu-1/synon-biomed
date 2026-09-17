package server

import "strings"

func agentKernelAuthorityAndExecutionInputs(publicName string, input map[string]any) (map[string]any, map[string]any) {
	authorityInput := input
	executionInput := input
	if publicName == "repl" {
		executionInput = normalizeAgentKernelReplContractAliases(input)
	}
	return authorityInput, executionInput
}

// normalizeAgentKernelReplContractAliases is the single compatibility edge
// for provider-authored REPL code. The durable tool call remains unchanged for
// audit and approval; only execution receives the provider-neutral task-state
// alias. Scientific result fields are data and must never be silently rewritten
// here: their schema belongs to the selected MCP method and its durable result.
func normalizeAgentKernelReplContractAliases(input map[string]any) map[string]any {
	code := stringValue(input["code"])
	if code == "" {
		return input
	}
	normalized := code
	if strings.Contains(normalized, "host.global_data") {
		normalized = strings.ReplaceAll(normalized, "host.global_data", "_synon_task_state")
		normalized = "_synon_task_state = globals().setdefault(\"_synon_task_state\", {})\n" + normalized
	}
	if normalized == code {
		return input
	}
	result := copyMapAny(input)
	result["code"] = normalized
	return result
}
