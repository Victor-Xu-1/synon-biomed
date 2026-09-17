package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceExistencePreflightRejectsMissingReadAndStandaloneScript(t *testing.T) {
	root := t.TempDir()
	identity := &agentKernelContext{workspaceDir: root}
	for _, test := range []struct {
		name  string
		input map[string]any
	}{
		{name: "read_file", input: map[string]any{"file_path": "reproducible_model.py"}},
		{name: "bash", input: map[string]any{"command": "cd $PWD && python reproducible_model.py"}},
	} {
		value := agentRuntimeWorkspaceExistencePreflight(test.name, test.input, identity)
		if value == nil || value["status"] != "workspace_file_preflight_required" || value["executed"] != false {
			t.Fatalf("tool=%s missing-file preflight=%#v", test.name, value)
		}
		if test.name == "read_file" && (!strings.Contains(stringValue(value["recovery"]), "available_files") ||
			!strings.Contains(stringValue(value["recovery"]), "do not guess another filename")) {
			t.Fatalf("read recovery=%#v", value)
		}
		if test.name == "bash" && !strings.Contains(stringValue(value["recovery"]), "edit_file") {
			t.Fatalf("bash recovery=%#v", value)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "reproducible_model.py"), []byte("print('ok')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if value := agentRuntimeWorkspaceExistencePreflight("bash", map[string]any{
		"command": "python reproducible_model.py",
	}, identity); value != nil {
		t.Fatalf("existing script was blocked: %#v", value)
	}
}

func TestWorkspaceExistencePreflightReturnsExactBoundedFileInventory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"reports/decision_report.md", "source_ledger.csv"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte("ok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	value := agentRuntimeWorkspaceExistencePreflight("read_file", map[string]any{
		"file_path": "guessed_ledger.csv",
	}, &agentKernelContext{workspaceDir: root})
	files := stringArrayValue(value["available_files"])
	if len(files) != 2 || files[0] != "reports/decision_report.md" || files[1] != "source_ledger.csv" {
		t.Fatalf("available files=%#v", files)
	}
}

func TestWorkspaceExistencePreflightLeavesArtifactAndModuleExecutionToCanonicalPaths(t *testing.T) {
	identity := &agentKernelContext{workspaceDir: t.TempDir()}
	for _, test := range []struct {
		name  string
		input map[string]any
	}{
		{name: "read_file", input: map[string]any{"version_id": "version-id"}},
		{name: "bash", input: map[string]any{"command": "python -m pytest"}},
		{name: "python", input: map[string]any{"code": "open('future.txt')"}},
	} {
		if value := agentRuntimeWorkspaceExistencePreflight(test.name, test.input, identity); value != nil {
			t.Fatalf("tool=%s canonical path was blocked: %#v", test.name, value)
		}
	}
}
