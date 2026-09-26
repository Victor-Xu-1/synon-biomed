package server

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPreflightDiagnosticPreservesSelectorsWithoutRawPayload(t *testing.T) {
	raw := agentRuntimePreflightDiagnostic(map[string]any{
		"status": "required_skill_load_pending", "required_skill": "engine-contract",
		"required_skills": []string{"engine-contract"}, "required_filter": "metadata",
		"required_reads": []string{"task/input.cif"}, "decision_required": false,
		"required_entrypoint": "/workspace/.synon/runtime/skills/engine/digest/scripts/run.py",
		"download":            map[string]any{"url": "https://example.org/engine.tar.gz", "filename": "engine.tar.gz", "expected_sha256": strings.Repeat("a", 64)},
		"message":             "Required contract missing", "recovery": "Load required_skill once.",
		"arguments": "private-input", "token": "private-token",
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got["required_skill"] != "engine-contract" || got["required_filter"] != "metadata" ||
		got["decision_required"] != false || len(anySliceValue(got["required_reads"])) != 1 ||
		len(anySliceValue(got["required_skills"])) != 1 || strings.Contains(raw, "private-") ||
		got["required_entrypoint"] != "/workspace/.synon/runtime/skills/engine/digest/scripts/run.py" ||
		mapValue(got["download"])["expected_sha256"] != strings.Repeat("a", 64) {
		t.Fatalf("unsafe or incomplete diagnostic: %s", raw)
	}
}

func TestPreflightDiagnosticBudgetKeepsJSONAndExactSelector(t *testing.T) {
	for _, text := range []string{strings.Repeat("汉字", 2000), strings.Repeat("\x00<", 2000)} {
		raw := agentRuntimePreflightDiagnostic(map[string]any{
			"status": "required_skill_load_pending", "required_skill": "exact-engine",
			"required_packages": []string{strings.Repeat("package", 1000)},
			"message":           text, "recovery": text,
		})
		var got map[string]any
		if len(raw) > 1800 || !utf8.ValidString(raw) || json.Unmarshal([]byte(raw), &got) != nil {
			t.Fatalf("invalid bounded diagnostic (%d bytes): %s", len(raw), raw)
		}
		if got["diagnostic_truncated"] != true {
			t.Fatalf("truncation was not explicit: %s", raw)
		}
		if got["required_skill"] != "exact-engine" {
			t.Fatalf("explanatory text displaced the actionable identity: %s", raw)
		}
	}
}
