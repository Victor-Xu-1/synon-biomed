package server

import (
	"path/filepath"
	"testing"
)

func TestKernelMCPOutputPathsAreAnchoredToTaskWorkspace(t *testing.T) {
	workspace := t.TempDir()
	input := map[string]any{
		"source_id": "clinicaltrials-skill",
		"input": map[string]any{
			"raw_output_path": "./records/polq.json",
			"cache_path":      "leave-relative.json",
		},
	}
	normalized := normalizeKernelMCPWorkspaceOutputPaths(input, workspace)
	nested := mapValue(normalized["input"])
	want := filepath.Join(workspace, "records", "polq.json")
	if nested["raw_output_path"] != want || nested["cache_path"] != "leave-relative.json" {
		t.Fatalf("workspace output normalization=%#v", normalized)
	}
	if mapValue(input["input"])["raw_output_path"] != "./records/polq.json" {
		t.Fatalf("normalization mutated audited model input: %#v", input)
	}
}

func TestKernelMCPOutputPathsCannotEscapeTaskWorkspace(t *testing.T) {
	workspace := t.TempDir()
	for _, requested := range []string{"../../outside.json", filepath.Join(filepath.Dir(workspace), "absolute.json")} {
		normalized := normalizeKernelMCPWorkspaceOutputPaths(map[string]any{
			"input": map[string]any{"raw_output_path": requested},
		}, workspace)
		path := stringValue(mapValue(normalized["input"])["raw_output_path"])
		if !hostPathWithin(workspace, path) || filepath.Dir(path) != filepath.Clean(workspace) {
			t.Fatalf("output path escaped workspace: requested=%q normalized=%q", requested, path)
		}
	}
}
