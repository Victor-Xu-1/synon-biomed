"""Resolve exact non-embedded runtime inputs to their actual Go consumers.

Declarations add roots to the normal production/test dependency closure. They
never exempt runtime tests; unknown paths and unavailable owners stay broad.
"""

from __future__ import annotations

import json
from pathlib import Path, PurePosixPath


CONTRACT = "scripts/quality/runtime_input_scope.json"


def load(repo: Path) -> dict[str, set[str]]:
    path = repo / CONTRACT
    if not path.exists():
        return {}
    if path.is_symlink() or path.stat().st_size > 65536:
        raise ValueError("invalid runtime input scope file")
    value = json.loads(path.read_text(encoding="utf-8"))
    if (not isinstance(value, dict) or set(value) != {"schema", "groups"}
            or value["schema"] != "synon.runtime-input-scope.v1"
            or not isinstance(value["groups"], list) or not value["groups"]):
        raise ValueError("invalid runtime input scope contract")
    result: dict[str, set[str]] = {}
    names = set()
    for group in value["groups"]:
        if (not isinstance(group, dict) or set(group) != {"name", "paths", "packages"}
                or not isinstance(group["name"], str) or not group["name"]
                or group["name"] in names
                or not isinstance(group["paths"], list) or not group["paths"]
                or not isinstance(group["packages"], list) or not group["packages"]):
            raise ValueError("invalid runtime input scope group")
        names.add(group["name"])
        for package in group["packages"]:
            if (not isinstance(package, str) or not package
                    or package.startswith(("/", ".")) or ".." in package.split("/")
                    or any(c in package for c in "\\*?[]\x00\r\n ")
                    or PurePosixPath(package).as_posix() != package):
                raise ValueError("invalid runtime input package owner")
        if len(set(group["packages"])) != len(group["packages"]):
            raise ValueError("duplicate runtime package owner")
        for item in group["paths"]:
            if (not isinstance(item, str) or not item.startswith("assets/")
                    or PurePosixPath(item).as_posix() != item
                    or any(part in {".", ".."} for part in item.split("/"))
                    or any(c in item for c in "\\*?[]\x00\r\n")
                    or item.endswith(".go") or item in result
                    or PurePosixPath(item).name in {"go.mod", "go.sum", "go.work", "go.work.sum"}):
                raise ValueError("runtime input ownership requires unique exact asset paths")
            result[item] = set(group["packages"])
    return result
