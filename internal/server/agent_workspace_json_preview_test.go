package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestWorkspaceJSONTextPreviewUsesDecodedSourceCharacters(t *testing.T) {
	source := strings.Repeat("原始文本\n<data>θ🧬\t\"quoted\" & value\\path\n", 2000)
	raw, err := json.Marshal(map[string]any{"body": source, "url": "https://example.org/source"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withAgentWorkspaceReadBudget(context.Background(), runnerLargeToolResultInlineLimitBytes)
	result, ok := agentWorkspaceJSONNavigation(ctx, raw, "source.json", "application/json", int64(len(raw)), map[string]any{"version_id": "source-version"})
	if !ok || !agentWorkspaceReadResultFits(ctx, result) {
		t.Fatal("source preview unavailable or outside transport budget")
	}
	for _, section := range result["sections"].([]map[string]any) {
		if section["json_pointer"] != "/body" {
			continue
		}
		preview := stringValue(section["value_preview"])
		if preview == "" || !utf8.ValidString(preview) || !strings.HasPrefix(source, preview) {
			t.Fatalf("preview is serialized JSON rather than source text: %.180q", preview)
		}
		if boolValue(section["value_complete"], true) || mapValue(section["read_with"])["json_pointer"] != "/body" {
			t.Fatal("partial source lost its completeness status or full-value handle")
		}
		return
	}
	t.Fatal("body section missing")
}
