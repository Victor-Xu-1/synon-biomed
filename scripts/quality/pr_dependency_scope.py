"""Resolve changed Go module identities and their real package consumers."""

from __future__ import annotations

import json
from pathlib import Path
import re
import subprocess
import tempfile


def output(repo: Path, *arguments: str) -> str:
    result = subprocess.run(arguments, cwd=repo, text=True, capture_output=True, check=False)
    if result.returncode:
        raise RuntimeError(result.stderr or result.stdout)
    return result.stdout


def snapshot(repo: Path, revision: str, name: str, *, optional: bool = False) -> str:
    if optional and not output(repo, "git", "ls-tree", "--name-only", revision, "--", name).strip():
        return ""
    return output(repo, "git", "show", f"{revision}:{name}")


def manifest(repo: Path, text: str) -> dict:
    # Ask Go to parse its own grammar; do not approximate replace/toolchain
    # directives with line matching or write scratch inputs into the checkout.
    with tempfile.TemporaryDirectory(prefix="synon-module-manifest-") as directory:
        path = Path(directory) / "snapshot.mod"
        path.write_text(text, encoding="utf-8")
        return json.loads(output(repo, "go", "mod", "edit", "-json", str(path)))


def sums(text: str) -> set[tuple[str, str, str]]:
    result = set()
    for line in text.splitlines():
        if not line.strip():
            continue
        fields = line.split()
        if len(fields) != 3 or not fields[2].startswith("h1:"):
            raise ValueError("invalid Go checksum entry")
        result.add(tuple(fields))
    return result


def changed_modules(repo: Path, base: str, head: str) -> set[str] | None:
    """None means a toolchain/module authority change needs broad coverage."""
    if not all(re.fullmatch(r"[0-9a-f]{40,64}", value) for value in (base, head)):
        raise ValueError("module comparison requires full immutable commit IDs")
    if set(base) == {"0"}:
        return None
    before = manifest(repo, snapshot(repo, base, "go.mod"))
    after = manifest(repo, snapshot(repo, head, "go.mod"))
    old_require = {item["Path"]: item["Version"] for item in before.pop("Require", None) or []}
    new_require = {item["Path"]: item["Version"] for item in after.pop("Require", None) or []}
    if before != after:
        return None
    modules = {path for path in old_require.keys() | new_require.keys()
               if old_require.get(path) != new_require.get(path)}
    old_sums = sums(snapshot(repo, base, "go.sum", optional=True))
    new_sums = sums(snapshot(repo, head, "go.sum", optional=True))
    modules.update(item[0] for item in old_sums ^ new_sums)
    return modules


def consumers(graph: list[dict], modules: set[str]) -> set[str]:
    """Include external indirection and production, internal and external tests."""
    affected = {
        entry["ImportPath"] for entry in graph
        if entry.get("Module", {}).get("Path") in modules or any(
            entry["ImportPath"] == name or entry["ImportPath"].startswith(name + "/")
            for name in modules
        )
    }
    while True:
        previous = set(affected)
        for entry in graph:
            imports = set(entry.get("Imports", [])) | set(entry.get("TestImports", [])) | set(entry.get("XTestImports", []))
            if imports.intersection(affected):
                affected.add(entry["ImportPath"])
        if affected == previous:
            return affected
