package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"sort"
	"strings"
	"unicode"

	kernelruntime "synon-go/internal/kernel"
)

func agentKernelPythonCodePreflight(
	spec kernelruntime.SessionSpec,
	outcome kernelruntime.ExecutionOutcome,
	exitStatus string,
) (map[string]any, error) {
	if strings.EqualFold(strings.TrimSpace(spec.KernelKind), "bash") {
		return nil, nil
	}
	preflight := outcome.Response.Preflight
	if len(preflight) == 0 {
		return nil, nil
	}
	diagnostics := agentKernelPythonCodePreflightDiagnostics(preflight)
	if strings.ToLower(strings.TrimSpace(spec.Language)) != "python" ||
		strings.TrimSpace(exitStatus) != "ok" ||
		len(outcome.FilesWritten) != 0 || stringValue(preflight["schema"]) != "synon.python-code-preflight.v1" ||
		stringValue(preflight["status"]) != "code_preflight_required" || boolValue(preflight["executed"], true) ||
		!agentKernelPythonPreflightAllowsCapturedStdout(diagnostics, outcome.Response.Stdout) ||
		strings.TrimSpace(outcome.Response.Stderr) != "" {
		return nil, errors.New("kernel Python code preflight result is invalid")
	}
	if len(diagnostics) == 0 || strings.TrimSpace(stringValue(preflight["message"])) == "" ||
		strings.TrimSpace(stringValue(preflight["recovery"])) == "" {
		return nil, errors.New("kernel Python code preflight result is incomplete")
	}
	return copyMapAny(preflight), nil
}

func agentKernelPythonCodePreflightDiagnostics(preflight map[string]any) []any {
	diagnostics, ok := preflight["diagnostics"].([]any)
	if !ok {
		encoded, err := json.Marshal(preflight["diagnostics"])
		if err == nil {
			_ = json.Unmarshal(encoded, &diagnostics)
		}
	}
	return diagnostics
}

func agentKernelPythonPreflightAllowsCapturedStdout(diagnostics []any, stdout string) bool {
	if strings.TrimSpace(stdout) == "" {
		return true
	}
	return pythonPreflightDiagnosticsAreImportOnly(diagnostics)
}

func pythonPreflightDiagnosticsAreImportOnly(diagnostics []any) bool {
	if len(diagnostics) == 0 {
		return false
	}
	for _, raw := range diagnostics {
		diagnostic, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		code := strings.ToLower(strings.TrimSpace(stringValue(diagnostic["code"])))
		if !strings.HasPrefix(code, "python_import_") && !strings.HasPrefix(code, "python_imported_") {
			return false
		}
	}
	return true
}

// agentKernelExecutionFailureCode classifies only stable interpreter exception
// types. Traceback messages, paths, source code, and task data remain confined
// to stderr and never enter lifecycle summaries.
func agentKernelExecutionFailureCode(language, exitStatus, stderr string) string {
	if strings.TrimSpace(exitStatus) == "ok" || strings.ToLower(strings.TrimSpace(language)) != "python" {
		return ""
	}
	candidates := []struct {
		marker string
		code   string
	}{
		{"kernel worker was killed — possibly out of memory", "python_memory_exhausted"},
		{"kernel worker stopped: signal: killed", "python_memory_exhausted"},
		{"FileNotFoundError:", "python_file_not_found"},
		{"ModuleNotFoundError:", "python_module_not_found"},
		{"PermissionError:", "python_permission_denied"},
		{"MemoryError:", "python_memory_exhausted"},
		{"TimeoutError:", "python_timeout"},
		{"ImportError:", "python_import_failed"},
		{"SyntaxError:", "python_syntax_error"},
		{"IndentationError:", "python_indentation_error"},
		{"AttributeError:", "python_attribute_error"},
		{"TypeError:", "python_type_error"},
		{"ValueError:", "python_value_error"},
	}
	lowerStderr := strings.ToLower(stderr)
	if strings.Contains(lowerStderr, "possibly out of memory") || strings.Contains(lowerStderr, "out of memory") ||
		strings.Contains(lowerStderr, "kernel worker exited unexpectedly with exit code 137") {
		return "python_memory_exhausted"
	}
	lines := strings.Split(stderr, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		for _, candidate := range candidates {
			if strings.HasPrefix(line, candidate.marker) {
				return candidate.code
			}
		}
	}
	return "python_execution_failed"
}

