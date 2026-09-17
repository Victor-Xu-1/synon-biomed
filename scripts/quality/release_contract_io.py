"""Fail-closed filesystem and Git primitives for release contracts."""

from __future__ import annotations

from datetime import datetime
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import subprocess
import tempfile
from typing import Any


SHA = re.compile(r"^[0-9a-f]{40,64}$")


class CandidateError(RuntimeError):
    """Stable release-candidate validation failure."""


def reject_duplicate(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
    result: dict[str, Any] = {}
    for key, value in pairs:
        if key in result:
            raise CandidateError("candidate_json_duplicate_key")
        result[key] = value
    return result


def read_regular(path: Path, code: str, max_bytes: int | None = None) -> bytes:
    try:
        info = path.lstat()
        if (
            not stat.S_ISREG(info.st_mode)
            or stat.S_ISLNK(info.st_mode)
            or info.st_nlink != 1
            or (max_bytes is not None and info.st_size > max_bytes)
        ):
            raise CandidateError(code)
        return path.read_bytes()
    except OSError as error:
        raise CandidateError(code) from error


def read_json(path: Path, code: str) -> dict[str, Any]:
    try:
        value = json.loads(
            read_regular(path, code, max_bytes=1 << 20).decode("utf-8"),
            object_pairs_hook=reject_duplicate,
        )
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise CandidateError(code) from error
    if not isinstance(value, dict):
        raise CandidateError(code)
    return value


def digest(path: Path) -> str:
    result = hashlib.sha256()
    try:
        with path.open("rb") as handle:
            for chunk in iter(lambda: handle.read(1024 * 1024), b""):
                result.update(chunk)
    except OSError as error:
        raise CandidateError("candidate_artifact_read_invalid") from error
    return result.hexdigest()


def run(repo: Path, *arguments: str, code: str) -> bytes:
    try:
        result = subprocess.run(
            [*arguments],
            cwd=repo,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=False,
            timeout=120,
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise CandidateError(code) from error
    if result.returncode:
        raise CandidateError(code)
    return result.stdout


def git(repo: Path, *arguments: str, code: str = "candidate_git_invalid") -> str:
    try:
        return run(repo, "git", *arguments, code=code).decode("utf-8").strip()
    except UnicodeDecodeError as error:
        raise CandidateError(code) from error


def validate_source(repo: Path) -> tuple[str, str, str]:
    repo = repo.resolve()
    top = Path(git(repo, "rev-parse", "--show-toplevel")).resolve()
    if top != repo:
        raise CandidateError("candidate_repository_root_invalid")
    status = run(
        repo,
        "git",
        "status",
        "--porcelain=v1",
        "-z",
        "--untracked-files=normal",
        code="candidate_source_status_invalid",
    )
    if status:
        raise CandidateError("candidate_source_dirty")
    commit = git(repo, "rev-parse", "HEAD")
    tree = git(repo, "rev-parse", "HEAD^{tree}")
    committed_at = git(repo, "show", "-s", "--format=%cI", "HEAD")
    if not SHA.fullmatch(commit) or not SHA.fullmatch(tree):
        raise CandidateError("candidate_source_revision_invalid")
    try:
        committed = datetime.fromisoformat(committed_at.replace("Z", "+00:00"))
    except ValueError as error:
        raise CandidateError("candidate_source_time_invalid") from error
    if committed.tzinfo is None or committed.utcoffset() is None:
        raise CandidateError("candidate_source_time_invalid")
    return commit, tree, committed_at


def same_tree_json(repo: Path, relative: str, code: str) -> dict[str, Any]:
    pure = PurePosixPath(relative)
    if pure.is_absolute() or ".." in pure.parts or pure.as_posix() != relative:
        raise CandidateError(code)
    path = repo.joinpath(*pure.parts)
    working = read_regular(path, code, max_bytes=1 << 20)
    committed = run(repo, "git", "show", f"HEAD:{relative}", code=code)
    if working != committed:
        raise CandidateError("candidate_control_tree_mismatch")
    try:
        value = json.loads(working, object_pairs_hook=reject_duplicate)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise CandidateError(code) from error
    if not isinstance(value, dict):
        raise CandidateError(code)
    return value


def atomic_new_json(path: Path, value: dict[str, Any]) -> None:
    if path.exists():
        raise CandidateError("candidate_manifest_exists")
    descriptor, temporary = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary_path = Path(temporary)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="\n") as handle:
            json.dump(value, handle, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.chmod(temporary_path, 0o644)
        os.replace(temporary_path, path)
    except Exception:
        temporary_path.unlink(missing_ok=True)
        raise
