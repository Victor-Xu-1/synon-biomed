package server

import "strings"

// agentRuntimeSkillSearchModelResult marks catalog matches whose exact Skill
// contract is already active in the logical task. The catalog remains
// discoverable for later independent stages, but a weak model receives a
// compact, explicit signal instead of cycling through search -> load for the
// same capability.
func agentRuntimeSkillSearchModelResult(
	value any,
	input map[string]any,
	run *sessionRunnerChatRun,
) any {
	if run == nil {
		return value
	}
	loadedNames := normalizedSkillNameSet(run.executedSkillNamesSnapshot())
	if len(loadedNames) == 0 {
		return value
	}
	payload := mapValue(value)
	if payload == nil {
		return value
	}
	matches := stringArrayValue(payload["matches"])
	loadedMatches := make([]string, 0)
	for _, name := range matches {
		if _, loaded := loadedNames[strings.ToLower(strings.TrimSpace(name))]; loaded {
			loadedMatches = append(loadedMatches, name)
		}
	}
	if len(loadedMatches) == 0 {
		return value
	}

	result := make(map[string]any, len(payload)+6)
	for key, item := range payload {
		result[key] = item
	}
	result["loaded_matches"] = loadedMatches
	result["loaded_match_count"] = len(loadedMatches)
	result["message"] = "Matching capabilities listed in loaded_matches are already active for this task."
	result["nextAction"] = "Continue with the requested work or provide the requested final answer. Do not reload a capability listed in loaded_matches unless its arguments or the task change."

	details, _ := payload["skill_matches"].([]map[string]any)
	if len(details) > 0 {
		copied := make([]map[string]any, 0, len(details))
		for _, detail := range details {
			item := make(map[string]any, len(detail)+1)
			for key, value := range detail {
				item[key] = value
			}
			if _, loaded := loadedNames[strings.ToLower(strings.TrimSpace(stringValue(detail["name"])))]; loaded {
				item["loaded"] = true
			}
			copied = append(copied, item)
		}
		result["skill_matches"] = copied
	}

	query := strings.ToLower(strings.TrimSpace(stringValue(input["query"])))
	query = strings.TrimPrefix(query, "select:")
	query = strings.TrimPrefix(query, "/")
	if _, loaded := loadedNames[query]; loaded {
		// The first search remains complete and auditable. Once this exact Skill
		// is active, return only identities plus the convergence contract to the
		// model; replaying every preview and schema makes the already-satisfied
		// discovery look like fresh evidence and can trap weaker models in a
		// search -> load loop.
		return map[string]any{
			"ok":                 true,
			"reused":             true,
			"status":             "discovery_already_satisfied",
			"query":              stringValue(input["query"]),
			"matches":            matches,
			"loaded_matches":     loadedMatches,
			"loaded_match_count": len(loadedMatches),
			"message":            result["message"],
			"nextAction":         result["nextAction"],
		}
	}
	return result
}
