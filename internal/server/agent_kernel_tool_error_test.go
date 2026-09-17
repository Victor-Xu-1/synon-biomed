package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kernelruntime "synon-go/internal/kernel"
)

func TestAgentKernelLegacyWorkspaceAliasIsNarrow(t *testing.T) {
	absoluteAlias := filepath.Join(filepath.VolumeName(os.TempDir())+string(filepath.Separator), "home", "project", "workspace")
	if !agentKernelLegacyWorkspaceAlias(absoluteAlias) ||
		!agentKernelLegacyWorkspaceAlias(filepath.Join(absoluteAlias, "proj_544d54552e37")) {
		t.Fatal("narrow absolute legacy workspace aliases were not recognized")
	}
	for _, value := range []string{
		"workspace", filepath.Join("relative", "workspace"),
		filepath.Join(string(filepath.Separator), "home", "project", "workspaces"),
		filepath.Join(absoluteAlias, "project", "nested"),
	} {
		if agentKernelLegacyWorkspaceAlias(value) {
			t.Fatalf("unexpected legacy workspace alias: %q", value)
		}
	}
}

func TestAgentKernelPythonCodePreflightCompatibilityParserIsFailClosed(t *testing.T) {
	outcome := kernelruntime.ExecutionOutcome{Response: kernelruntime.Response{Preflight: map[string]any{
		"schema": "synon.python-code-preflight.v1", "status": "code_preflight_required",
		"executed": false, "diagnostics": []any{map[string]any{"code": "python_imported_api_unavailable"}},
		"message": "legacy execution safely deferred", "recovery": "inspect the documented interface",
	}}}
	preflight, err := agentKernelPythonCodePreflight(kernelruntime.SessionSpec{Language: "python"}, outcome, "ok")
	if err != nil || preflight["status"] != "code_preflight_required" || preflight["executed"] != false {
		t.Fatalf("legacy preflight parser=%#v err=%v", preflight, err)
	}
	outcome.Response.Stdout = "submitted code ran"
	outcome.Response.Preflight["diagnostics"] = []any{map[string]any{"code": "python_syntax_invalid"}}
	if _, err := agentKernelPythonCodePreflight(kernelruntime.SessionSpec{Language: "python"}, outcome, "ok"); err == nil {
		t.Fatal("ambiguous legacy preflight and executed stdout was accepted")
	}
}

func TestAgentKernelDescriptionsDeclareSynonDocsFirstAndEnvironmentBoundaries(t *testing.T) {
	for _, required := range []string{"persistent Synon kernel", "standard Python", "not IPython or Jupyter", "%run", "python script.py", "Load its Skill", "inspect the installed API", "failed cell ends", "manage_environments", "manage_packages", "DataFrame.to_csv", "DataFrame.to_markdown", "tabulate"} {
		if !strings.Contains(agentKernelPythonDescription, required) {
			t.Fatalf("python description is missing %q: %s", required, agentKernelPythonDescription)
		}
	}
	for _, required := range []string{"host.artifact_path", "ltr-*", "/api/artifacts/", "filesystem path"} {
		if !strings.Contains(agentKernelPythonDescription, required) {
			t.Fatalf("python description is missing artifact reference boundary %q: %s", required, agentKernelPythonDescription)
		}
	}
	for name, description := range map[string]string{"bash": agentKernelBashDescription, "r": agentKernelRDescription} {
		for _, required := range []string{"task-relative workspace files", "{{artifact:VERSION_ID}}", "/api/artifacts/", "remain unchanged"} {
			if !strings.Contains(description, required) {
				t.Fatalf("%s description is missing source-identity artifact boundary %q: %s", name, required, description)
			}
		}
	}
	for _, required := range []string{"network-isolated", "independently injected synon host bridge", "raw sockets", "urllib", "download_public_scientific_file", "search_skills", "advisory", "live connector snapshot", "manage_environments", "manage_packages", "failed host operation ends"} {
		if !strings.Contains(strings.ToLower(agentKernelReplDescription), required) {
			t.Fatalf("repl description is missing %q: %s", required, agentKernelReplDescription)
		}
	}
}

