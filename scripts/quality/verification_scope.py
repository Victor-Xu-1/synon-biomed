"""Exact ownership declarations for repository verification tools.

Runtime Go ownership is resolved by the caller before these declarations.
Unlisted inputs keep the conservative runtime fallback.
"""

from __future__ import annotations

import json
from pathlib import Path, PurePosixPath
import re


CONTRACT = "scripts/quality/verification_scope.json"


def load(repo: Path) -> list[dict]:
    path = repo / CONTRACT
    if not path.exists():
        return []
    if path.is_symlink() or path.stat().st_size > 65536:
        raise ValueError("invalid verification scope contract file")
    value = json.loads(path.read_text(encoding="utf-8"))
    if (not isinstance(value, dict) or set(value) != {"schema", "groups"}
            or value["schema"] != "synon.verification-scope.v1"
            or not isinstance(value["groups"], list)):
        raise ValueError("invalid verification scope contract")
    names: set[str] = set()
    paths: set[str] = set()
    for group in value["groups"]:
        if (not isinstance(group, dict) or set(group) != {"name", "paths", "frontend", "checks"}
                or not isinstance(group["name"], str)
                or not re.fullmatch(r"[a-z][a-z0-9-]{0,63}", group["name"])
                or group["name"] in names or type(group["frontend"]) is not bool
                or not isinstance(group["paths"], list) or not group["paths"]
                or not isinstance(group["checks"], list) or not group["checks"]):
            raise ValueError("invalid verification scope group")
        names.add(group["name"])
        for item in group["paths"]:
            if (not isinstance(item, str) or not item.startswith(("scripts/", ".github/", "docs/governance/"))
                    or PurePosixPath(item).as_posix() != item
                    or any(part in {".", ".."} for part in item.split("/"))
                    or any(char in item for char in "\\*?[]\x00\r\n")
                    or PurePosixPath(item).name in {"go.mod", "go.sum", "go.work", "go.work.sum"}
                    or item.endswith(".go") or item in paths):
                raise ValueError("verification ownership requires unique exact non-Go tooling paths")
            paths.add(item)
        for command in group["checks"]:
            if (not isinstance(command, list) or len(command) < 2
                    or any(not isinstance(arg, str) or not arg or "\x00" in arg for arg in command)
                    or command[0] not in {"python3", "go", "npm", "bash"}):
                raise ValueError("invalid verification scope check")
    if CONTRACT not in paths:
        raise ValueError("verification scope contract must retain its own verification group")
    return value["groups"]


def matched(repo: Path, changed: list[str]) -> list[dict]:
    paths = set(changed)
    return [group for group in load(repo) if paths.intersection(group["paths"])]


def checks(groups: list[dict]) -> list[list[str]]:
    result: list[list[str]] = []
    for group in groups:
        for command in group["checks"]:
            if command not in result:
                result.append(command)
    return result
