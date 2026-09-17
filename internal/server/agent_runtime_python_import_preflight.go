package server

import (
	"regexp"
	"sort"
	"strings"
)

var agentRuntimeExplicitPythonModules = []string{
	"asyncio", "collections", "csv", "datetime", "functools", "hashlib", "itertools", "json",
	"math", "os", "pathlib", "random", "re", "shutil", "statistics", "subprocess", "sys", "tempfile", "time",
}

// agentRuntimePythonExplicitModulePreflight rejects a small, statically
// decidable class of NameError before a public tool lifecycle begins. Requiring
// an explicit import in the current cell avoids depending on accidental kernel
// history and remains valid for both persistent and replaced workers.
func agentRuntimePythonExplicitModulePreflight(publicName string, input map[string]any) map[string]any {
	name := strings.ToLower(strings.TrimSpace(publicName))
	if name != "python" && name != "repl" {
		return nil
	}
	code := pythonCodeWithoutStringsOrComments(stringValue(input["code"]))
	if strings.TrimSpace(code) == "" {
		return nil
	}
	missing := make([]string, 0, 4)
	for _, module := range agentRuntimeExplicitPythonModules {
		receiver := regexp.MustCompile(`\b` + regexp.QuoteMeta(module) + `\s*\.`)
		if !receiver.MatchString(code) {
			continue
		}
		imported := regexp.MustCompile(`(?m)^\s*(?:import\s+[^\n#]*\b` + regexp.QuoteMeta(module) + `\b|from\s+` + regexp.QuoteMeta(module) + `(?:\.|\s))`).MatchString(code)
		assigned := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(module) + `\s*=`).MatchString(code)
		if !imported && !assigned {
			missing = append(missing, module)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return map[string]any{
		"ok": false, "status": "python_import_preflight_required", "executed": false,
		"message":  "The Python cell uses modules without importing or defining them in this cell: " + strings.Join(missing, ", ") + ".",
		"recovery": "Add explicit imports for the listed modules in the same cell, then issue one corrected call. Do not rely on imports from an earlier kernel process or retry the unchanged cell.",
	}
}
