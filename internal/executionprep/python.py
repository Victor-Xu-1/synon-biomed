"""Non-executing Python AST adapter. Only resolved language facts leave it."""
import ast
import json
import sys


class Analyzer(ast.NodeVisitor):
    def __init__(self):
        self.bindings = {}
        self.facts = []
        self.functions = {}
        self.depth = 0

    def emit(self, kind, name, args, node):
        if len(self.facts) < 256:
            self.facts.append(dict(kind=kind, name=name, args=args,
                                   line=getattr(node, "lineno", 0)))

    def value(self, node):
        if isinstance(node, ast.Constant):
            return node.value
        if isinstance(node, ast.Name):
            return self.bindings.get(node.id, ("symbol", node.id))
        if isinstance(node, (ast.List, ast.Tuple)):
            return [self.value(item) for item in node.elts]
        if isinstance(node, ast.BinOp) and isinstance(node.op, ast.Add):
            left, right = self.value(node.left), self.value(node.right)
            if type(left) is type(right) and isinstance(left, (str, list)):
                return left + right
        if isinstance(node, ast.Attribute):
            base = self.value(node.value)
            if isinstance(base, tuple):
                if base[0] == "symbol":
                    return ("symbol", base[1] + "." + node.attr)
                if base[0] == "r_package":
                    return ("symbol", "r:" + base[1] + "::" + node.attr.replace("_", "."))
                if base[0] == "response" and node.attr in ("text", "content"):
                    return ("http_body", base[1])
                if base[0] == "response" and node.attr == "read":
                    return ("reader", base[1])
                if base[0] == "response" and node.attr == "raw":
                    return ("http_stream", base[1])
                if base[0] == "response" and node.attr in ("iter_content", "iter_lines", "iter_bytes", "iter_raw"):
                    return ("http_iterator", base[1])
                if base[0] == "http_stream" and node.attr == "read":
                    return ("reader", base[1])
                if base[0] in ("file", "path"):
                    return ("writer", base[1], node.attr)
        if isinstance(node, ast.Subscript):
            base, key = self.value(node.value), self.value(node.slice)
            if base == ("symbol", "rpy2.robjects.r") and isinstance(key, str):
                return ("symbol", "r:" + key)
        if isinstance(node, ast.Call):
            reader = self.value(node.func)
            if isinstance(reader, tuple) and reader[0] == "reader":
                return ("http_body", reader[1])
            if isinstance(reader, tuple) and reader[0] == "http_iterator":
                return ("http_chunks", reader[1])
            target = self.name(node.func)
            first = self.value(node.args[0]) if node.args else None
            if not node.args:
                first = next((self.value(item.value) for item in node.keywords if item.arg == "url"), None)
            if target == "rpy2.robjects.packages.importr" and isinstance(first, str):
                return ("r_package", first)
            private_request = any(item.arg in ("headers", "auth", "cookies", "cert") for item in node.keywords)
            if target in ("requests.get", "httpx.get", "urllib.request.urlopen") and isinstance(first, str) and not private_request:
                return ("response", first)
            if target in ("open", "io.open") and isinstance(first, str):
                return ("file", first)
            if target in ("pathlib.Path", "Path") and isinstance(first, str):
                return ("path", first)
        return None

    def name(self, node):
        value = self.value(node)
        return value[1] if isinstance(value, tuple) and value[0] == "symbol" else ""

    def visit_Import(self, node):
        for item in node.names:
            local = item.asname or item.name.split(".")[0]
            self.bindings[local] = ("symbol", item.name if item.asname else local)

    def visit_ImportFrom(self, node):
        if node.level == 0 and node.module:
            for item in node.names:
                self.bindings[item.asname or item.name] = ("symbol", node.module + "." + item.name)

    def visit_Assign(self, node):
        self.visit(node.value)
        value = self.value(node.value)
        for target in node.targets:
            if isinstance(target, ast.Name):
                self.bindings[target.id] = value

    def visit_With(self, node):
        for item in node.items:
            self.visit(item.context_expr)
            if isinstance(item.optional_vars, ast.Name):
                self.bindings[item.optional_vars.id] = self.value(item.context_expr)
        for statement in node.body:
            self.visit(statement)

    def bind_target(self, target, value):
        if isinstance(target, ast.Name):
            self.bindings[target.id] = value
        elif isinstance(target, (ast.Tuple, ast.List)):
            # Unknown destructuring must not reuse an earlier HTTP binding.
            for item in target.elts:
                self.bind_target(item, None)
        elif isinstance(target, ast.Starred):
            self.bind_target(target.value, None)

    def visit_For(self, node):
        self.visit(node.iter)
        iterator = self.value(node.iter)
        before = self.bindings.copy()
        item = None
        if isinstance(iterator, tuple) and iterator[0] in ("http_chunks", "response", "http_stream"):
            item = ("http_body", iterator[1])
        self.bind_target(node.target, item)
        for statement in node.body:
            self.visit(statement)
        # A loop may execute zero times. Keep only bindings that agree on
        # both paths instead of leaking the hypothetical final chunk.
        after = self.bindings
        self.bindings = {key: before.get(key) if before.get(key) == after.get(key) else None
                         for key in before.keys() | after.keys()}
        for statement in node.orelse:
            self.visit(statement)

    def visit_FunctionDef(self, node):
        self.functions[node.name] = node
        self.bindings[node.name] = ("local_function", node.name)

    visit_AsyncFunctionDef = visit_FunctionDef

    def visit_Call(self, node):
        target = self.name(node.func)
        args = [self.value(value) for value in node.args]
        positional_args = args
        named = {item.arg: self.value(item.value) for item in node.keywords if item.arg}
        if not args:
            for key in ("url", "filepath_or_buffer", "path", "args", "code"):
                if key in named:
                    args = [named[key]]
                    break
        first = args[0] if args else None
        if target == "rpy2.robjects.r" and isinstance(first, str):
            self.emit("source", "r", [first], node)
        elif target.startswith("r:"):
            self.emit("call", target[2:], [x if isinstance(x, str) else "" for x in args], node)
        elif target in ("os.system", "os.popen") and isinstance(first, str):
            self.emit("source", "bash", [first], node)
        elif target in ("subprocess.run", "subprocess.call", "subprocess.check_call",
                        "subprocess.check_output", "subprocess.Popen"):
            if isinstance(first, list):
                argv = ["python" if item == ("symbol", "sys.executable") else item for item in first]
                if all(isinstance(item, str) for item in argv):
                    self.emit("process", "", argv, node)
            elif isinstance(first, str) and named.get("shell") is True:
                self.emit("source", "bash", [first], node)
        elif target in ("exec", "eval") and isinstance(first, str):
            self.emit("source", "python", [first], node)
        elif target == "runpy.run_path" and isinstance(first, str):
            self.emit("script", "python", [first], node)
        elif target:
            self.emit("call", target, [x if isinstance(x, str) else "" for x in args], node)
        writer = self.value(node.func)
        if (isinstance(writer, tuple) and writer[0] == "writer" and
                writer[2] in ("write", "write_bytes", "write_text") and
                isinstance(first, tuple) and first[0] == "http_body"):
            self.emit("call", "file.write_http", [first[1], writer[1]], node)
        if (target == "shutil.copyfileobj" and len(args) >= 2 and
                isinstance(first, tuple) and first[0] in ("response", "http_stream") and
                isinstance(args[1], tuple) and args[1][0] == "file"):
            self.emit("call", "file.write_http", [first[1], args[1][1]], node)
        local = self.value(node.func)
        if isinstance(local, tuple) and local[0] == "local_function" and self.depth < 8:
            function = self.functions[local[1]]
            saved = self.bindings.copy()
            self.depth += 1
            positional = function.args.posonlyargs + function.args.args
            parameters = positional + function.args.kwonlyargs
            # Unresolved parameters shadow globals; keyword calls must retain
            # the actual response/file identity just like positional calls.
            for parameter in parameters:
                self.bindings[parameter.arg] = None
            for parameter, value in zip(positional, positional_args):
                self.bindings[parameter.arg] = value
            for parameter in function.args.args + function.args.kwonlyargs:
                if parameter.arg in named:
                    self.bindings[parameter.arg] = named[parameter.arg]
            for statement in function.body:
                self.visit(statement)
            self.bindings = saved
            self.depth -= 1
        self.generic_visit(node)


