package server

import (
	"strings"
	"testing"
)

func TestSessionRunnerPlanningGuidanceLeavesResearchMechanicsToStructuredState(t *testing.T) {
	guidance := sessionRunnerPlanningGuidance(false)
	for _, marker := range []string{
		"one concise working plan",
		"structured step state",
		"research steps",
		"synthesis steps",
		"delivery steps",
		"continue without waiting for approval",
	} {
		if !strings.Contains(guidance, marker) {
			t.Fatalf("planning guidance missing %q: %s", marker, guidance)
		}
	}
	if len(guidance) > 600 {
		t.Fatalf("planning guidance became a second research implementation: len=%d text=%s", len(guidance), guidance)
	}
	for _, forbidden := range []string{
		"minimum", "source count", "elapsed time", "word count", "coverage set", "gap", "GSE120575", "scRNA", "CRBN",
	} {
		if strings.Contains(guidance, forbidden) {
			t.Fatalf("planning guidance contains prompt-level research policy %q: %s", forbidden, guidance)
		}
	}
}

func TestSessionRunnerPlanningGuidanceKeepsExplicitReviewSeparate(t *testing.T) {
	reviewed := sessionRunnerPlanningGuidance(true)
	autonomous := sessionRunnerPlanningGuidance(false)
	if !strings.Contains(reviewed, "wait for approval before execution") {
		t.Fatalf("explicit-review guidance=%s", reviewed)
	}
	if strings.Contains(autonomous, "wait for approval before execution") {
		t.Fatalf("autonomous guidance inherited the review pause: %s", autonomous)
	}
}
