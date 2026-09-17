package assets

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBundledClinicalTrialsSearchUsesOneUnambiguousListContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	body, err := os.ReadFile(filepath.Join(
		repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "lib", "mcp_clinical_trials", "schemas.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	var catalog struct {
		Tools []struct {
			Name        string         `json:"name"`
			InputSchema map[string]any `json:"input_schema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(body, &catalog); err != nil {
		t.Fatal(err)
	}
	for _, tool := range catalog.Tools {
		if tool.Name != "search_trials" {
			continue
		}
		properties, _ := tool.InputSchema["properties"].(map[string]any)
		for _, field := range []string{"status", "phase"} {
			property, _ := properties[field].(map[string]any)
			alternatives, _ := property["anyOf"].([]any)
			if len(alternatives) == 0 {
				t.Fatalf("search_trials %s has no alternatives", field)
			}
			var hasString bool
			var arrayEnum []any
			for _, raw := range alternatives {
				alternative, _ := raw.(map[string]any)
				switch alternative["type"] {
				case "string":
					hasString = true
				case "array":
					items, _ := alternative["items"].(map[string]any)
					arrayEnum, _ = items["enum"].([]any)
				}
			}
			if hasString || len(arrayEnum) == 0 {
				t.Fatalf("search_trials %s must expose only closed arrays plus null: %#v", field, property)
			}
		}
		return
	}
	t.Fatal("search_trials schema not found")
}

func TestBundledClinicalTrialsTier1EnforcesPublishedSchemaBeforeHandler(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is unavailable")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	lib := filepath.Join(repositoryRoot, "assets", "optional", "mcp-servers", "bio-tools", "lib")
	script := `
import sys
sys.path.insert(0, sys.argv[1])
from mcp_clinical_trials.server import build_server
server = build_server()
invalid = [
    {"condition":"EGFR", "page_size":"20"},
    {"condition":"EGFR", "count_total":"false"},
    {"condition":"EGFR", "phase":"PHASE2"},
]
for value in invalid:
    error = server.input_validation_error("search_trials", value)
    assert error and error["code"] == "invalid_tool_arguments", (value, error)
assert server.input_validation_error("search_trials", {"condition":"EGFR", "phase":["PHASE2"], "page_size":20}) is None
`
	command := exec.Command(python, "-c", script, lib)
	command.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("tier1 schema validation failed: %v\n%s", err, output)
	}
}