// agentKernelEnvironmentFailureCode recognizes only stable runtime-level
// incompatibility families. It deliberately ignores task-domain exceptions:
// a failed scientific input must not condemn an otherwise healthy immutable
// environment generation.
func agentKernelEnvironmentFailureCode(exitStatus, stderr string) string {
	if strings.TrimSpace(exitStatus) == "ok" {
		return ""
	}
	lower := strings.ToLower(stderr)
	for _, marker := range []string{
		"does not contain the observed gpu architecture",
		"no kernel image is available for execution on the device",
		"unsupported gpu architecture",
		"gpu architecture is unsupported",
		"device kernel image is invalid",
		"invalid device function",
	} {
		if strings.Contains(lower, marker) {
			return "managed_environment_accelerator_incompatible"
		}
	}
	for _, marker := range []string{
		"undefined symbol:",
		"version `glibcxx_",
		"version 'glibcxx_",
		"cannot open shared object file: no such file or directory",
	} {
		if strings.Contains(lower, marker) {
			return "managed_environment_binary_incompatible"
		}
	}
	return ""
}

func applyAgentKernelFailureRecovery(result map[string]any, code string) {
	if result == nil || strings.TrimSpace(code) == "" {
		return
	}
	result["code"] = code
	if code == "python_memory_exhausted" {
		result["recovery"] = "The computation exceeded available memory. Do not rerun the same working set, choose an arbitrary fraction, or merely rename the environment. Use the machine resource snapshot and the prior peak memory. Start with one independent input unit, persist its result, release its large objects, and use that measured peak to choose a safe sparse, streaming, chunked, or sequential batch size; checkpoint every completed batch, or select an admitted compute route with sufficient memory."
		result["recoverable"] = true
	}
	if code == "managed_environment_accelerator_incompatible" {
		result["recovery"] = "The active immutable environment generation proved unable to execute on the observed accelerator. Preserve the selected scientific implementation, inputs, and completed artifacts. Do not add downstream packages to or reuse this same generation. Re-run managed environment inventory and preflight, then create or fork one new immutable generation whose complete framework, accelerator runtime, and compiled-extension plan is derived from current verified sources. If the compatible build is absent from one package authority, change authority or installation phases instead of lowering the established compatibility target. Validate imports and one real accelerator operation before using it."
		result["recoverable"] = true
		result["environment_incompatible"] = true
	}
	if code == "managed_environment_binary_incompatible" {
		result["recovery"] = "The active immutable environment generation has a binary or shared-library incompatibility. Preserve the selected implementation and completed artifacts, inspect its exact package inventory, and create or fork a coherent replacement generation from verified sources. Do not patch the active generation in place or hide the failing import. Validate imports and the documented workload witness before reuse."
		result["recoverable"] = true
		result["environment_incompatible"] = true
	}
}

func agentKernelWorkspacePolicyWrites(files []kernelruntime.FileWrite) []kernelruntime.FileWrite {
	result := make([]kernelruntime.FileWrite, 0)
	for _, file := range files {
		if strings.TrimSpace(file.PolicyCode) != "" {
			result = append(result, file)
		}
	}
	return result
}

func agentKernelWorkspacePolicyPayload(files []kernelruntime.FileWrite) []map[string]any {
	result := make([]map[string]any, 0, len(files))
	for _, file := range files {
		result = append(result, map[string]any{
			"path": file.Path, "code": file.PolicyCode, "action": file.PolicyAction, "message": file.PolicyMessage,
		})
	}
	return result
}

