"""Bounded, disjoint PR test partitions preserve every discovered test."""
from __future__ import annotations

import hashlib

if __package__:
    from .runtime_test_inventory import partition
else:
    from runtime_test_inventory import partition

MAX_SHARDS = 4


def job_matrix(packages: list[str]) -> dict:
    count = min(MAX_SHARDS, max(1, len(packages)))
    return {"include": [{"index": index, "count": count} for index in range(count)]}


def select(inventory: dict[str, list[str]], index: int, count: int) -> dict[str, list[str]]:
    if not 1 <= count <= MAX_SHARDS or not 0 <= index < count:
        raise ValueError("invalid affected-test partition")
    selected = {}
    for package, names in inventory.items():
        if names:
            tests = partition(names, count)[index]
            if tests:
                selected[package] = tests
        elif int.from_bytes(hashlib.sha256(package.encode()).digest()[:4], "big") % count == index:
            # Compile packages without tests exactly once, not once per shard.
            selected[package] = []
    return selected
