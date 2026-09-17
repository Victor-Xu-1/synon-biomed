package agentruntime

import (
	_ "embed"
	"strings"
)

//go:embed assets/synon_platform_policy.md
var synonPlatformPolicy string

func SynonPlatformPolicy() string {
	return strings.TrimSpace(synonPlatformPolicy)
}

func agentUsesSynonPlatformPolicy(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "ONBOARDING", "REVIEWER", "BOOKMARKER":
		return false
	default:
		return true
	}
}
