"""Compile standard Python and admit its unconditional import prefix."""
import ast
import importlib.util
import sys

from .python_code_compatibility import prepare_python_source


def correction(message, diagnostics):
    return {
        "schema": "synon.python-code-preflight.v1", "status": "code_preflight_required",
        "executed": False, "message": message, "diagnostics": diagnostics,
        "recovery": "Correct the reported source or select a verified environment before resubmitting.",
    }


def prepare(source, namespace, filename):
    source = prepare_python_source(source, namespace)
    try:
        tree = ast.parse(source, filename=filename)
        compiled = compile(tree, filename, "exec", dont_inherit=True)
    except (SyntaxError, ValueError) as error:
        return None, correction(str(error), [
            {"code": "python_syntax_error", "line": getattr(error, "lineno", None)}
        ])
    roots = set()
    for node in tree.body:
        if isinstance(node, ast.Import):
            roots.update(alias.name.split(".")[0] for alias in node.names)
        elif isinstance(node, ast.ImportFrom) and node.level == 0 and node.module:
            roots.add(node.module.split(".")[0])
        elif isinstance(node, ast.Expr) and isinstance(node.value, ast.Constant) and isinstance(node.value.value, str):
            continue
        else:
            break
    missing = []
    for name in sorted(roots):
        if name in namespace or sys.modules.get(name) is not None:
            continue
        try:
            available = importlib.util.find_spec(name) is not None
        except (ImportError, ValueError):
            available = False
        if not available:
            missing.append(name)
    if missing:
        return None, correction("Unavailable Python imports: " + ", ".join(missing), [
            {"code": "python_import_module_unavailable", "modules": missing}
        ])
    return compiled, None