func TestAgentKernelReplContractAliasesPreserveScientificResultFields(t *testing.T) {
	input := map[string]any{"code": `import host
result = host.mcp("structures-interactions", "pdb_get_structures", pdb_ids=["1ABC"])
host.global_data["result"] = result
print(result["records"][0]["coordinate_url"])
print(result["records"][0]["coordinate_files"].get("pdb_url"))`}
	normalized := normalizeAgentKernelReplContractAliases(input)
	code := stringValue(normalized["code"])
	for _, forbidden := range []string{"host.global_data"} {
		if strings.Contains(code, forbidden) {
			t.Fatalf("normalized code retains %q: %s", forbidden, code)
		}
	}
	for _, required := range []string{"globals().setdefault", "_synon_task_state", "coordinate_url", "coordinate_files", "pdb_url"} {
		if !strings.Contains(code, required) {
			t.Fatalf("normalized code is missing %q: %s", required, code)
		}
	}
	canonical := map[string]any{"code": `result = {"ok": True}`}
	if normalized := normalizeAgentKernelReplContractAliases(canonical); stringValue(normalized["code"]) != stringValue(canonical["code"]) {
		t.Fatalf("canonical code changed: %#v", normalized)
	}
}

func TestAgentKernelExecutionCompatibilityDoesNotReplaceDurableAuthority(t *testing.T) {
	originalCode := `import host
host.global_data["result"] = {"ok": True}`
	authority, execution := agentKernelAuthorityAndExecutionInputs("repl", map[string]any{
		"code": originalCode, "human_description": "Saving task state",
	})
	if stringValue(authority["code"]) != originalCode {
		t.Fatalf("durable authority changed: %#v", authority)
	}
	if stringValue(execution["code"]) == originalCode || strings.Contains(stringValue(execution["code"]), "host.global_data") {
		t.Fatalf("execution compatibility input was not normalized independently: %#v", execution)
	}
}

func TestAgentKernelOptionalFormatterPreflightPreventsDisplayDependencyFailure(t *testing.T) {
	for _, tool := range []string{"python", "repl"} {
		preflight := agentKernelOptionalFormatterPreflight(tool, map[string]any{
			"code": "result = calculate()\nprint(result.to_markdown(index=False))\nresult.to_csv('result.csv', index=False)",
		})
		if preflight == nil || preflight["status"] != "code_preflight_required" || preflight["executed"] != false ||
			!strings.Contains(stringValue(preflight["recovery"]), "to_csv") {
			t.Fatalf("tool=%s optional formatter preflight=%#v", tool, preflight)
		}
	}
	if allowed := agentKernelOptionalFormatterPreflight("python", map[string]any{
		"code": "result = calculate()\nprint(result.to_string(index=False))\nresult.to_csv('result.csv', index=False)",
	}); allowed != nil {
		t.Fatalf("portable formatter was blocked: %#v", allowed)
	}
	fixture := newAgentSaveArtifactsFixture(t)
	result, err := fixture.server.executeAgentKernelTool(
		context.Background(), fixture.identity, "python",
		map[string]any{
			"code":        "import pandas as pd\nprint(pd.DataFrame({'x':[1]}).to_markdown(index=False))",
			"environment": agentKernelManagedPythonEnvironment, "human_description": "Formatting a result table",
		},
	)
	if err != nil || result["status"] != "code_preflight_required" || result["executed"] != false {
		t.Fatalf("integrated optional formatter preflight=%#v err=%v", result, err)
	}
	var operations int
	if err := fixture.db.QueryRow(`SELECT COUNT(*) FROM kernel_local_operations`).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("optional formatter preflight created operations=%d err=%v", operations, err)
	}
}

