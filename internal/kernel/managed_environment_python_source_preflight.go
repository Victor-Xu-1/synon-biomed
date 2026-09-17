package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

const managedPythonSourceImportProbeScript = `import ast, contextlib, importlib, io, json, sys
source = sys.stdin.read()
try:
    tree = ast.parse(source, filename="<agent-cell>", mode="exec")
except SyntaxError:
    print(json.dumps({"ok": False, "error": "syntax"}, sort_keys=True))
    raise SystemExit(0)

bindings = {}
probes = {}

def add_probe(module, attrs):
    if not module or module.startswith("."):
        return
    target = ".".join([module] + list(attrs))
    if len(probes) < 64:
        probes[target] = (module, tuple(attrs))

for node in ast.walk(tree):
    if isinstance(node, ast.Import):
        for alias in node.names:
            parts = alias.name.split(".")
            local = alias.asname or parts[0]
            if alias.asname:
                bindings[local] = (alias.name, ())
            else:
                bindings[local] = (parts[0], tuple(parts[1:]))
            add_probe(alias.name, ())
    elif isinstance(node, ast.ImportFrom) and node.level == 0 and node.module:
        for alias in node.names:
            if alias.name == "*":
                continue
            local = alias.asname or alias.name
            bindings[local] = (node.module, (alias.name,))
            add_probe(node.module, (alias.name,))

for node in ast.walk(tree):
    if not isinstance(node, ast.Attribute):
        continue
    attrs = []
    current = node
    while isinstance(current, ast.Attribute):
        attrs.append(current.attr)
        current = current.value
    if not isinstance(current, ast.Name) or current.id not in bindings:
        continue
    module, prefix = bindings[current.id]
    add_probe(module, tuple(prefix) + tuple(reversed(attrs)))

missing = []
sink = io.StringIO()
for target, (module, attrs) in probes.items():
    try:
        with contextlib.redirect_stdout(sink), contextlib.redirect_stderr(sink):
            value = importlib.import_module(module)
            for attr in attrs:
                value = getattr(value, attr)
    except BaseException:
        missing.append(target)
print(json.dumps({"ok": True, "missing": sorted(set(missing))}, sort_keys=True))
`

// ManagedPythonSourcePreflight is the bounded API-availability witness for one
// model-authored cell in the exact managed environment selected for execution.
// It contains identifiers only; code and interpreter paths never leave the
// kernel boundary.
type ManagedPythonSourcePreflight struct {
	Missing []string `json:"missing,omitempty"`
}

// PreflightManagedPythonSource parses imports and imported attribute access in
// the selected immutable environment before a public tool lifecycle begins.
// The check is generic: it asks the environment what exists instead of keeping
// a task-, package-, version-, or API-specific compatibility blacklist.
func (m *Manager) PreflightManagedPythonSource(
	ctx context.Context,
	environment string,
	source string,
) (ManagedPythonSourcePreflight, error) {
	if strings.TrimSpace(source) == "" {
		return ManagedPythonSourcePreflight{}, nil
	}
	if len(source) > maxManagedEnvironmentInputBytes {
		return ManagedPythonSourcePreflight{}, errors.New("Python source preflight input is too large")
	}
	prefix, err := m.managedPythonWitnessPrefix(environment)
	if err != nil {
		return ManagedPythonSourcePreflight{}, err
	}
	python, err := managedPythonExecutableAtPrefix(prefix)
	if err != nil {
		return ManagedPythonSourcePreflight{}, err
	}
	return runManagedPythonSourceImportProbe(ctx, python, prefix, source)
}

func runManagedPythonSourceImportProbe(
	ctx context.Context,
	python string,
	prefix string,
	source string,
) (ManagedPythonSourcePreflight, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command := newWorkerProcessCommand(ctx, python, "-I", "-c", managedPythonSourceImportProbeScript)
	if strings.TrimSpace(prefix) != "" {
		command.Env = managedEnvironmentRuntimeEnv(prefix)
	}
	command.Stdin = strings.NewReader(source)
	var stdout bytes.Buffer
	stderr := newTailBuffer(maxDiagnosticBytes)
	command.Stdout, command.Stderr = &stdout, stderr
	if err := runWorkerProcess(command); err != nil {
		return ManagedPythonSourcePreflight{}, fmt.Errorf(
			"managed Python source preflight failed: %w: %s",
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	var envelope struct {
		OK      bool     `json:"ok"`
		Error   string   `json:"error"`
		Missing []string `json:"missing"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &envelope); err != nil {
		return ManagedPythonSourcePreflight{}, errors.New("managed Python source preflight returned an invalid result")
	}
	if !envelope.OK {
		return ManagedPythonSourcePreflight{}, errors.New("managed Python source preflight could not parse the cell")
	}
	if len(envelope.Missing) > 64 {
		return ManagedPythonSourcePreflight{}, errors.New("managed Python source preflight returned too many identifiers")
	}
	missing := make([]string, 0, len(envelope.Missing))
	for _, target := range envelope.Missing {
		target = strings.TrimSpace(target)
		if !managedImportName.MatchString(target) {
			return ManagedPythonSourcePreflight{}, errors.New("managed Python source preflight returned an invalid identifier")
		}
		missing = append(missing, target)
	}
	sort.Strings(missing)
	unique := missing[:0]
	for _, target := range missing {
		if len(unique) == 0 || unique[len(unique)-1] != target {
			unique = append(unique, target)
		}
	}
	return ManagedPythonSourcePreflight{Missing: unique}, nil
}
