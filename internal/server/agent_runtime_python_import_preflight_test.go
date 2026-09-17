package server

import (
	"strings"
	"testing"
)

func TestPythonExplicitModulePreflightRejectsMissingImportAndAvoidsTextFalsePositive(t *testing.T) {
	blocked := agentRuntimePythonExplicitModulePreflight("repl", map[string]any{
		"code": `import host
result = host.mcp("genes", "lookup", accession="Q96SW2")
print(json.dumps(result))`,
	})
	if blocked == nil || blocked["status"] != "python_import_preflight_required" ||
		!strings.Contains(stringValue(blocked["message"]), "json") {
		t.Fatalf("missing import preflight=%#v", blocked)
	}
	for _, code := range []string{
		`import host, json
print(json.dumps({"ok": True}))`,
		`from json import dumps
print(dumps({"text": "os.path and json.dumps are data"}))`,
		`# json.dumps(result) is only a comment
print("json.dumps and os.path are only text")`,
	} {
		if diagnostic := agentRuntimePythonExplicitModulePreflight("python", map[string]any{"code": code}); diagnostic != nil {
			t.Fatalf("valid or non-code module reference was blocked for %q: %#v", code, diagnostic)
		}
	}
}