func TestAgentKernelPythonFileHandleShadowPreflightPreventsLateCellFailure(t *testing.T) {
	preflight := agentKernelPythonFileHandleShadowPreflight("python", map[string]any{
		"code": "bioavailability = 0.4\nwith open('report.txt', 'w') as bioavailability:\n    bioavailability.write('result')\nprint(bioavailability * 100)",
	})
	if preflight == nil || preflight["status"] != "code_preflight_required" || preflight["executed"] != false ||
		!strings.Contains(stringValue(preflight["recovery"]), "distinct descriptive name") {
		t.Fatalf("file-handle shadow preflight=%#v", preflight)
	}
	if allowed := agentKernelPythonFileHandleShadowPreflight("python", map[string]any{
		"code": "bioavailability = 0.4\nwith open('report.txt', 'w') as report_file:\n    report_file.write(str(bioavailability))",
	}); allowed != nil {
		t.Fatalf("distinct file handle was blocked: %#v", allowed)
	}
	fixture := newAgentSaveArtifactsFixture(t)
	result, err := fixture.server.executeAgentKernelTool(
		context.Background(), fixture.identity, "python",
		map[string]any{
			"code":        "fraction = 0.4\nwith open('result.txt', 'w') as fraction:\n    fraction.write('x')\nprint(fraction * 100)",
			"environment": agentKernelManagedPythonEnvironment, "human_description": "Writing calculated results",
		},
	)
	if err != nil || result["status"] != "code_preflight_required" || result["executed"] != false {
		t.Fatalf("integrated file-handle shadow preflight=%#v err=%v", result, err)
	}
}

func TestAgentKernelLargeToolResultRoutesThroughReadFileBeforeExecution(t *testing.T) {
	input := map[string]any{
		"code": `import json
artifact_path = "./large-tool-result-0123456789abcdef"
with open(artifact_path) as handle:
    payload = json.load(handle)`,
	}
	for _, tool := range []string{"python", "repl"} {
		preflight := agentKernelLargeToolResultPathPreflight(tool, input)
		if preflight == nil || preflight["status"] != "code_preflight_required" || preflight["executed"] != false ||
			!strings.Contains(stringValue(preflight["recovery"]), "read_file") {
			t.Fatalf("tool=%s preflight=%#v", tool, preflight)
		}
	}
	if allowed := agentKernelLargeToolResultPathPreflight("python", map[string]any{
		"code": `from read_file import read_file
payload = read_file("large-tool-result-0123456789abcdef")`,
	}); allowed != nil {
		t.Fatalf("bounded read_file path was blocked: %#v", allowed)
	}
	if allowed := agentKernelLargeToolResultPathPreflight("repl", map[string]any{
		"code": `import host
large_path = host.artifact_path("large-tool-result-0123456789abcdef")
with open("ordinary-input.csv") as handle:
    ordinary = handle.read()`,
	}); allowed != nil {
		t.Fatalf("authorized artifact materialization was confused with a direct large-result path: %#v", allowed)
	}
}

func TestAgentKernelPythonDescriptionPreservesManagedEnvironmentSuccessor(t *testing.T) {
	for _, marker := range []string{"immutable successor", "discard the source environment name", "environment.name", "every subsequent Python call"} {
		if !strings.Contains(agentKernelPythonDescription, marker) {
			t.Fatalf("python tool description is missing %q", marker)
		}
	}
}

func TestAgentKernelMCPCatalogRequiresServerBeforeExecution(t *testing.T) {
	preflight := agentKernelMCPCatalogCallPreflight("repl", map[string]any{
		"code": "methods = host.mcp.list_methods()",
	})
	if preflight == nil || preflight["status"] != "code_preflight_required" || preflight["executed"] != false ||
		!strings.Contains(stringValue(preflight["recovery"]), "list_servers") {
		t.Fatalf("catalog preflight=%#v", preflight)
	}
	if allowed := agentKernelMCPCatalogCallPreflight("repl", map[string]any{
		"code": `methods = host.mcp.list_methods("clinical-trials")`,
	}); allowed != nil {
		t.Fatalf("server-scoped catalog call was blocked: %#v", allowed)
	}
}
