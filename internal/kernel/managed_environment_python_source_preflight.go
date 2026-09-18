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
from_bindings = {}

def add_probe(module, attrs):
    if not module or module.startswith("."):
        return
    target = ".".join([module] + list(attrs))
    if len(probes) < 64:
        probes[target] = (module, tuple(attrs))

def probe_attributes(statement):
    for node in ast.walk(statement):
        if not isinstance(node, ast.Attribute) or not isinstance(node.ctx, ast.Load):
            continue
        attrs = []
        current = node
        while isinstance(current, ast.Attribute):
            attrs.append(current.attr)
            current = current.value
        if isinstance(current, ast.Name) and current.id in bindings:
            module, prefix = bindings[current.id]
            add_probe(module, tuple(prefix) + tuple(reversed(attrs)))

# Only unconditional module-level imports can establish a static witness.
# Optional imports, function parameters and rebound locals are not evidence
# that an installed environment lacks an interface.
for node in tree.body:
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
            from_bindings[(node.module, alias.name)] = True
            add_probe(node.module, (alias.name,))
    elif isinstance(node, (ast.Expr, ast.Assign, ast.AnnAssign, ast.AugAssign, ast.Delete)):
        # Lambda/comprehension scopes may shadow imported identifiers.
        if not any(isinstance(child, (ast.Lambda, ast.ListComp, ast.SetComp, ast.DictComp, ast.GeneratorExp)) for child in ast.walk(node)):
            probe_attributes(node)
        for child in ast.walk(node):
            if isinstance(child, ast.Name) and isinstance(child.ctx, (ast.Store, ast.Del)):
                bindings.pop(child.id, None)
    else:
        # Conditional writes and definitions make later alias identity unknown.
        for child in ast.walk(node):
            if isinstance(child, ast.Name) and isinstance(child.ctx, (ast.Store, ast.Del)):
                bindings.pop(child.id, None)
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            bindings.pop(node.name, None)

missing = []
unresolved = []
class DiscardOutput(io.TextIOBase):
    def write(self, value):
        return len(value)

sink = DiscardOutput()
for target, (module, attrs) in probes.items():
    try:
        with contextlib.redirect_stdout(sink), contextlib.redirect_stderr(sink):
            if attrs and (module, attrs[0]) in from_bindings:
                # Native from-import semantics load package submodules before
                # looking up the exported attribute; getattr alone cannot.
                value = __import__(module, fromlist=[attrs[0]])
            else:
                value = importlib.import_module(module)
            for attr in attrs:
                value = getattr(value, attr)
    except ModuleNotFoundError as error:
        if error.name and (target == error.name or target.startswith(error.name + ".")):
            missing.append(target)
        else:
            unresolved.append(target)
    except AttributeError:
        missing.append(target)
    except Exception:
        unresolved.append(target)
print(json.dumps({"ok": True, "missing": sorted(set(missing)), "unresolved": sorted(set(unresolved))}, sort_keys=True))
`

// ManagedPythonSourcePreflight is the bounded API-availability witness for one
// model-authored cell in the exact managed environment selected for execution.
// It contains identifiers only; code and interpreter paths never leave the
// kernel boundary.
type ManagedPythonSourcePreflight struct {
	Missing    []string `json:"missing,omitempty"`
	Unresolved []string `json:"unresolved,omitempty"`
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
		OK         bool     `json:"ok"`
		Error      string   `json:"error"`
		Missing    []string `json:"missing"`
		Unresolved []string `json:"unresolved"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &envelope); err != nil {
		return ManagedPythonSourcePreflight{}, errors.New("managed Python source preflight returned an invalid result")
	}
	if !envelope.OK {
		return ManagedPythonSourcePreflight{}, errors.New("managed Python source preflight could not parse the cell")
	}
	if len(envelope.Missing)+len(envelope.Unresolved) > 64 {
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
	for _, target := range envelope.Unresolved {
		if !managedImportName.MatchString(target) {
			return ManagedPythonSourcePreflight{}, errors.New("managed Python source preflight returned an invalid identifier")
		}
	}
	return ManagedPythonSourcePreflight{Missing: unique, Unresolved: envelope.Unresolved}, nil
}
