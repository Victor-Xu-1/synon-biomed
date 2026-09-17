package memoryprompt

import (
	"strings"
	"testing"
)

func TestMemoryRulesIncludeToolAndPrivacyContracts(t *testing.T) {
	rules := SystemRules()
	if !strings.HasPrefix(rules, SystemHeader+"\n\n") {
		t.Fatal("policy prose changed the serialized context trust boundary")
	}
	checks := []string{
		"use search_memory",
		"frame: temporary notes belonging only to the current conversation",
		"category listing is the authority",
		"## Memory privacy and quality",
		"use write_memory to record it",
		"protected personal attributes",
		"exclusions still apply when someone asks",
		"abandon honest assessment",
	}
	for _, check := range checks {
		if !strings.Contains(rules, check) {
			t.Fatalf("workspace memory contract missing %q", check)
		}
	}
}
