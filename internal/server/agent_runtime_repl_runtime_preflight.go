package server

import (
	"sort"
	"strings"
)

var agentRuntimeREPLStandardModules = map[string]struct{}{
	"abc": {}, "argparse": {}, "asyncio": {}, "base64": {}, "collections": {}, "concurrent": {},
	"contextlib": {}, "copy": {}, "csv": {}, "dataclasses": {}, "datetime": {}, "decimal": {},
	"difflib": {}, "enum": {}, "functools": {}, "glob": {}, "gzip": {}, "hashlib": {}, "heapq": {},
	"html": {}, "http": {}, "importlib": {}, "inspect": {}, "io": {}, "itertools": {}, "json": {},
	"linecache": {}, "logging": {}, "math": {}, "mimetypes": {}, "operator": {}, "os": {},
	"pathlib": {}, "pickle": {}, "platform": {}, "pprint": {}, "queue": {}, "random": {}, "re": {},
	"read_file": {}, "shlex": {}, "shutil": {}, "signal": {}, "socket": {}, "sqlite3": {}, "statistics": {},
	"string": {}, "struct": {}, "subprocess": {}, "sys": {}, "tempfile": {}, "textwrap": {}, "threading": {},
	"time": {}, "traceback": {}, "types": {}, "typing": {}, "unicodedata": {}, "urllib": {}, "uuid": {},
	"warnings": {}, "xml": {}, "zipfile": {}, "host": {},
}

// agentRuntimeREPLThirdPartyImportPreflight keeps the stdlib-only control
// kernel from becoming an accidental scientific-compute path. The decision is
// deterministic and task-independent, so it remains active while a recovery
// runner is being rebound to its durable task state.
func agentRuntimeREPLThirdPartyImportPreflight(publicName string, input map[string]any) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(publicName), "repl") {
		return nil
	}
	modules := agentKernelPythonImportedTopLevelModules(stringValue(input["code"]))
	thirdParty := make([]string, 0)
	for _, module := range modules {
		if _, standard := agentRuntimeREPLStandardModules[module]; !standard {
			thirdParty = append(thirdParty, module)
		}
	}
	if len(thirdParty) == 0 {
		return nil
	}
	sort.Strings(thirdParty)
	return map[string]any{
		"ok": false, "schema": "synon.python-code-preflight.v1",
		"status": "code_preflight_required", "executed": false,
		"message": "Third-party Python imports are unavailable in the stdlib-only repl control kernel: " +
			strings.Join(thirdParty, ", ") + ".",
		"recovery": "Use manage_environments to select a verified environment, then run this scientific code with the python tool and that environment. No code ran in repl.",
		"diagnostics": []any{map[string]any{
			"code": "python_imported_package_requires_managed_runtime", "modules": thirdParty,
		}},
	}
}