def observation_operations(tree):
    """Cover every executable node, with no name lookup except guarded output."""
    if not tree.body or len(list(ast.walk(tree))) > 1024:
        return None

    def literal(node, depth=0):
        if depth > 32:
            return False
        if isinstance(node, ast.Constant):
            return type(node.value) in (type(None), bool, int, float, complex, str, bytes)
        if isinstance(node, (ast.Tuple, ast.List)):
            return all(literal(item, depth + 1) for item in node.elts)
        if isinstance(node, ast.UnaryOp) and isinstance(node.op, (ast.UAdd, ast.USub)):
            return isinstance(node.operand, ast.Constant) and type(node.operand.value) in (int, float, complex)
        return False

    operations = set()
    for statement in tree.body:
        if not isinstance(statement, ast.Expr):
            return None
        value = statement.value
        if literal(value):
            operations.add('python.literal')
            continue
        if not (isinstance(value, ast.Call) and isinstance(value.func, ast.Name)
                and value.func.id == 'print' and all(literal(arg) for arg in value.args)):
            return None
        for keyword in value.keywords:
            if keyword.arg in ('sep', 'end'):
                if not isinstance(keyword.value, ast.Constant) or type(keyword.value.value) not in (str, type(None)):
                    return None
            elif keyword.arg == 'flush':
                if not isinstance(keyword.value, ast.Constant) or type(keyword.value.value) is not bool:
                    return None
            else:
                return None
        if len({keyword.arg for keyword in value.keywords}) != len(value.keywords):
            return None
        operations.add('python.output')
    return sorted(operations)


source = sys.stdin.read(262145)
if len(source) > 262144:
    raise ValueError("source too large")
tree = ast.parse(source, filename="<execution>")
analysis = Analyzer()
analysis.visit(tree)
operations = observation_operations(tree)
if operations is not None:
    analysis.facts.append(dict(kind='observation', name='synon.execution-observation.v1', args=operations))
print(json.dumps(analysis.facts, separators=(",", ":")))
