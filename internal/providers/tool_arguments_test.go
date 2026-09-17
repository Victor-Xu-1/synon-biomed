package providers

import (
	"encoding/json"
	"testing"
)

func TestNormalizeProviderToolArgumentsObjectAcceptsSafeRepresentations(t *testing.T) {
	tests := map[string]string{
		`{"query":"6M0J"}`:                                   `{"query":"6M0J"}`,
		`"{\"query\":\"6M0J\"}"`:                             `{"query":"6M0J"}`,
		"```json\n{\"query\":\"6M0J\"}\n```":                 `{"query":"6M0J"}`,
		"```\n{\"query\":\"6M0J\"}\n```":                     `{"query":"6M0J"}`,
		"```json\n{\"content\":\"line one\nline two\"}\n```": `{"content":"line one\nline two"}`,
	}
	for input, want := range tests {
		got, ok := normalizeProviderToolArgumentsObject(input)
		if !ok || string(got) != want {
			t.Fatalf("normalize(%q)=%q ok=%t want=%q", input, got, ok, want)
		}
	}
}

func TestNormalizeProviderToolArgumentsObjectEscapesLiteralStringControls(t *testing.T) {
	input := "{\"file_path\":\"report.md\",\"new_string\":\"first\nsecond\tcolumn\"}"
	got, ok := normalizeProviderToolArgumentsObject(input)
	if !ok || !json.Valid(got) {
		t.Fatalf("normalize literal controls=%q ok=%t", got, ok)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["new_string"] != "first\nsecond\tcolumn" {
		t.Fatalf("decoded new_string=%q", decoded["new_string"])
	}
}

func TestNormalizeProviderToolArgumentsObjectRejectsAmbiguousShapes(t *testing.T) {
	for _, input := range []string{
		`["not","an","object"]`,
		`{"query":"unfinished"`,
		`{"query":"complete"} trailing`,
		"```yaml\nquery: 6M0J\n```",
	} {
		if got, ok := normalizeProviderToolArgumentsObject(input); ok {
			t.Fatalf("normalize(%q) unexpectedly accepted %q", input, got)
		}
	}
}
