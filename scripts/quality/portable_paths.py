#!/usr/bin/env python3
"""Reject tracked paths that cannot coexist in a portable source checkout."""

from __future__ import annotations

import argparse
from collections import defaultdict
from pathlib import Path, PurePosixPath
import subprocess
import sys
import unicodedata
from typing import Iterable


WINDOWS_RESERVED_BASENAMES = {
    "aux",
    "con",
    "nul",
    "prn",
    *(f"com{index}" for index in range(1, 10)),
    *(f"lpt{index}" for index in range(1, 10)),
}
WINDOWS_INVALID_CHARACTERS = frozenset('<>:"\\|?*')


class AuditError(RuntimeError):
    """Raised when the tracked tree is not portable."""


def tracked_paths(root: Path) -> list[str]:
    result = subprocess.run(
        [
            "git",
            "-c",
            f"safe.directory={root}",
            "-C",
            str(root),
            "ls-files",
            "--cached",
            "-z",
        ],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
        timeout=120,
    )
    if result.returncode:
        detail = result.stderr.decode("utf-8", "replace").strip()
        raise AuditError(f"git tracked-path inventory failed ({result.returncode}): {detail}")
    return [raw.decode("utf-8", "surrogateescape") for raw in result.stdout.split(b"\0") if raw]


def portable_key(path: str) -> str:
    return unicodedata.normalize("NFC", path).casefold()


def path_nodes(path: str) -> Iterable[str]:
    parts = PurePosixPath(path).parts
    for length in range(1, len(parts) + 1):
        yield "/".join(parts[:length])


def invalid_windows_segments(paths: Iterable[str]) -> list[str]:
    failures: set[str] = set()
    for path in paths:
        for segment in PurePosixPath(path).parts:
            basename = segment.split(".", 1)[0].casefold()
            if (
                not segment
                or segment.endswith((" ", "."))
                or basename in WINDOWS_RESERVED_BASENAMES
                or any(character in WINDOWS_INVALID_CHARACTERS or ord(character) < 32 for character in segment)
            ):
                failures.add(path)
                break
    return sorted(failures)


def case_insensitive_collisions(paths: Iterable[str]) -> list[list[str]]:
    nodes_by_key: dict[str, set[str]] = defaultdict(set)
    for path in paths:
        for node in path_nodes(path):
            nodes_by_key[portable_key(node)].add(node)
    return sorted(
        (sorted(nodes) for nodes in nodes_by_key.values() if len(nodes) > 1),
        key=lambda nodes: portable_key(nodes[0]),
    )


def validate_paths(paths: Iterable[str]) -> int:
    tracked = list(paths)
    invalid = invalid_windows_segments(tracked)
    collisions = case_insensitive_collisions(tracked)
    failures: list[str] = []
    if invalid:
        failures.append("Windows-invalid tracked paths: " + ", ".join(invalid))
    for collision in collisions:
        failures.append("case-insensitive tracked path collision: " + " <> ".join(collision))
    if failures:
        raise AuditError("; ".join(failures))
    return len(tracked)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        count = validate_paths(tracked_paths(args.repo.resolve()))
    except (AuditError, OSError, subprocess.SubprocessError) as exc:
        print(f"portable-path audit failed: {exc}", file=sys.stderr)
        return 2
    print(f"portable-path audit passed: {count} tracked files")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
