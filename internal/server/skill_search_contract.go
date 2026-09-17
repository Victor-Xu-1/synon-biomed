package server

import (
	"strings"

	"synon-go/internal/skills"
)

// runtimeSkillSearchMetadata is the model-facing discovery contract. Skill
// source paths belong to the host catalog and are intentionally omitted: the
// only supported transition from discovery to loading is the Skill tool.
func runtimeSkillSearchMetadata(skill skills.Skill) map[string]any {
	return map[string]any{
		"name":           skill.Name,
		"description":    skill.Description,
		"tags":           skill.Tags,
		"keywords":       skill.Keywords,
		"tools":          skill.Tools,
		"arguments":      skill.Arguments,
		"references":     skill.References,
		"requiredSkills": skill.RequiredSkills,
		"body_hash":      skill.BodyHash,
		"body_preview":   strings.TrimSpace(truncateServerString(skill.Body, 512)),
		"invoke": map[string]any{
			"tool": "skill",
			"input": map[string]any{
				"skill": skill.Name,
				"args":  "",
			},
		},
	}
}
