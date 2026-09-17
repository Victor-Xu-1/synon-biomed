"""Repair unbound JSON literal spellings using the actual kernel namespace."""

import ast


def prepare_python_source(source, namespace):
    if not any(name in source for name in ("true", "false", "null")):
        return source
    try:
        tree = ast.parse(source, mode="exec")
    except (SyntaxError, ValueError):
        return source
    bound = set(namespace)
    builtins = namespace.get("__builtins__", {})
    bound.update(builtins if isinstance(builtins, dict) else vars(builtins))
    for node in ast.walk(tree):
        if isinstance(node, ast.Name) and isinstance(node.ctx, (ast.Store, ast.Del)):
            bound.add(node.id)
        elif isinstance(node, ast.arg):
            bound.add(node.arg)
        elif isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            bound.add(node.name)
        elif isinstance(node, ast.alias):
            if node.name == "*":
                return source
            bound.add(node.asname or node.name.split(".", 1)[0])
        elif isinstance(node, ast.ExceptHandler) and isinstance(node.name, str):
            bound.add(node.name)
        elif isinstance(node, (ast.Global, ast.Nonlocal)):
            bound.update(node.names)
        elif isinstance(node, (ast.MatchAs, ast.MatchStar)) and node.name:
            bound.add(node.name)
        elif isinstance(node, ast.MatchMapping) and node.rest:
            bound.add(node.rest)
        elif isinstance(node, ast.Call) and isinstance(node.func, ast.Name) and node.func.id in {"exec", "eval"}:
            # Dynamic bindings in this cell cannot be established before execution.
            return source
    literals = {"true": "True", "false": "False", "null": "None"}
    edits = []
    for node in ast.walk(tree):
        if (isinstance(node, ast.Name) and isinstance(node.ctx, ast.Load)
                and node.id in literals and node.id not in bound
                and node.lineno == node.end_lineno):
            edits.append((node.lineno - 1, node.col_offset, node.end_col_offset, node.id))
    lines = source.splitlines(keepends=True)
    for line_index, start, end, name in sorted(edits, reverse=True):
        encoded = lines[line_index].encode("utf-8")
        if encoded[start:end] == name.encode("utf-8"):
            lines[line_index] = (encoded[:start] + literals[name].encode("ascii") + encoded[end:]).decode("utf-8")
    return "".join(lines)
