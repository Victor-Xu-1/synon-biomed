#!/usr/bin/env python3
"""Validate the governed Synon Biomed repository module topology."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import stat
import subprocess


SCHEMA = "synon.governance.module-topology.v1"
GO_IMPORT_LINE = re.compile(r'^(?:[A-Za-z_][A-Za-z0-9_]*\s+)?"([^"]+)"$')


def validate_architecture_authority(repo: Path, rule: dict[str, object]) -> list[str]:
    relative = str(rule.get("path", ""))
    object_key = str(rule.get("objectKey", ""))
    path = repo / relative
    if not relative or not object_key or not path.resolve().is_relative_to(repo.resolve()):
        return ["architecture_authority_rule_invalid"]
    try:
        document = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeError, json.JSONDecodeError):
        return [f"architecture_authority_invalid:{relative}"]
    architecture = document.get(object_key) if isinstance(document, dict) else None
    if (not isinstance(architecture, dict) or set(document) != {"schemaVersion", object_key}
            or document.get("schemaVersion") != 1 or set(architecture) != {"modules"}):
        return [f"architecture_document_schema_invalid:{relative}"]
    modules = architecture["modules"]
    if not isinstance(modules, list) or not modules:
        return [f"architecture_module_boundaries_missing:{relative}"]
    failures: list[str] = []
    names: set[str] = set()
    ordered = set(rule.get("requiredOrderedModules", []))
    for module in modules:
        if not isinstance(module, dict):
            failures.append(f"architecture_module_identity_invalid:{relative}")
            continue
        name = module.get("name")
        if not isinstance(name, str) or not name.strip() or name in names:
            failures.append(f"architecture_module_identity_invalid:{relative}")
            continue
        names.add(name)
        owns = module.get("owns")
        if (not isinstance(module.get("target"), str) or not module["target"].strip()
                or not isinstance(owns, list) or not owns
                or any(not isinstance(value, str) or not value.strip() for value in owns)):
            failures.append(f"architecture_module_ownership_invalid:{name}")
        if name in ordered or "order" in module:
            order = module.get("order")
            if (not isinstance(order, list) or not order
                    or any(not isinstance(value, str) or not value.strip() for value in order)
                    or len(set(order)) != len(order)):
                failures.append(f"architecture_module_order_invalid:{name}")
    for name in set(rule.get("requiredModuleNames", [])) | ordered:
        if name not in names:
            failures.append(f"architecture_required_module_missing:{name}")
    return sorted(set(failures))


def tracked_paths(repo: Path) -> list[str]:
    result = subprocess.run(
        ["git", "-C", str(repo), "ls-files", "-z"],
        check=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=30,
    )
    values = (value.decode("utf-8") for value in result.stdout.split(b"\0") if value)
    return sorted(value for value in values if repo.joinpath(*Path(value).parts).exists())


def go_imports(path: Path) -> list[str]:
    imports: list[str] = []
    in_block = False
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not in_block:
            if line == "import (":
                in_block = True
                continue
            if not line.startswith("import "):
                continue
            match = GO_IMPORT_LINE.match(line.removeprefix("import ").strip())
            if match:
                imports.append(match.group(1))
            continue
        if line == ")":
            in_block = False
            continue
        match = GO_IMPORT_LINE.match(line)
        if match:
            imports.append(match.group(1))
    return imports


def validate_package_dependency_rule(repo: Path, rule: dict[str, object]) -> list[str]:
    relative = str(rule.get("path", "")).strip().strip("/")
    forbidden = [str(value).strip() for value in rule.get("forbiddenImports", []) if str(value).strip()]
    root = repo / relative
    if not relative or not root.is_dir() or not forbidden:
        return [f"package_dependency_rule_invalid:{relative or 'unknown'}"]
    failures: list[str] = []
    for source in sorted(root.rglob("*.go")):
        if source.name.endswith("_test.go") or source.is_symlink():
            continue
        for imported in go_imports(source):
            for denied in forbidden:
                if imported == denied or imported.startswith(denied.rstrip("/") + "/"):
                    source_name = source.relative_to(repo).as_posix()
                    failures.append(f"package_dependency_forbidden:{source_name}:{imported}")
    return failures


def validate(repo: Path, spec: dict[str, object]) -> list[str]:
    failures: list[str] = []
    if spec.get("schema") != SCHEMA or spec.get("authorityOwner") != "repository-steward":
        return ["module_topology_schema_invalid"]
    paths = tracked_paths(repo)
    path_set = set(paths)
    for relative in spec.get("requiredDirectories", []):
        path = repo / str(relative)
        try:
            metadata = path.lstat()
        except OSError:
            failures.append(f"required_directory_missing:{relative}")
            continue
        if not stat.S_ISDIR(metadata.st_mode) or path.is_symlink():
            failures.append(f"required_directory_invalid:{relative}")
    for relative in spec.get("requiredFiles", []):
        path = repo / str(relative)
        try:
            metadata = path.lstat()
        except OSError:
            failures.append(f"required_file_missing:{relative}")
            continue
        if not stat.S_ISREG(metadata.st_mode) or path.is_symlink():
            failures.append(f"required_file_invalid:{relative}")
    for relative in spec.get("bannedPaths", []):
        if (repo / str(relative)).exists() or any(
            path == relative or path.startswith(str(relative).rstrip("/") + "/") for path in paths
        ):
            failures.append(f"banned_path_present:{relative}")
    allowed_roots = set(str(value) for value in spec.get("allowedTrackedRootEntries", []))
    roots = {path.split("/", 1)[0] for path in paths}
    for unexpected in sorted(roots - allowed_roots):
        failures.append(f"unexpected_root_entry:{unexpected}")
    for rule in spec.get("fileAuthorities", []):
        suffix, prefix = str(rule.get("suffix", "")), str(rule.get("prefix", ""))
        for path in paths:
            if path.endswith(suffix) and not path.startswith(prefix):
                failures.append(f"file_authority_drift:{path}")
    for rule in spec.get("architectureAuthorities", []):
        failures.extend(validate_architecture_authority(repo, rule))
    for rule in spec.get("packageDependencyRules", []):
        failures.extend(validate_package_dependency_rule(repo, rule))
    for ceiling in spec.get("growthCeilings", []):
        relative = str(ceiling.get("path", ""))
        maximum = int(ceiling.get("maximum", -1))
        kind = str(ceiling.get("kind", ""))
        target = repo / relative
        if kind == "lines":
            count = len(target.read_bytes().splitlines())
        elif kind == "direct-files":
            count = sum(1 for item in target.iterdir() if item.is_file() and not item.is_symlink())
        else:
            failures.append(f"growth_ceiling_kind_invalid:{relative}")
            continue
        if count > maximum:
            failures.append(f"growth_ceiling_exceeded:{relative}:{count}>{maximum}")
    return sorted(set(failures))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=".")
    parser.add_argument("--spec", default="docs/governance/module-topology.json")
    args = parser.parse_args()
    repo = Path(args.repo).resolve()
    spec = json.loads((repo / args.spec).read_text(encoding="utf-8"))
    failures = validate(repo, spec)
    print(json.dumps({"ok": not failures, "failures": failures}, sort_keys=True))
    return 0 if not failures else 3


if __name__ == "__main__":
    raise SystemExit(main())
