package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const agentRuntimePythonCompileScript = `import json, sys, warnings
source = sys.stdin.read()
warnings.simplefilter("error", SyntaxWarning)
try:
    compile(source, "<agent-cell>", "exec")
except (SyntaxError, SyntaxWarning) as exc:
    print(json.dumps({"message": exc.msg, "line": exc.lineno, "offset": exc.offset}))
    raise SystemExit(2)
`

// agentRuntimePythonSyntaxPreflight compiles model-authored Python without
// executing it. This moves a deterministic parser failure before the durable
// tool-start boundary so the model can correct it without creating a false
// runtime failure or side effect.
func agentRuntimePythonSyntaxPreflight(publicName string, input map[string]any) map[string]any {
	name := strings.ToLower(strings.TrimSpace(publicName))
	if name != "python" && name != "repl" {
		return nil
	}
	code := stringValue(input["code"])
	if strings.TrimSpace(code) == "" {
		return nil
	}
	executable := agentRuntimePythonCompilerExecutable()
	if executable == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-I", "-S", "-c", agentRuntimePythonCompileScript)
	command.Stdin = strings.NewReader(code)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &bytes.Buffer{}
	err := command.Run()
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	diagnostic := struct {
		Message string `json:"message"`
		Line    int    `json:"line"`
		Offset  int    `json:"offset"`
	}{}
	if json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &diagnostic) != nil || strings.TrimSpace(diagnostic.Message) == "" {
		// Interpreter availability failures are runtime health concerns, not proof
		// that the model code is invalid, so they do not reject the call here.
		return nil
	}
	return map[string]any{
		"ok": false, "status": "python_syntax_preflight_required", "executed": false,
		"message":  fmt.Sprintf("Python syntax validation failed at line %d, column %d: %s.", diagnostic.Line, diagnostic.Offset, strings.TrimSpace(diagnostic.Message)),
		"recovery": "Correct only the reported syntax in one new standard-Python cell. If the cell primarily authors a text, Markdown, JSON, or CSV deliverable, switch to the advertised file-editing tool with validated literal content instead of rebuilding a long document inside Python. Do not repeat the same source and do not use IPython or Jupyter magics.",
	}
}

func agentRuntimePythonCompilerExecutable() string {
	candidates := []string{"python3", "python"}
	if runtime.GOOS == "windows" {
		candidates = []string{"python.exe", "python3.exe", "python"}
	}
	for _, candidate := range candidates {
		if resolved, err := exec.LookPath(candidate); err == nil {
			return resolved
		}
	}
	return ""
}
