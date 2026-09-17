#!/usr/bin/env python3
"""Fail closed when dependency manifests, locks, manager or workspaces drift."""

from __future__ import annotations

import argparse
import hashlib
import json
import pathlib
import stat
import subprocess
import sys
from typing import Any


SCHEMA = "synon.governance.dependency-authority.v1"
TOP_KEYS = {"schema", "go", "frontend", "rule"}


class DependencyError(Exception):
    """Controlled dependency-authority failure."""


def _load(path: pathlib.Path, code: str) -> dict[str, Any]:
    try:
        value = json.loads(path.read_bytes().decode("utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise DependencyError(code) from exc
    if type(value) is not dict:
        raise DependencyError(code)
    return value


def _relative(value: Any, code: str) -> pathlib.PurePosixPath:
    if type(value) is not str or not value or "\\" in value:
        raise DependencyError(code)
    path = pathlib.PurePosixPath(value)
    if path.is_absolute() or ".." in path.parts or path.as_posix() != value:
        raise DependencyError(code)
    return path


def _path(repo: pathlib.Path, relative: Any, code: str) -> pathlib.Path:
    pure = _relative(relative, code)
    candidate = repo.joinpath(*pure.parts)
    try:
        metadata = candidate.lstat()
        resolved = candidate.resolve(strict=True)
        resolved.relative_to(repo.resolve(strict=True))
    except (OSError, ValueError) as exc:
        raise DependencyError(code) from exc
    if not stat.S_ISREG(metadata.st_mode) or metadata.st_nlink != 1:
        raise DependencyError(code)
    return resolved


def _git(repo: pathlib.Path, *args: str) -> str:
    try:
        result = subprocess.run(
            ["git", "-C", str(repo), *args], check=True, text=True,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise DependencyError("dependency_git_check_failed") from exc
    return result.stdout.strip()


def _tracked(repo: pathlib.Path, relative: str) -> None:
    try:
        _git(repo, "ls-files", "--error-unmatch", relative)
    except DependencyError as exc:
        raise DependencyError("dependency_authority_not_tracked") from exc


def _digest(path: pathlib.Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def _validate_spec(spec: dict[str, Any]) -> None:
    if set(spec) != TOP_KEYS or spec.get("schema") != SCHEMA or type(spec.get("rule")) is not str or not spec["rule"]:
        raise DependencyError("dependency_spec_shape_invalid")
    go = spec.get("go")
    if type(go) is not dict or set(go) != {"manifest", "lock"}:
        raise DependencyError("dependency_go_spec_invalid")
    _relative(go["manifest"], "dependency_go_spec_invalid")
    _relative(go["lock"], "dependency_go_spec_invalid")
    frontend = spec.get("frontend")
    if type(frontend) is not dict or set(frontend) != {
        "root", "manager", "install_argv", "manifest", "lock", "lockfile_version", "forbidden_paths"
    }:
        raise DependencyError("dependency_frontend_spec_invalid")
    if frontend["manager"] != "npm" or frontend["install_argv"] != ["npm", "ci"] or type(frontend["lockfile_version"]) is not int:
        raise DependencyError("dependency_frontend_spec_invalid")
    for key in ("root", "manifest", "lock"):
        _relative(frontend[key], "dependency_frontend_spec_invalid")
    forbidden = frontend["forbidden_paths"]
    if type(forbidden) is not list or not forbidden or len(forbidden) != len(set(forbidden)):
        raise DependencyError("dependency_forbidden_paths_invalid")
    for value in forbidden:
        _relative(value, "dependency_forbidden_paths_invalid")


def check(repo: pathlib.Path, spec: dict[str, Any]) -> dict[str, Any]:
    _validate_spec(spec)
    if not repo.is_dir():
        raise DependencyError("dependency_repo_invalid")
    go = spec["go"]
    frontend = spec["frontend"]
    authorities: dict[str, pathlib.Path] = {}
    for key, relative in (
        ("go_manifest", go["manifest"]), ("go_lock", go["lock"]),
        ("frontend_manifest", frontend["manifest"]), ("frontend_lock", frontend["lock"]),
    ):
        authorities[key] = _path(repo, relative, f"dependency_{key}_invalid")
        _tracked(repo, relative)
    for relative in frontend["forbidden_paths"]:
        candidate = repo.joinpath(*pathlib.PurePosixPath(relative).parts)
        if candidate.exists() or candidate.is_symlink() or _git(repo, "ls-files", "--", relative):
            raise DependencyError("dependency_competing_authority_present")

    package = _load(authorities["frontend_manifest"], "dependency_package_json_invalid")
    lock = _load(authorities["frontend_lock"], "dependency_package_lock_invalid")
    if lock.get("lockfileVersion") != frontend["lockfile_version"] or type(lock.get("packages")) is not dict:
        raise DependencyError("dependency_package_lock_shape_invalid")
    root = lock["packages"].get("")
    if type(root) is not dict or root.get("name") != package.get("name") or root.get("version") != package.get("version"):
        raise DependencyError("dependency_package_lock_root_mismatch")
    manager = package.get("packageManager")
    if manager is not None and (type(manager) is not str or not manager.startswith("npm@")):
        raise DependencyError("dependency_package_manager_mismatch")
    workspaces = package.get("workspaces")
    if type(workspaces) is not list or not workspaces or any(type(item) is not str or not item for item in workspaces):
        raise DependencyError("dependency_workspaces_invalid")
    workspace_count = 0
    for workspace in workspaces:
        if "*" in workspace or "?" in workspace or "[" in workspace:
            raise DependencyError("dependency_workspace_glob_forbidden")
        relative = pathlib.PurePosixPath(frontend["root"], workspace, "package.json").as_posix()
        _path(repo, relative, "dependency_workspace_manifest_invalid")
        _tracked(repo, relative)
        if workspace not in lock["packages"]:
            raise DependencyError("dependency_workspace_lock_missing")
        workspace_count += 1
    return {
        "valid": True,
        "manager": "npm",
        "install_argv": ["npm", "ci"],
        "workspace_count": workspace_count,
        "go_sum_sha256": _digest(authorities["go_lock"]),
        "package_lock_sha256": _digest(authorities["frontend_lock"]),
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", required=True)
    parser.add_argument("--spec", required=True)
    args = parser.parse_args(argv)
    try:
        result = check(pathlib.Path(args.repo), _load(pathlib.Path(args.spec), "dependency_spec_json_invalid"))
    except DependencyError as exc:
        print(json.dumps({"ok": False, "code": str(exc)}, sort_keys=True))
        return 2
    print(json.dumps({"ok": True, **result}, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
