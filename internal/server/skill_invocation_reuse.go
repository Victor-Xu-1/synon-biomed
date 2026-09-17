package server

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func runtimeSkillInvocationKey(skillName, args, filter string) string {
	skillName = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(skillName), "/"))
	if skillName == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(strings.Join([]string{
		skillName,
		strings.TrimSpace(args),
		strings.TrimSpace(filter),
	}, "\x00")))
	return hex.EncodeToString(digest[:])
}

func runtimeSkillInvocationKeyFromInput(input map[string]any) string {
	return runtimeSkillInvocationKey(
		stringValue(input["skill"]),
		stringValue(input["args"]),
		stringValue(input["filter"]),
	)
}

func repeatedRuntimeSkillInvocationResult(skillName string, loadedSkills []string) map[string]any {
	return map[string]any{
		"ok":           true,
		"success":      true,
		"loaded":       true,
		"reused":       true,
		"executed":     false,
		"status":       "already_loaded",
		"skill":        strings.TrimSpace(skillName),
		"loadedSkills": append([]string(nil), loadedSkills...),
		"message":      "This capability and argument set is already active for the current task.",
		"nextAction":   "Continue with the requested work or provide the requested final answer. Do not search for or load the same capability again unless the task or arguments change.",
	}
}
