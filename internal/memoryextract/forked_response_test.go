package memoryextract

import "testing"

func TestWorkspaceForkedResponseParsesFirstBalancedJSONObject(t *testing.T) {
	input := "```json\nmodel preface {\"append\":[{\"text\":\"brace } and \\\"quote\\\"\",\"evidence\":\"stated\"}],\"nested\":{\"ok\":true}} ignored {\"remove\":[\"later\"]}\n```"
	got := ParseForkedResponse(input)
	appendRows, ok := got["append"].([]any)
	if !ok || len(appendRows) != 1 {
		t.Fatalf("forked response = %#v", got)
	}
	if nested, ok := got["nested"].(map[string]any); !ok || nested["ok"] != true {
		t.Fatalf("nested response = %#v", got["nested"])
	}
	if _, exists := got["remove"]; exists {
		t.Fatalf("parser consumed a later JSON object: %#v", got)
	}
}

func TestWorkspaceForkedResponseFallsBackToEmptyObject(t *testing.T) {
	for name, input := range map[string]string{
		"no object":    "nothing durable",
		"unterminated": `before {"append": [`,
		"invalid":      `before {append: []} after`,
		"json null":    `before {"append": null,} after`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := ParseForkedResponse(input); len(got) != 0 {
				t.Fatalf("ParseForkedResponse(%q) = %#v", input, got)
			}
		})
	}
}
