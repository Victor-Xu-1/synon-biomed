"""Complete, deterministic Go inventories and bounded runtime test plans."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import re
import subprocess

PACKAGE_SHARDS = {
    "server": ("internal/server", 4),
    "workspace": ("internal/persistence/workspace", 4),
    "transcript": ("internal/persistence/transcript", 2),
}
# Actual CI race evidence: workspace initialization ~40s; server shards exhaust
# 30m after only 106-129 of ~596 tests. Keep regular execution unchanged while
# distributing instrumented work more finely. Counts affect coverage placement,
# never whether a test runs. Small process batches retain the timeout safeguard.
RACE_SHARDS = {"core": 8, "server": 16, "workspace": 8, "transcript": 2}
RACE_BATCH_SIZE = 8
TEST_NAME = re.compile(r"^(?:Test|Fuzz|Example)\w*$")


def matrix(race: bool = False) -> dict:
    counts = RACE_SHARDS if race else {"core": 1, **{k: v[1] for k, v in PACKAGE_SHARDS.items()}}
    return {"include": [{"group": group, "index": index}
                        for group, count in counts.items() for index in range(count)]}


def go_output(repo: Path, *args: str) -> str:
    result = subprocess.run(["go", *args], cwd=repo, text=True, capture_output=True, check=False)
    if result.returncode:
        raise RuntimeError(f"go {' '.join(args)} failed:\n{result.stderr}{result.stdout}")
    return result.stdout


def partition(names: list[str], count: int) -> list[list[str]]:
    if count < 1 or len(names) != len(set(names)):
        raise ValueError("shard count must be positive and inventory names must be unique")
    shards: list[list[str]] = [[] for _ in range(count)]
    for index, name in enumerate(sorted(names)):
        shards[index % count].append(name)
    if sorted(name for shard in shards for name in shard) != sorted(names):
        raise ValueError("test shard inventory is incomplete")
    return shards


def test_batches(names: list[str], race: bool, size: int | None = None) -> list[list[str]]:
    ordered = sorted(names)
    if size is None:
        size = RACE_BATCH_SIZE if race else max(1, len(ordered))
    if size < 1:
        raise ValueError("process batch size must be positive")
    return [ordered[start:start + size] for start in range(0, len(ordered), size)] or [[]]


def discover(repo: Path, packages: list[str], race: bool) -> dict[str, list[str]]:
    args = ["test", "-buildvcs=false", "-json", "-p", "1", "-list", "."]
    if race:
        args.append("-race")
    inventory: dict[str, list[str]] = {package: [] for package in packages}
    completed = set()
    for line in go_output(repo, *args, *packages).splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError as error:
            raise ValueError("Go inventory output is not JSON") from error
        package = event.get("Package")
        if package not in inventory:
            raise ValueError("Go inventory contains an unexpected package")
        name = event.get("Output", "").strip()
        if event.get("Action") == "output" and TEST_NAME.fullmatch(name):
            inventory[package].append(name)
        if event.get("Action") in {"pass", "skip"}:
            completed.add(package)
    if completed != set(packages):
        raise ValueError("Go inventory did not complete every package")
    for names in inventory.values():
        partition(names, 1)  # Reject duplicate discoveries rather than omit tests.
    return inventory


def plan(repo: Path, group: str, index: int, race: bool) -> dict:
    module = go_output(repo, "list", "-m").strip()
    packages = go_output(repo, "list", "-buildvcs=false", "./...").splitlines()
    heavy = {f"{module}/{path}" for path, _ in PACKAGE_SHARDS.values()}
    if not heavy.issubset(set(packages)):
        raise ValueError("configured runtime package is absent; update the shard authority")
    entries = matrix(race)["include"]
    if {"group": group, "index": index} not in entries:
        raise ValueError(f"invalid runtime shard: {group}/{index}")
    count = sum(entry["group"] == group for entry in entries)
    if group == "core":
        targets = partition([package for package in packages if package not in heavy], count)[index]
    else:
        targets = [f"{module}/{PACKAGE_SHARDS[group][0]}"]
    if not targets:
        raise ValueError("runtime shard has no packages")
    inventory = discover(repo, targets, race)
    selected = {package: (names if group == "core" else partition(names, count)[index])
                for package, names in inventory.items()}
    if group != "core" and not any(selected.values()):
        raise ValueError("runtime shard has no executable tests")
    return execution_plan(group, index, race, inventory, selected)


def execution_plan(group: str, index: int, race: bool, inventory: dict[str, list[str]],
                   selected: dict[str, list[str]], batch_size: int | None = None) -> dict:
    targets = list(selected)
    if not targets:
        raise ValueError("runtime plan has no packages")
    args = ["go", "test", "-buildvcs=false", "-json", "-p", "1", "-timeout=30m", "-count=1"]
    if race:
        args.append("-race")
    commands = []
    if not race and group == "core" and batch_size is None:
        commands.append([*args, *targets])
    else:
        for package, names in selected.items():
            for batch in test_batches(names, race, batch_size):
                pattern = "^(?:" + "|".join(re.escape(name) for name in batch) + ")$" if batch else "^$"
                commands.append([*args, "-run", pattern, package])
    digest = hashlib.sha256(json.dumps(inventory, sort_keys=True).encode()).hexdigest()
    return {"group": group, "index": index, "race": race, "packages": targets,
            "inventory_count": sum(map(len, inventory.values())), "inventory_sha256": digest,
            "selected": selected, "commands": commands}
