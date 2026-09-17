package server

import (
	"strings"
	"testing"
)

func TestAppendWebComposerRuntimeContextKeepsTypedSelectionsAndQuotesIdentifiers(t *testing.T) {
	prompt := appendWebComposerRuntimeContext("base", map[string]any{
		"files":                 []any{"inputs/cohort.csv", "inputs/a\"b.txt"},
		"inject_skills":         []any{"literature-review"},
		"inject_mcp_server_ids": []any{"pubmed-connector"},
	})
	for _, expected := range []string{
		"base",
		"Input files selected for this turn",
		`["inputs/cohort.csv","inputs/a\"b.txt"]`,
		"Skills explicitly selected for this turn",
		"literature-review",
		"MCP connectors explicitly selected for this turn",
		"pubmed-connector",
		"never claim MCP use without a tool receipt",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("composer prompt omitted %q: %q", expected, prompt)
		}
	}
}

func TestAppendWebComposerRuntimeContextLeavesEmptyInputUnchanged(t *testing.T) {
	if got := appendWebComposerRuntimeContext("base", map[string]any{}); got != "base" {
		t.Fatalf("empty composer context changed prompt: %q", got)
	}
}