func agentKernelWorkspacePolicyRecovery(files []kernelruntime.FileWrite) string {
	paths := make([]string, 0, len(files))
	seen := map[string]struct{}{}
	for _, file := range files {
		path := strings.TrimSpace(file.Path)
		if path == "" {
			continue
		}
		if _, found := seen[path]; found {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return fmt.Sprintf("Workspace policy rejected new JSON/JSONL scientific output (%s). The runtime removed the file before publishing the write receipt. Moving the file to handoff/ or any other persistent workspace directory does not make JSON allowed; do not retry the same content under another .json/.jsonl path. Write the user-facing result as Markdown (.md), CSV (.csv), HTML, or another scientific format now. Protocol-only JSON belongs only in a runtime-provided ephemeral directory outside the workspace. This cell failed recoverably, so continue the same task with an allowed format.", strings.Join(paths, ", "))
}

// agentKernelOptionalFormatterPreflight keeps nonessential presentation
// helpers from aborting a complete scientific cell after its calculations have
// already begun. Markdown formatting through pandas depends on the optional
// tabulate package; portable CSV, to_string, or a short native table preserves
// the same scientific result without coupling execution to that formatter.
func agentKernelOptionalFormatterPreflight(publicName string, input map[string]any) map[string]any {
	if publicName != "python" && publicName != "repl" {
		return nil
	}
	code := stringValue(input["code"])
	if !strings.Contains(code, ".to_markdown(") {
		return nil
	}
	return map[string]any{
		"ok": false, "schema": "synon.python-code-preflight.v1",
		"status": "code_preflight_required", "executed": false,
		"message":  "The Python cell depends on the optional pandas tabulate formatter and was not executed.",
		"recovery": "Replace DataFrame.to_markdown with DataFrame.to_csv, DataFrame.to_string, or a short native Markdown formatter, then run the complete corrected cell once. Presentation-only formatting must not be able to abort calculation or output files.",
		"diagnostics": []any{map[string]any{
			"code": "python_optional_formatter_not_portable", "formatter": "DataFrame.to_markdown",
		}},
	}
}

var (
	agentKernelLargeResultLiteralPathPattern = regexp.MustCompile(`(?im)(?:^|[^A-Za-z0-9_])(?:open|Path)\(\s*["'](?:\./)?large-tool-result-[^"']*["']`)
	agentKernelLargeResultAssignmentPattern  = regexp.MustCompile(`(?m)^\s*([A-Za-z_]\w*)\s*=\s*["'](?:\./)?large-tool-result-[^"']*["']\s*$`)
)

func agentKernelLargeToolResultPathPreflight(publicName string, input map[string]any) map[string]any {
	if publicName != "python" && publicName != "repl" {
		return nil
	}
	code := stringValue(input["code"])
	directPath := agentKernelLargeResultLiteralPathPattern.MatchString(code)
	if !directPath {
		for _, match := range agentKernelLargeResultAssignmentPattern.FindAllStringSubmatch(code, -1) {
			if len(match) != 2 {
				continue
			}
			consumer := regexp.MustCompile(`(?m)(?:open|Path)\(\s*` + regexp.QuoteMeta(match[1]) + `(?:\s*[,\)])`)
			if consumer.MatchString(code) {
				directPath = true
				break
			}
		}
	}
	if !directPath {
		return nil
	}
	return map[string]any{
		"ok": false, "schema": "synon.python-code-preflight.v1",
		"status": "code_preflight_required", "executed": false,
		"message":  "An oversized tool result is an immutable artifact version, not a task-relative filesystem path.",
		"recovery": "Import read_file from the bounded read_file compatibility module and call read_file with the exact large-tool-result-* version ID, then parse the returned text. Do not prefix it with ./, pass it to open/Path, or guess a storage path. No code ran.",
		"diagnostics": []any{map[string]any{
			"code": "python_artifact_version_requires_read_file",
		}},
	}
}

var agentKernelMCPListMethodsWithoutServerPattern = regexp.MustCompile(`host\.mcp\.list_methods\(\s*\)`)

var (
	agentKernelPythonSimpleAssignmentPattern = regexp.MustCompile(`(?m)^\s*([A-Za-z_]\w*)\s*=[^=]`)
	agentKernelPythonOpenHandlePattern       = regexp.MustCompile(`(?m)^\s*with\s+open\([^\n]*\)\s+as\s+([A-Za-z_]\w*)\s*:`)
)

// agentKernelPythonFileHandleShadowPreflight prevents a complete scientific
// cell from failing late because a data variable is reused as a file handle.
// The check is syntax-shape based and domain neutral; it does not inspect task
// values or prescribe filenames, calculations, or a workflow.
func agentKernelPythonFileHandleShadowPreflight(publicName string, input map[string]any) map[string]any {
	if publicName != "python" && publicName != "repl" {
		return nil
	}
	code := stringValue(input["code"])
	assignments := agentKernelPythonSimpleAssignmentPattern.FindAllStringSubmatchIndex(code, -1)
	for _, handle := range agentKernelPythonOpenHandlePattern.FindAllStringSubmatchIndex(code, -1) {
		if len(handle) < 4 {
			continue
		}
		name := code[handle[2]:handle[3]]
		for _, assignment := range assignments {
			if len(assignment) >= 4 && assignment[0] < handle[0] && code[assignment[2]:assignment[3]] == name {
				return map[string]any{
					"ok": false, "schema": "synon.python-code-preflight.v1",
					"status": "code_preflight_required", "executed": false,
					"message":  "The Python cell reuses data variable " + name + " as a file handle and was not executed.",
					"recovery": "Rename the file handle to a distinct descriptive name, keep the original data variable unchanged, then run the complete corrected cell once.",
					"diagnostics": []any{map[string]any{
						"code": "python_file_handle_shadows_data_variable", "variable": name,
					}},
				}
			}
		}
	}
	return nil
}

func agentKernelMCPCatalogCallPreflight(publicName string, input map[string]any) map[string]any {
	if publicName != "repl" || !agentKernelMCPListMethodsWithoutServerPattern.MatchString(stringValue(input["code"])) {
		return nil
	}
	return map[string]any{
		"ok": false, "schema": "synon.python-code-preflight.v1",
		"status": "code_preflight_required", "executed": false,
		"message":  "host.mcp.list_methods requires one non-empty server name.",
		"recovery": "Call host.mcp.list_servers() first; it returns a list of server-name strings. Then call host.mcp.list_methods(server) once for each required server and iterate the returned method dictionaries directly. Do not call .keys() on either list. No code ran.",
		"diagnostics": []any{map[string]any{
			"code": "python_mcp_catalog_server_required",
		}},
	}
}

func agentKernelPythonImportedTopLevelModules(source string) []string {
	seen := make(map[string]struct{})
	for _, rawLine := range strings.Split(source, "\n") {
		line := strings.TrimSpace(strings.SplitN(rawLine, "#", 2)[0])
		if strings.HasPrefix(line, "from ") {
			fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "from ")))
			if len(fields) > 0 && !strings.HasPrefix(fields[0], ".") {
				module := strings.SplitN(pythonImportModuleToken(fields[0]), ".", 2)[0]
				if module != "" {
					seen[module] = struct{}{}
				}
			}
			continue
		}
		if !strings.HasPrefix(line, "import ") {
			continue
		}
		for _, item := range strings.Split(strings.TrimSpace(strings.TrimPrefix(line, "import ")), ",") {
			fields := strings.Fields(strings.TrimSpace(item))
			if len(fields) == 0 {
				continue
			}
			module := strings.SplitN(pythonImportModuleToken(fields[0]), ".", 2)[0]
			if module != "" {
				seen[module] = struct{}{}
			}
		}
	}
	return uniqueSortedFoldedMapKeys(seen)
}

// pythonImportModuleToken extracts the module token from the first import
// clause without treating statement separators as part of the module name.
// Python permits several statements on one line (for example, "import time;
// time.sleep(1)"); the preflight must recognize the same stdlib module as a
// newline-delimited import. This parser remains lexical and bounded: it does
// not execute model-authored code or maintain a package blacklist.
func pythonImportModuleToken(value string) string {
	value = strings.TrimSpace(value)
	for index, character := range value {
		if unicode.IsSpace(character) || character == ';' || character == ',' || character == '(' || character == ')' || character == ':' {
			value = value[:index]
			break
		}
	}
	return strings.Trim(value, ".")
}

func uniqueSortedFoldedMapKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		return strings.ToLower(result[i]) < strings.ToLower(result[j])
	})
	return result
}
