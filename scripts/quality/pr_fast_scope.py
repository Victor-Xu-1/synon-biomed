#!/usr/bin/env python3
"""Run the smallest complete Go test or vet scope for an exact change.

The selector uses the real Go package dependency graph. A changed package and
every in-repository package that imports it are tested. Dependency updates
select the changed modules' real production and test consumers. Full runtime and race
coverage remains in the full quality workflow. An all-zero push base selects
every tracked path so a newly created integration branch fails conservatively.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys

if __package__:
    from . import pr_dependency_scope, pr_test_partition, verification_scope
    from .runtime_test_inventory import discover, execution_plan
    from .runtime_test_shards import execute
else:
    import pr_dependency_scope
    import pr_test_partition
    import verification_scope
    from runtime_test_inventory import discover, execution_plan
    from runtime_test_shards import execute


def run(repo: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run([*args], cwd=repo, text=True, check=False)


def packages(repo: Path, *, dependencies: bool = False) -> list[dict]:
    graph_flags = ["-deps", "-test"] if dependencies else []
    result = subprocess.run(
        ["go", "list", "-buildvcs=false", "-json", *graph_flags, "./..."],
        cwd=repo, text=True, capture_output=True, check=False,
    )
    if result.returncode:
        raise RuntimeError(result.stderr or result.stdout)
    decoder = json.JSONDecoder()
    entries: list[dict] = []
    offset = 0
    while offset < len(result.stdout):
        while offset < len(result.stdout) and result.stdout[offset].isspace():
            offset += 1
        if offset == len(result.stdout):
            break
        entry, end = decoder.raw_decode(result.stdout, offset)
        entries.append(entry)
        offset = end
    return entries


def changed_paths(repo: Path, base: str, head: str) -> list[str]:
    if not all(re.fullmatch(r"[0-9a-f]{40,64}", revision) for revision in (base, head)):
        raise ValueError("change revisions must be full immutable commit IDs")
    if set(base) == {"0"}:
        result = subprocess.run(
            ["git", "ls-tree", "-r", "--name-only", "-z", head],
            cwd=repo, text=True, capture_output=True, check=False,
        )
        if result.returncode:
            raise RuntimeError(result.stderr or result.stdout)
        return [path for path in result.stdout.split("\0") if path]
    result = subprocess.run(
        ["git", "diff", "--name-only", "--no-renames", "-z", base, head, "--"],
        cwd=repo, text=True, capture_output=True, check=False,
    )
    if result.returncode:
        raise RuntimeError(result.stderr or result.stdout)
    return [path for path in result.stdout.split("\0") if path]


def frontend_affected(paths: list[str], repo: Path | None = None) -> bool:
    return any(path.startswith("frontend/") or path in {"identity.go", "product-identity.json"}
               for path in paths) or (repo is not None and any(
                   group["frontend"] for group in verification_scope.matched(repo, paths)))


def documentation_only(path: str) -> bool:
    return (path in {"README.md", "AGENTS.md", "CONTRIBUTING.md", "LICENSE", ".gitignore", ".gitattributes", ".editorconfig",
                    ".github/pull_request_template.md"}
            or path.startswith("docs/assets/") or path.startswith("docs/") and path.endswith(".md"))


def select_packages(repo: Path, paths: list[str], entries: list[dict],
                    dependency_owners: set[str] | None = None) -> tuple[list[str], str]:
    verification_paths = {path for group in verification_scope.load(repo) for path in group["paths"]}
    local = [entry for entry in entries if Path(entry["Dir"]).is_relative_to(repo)]
    all_packages = sorted(entry["ImportPath"] for entry in local)
    directories = {entry["ImportPath"]: Path(entry["Dir"]).relative_to(repo).as_posix() for entry in local}
    changed: set[str] = set()
    for path in paths:
        if Path(path).is_absolute() or ".." in Path(path).parts:
            raise ValueError("changed path is outside source")
        if Path(path).name in {"go.mod", "go.sum", "go.work", "go.work.sum"}:
            if path in {"go.mod", "go.sum"} and dependency_owners is not None:
                changed.update(dependency_owners.intersection(all_packages))
                continue
            return all_packages, "Go dependency authority changed"
        if documentation_only(path) or path.startswith("frontend/") and not path.endswith(".go"):
            continue
        owners = set()
        for entry in local:
            name = entry["ImportPath"]
            directory = directories[name]
            embeds = [item for field in ("EmbedFiles", "TestEmbedFiles", "XTestEmbedFiles")
                      for item in entry.get(field, [])]
            embedded = {(Path(directory) / item).as_posix() for item in embeds}
            testdata = (Path(directory) / "testdata").as_posix() + "/"
            if (path.endswith(".go") and Path(path).parent.as_posix() == directory
                    or path in embedded or path.startswith(testdata)):
                owners.add(name)
        if not owners:
            if path in verification_paths:
                continue
            # Skills, Python/MCP assets, CI scripts and other package-external
            # inputs can be read at runtime. Unknown ownership broadens scope.
            return all_packages, "Package-external input requires conservative Go coverage"
        changed.update(owners)
    selected = set(changed)
    while True:
        previous = set(selected)
        for entry in local:
            imports = set(entry.get("Imports", [])) | set(entry.get("TestImports", [])) | set(entry.get("XTestImports", []))
            if selected.intersection(imports):
                selected.add(entry["ImportPath"])
        if selected == previous:
            break
    return sorted(selected), "Changed packages, dependency modules and production/test import consumers"


def resolve_packages(repo: Path, base: str, head: str, paths: list[str]) -> tuple[list[str], str]:
    owners = None
    if {"go.mod", "go.sum"}.intersection(paths):
        modules = pr_dependency_scope.changed_modules(repo, base, head)
        if modules is not None:
            owners = pr_dependency_scope.consumers(packages(repo, dependencies=True), modules) if modules else set()
    return select_packages(repo, paths, packages(repo), owners)


def selected_packages(repo: Path, base: str, head: str) -> tuple[list[str], list[str]]:
    paths = changed_paths(repo, base, head)
    selected, _ = resolve_packages(repo, base, head, paths)
    return selected, paths


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=Path, default=Path.cwd())
    parser.add_argument("--base", required=True)
    parser.add_argument("--head", required=True)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--frontend", action="store_true")
    mode.add_argument("--vet", action="store_true")
    mode.add_argument("--matrix", action="store_true")
    parser.add_argument("--log-dir", type=Path)
    parser.add_argument("--shard-index", type=int, default=0)
    parser.add_argument("--shard-count", type=int, default=1)
    args = parser.parse_args()
    repo = args.repo.resolve()
    try:
        paths = changed_paths(repo, args.base, args.head)
        groups = verification_scope.matched(repo, paths)
        if not args.frontend and not args.vet and not args.matrix:
            for command in verification_scope.checks(groups):
                result = run(repo, *command)
                if result.returncode:
                    return result.returncode
        if args.frontend:
            if not frontend_affected(paths, repo):
                print("No frontend package is affected by this change; Go and policy gates remain active.")
                return 0
            # Check reviewed content before npm creates ignored output trees.
            # Passing component tests cannot authorize stale source provenance.
            provenance = run(repo, sys.executable, 'scripts/audit/audit_frontend_migration.py',
                             '--root', str(repo), '--check', '--require-clean')
            if provenance.returncode:
                return provenance.returncode
            commands = [
                ["npm", "ci", "--ignore-scripts"], ["npm", "run", "i18n:types"],
                ["npm", "run", "typecheck"], ["npm", "run", "lint"],
                ["npm", "run", "format:check"], ["npm", "run", "test"], ["npm", "run", "build"],
                ["npm", "run", "test:packaged"],
            ]
            for command in commands:
                result = run(repo / "frontend", *command)
                if result.returncode:
                    return result.returncode
            return 0
        selected, reason = resolve_packages(repo, args.base, args.head, paths)
        if args.matrix:
            print(json.dumps(pr_test_partition.job_matrix(selected)))
            return 0
        scope = {"base": args.base, "head": args.head, "changed_paths": paths, "packages": selected,
                 "verification_groups": [group["name"] for group in groups], "reason": reason,
                 "shard_index": args.shard_index, "shard_count": args.shard_count}
        print(json.dumps(scope, ensure_ascii=False))
        if args.vet:
            if not selected:
                print("No Go package is affected by this change; formatting and policy gates remain active.")
                return 0
            return run(repo, "go", "vet", "-buildvcs=false", *selected).returncode
        if args.log_dir is not None:
            log_dir = args.log_dir.resolve()
            if log_dir == repo or repo in log_dir.parents:
                raise ValueError("test evidence must be outside source")
            log_dir.mkdir(parents=True, exist_ok=True)
            (log_dir / "scope.json").write_text(json.dumps(scope, indent=2) + "\n")
        if selected:
            if args.log_dir is None:
                raise ValueError("Go execution requires an external --log-dir")
            inventory = discover(repo, selected, race=False)
            shard = pr_test_partition.select(inventory, args.shard_index, args.shard_count)
            if not shard:
                print("No tests assigned to this partition; other partitions retain the inventory.")
                return 0
            test_plan = execution_plan("pr", args.shard_index, False, inventory, shard, batch_size=64)
            test_plan["shard_count"] = args.shard_count
            return execute(repo, test_plan, args.log_dir)
        print("No Go package is affected by this change; frontend and policy gates remain active.")
        return 0
    except (OSError, RuntimeError, ValueError) as error:
        print(f"ERROR: PR scope selection failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
