package server

import (
	"sort"
	"strings"

	"synon-go/internal/toolcontract"
	"synon-go/internal/tools/registry"
)

func hasChatRole(messages []chatCompletionMessage, role string) bool {
	for _, message := range messages {
		if message.Role == role {
			return true
		}
	}
	return false
}

func buildSessionRunnerTaskContract(task, taskIntentID string, taskIntentRevision int64) sessionRunnerTaskContract {
	task = strings.TrimSpace(task)
	checks := []string{
		"Every explicit canonical entity, boundary condition, range, comparison, method or evidence requirement, and deliverable remains unchanged and is supported by executed evidence.",
		"Any delegated choice fills only an unspecified detail and does not replace an explicit task condition with a proxy or representative scenario.",
		"Confirm the final answer addresses the complete canonical task and report every unmet item explicitly.",
	}
	return sessionRunnerTaskContract{
		Version: 2, TaskIntentID: strings.TrimSpace(taskIntentID),
		TaskIntentRevision: taskIntentRevision, TaskIntentSHA256: generatedPlanTaskIntentSHA(task),
		AcceptanceChecks: checks, TemporalScopes: sessionRunnerTaskTemporalScopes(task),
	}
}

func applyRecoveredRunnerCorrectionToTaskContract(
	contract sessionRunnerTaskContract,
	correction recoveredRunnerCorrection,
) sessionRunnerTaskContract {
	if strings.TrimSpace(correction.ReasonCode) == "" {
		return contract
	}
	contract.CorrectionReason = strings.TrimSpace(correction.ReasonCode)
	if correction.Condition != nil {
		contract.CorrectionConditionID = correction.Condition.ContentID()
		contract.CorrectionFingerprint = correction.Condition.Fingerprint()
	}
	detail := truncateTaskContractText(strings.TrimSpace(correction.Detail), 1024)
	if detail != "" {
		contract.AcceptanceChecks = uniqueTaskContractChecks(append(contract.AcceptanceChecks,
			"Resolve the durable correction before completion: "+detail,
		))
	}
	return contract
}

func uniqueTaskContractChecks(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		key := strings.ToLower(value)
		if value == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, value)
	}
	return result
}

func truncateTaskContractText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func (s *Server) chatRunnerTools(allowedTools []string) []chatCompletionTool {
	if s == nil || s.tools == nil {
		return nil
	}
	allowed := chatRunnerAllowedToolSet(allowedTools)
	names := s.modelSchemaNames(allowedTools)
	tools := make([]chatCompletionTool, 0, len(names))
	for _, name := range names {
		if !chatRunnerToolAllowed(name, allowed) {
			continue
		}
		tool, ok := s.registeredTool(name)
		if !ok || !tool.Executable {
			continue
		}
		tools = append(tools, chatCompletionTool{
			Type: "function",
			Function: chatCompletionToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  chatToolParameters(tool),
			},
		})
	}
	return tools
}

func chatRunnerToolAllowed(name string, allowed map[string]struct{}) bool {
	if strings.HasPrefix(name, "session_") || strings.HasPrefix(name, "pairing_") {
		return false
	}
	switch name {
	case "synon_link", "shell_exec", "Bash", "Shell", "powershell":
		return false
	}
	if len(allowed) == 0 {
		return true
	}
	_, ok := allowed[name]
	return ok
}

func chatRunnerAllowedToolSet(allowedTools []string) map[string]struct{} {
	names := normalizeChatToolNames(allowedTools)
	if len(names) == 0 {
		if len(allowedTools) > 0 {
			return map[string]struct{}{noChatToolsAllowedSentinel: {}}
		}
		return nil
	}
	allowed := make(map[string]struct{}, len(names))
	for _, name := range names {
		allowed[name] = struct{}{}
	}
	return allowed
}

func normalizeChatToolNames(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(names))
	normalized := make([]string, 0, len(names))
	for _, name := range names {
		canonical, ok := toolcontract.NormalizeRuntimeName(name)
		if !ok {
			continue
		}
		name = canonical
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized
}

func chatToolParameters(tool registry.Tool) map[string]any {
	properties := map[string]any{}
	required := make([]string, 0)
	fieldNames := make([]string, 0, len(tool.Input))
	for name := range tool.Input {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)
	for _, name := range fieldNames {
		field := tool.Input[name]
		properties[name] = chatToolFieldSchema(field)
		if field.Required {
			required = append(required, name)
		}
	}
	parameters := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false,
	}
	if tool.Name == "StructuredOutput" {
		parameters["additionalProperties"] = true
	}
	return parameters
}

func chatToolFieldSchema(field registry.Field) map[string]any {
	schema := copyMapAny(field.Schema)
	if schema == nil {
		schema = map[string]any{}
	}
	if _, defined := schema["type"]; !defined && field.Type != "" && field.Type != "any" {
		schema["type"] = field.Type
	}
	return schema
}
