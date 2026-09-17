package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamArgumentsNeverPromoteNestedObjectToWholeCall(t *testing.T) {
	for _, raw := range []string{
		`[{"name":"Research","steps":[]},{"name":"Save","steps":[]}]`,
		`{"phases":[{"name":"Research","steps":[{"title":"Read"}]},{"name":"Save","steps":[]}]`,
		`{"content":"unfinished string with {\"nested\":true}`,
		`{"first":1},{"second":2}`,
	} {
		if got, ok := normalizeOpenAIChatStreamArguments(raw); ok {
			t.Errorf("partial structure became executable arguments: %s", got)
		}
	}
}

func TestStreamArgumentsPreferCompleteAssemblyOverNestedRecovery(t *testing.T) {
	steps := make([]map[string]any, 160)
	for i := range steps {
		steps[i] = map[string]any{"title": "Source", "description": strings.Repeat("context ", 3)}
	}
	raw, err := json.Marshal(map[string]any{"task": "Research", "phases": []any{
		map[string]any{"name": "Investigate", "steps": steps},
		map[string]any{"name": "Deliver", "steps": []any{map[string]any{"title": "Save"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	parts := []string{`{"phases":[`, string(raw)}
	// The provider's complete snapshot assembly is valid. Recovery from the
	// malformed concatenation must not beat it with an arbitrary inner object.
	got, ok := resolveOpenAIChatStreamArguments(parts, string(raw))
	if !ok || got != string(raw) {
		t.Fatalf("complete plan replaced by partial parameters: %s", got)
	}
}

func TestStreamArgumentsArrayRemainsRecoverableProtocolFeedback(t *testing.T) {
	chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{
		"index": 0, "finish_reason": "tool_calls", "delta": map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call-array", "type": "function", "function": map[string]any{"name": "inspect", "arguments": `[{"name":"first"},{"name":"last"}]`},
		}}},
	}}})
	payload := "data: " + string(chunk) + "\n\ndata: [DONE]\n\n"
	response, _, _, _, err := readOpenAIChatStream(strings.NewReader(payload), 64*1024, nil)
	if err != nil || len(response.Message.ToolCalls) != 1 {
		t.Fatalf("recoverable shape error ended the model round: %v", err)
	}
	call := response.Message.ToolCalls[0]
	if string(call.Arguments) != "{}" || !strings.Contains(call.ProviderProtocolDiagnostic, "invalid JSON object arguments") {
		t.Fatalf("nested object reached execution instead of protocol repair: %#v", call)
	}
}

func TestProviderToolArgumentSyntaxDiagnosticIsStructuralOnly(t *testing.T) {
	diagnostic := providerToolArgumentsSyntaxDiagnostic(`{"private-key":"do-not-log" "another-private-field":1}`)
	for _, forbidden := range []string{"private-key", "do-not-log", "another-private-field"} {
		if strings.Contains(diagnostic, forbidden) {
			t.Fatalf("syntax diagnostic exposed %q: %s", forbidden, diagnostic)
		}
	}
	for _, marker := range []string{"syntax=invalid", "offset=", "token=", "prev=", "next="} {
		if !strings.Contains(diagnostic, marker) {
			t.Fatalf("syntax diagnostic %q missing %q", diagnostic, marker)
		}
	}
	if got := providerToolArgumentsSyntaxDiagnostic(`{"ok":true}`); got != "syntax=valid root=object_open" {
		t.Fatalf("valid diagnostic=%q", got)
	}
}

func TestProviderToolArgumentsRepairOnlyStructuralTrailingCommas(t *testing.T) {
	raw := `{"phases":[{"name":"Research",},],"note":"literal comma,] remains",}`
	got, ok := normalizeProviderToolArgumentsObject(raw)
	want := `{"phases":[{"name":"Research"}],"note":"literal comma,] remains"}`
	if !ok || string(got) != want {
		t.Fatalf("trailing-comma repair=%q ok=%t want=%q", got, ok, want)
	}
	for _, ambiguous := range []string{
		`{"name":"Research" "steps":[]}`,
		`{"name":"Research"}}`,
	} {
		if got, ok := normalizeProviderToolArgumentsObject(ambiguous); ok {
			t.Fatalf("ambiguous malformed structure became executable: %s", got)
		}
	}
}
