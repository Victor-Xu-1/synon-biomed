#!/usr/bin/env python3
"""Generate or verify the content-addressed bundled Agent catalog manifest."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import stat


SOURCE = "synonbiomed-v1.1/runtime/assets/agents"
AGENT_ROOT = Path("assets/synonbiomed/agents")
MANIFEST_PATH = Path("assets/synonbiomed/agents.manifest.json")


def build_manifest(repo: Path) -> dict[str, object]:
    root = repo.joinpath(AGENT_ROOT)
    root_stat = root.lstat()
    if not stat.S_ISDIR(root_stat.st_mode) or root.is_symlink():
        raise ValueError("agent root must be a real directory")
    agents = sorted(
        entry.name
        for entry in root.iterdir()
        if entry.is_dir() and not entry.is_symlink() and entry.joinpath("metadata.yaml").is_file()
    )
    if not agents:
        raise ValueError("agent root contains no Agent metadata")
    files: list[dict[str, object]] = []
    for path in sorted(root.rglob("*"), key=lambda value: value.relative_to(root).as_posix()):
        metadata = path.lstat()
        relative = path.relative_to(root).as_posix()
        if stat.S_ISLNK(metadata.st_mode):
            raise ValueError(f"agent catalog contains symbolic link: {relative}")
        if stat.S_ISDIR(metadata.st_mode):
            continue
        if not stat.S_ISREG(metadata.st_mode):
            raise ValueError(f"agent catalog contains non-regular file: {relative}")
        raw = path.read_bytes()
        files.append({"path": relative, "sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)})
    return {"schemaVersion": 1, "source": SOURCE, "agents": agents, "files": files}


def encoded_manifest(value: dict[str, object]) -> bytes:
    return (json.dumps(value, ensure_ascii=False, indent=2) + "\n").encode("utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=".")
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument("--check", action="store_true")
    mode.add_argument("--write", action="store_true")
    args = parser.parse_args()
    repo = Path(args.repo).resolve()
    output = repo.joinpath(MANIFEST_PATH)
    expected = encoded_manifest(build_manifest(repo))
    if args.check:
        try:
            current = output.read_bytes()
        except OSError as exc:
            raise SystemExit(f"agent manifest is unavailable: {exc}") from exc
        if current != expected:
            raise SystemExit("agent manifest drift; run agent_manifest.py --write after review")
        print("agent-manifest: ok")
        return 0
    staging = output.with_name(output.name + ".tmp")
    staging.write_bytes(expected)
    os.replace(staging, output)
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
