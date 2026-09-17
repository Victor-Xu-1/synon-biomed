package server

import (
	"sort"
	"strings"

	"synon-go/internal/skills"
)

// requiredScientificCapabilities projects declarative Skill metadata only.
// Execution admission and validation belong to the selected execution pack;
// this metadata never creates a second completion gate.
func requiredScientificCapabilities(selected []skills.Skill) []string {
	result := make([]string, 0)
	for _, skill := range selected {
		result = append(result, skill.RequiredCapabilities...)
	}
	return uniqueSortedScientificCapabilities(result)
}

func uniqueSortedScientificCapabilities(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
