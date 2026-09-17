#!/usr/bin/env python3
"""Verify an artifact against a JSON manifest in its directory ancestry."""

from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path, PurePosixPath
from typing import Any


def fail(message: str) -> int:
    print(message, file=sys.stderr)
    return 1


def is_within(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
    except ValueError:
        return False
    return True


def artifact_digest(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as artifact:
        for chunk in iter(lambda: artifact.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def manifest_files(document: Any) -> list[dict[str, Any]]:
    if not isinstance(document, dict):
        return []
    files = document.get("files")
    if not isinstance(files, list):
        return []
    return [entry for entry in files if isinstance(entry, dict)]


def normalized_entry_path(raw_path: Any) -> PurePosixPath | None:
    if not isinstance(raw_path, str) or not raw_path:
        return None
    path = PurePosixPath(raw_path)
    if path.is_absolute() or ".." in path.parts:
        return None
    while path.parts and path.parts[0] == ".":
        path = PurePosixPath(*path.parts[1:])
    return path if path.parts else None


def candidate_manifests(root: Path, artifact: Path) -> list[Path]:
    manifests: list[Path] = []
    directory = artifact.parent
    while True:
        manifests.extend(sorted(directory.glob("*.manifest.json")))
        manifest = directory / "manifest.json"
        if manifest.is_file():
            manifests.append(manifest)
        if directory == root:
            break
        directory = directory.parent
    return manifests


def artifact_paths_for_manifest(manifest: Path, artifact: Path) -> set[PurePosixPath]:
    roots = [manifest.parent]
    suffix = ".manifest.json"
    if manifest.name.endswith(suffix):
        pack_name = manifest.name[: -len(suffix)]
        if pack_name:
            roots.insert(0, manifest.parent / pack_name)
    relatives: set[PurePosixPath] = set()
    for candidate in roots:
        try:
            relative = artifact.relative_to(candidate)
        except ValueError:
            continue
        if relative.parts:
            relatives.add(PurePosixPath(relative.as_posix()))
    return relatives


def verify(root_arg: str, artifact_arg: str) -> int:
    root = Path(root_arg).resolve(strict=True)
    artifact = Path(artifact_arg).resolve(strict=True)
    if not root.is_dir() or not artifact.is_file() or not is_within(artifact, root):
        return fail(f"artifact is not a regular file inside the source root: {artifact}")

    artifact_hash = artifact_digest(artifact)
    artifact_bytes = artifact.stat().st_size

    for manifest in candidate_manifests(root, artifact):
        try:
            document = json.loads(manifest.read_text(encoding="utf-8"))
        except (OSError, UnicodeError, json.JSONDecodeError):
            continue

        relative_artifacts = artifact_paths_for_manifest(manifest, artifact)
        for entry in manifest_files(document):
            entry_path = normalized_entry_path(entry.get("path"))
            entry_bytes = entry.get("bytes")
            if (
                entry_path in relative_artifacts
                and isinstance(entry_bytes, int)
                and not isinstance(entry_bytes, bool)
                and entry_bytes == artifact_bytes
                and isinstance(entry.get("sha256"), str)
                and entry["sha256"].lower() == artifact_hash
            ):
                return 0

    return fail(f"no matching provenance manifest found for {artifact}")


def main() -> int:
    if len(sys.argv) != 3:
        return fail("usage: verify-artifact-provenance.py ROOT ARTIFACT")
    try:
        return verify(sys.argv[1], sys.argv[2])
    except (OSError, RuntimeError) as error:
        return fail(str(error))


if __name__ == "__main__":
    raise SystemExit(main())
