package server

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// This parser does not execute submitted Python. An inspection-only cell is
// exempt from the scientific computation entrypoint rule, not from environment,
// permission, confinement, or output-ownership checks.
const pythonInspectionClassifier = `import ast, sys
tree = ast.parse(sys.stdin.read(262145))
bindings = {}
reserved = {"print", "dir", "help", "eval", "exec", "__import__"}
observed = False

def symbol(node):
    if isinstance(node, ast.Name):
        return bindings.get(node.id, "")
    if isinstance(node, ast.Attribute):
        parent = symbol(node.value)
        return parent + "." + node.attr if parent else ""
    return ""

def observation(node):
    if isinstance(node, ast.Attribute) and node.attr in ("__version__", "__doc__", "__name__"):
        return bool(symbol(node.value))
    if not isinstance(node, ast.Call) or node.keywords or len(node.args) != 1:
        return False
    if isinstance(node.func, ast.Name) and node.func.id == "dir":
        return bool(symbol(node.args[0]))
    if symbol(node.func) in ("inspect.signature", "inspect.getdoc", "inspect.getsource"):
        return bool(symbol(node.args[0]))
    return False

valid = len(tree.body) <= 64
for statement in tree.body:
    if isinstance(statement, ast.Import):
        for item in statement.names:
            local = item.asname or item.name.split(".")[0]
            if local in reserved:
                valid = False
            bindings[local] = item.name if item.asname else local
    elif isinstance(statement, ast.ImportFrom) and statement.level == 0 and statement.module:
        for item in statement.names:
            local = item.asname or item.name
            if local in reserved or item.name == "*":
                valid = False
            bindings[local] = statement.module + "." + item.name
    elif (isinstance(statement, ast.Expr) and isinstance(statement.value, ast.Call)
          and isinstance(statement.value.func, ast.Name) and statement.value.func.id == "print"
          and statement.value.args and not statement.value.keywords):
        for argument in statement.value.args:
            if observation(argument):
                observed = True
            elif not isinstance(argument, ast.Constant):
                valid = False
    else:
        valid = False
raise SystemExit(0 if valid and observed else 1)
`

func isManagedPythonAPIInspection(source string) bool {
	if len(source) == 0 || len(source) > 32<<10 {
		return false
	}
	python := agentRuntimePythonCompilerExecutable()
	if python == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-I", "-S", "-c", pythonInspectionClassifier)
	command.Stdin = strings.NewReader(source)
	return command.Run() == nil
}
