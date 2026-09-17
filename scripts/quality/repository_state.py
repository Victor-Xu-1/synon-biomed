#!/usr/bin/env python3
"""Create, compare, and validate read-only Git repository evidence."""

from __future__ import annotations

import argparse
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import stat
import subprocess
import sys
import tempfile
from typing import Any, Iterable


SCHEMA_VERSION = 3
CLASSIFICATION_VERSION = 5
HYGIENE_REPORT_SCHEMA_VERSION = 1
HASH_LENGTH = 64
PORTABLE_CATEGORIES = {"source", "test", "docs"}
WITHHELD_CATEGORIES = {"credential", "user-data", "unknown"}
ALL_CATEGORIES = PORTABLE_CATEGORIES | WITHHELD_CATEGORIES | {"generated", "dependency", "build", "cache", "log"}
ASSIGNED_OWNERS = {"core-runtime", "product-ux", "repository-steward"}
SENSITIVE_NAMES = {
    ".env",
    ".env.local",
    ".env.production",
    ".netrc",
    ".npmrc",
    ".pypirc",
    "credentials.json",
    "id_ed25519",
    "id_rsa",
    "secrets.json",
    "secrets.yaml",
    "secrets.yml",
    "service-account.json",
}
SENSITIVE_SUFFIXES = {".key", ".p12", ".pfx", ".pem"}
USER_DATA_SUFFIXES = {".db", ".sqlite", ".sqlite3"}
USER_DATA_ROOTS = {"uploads", "user-data", "runtime-data"}
SOURCE_SUFFIXES = {
    ".c",
    ".cc",
    ".cpp",
    ".css",
    ".go",
    ".h",
    ".html",
    ".java",
    ".js",
    ".jsx",
    ".mjs",
    ".py",
    ".ps1",
    ".r",
    ".rs",
    ".scss",
    ".sh",
    ".sql",
    ".ts",
    ".tsx",
    ".yaml",
    ".yml",
    ".json",
    ".toml",
}
STATIC_ASSET_SUFFIXES = {".gif", ".ico", ".jpeg", ".jpg", ".png", ".svg", ".webp", ".woff", ".woff2"}
DEPENDENCY_LOCK_NAMES = {
    "go.sum",
    "package-lock.json",
    "pnpm-lock.yaml",
    "yarn.lock",
    "poetry.lock",
    "requirements.txt",
    "cargo.lock",
}
SAFE_ENV_TEMPLATE_NAMES = {".env.example", ".env.sample"}
SOURCE_CONFIG_NAMES = SAFE_ENV_TEMPLATE_NAMES | {
    ".dockerignore",
    ".gitattributes",
    ".gitignore",
    "containerfile",
    "dockerfile",
    "go.mod",
    "makefile",
    "manifest.webmanifest",
    "package.json",
    "pnpm-workspace.yaml",
}
ROOT_RUNTIME_DATABASE_NAMES = {"mcp-ketcher", "mcp-ketcher-shm", "mcp-ketcher-wal"}
ROOT_BUILD_OUTPUT_NAMES = {"server.test", "synon"}
KNOWN_ONE_OFF_FRONTEND_PROBE_PATHS = {
    "frontend/complex_probe_no_composer.png",
    "frontend/complex_task_probe.mjs",
    "frontend/pw_login.mjs",
    "frontend/pw_new_task.mjs",
    "frontend/pw_new_task_kras.mjs",
    "frontend/pw_probe.mjs",
    "frontend/pw_probe2.mjs",
    "frontend/pw_task1.mjs",
    "frontend/pw_task_run.mjs",
    "frontend/pw_task_run2.mjs",
}
COMPETING_DEPENDENCY_AUTHORITY_PATHS = {
    "frontend/bun.lock",
    "frontend/bun.lockb",
    "frontend/pnpm-lock.yaml",
    "frontend/pnpm-workspace.yaml",
    "frontend/yarn.lock",
}


class AuditError(RuntimeError):
    """Raised for an invalid or unverifiable repository state."""


def run_git(root: Path, *args: str, timeout: int = 120) -> bytes:
    environment = dict(os.environ)
    environment["GIT_OPTIONAL_LOCKS"] = "0"
    result = subprocess.run(
        ["git", "-C", str(root), *args],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        timeout=timeout,
        check=False,
        env=environment,
    )
    if result.returncode:
        message = result.stderr.decode("utf-8", "replace").strip()
        raise AuditError(f"git {' '.join(args)} failed ({result.returncode}): {message}")
    return result.stdout


def decode_path(raw: bytes) -> str:
    return raw.decode("utf-8", "surrogateescape")


def split_status_record(raw: bytes) -> tuple[str, str, bytes, dict[str, str | None]]:
    kind = chr(raw[0])
    if kind == "1":
        fields = raw.split(b" ", 8)
        if len(fields) != 9:
            raise AuditError("invalid porcelain v2 ordinary record")
        metadata = {
            "submodule": fields[2].decode("ascii"), "head_mode": fields[3].decode("ascii"),
            "index_mode": fields[4].decode("ascii"), "worktree_mode": fields[5].decode("ascii"),
            "head_oid": fields[6].decode("ascii"), "index_oid": fields[7].decode("ascii"),
        }
        return kind, fields[1].decode("ascii"), fields[8], metadata
    if kind == "2":
        fields = raw.split(b" ", 9)
        if len(fields) != 10:
            raise AuditError("invalid porcelain v2 rename/copy record")
        metadata = {
            "submodule": fields[2].decode("ascii"), "head_mode": fields[3].decode("ascii"),
            "index_mode": fields[4].decode("ascii"), "worktree_mode": fields[5].decode("ascii"),
            "head_oid": fields[6].decode("ascii"), "index_oid": fields[7].decode("ascii"),
            "score": fields[8].decode("ascii"),
        }
        return kind, fields[1].decode("ascii"), fields[9], metadata
    if kind == "u":
        fields = raw.split(b" ", 10)
        if len(fields) != 11:
            raise AuditError("invalid porcelain v2 unmerged record")
        metadata = {
            "submodule": fields[2].decode("ascii"), "head_mode": fields[3].decode("ascii"),
            "index_mode": fields[4].decode("ascii"), "worktree_mode": fields[5].decode("ascii"),
            "head_oid": fields[7].decode("ascii"), "index_oid": fields[8].decode("ascii"),
        }
        return kind, fields[1].decode("ascii"), fields[10], metadata
    if kind in {"?", "!"} and raw[1:2] == b" ":
        return kind, "??" if kind == "?" else "!!", raw[2:], {
            "submodule": None, "head_mode": None, "index_mode": None,
            "worktree_mode": None, "head_oid": None, "index_oid": None,
        }
    raise AuditError(f"unsupported porcelain v2 record: {kind!r}")


def parse_status(raw: bytes) -> list[dict[str, Any]]:
    tokens = raw.split(b"\0")
    records: list[dict[str, Any]] = []
    cursor = 0
    while cursor < len(tokens):
        token = tokens[cursor]
        cursor += 1
        if not token:
            continue
        kind, xy, path_raw, metadata = split_status_record(token)
        previous: str | None = None
        if kind == "2":
            if cursor >= len(tokens) or not tokens[cursor]:
                raise AuditError("rename/copy record is missing its original path")
            previous = decode_path(tokens[cursor])
            cursor += 1
        path = decode_path(path_raw)
        record = classify_record(path, previous, kind, xy)
        record.update(metadata)
        records.append(record)
    return records


def status_states(kind: str, xy: str) -> list[str]:
    if kind == "?":
        return ["untracked"]
    if kind == "!":
        return ["ignored"]
    if kind == "u" or "U" in xy:
        return ["conflicted"]
    states: list[str] = []
    if "R" in xy:
        states.append("renamed")
    if "C" in xy:
        states.append("copied")
    if "A" in xy:
        states.append("added")
    if "D" in xy:
        states.append("deleted")
    if "M" in xy or "T" in xy:
        states.append("modified")
    return states or ["other"]


def path_category(path: str) -> str:
    posix = PurePosixPath(path)
    parts = {part.lower() for part in posix.parts}
    name = posix.name.lower()
    suffix = posix.suffix.lower()
    if name in {"license", "license.txt", "notice", "notice.txt"}:
        return "docs"
    if len(posix.parts) == 1 and name in ROOT_RUNTIME_DATABASE_NAMES:
        return "user-data"
    if len(posix.parts) == 1 and name in ROOT_BUILD_OUTPUT_NAMES:
        return "build"
    if name.endswith(":zone.identifier"):
        return "cache"
    if path in KNOWN_ONE_OFF_FRONTEND_PROBE_PATHS:
        return "cache"
    if (
        name in SENSITIVE_NAMES
        or suffix in SENSITIVE_SUFFIXES
        or (name.startswith(".env") and name not in SAFE_ENV_TEMPLATE_NAMES)
    ):
        return "credential"
    first = posix.parts[0].lower() if posix.parts else ""
    if suffix in USER_DATA_SUFFIXES or first in USER_DATA_ROOTS:
        return "user-data"
    if name in DEPENDENCY_LOCK_NAMES or suffix == ".lock" or parts.intersection({"node_modules", "vendor"}):
        return "dependency"
    if parts.intersection({"dist", "out", "release"}):
        return "build"
    if parts.intersection({".cache", ".pytest_cache", "__pycache__", "coverage", "playwright-report", "test-results"}):
        return "cache"
    if suffix == ".log" or "logs" in parts:
        return "log"
    if "generated" in parts or name.endswith((".generated.ts", ".generated.go")):
        return "generated"
    if (
        "tests" in parts
        or "testdata" in parts
        or name.endswith("_test.go")
        or ".test." in name
        or ".spec." in name
        or ".e2e." in name
    ):
        return "test"
    if "docs" in parts or suffix in {".md", ".mdx", ".rst"}:
        return "docs"
    if suffix in STATIC_ASSET_SUFFIXES and first in {"assets", "frontend"}:
        return "source"
    if name in SOURCE_CONFIG_NAMES or suffix in SOURCE_SUFFIXES:
        return "source"
    if suffix == ".txt" and parts.intersection({"assets", "prompts", "templates"}):
        return "source"
    return "unknown"


def path_owner(path: str) -> str:
    posix = PurePosixPath(path)
    first = posix.parts[0] if posix.parts else ""
    if first == "frontend":
        return "product-ux"
    if (
        first in {"assets", "build", "internal", "cmd", "packages", "skills", "tools"}
        or path in ROOT_BUILD_OUTPUT_NAMES
        or path in ROOT_RUNTIME_DATABASE_NAMES
        or path in {"go.mod", "go.sum"}
    ):
        return "core-runtime"
    if (
        first in {".github", "docs", "scripts"}
        or path
        in {
            ".dockerignore",
            ".env.example",
            ".gitattributes",
            ".gitignore",
            "CODEOWNERS",
            "CONTRIBUTING.md",
            "Makefile",
            "README.md",
            "SECURITY.md",
        }
    ):
        return "repository-steward"
    return "unassigned"


def hygiene_report(snapshot_value: dict[str, Any]) -> dict[str, Any]:
    failures = validate_snapshot(snapshot_value)
    if failures:
        raise AuditError(f"invalid snapshot: {','.join(failures[:8])}")

    records = snapshot_value["records"]
    dispositions = counter(records, "recommended_disposition")
    cohorts: dict[str, list[dict[str, Any]]] = {
        "discard_or_regenerate": [],
        "competing_dependency_authority": [],
        "dependency_change_review": [],
        "sensitive_quarantine": [],
        "unassigned_owner": [],
        "untracked_origin_review": [],
        "tracked_deletions": [],
    }
    for record in records:
        item = {
            "path": record["path"],
            "states": record["states"],
            "category": record["category"],
            "owner": record["owner"],
            "origin_provenance": record["origin_provenance"],
            "recommended_disposition": record["recommended_disposition"],
            "path_disclosure": record["path_disclosure"],
        }
        if record["category"] in {"build", "cache", "generated", "log"}:
            cohorts["discard_or_regenerate"].append(item)
        if record["path"] in COMPETING_DEPENDENCY_AUTHORITY_PATHS:
            cohorts["competing_dependency_authority"].append(item)
        elif record["category"] == "dependency":
            cohorts["dependency_change_review"].append(item)
        if record["category"] in {"credential", "user-data"}:
            cohorts["sensitive_quarantine"].append(item)
        if record["owner"] == "unassigned":
            cohorts["unassigned_owner"].append(item)
        if record["origin_provenance"] == "unknown":
            cohorts["untracked_origin_review"].append(item)
        if "deleted" in record["states"]:
            cohorts["tracked_deletions"].append(item)

    for items in cohorts.values():
        items.sort(key=lambda item: str(item["path"]))

    return {
        "schema_version": HYGIENE_REPORT_SCHEMA_VERSION,
        "kind": "repository-hygiene-report",
        "snapshot_fingerprint": snapshot_value["snapshot_fingerprint"],
        "head": snapshot_value["head"],
        "branch": snapshot_value["branch"],
        "captured_at": snapshot_value["captured_at"],
        "coverage": snapshot_value["coverage"],
        "summary": snapshot_value["summary"],
        "recommended_dispositions": dispositions,
        "cohort_counts": {name: len(items) for name, items in cohorts.items()},
        "cohorts": cohorts,
        "completion": {
            "all_paths_classified": snapshot_value["summary"]["categories"].get("unknown", 0) == 0,
            "all_paths_owned": snapshot_value["summary"]["owners"].get("unassigned", 0) == 0,
            "all_origins_reviewed": not cohorts["untracked_origin_review"],
            "all_ports_approved": snapshot_value["summary"]["port_approved"] == len(records),
            "full_repository_unchanged_claim_allowed": snapshot_value["coverage"][
                "full_repository_unchanged_claim_allowed"
            ],
        },
    }


def disposition(category: str, owner: str) -> str:
    if category in {"credential", "user-data"}:
        return "quarantine-sensitive-review"
    if category in {"build", "cache", "dependency", "log", "generated"}:
        return "discard-or-regenerate-review"
    if owner == "unassigned" or category == "unknown":
        return "quarantine-owner-review"
    return "review-before-port"


def category_sensitivity(category: str) -> str:
    return "potential-secret" if category == "credential" else "user-data" if category == "user-data" else "normal"


def classify_record(path: str, previous: str | None, kind: str, xy: str) -> dict[str, Any]:
    states = status_states(kind, xy)
    category = path_category(path)
    owner = path_owner(path)
    sensitivity = category_sensitivity(category)
    previous_category = path_category(previous) if previous else None
    previous_owner = path_owner(previous) if previous else None
    previous_sensitivity = category_sensitivity(previous_category) if previous_category else None
    cross_owner = bool(previous and previous_owner != owner)
    origin_provenance = (
        "git-reported-previous-path" if kind == "2" else
        "unknown" if kind == "?" or (kind == "1" and "added" in states) else "same-path"
    )
    return {
        "path": path,
        "previous_path": previous,
        "previous_category": previous_category,
        "previous_owner": previous_owner,
        "previous_sensitivity": previous_sensitivity,
        "cross_owner": cross_owner,
        "record_type": kind,
        "index_status": xy[0],
        "worktree_status": xy[1],
        "states": states,
        "tracked": kind not in {"?", "!"},
        "category": category,
        "owner": owner,
        "source_confidence": "unattributed",
        "origin_provenance": origin_provenance,
        "sensitivity": sensitivity,
        "recommended_disposition": (
            "quarantine-origin-review" if origin_provenance == "unknown" else
            "quarantine-cross-owner-review" if cross_owner else disposition(category, owner)
        ),
        "port_approved": False,
    }


def counter(records: Iterable[dict[str, Any]], field: str) -> dict[str, int]:
    return dict(sorted(Counter(str(record[field]) for record in records).items()))


def status_counter(records: Iterable[dict[str, Any]]) -> dict[str, int]:
    values: Counter[str] = Counter()
    for record in records:
        values.update(record["states"])
    return dict(sorted(values.items()))


def sha256_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def canonical_json(value: Any) -> bytes:
    return json.dumps(value, ensure_ascii=True, sort_keys=True, separators=(",", ":")).encode("utf-8")


def valid_sha256(value: Any) -> bool:
    return isinstance(value, str) and len(value) == HASH_LENGTH and value == value.lower() and all(
        character in "0123456789abcdef" for character in value
    )


def valid_git_oid(value: Any) -> bool:
    return isinstance(value, str) and len(value) in {40, 64} and value == value.lower() and all(
        character in "0123456789abcdef" for character in value
    )


def canonical_path(value: Any) -> bool:
    if not isinstance(value, str) or not value or "\\" in value or value.startswith("/"):
        return False
    path = PurePosixPath(value)
    return path.as_posix() == value and all(part not in {"", ".", ".."} for part in path.parts)


def valid_redacted_path(value: Any) -> bool:
    return isinstance(value, str) and value.startswith("redacted:") and valid_sha256(value[9:])


def redacted_path(path: str) -> str:
    return "redacted:" + sha256_bytes(path.encode("utf-8", "surrogateescape"))


def disclosed_path(path: Any) -> str:
    text = str(path)
    return redacted_path(text) if not text.startswith("<") and path_category(text) in WITHHELD_CATEGORIES else text


def hash_regular_file(path: Path) -> str:
    flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
    descriptor = os.open(path, flags)
    try:
        metadata = os.fstat(descriptor)
        if not stat.S_ISREG(metadata.st_mode):
            raise AuditError("content target changed type while being sampled")
        digest = hashlib.sha256()
        while True:
            chunk = os.read(descriptor, 1024 * 1024)
            if not chunk:
                return digest.hexdigest()
            digest.update(chunk)
    finally:
        os.close(descriptor)


def record_content(root: Path, record: dict[str, Any]) -> dict[str, Any]:
    category = record["category"]
    if record.get("origin_provenance") == "unknown":
        return {"state": "not-covered", "reason": "origin-provenance-unknown"}
    withheld_side = category if category in WITHHELD_CATEGORIES else record.get("previous_category")
    if withheld_side in WITHHELD_CATEGORIES:
        return {"state": "withheld", "reason": "current-or-previous-path-content-not-read"}
    if record.get("owner") not in ASSIGNED_OWNERS:
        return {"state": "not-covered", "reason": "owner-unassigned"}
    if category not in PORTABLE_CATEGORIES or record["sensitivity"] != "normal":
        return {"state": "not-covered", "reason": "category-not-portable"}
    if "conflicted" in record["states"]:
        return {"state": "not-covered", "reason": "conflicted-record"}
    relative = record["path"]
    if not canonical_path(relative):
        return {"state": "not-covered", "reason": "non-canonical-path"}
    target = root.joinpath(*PurePosixPath(relative).parts)
    if "deleted" in record["states"]:
        oid = record.get("head_oid") or record.get("index_oid")
        if not isinstance(oid, str) or not oid or set(oid) == {"0"}:
            return {"state": "not-covered", "reason": "deleted-git-oid-unavailable"}
        marker = b"deleted\0" + oid.encode("ascii")
        return {"state": "captured", "kind": "deleted", "git_oid": oid, "sha256": sha256_bytes(marker)}
    mode_values = {record.get("head_mode"), record.get("index_mode"), record.get("worktree_mode")}
    if "160000" in mode_values or str(record.get("submodule") or "").startswith("S"):
        oid = record.get("index_oid") or record.get("head_oid")
        if not isinstance(oid, str) or not oid or set(oid) == {"0"}:
            return {"state": "not-covered", "reason": "gitlink-oid-unavailable"}
        marker = b"gitlink\0" + oid.encode("ascii")
        return {"state": "captured", "kind": "gitlink", "git_oid": oid, "sha256": sha256_bytes(marker)}
    try:
        metadata = target.lstat()
    except FileNotFoundError:
        return {"state": "not-covered", "reason": "worktree-path-missing"}
    if stat.S_ISLNK(metadata.st_mode):
        link_target = os.readlink(target).encode("utf-8", "surrogateescape")
        return {"state": "captured", "kind": "symlink-target", "sha256": sha256_bytes(link_target)}
    if not stat.S_ISREG(metadata.st_mode):
        return {"state": "not-covered", "reason": "unsupported-file-type"}
    return {"state": "captured", "kind": "regular", "sha256": hash_regular_file(target)}


def external_record(record: dict[str, Any], content: dict[str, Any]) -> dict[str, Any]:
    result = dict(record)
    withheld = record["category"] in WITHHELD_CATEGORIES or record.get("previous_category") in WITHHELD_CATEGORIES
    if withheld:
        result["path"] = redacted_path(record["path"])
        result["previous_path"] = redacted_path(record["previous_path"]) if record.get("previous_path") else None
        result["path_disclosure"] = "redacted-local-only"
    else:
        result["path_disclosure"] = "external-evidence-allowed"
    result["content"] = content
    result["record_fingerprint"] = sha256_bytes(canonical_json(result))
    return result


def content_manifest(root: Path, records: list[dict[str, Any]]) -> tuple[list[dict[str, Any]], dict[str, Any], str]:
    evidence = [external_record(record, record_content(root, record)) for record in records]
    evidence.sort(key=lambda item: (item["path"], item["record_type"], item.get("previous_path") or ""))
    counts = Counter(item["content"]["state"] for item in evidence)
    coverage = {
        "complete": bool(not evidence or counts.get("captured", 0) == len(evidence)), "record_count": len(evidence),
        "captured": counts.get("captured", 0),
        "withheld": counts.get("withheld", 0), "not_covered": counts.get("not-covered", 0),
        "full_repository_unchanged_claim_allowed": bool(not evidence or counts.get("captured", 0) == len(evidence)),
    }
    manifest_hash = sha256_bytes(canonical_json([item["record_fingerprint"] for item in evidence]))
    return evidence, coverage, manifest_hash


def repository_id(root: Path) -> str:
    common = run_git(root, "rev-parse", "--git-common-dir").decode("utf-8", "surrogateescape").strip()
    common_path = Path(common)
    if not common_path.is_absolute():
        common_path = root / common_path
    return sha256_bytes(str(common_path.resolve()).encode("utf-8", "surrogateescape"))


def require_shape(condition: bool) -> None:
    if not condition:
        raise AuditError("evidence document shape is invalid")


def require_object(value: Any, required: set[str], optional: set[str] | None = None) -> dict[str, Any]:
    require_shape(type(value) is dict)
    optional = optional or set()
    require_shape(required <= set(value) and set(value) <= required | optional)
    return value


def is_string(value: Any) -> bool:
    return type(value) is str


def is_integer(value: Any) -> bool:
    return type(value) is int


def is_boolean(value: Any) -> bool:
    return type(value) is bool


def is_string_list(value: Any) -> bool:
    return type(value) is list and all(is_string(item) for item in value)


def is_count_map(value: Any) -> bool:
    return type(value) is dict and all(is_string(key) and is_integer(count) for key, count in value.items())


def validate_content_shape(value: Any) -> None:
    require_shape(type(value) is dict and is_string(value.get("state")))
    state = value["state"]
    if state == "captured":
        kind = value.get("kind")
        require_shape(is_string(kind) and kind in {"regular", "symlink-target", "deleted", "gitlink"})
        required = {"state", "kind", "sha256"} | ({"git_oid"} if kind in {"deleted", "gitlink"} else set())
        require_object(value, required)
        require_shape(is_string(value["sha256"]) and ("git_oid" not in value or is_string(value["git_oid"])))
        return
    require_shape(state in {"withheld", "not-covered"})
    require_object(value, {"state", "reason"})
    require_shape(is_string(value["reason"]) and value["reason"] in {
        "origin-provenance-unknown", "current-or-previous-path-content-not-read", "owner-unassigned",
        "category-not-portable", "conflicted-record", "non-canonical-path", "deleted-git-oid-unavailable",
        "gitlink-oid-unavailable", "worktree-path-missing", "unsupported-file-type",
    })


def validate_record_shape(value: Any) -> None:
    common = {
        "path", "previous_path", "previous_category", "previous_owner", "previous_sensitivity", "cross_owner",
        "record_type", "index_status", "worktree_status", "states", "tracked", "category", "owner",
        "source_confidence", "origin_provenance", "sensitivity", "recommended_disposition", "port_approved",
        "submodule", "head_mode", "index_mode", "worktree_mode", "head_oid", "index_oid", "path_disclosure",
        "content", "record_fingerprint",
    }
    record = require_object(value, common, {"score"})
    kind = record["record_type"]
    require_shape(is_string(kind) and kind in {"1", "2", "u", "?", "!"})
    require_shape((kind == "2") is ("score" in record) and ("score" not in record or is_string(record["score"])))
    string_fields = {
        "path", "index_status", "worktree_status", "category", "owner", "source_confidence",
        "origin_provenance", "sensitivity", "recommended_disposition", "path_disclosure", "record_fingerprint",
    }
    require_shape(all(is_string(record[field]) for field in string_fields))
    require_shape(is_string_list(record["states"]) and is_boolean(record["tracked"])
                  and is_boolean(record["cross_owner"]) and is_boolean(record["port_approved"]))
    previous = ("previous_path", "previous_category", "previous_owner", "previous_sensitivity")
    require_shape(all(is_string(record[field]) for field in previous) if kind == "2"
                  else all(record[field] is None for field in previous))
    metadata = ("submodule", "head_mode", "index_mode", "worktree_mode", "head_oid", "index_oid")
    require_shape(all(record[field] is None for field in metadata) if kind in {"?", "!"}
                  else all(is_string(record[field]) for field in metadata))
    validate_content_shape(record["content"])


def validate_snapshot_shape(value: Any, allow_missing_fingerprint: bool = False) -> None:
    top = {
        "schema_version", "kind", "label", "captured_at", "read_only", "tool", "repository_id", "head",
        "branch", "status_shape_hash_algorithm", "status_shape_sha256", "content_manifest_hash_algorithm",
        "content_manifest_sha256", "coverage", "sampling", "counting_semantics", "summary", "records",
        "worktree_list_sha256", "snapshot_fingerprint",
    }
    snapshot_value = require_object(value, top - ({"snapshot_fingerprint"} if allow_missing_fingerprint else set()),
                                    {"snapshot_fingerprint"} if allow_missing_fingerprint else set())
    require_shape(is_integer(snapshot_value["schema_version"]) and is_boolean(snapshot_value["read_only"]))
    strings = {"kind", "label", "captured_at", "repository_id", "head", "status_shape_hash_algorithm",
               "status_shape_sha256", "content_manifest_hash_algorithm", "content_manifest_sha256",
               "worktree_list_sha256"}
    require_shape(all(is_string(snapshot_value[field]) for field in strings))
    require_shape(snapshot_value["branch"] is None or is_string(snapshot_value["branch"]))
    if "snapshot_fingerprint" in snapshot_value:
        require_shape(is_string(snapshot_value["snapshot_fingerprint"]))
    tool = require_object(snapshot_value["tool"], {"name", "sha256", "classification_version", "git_version"})
    require_shape(is_string(tool["name"]) and is_string(tool["sha256"])
                  and is_integer(tool["classification_version"]) and is_string(tool["git_version"]))
    sampling = require_object(snapshot_value["sampling"],
                              {"stable", "passes", "head_and_status_checked_before_between_and_after"})
    require_shape(is_boolean(sampling["stable"]) and is_integer(sampling["passes"])
                  and is_boolean(sampling["head_and_status_checked_before_between_and_after"]))
    coverage = require_object(snapshot_value["coverage"],
                              {"complete", "record_count", "captured", "withheld", "not_covered",
                               "full_repository_unchanged_claim_allowed"})
    require_shape(all(is_boolean(coverage[field]) for field in
                      ("complete", "full_repository_unchanged_claim_allowed")))
    require_shape(all(is_integer(coverage[field]) for field in ("record_count", "captured", "withheld", "not_covered")))
    counting = require_object(snapshot_value["counting_semantics"],
                              {"status_records", "state_counts", "unique_current_paths"})
    require_shape(all(is_string(value) for value in counting.values()))
    summary = require_object(snapshot_value["summary"],
                             {"status_records", "unique_current_paths", "states", "categories", "owners",
                              "sensitivity", "port_approved"})
    require_shape(all(is_integer(summary[field]) for field in
                      ("status_records", "unique_current_paths", "port_approved")))
    require_shape(all(is_count_map(summary[field]) for field in ("states", "categories", "owners", "sensitivity")))
    records = snapshot_value["records"]
    require_shape(type(records) is list)
    for record in records:
        validate_record_shape(record)


def snapshot_fingerprint(value: dict[str, Any]) -> str:
    validate_snapshot_shape(value, allow_missing_fingerprint=True)
    tool, coverage = value["tool"], value["coverage"]
    authority = {
        "schema_version": value["schema_version"], "repository_id": value["repository_id"], "head": value["head"],
        "status_shape_sha256": value["status_shape_sha256"], "content_manifest_sha256": value["content_manifest_sha256"],
        "tool_sha256": tool["sha256"], "classification_version": tool["classification_version"], "coverage": coverage,
    }
    return sha256_bytes(canonical_json(authority))


def snapshot(root_arg: str, label: str) -> dict[str, Any]:
    root = Path(root_arg).resolve()
    if not root.is_dir():
        raise AuditError(f"repository path does not exist: {root}")
    top = Path(run_git(root, "rev-parse", "--show-toplevel").decode().strip()).resolve()
    if top != root:
        raise AuditError(f"expected repository root {root}, Git resolved {top}")
    head_before = run_git(root, "rev-parse", "HEAD").decode().strip()
    raw_status_before = run_git(root, "status", "--porcelain=v2", "-z", "--untracked-files=all")
    records = parse_status(raw_status_before)
    manifest_one, coverage_one, content_hash_one = content_manifest(root, records)
    raw_status_middle = run_git(root, "status", "--porcelain=v2", "-z", "--untracked-files=all")
    head_middle = run_git(root, "rev-parse", "HEAD").decode().strip()
    if head_before != head_middle or raw_status_before != raw_status_middle:
        raise AuditError("repository HEAD or status shape drifted during content sampling")
    manifest_two, coverage_two, content_hash_two = content_manifest(root, records)
    raw_status_after = run_git(root, "status", "--porcelain=v2", "-z", "--untracked-files=all")
    head_after = run_git(root, "rev-parse", "HEAD").decode().strip()
    if head_before != head_after or raw_status_before != raw_status_after:
        raise AuditError("repository HEAD or status shape drifted during repeated content sampling")
    if content_hash_one != content_hash_two or coverage_one != coverage_two or manifest_one != manifest_two:
        raise AuditError("repository content manifest drifted during repeated sampling")
    worktrees = run_git(root, "worktree", "list", "--porcelain").decode("utf-8", "replace")
    branch = run_git(root, "branch", "--show-current").decode().strip() or None
    status_counts = status_counter(records)
    tool_path = Path(__file__).resolve()
    result = {
        "schema_version": SCHEMA_VERSION,
        "kind": "shared-main-mutation-sentinel",
        "label": label,
        "captured_at": datetime.now(timezone.utc).isoformat(),
        "read_only": True,
        "tool": {
            "name": "scripts/quality/repository_state.py",
            "sha256": hashlib.sha256(tool_path.read_bytes()).hexdigest(),
            "classification_version": CLASSIFICATION_VERSION,
            "git_version": run_git(root, "--version").decode().strip(),
        },
        "repository_id": repository_id(root),
        "head": head_before,
        "branch": branch,
        "status_shape_hash_algorithm": "sha256",
        "status_shape_sha256": hashlib.sha256(raw_status_before).hexdigest(),
        "content_manifest_hash_algorithm": "sha256",
        "content_manifest_sha256": content_hash_one,
        "coverage": coverage_one,
        "sampling": {"stable": True, "passes": 2, "head_and_status_checked_before_between_and_after": True},
        "counting_semantics": {
            "status_records": "one porcelain-v2 record per current path; a rename original path is not counted twice",
            "state_counts": "a record may contribute to more than one state when index and worktree columns differ",
            "unique_current_paths": "deduplicated current paths from parsed records",
        },
        "summary": {
            "status_records": len(records),
            "unique_current_paths": len({record["path"] for record in records}),
            "states": status_counts,
            "categories": counter(records, "category"),
            "owners": counter(records, "owner"),
            "sensitivity": counter(records, "sensitivity"),
            "port_approved": sum(1 for record in records if record["port_approved"]),
        },
        "records": manifest_one,
        "worktree_list_sha256": hashlib.sha256(worktrees.encode()).hexdigest(),
    }
    result["snapshot_fingerprint"] = snapshot_fingerprint(result)
    return result


def identity(record: dict[str, Any]) -> tuple[str, str, str | None]:
    return record["path"], record["record_type"], record.get("previous_path")


def compare(before: dict[str, Any], after: dict[str, Any]) -> dict[str, Any]:
    for label, value in (("before", before), ("after", after)):
        reasons = validate_snapshot(value)
        if reasons:
            raise AuditError(f"invalid {label} snapshot: {','.join(reasons[:8])}")
    before_records = {identity(item): item for item in before["records"]}
    after_records = {identity(item): item for item in after["records"]}
    added = sorted(set(after_records) - set(before_records))
    removed = sorted(set(before_records) - set(after_records))
    changed: list[dict[str, Any]] = []
    for key in sorted(set(before_records).intersection(after_records)):
        left = before_records[key]
        right = after_records[key]
        fields = ["index_status", "worktree_status", "category", "owner", "sensitivity"]
        delta = {field: {"before": left[field], "after": right[field]} for field in fields if left[field] != right[field]}
        if delta:
            changed.append({"path": key[0], "changes": delta})
    binding_fields = {
        "schema_version": before.get("schema_version") == after.get("schema_version"), "repository_id": before.get("repository_id") == after.get("repository_id"),
        "head": before.get("head") == after.get("head"),
        "status_shape": before.get("status_shape_sha256") == after.get("status_shape_sha256"),
        "content_manifest": before.get("content_manifest_sha256") == after.get("content_manifest_sha256"),
        "tool": before.get("tool", {}).get("sha256") == after.get("tool", {}).get("sha256"),
        "classification_version": before.get("tool", {}).get("classification_version") == after.get("tool", {}).get("classification_version"),
    }
    evidence_equivalent = all(binding_fields.values())
    coverage_complete = bool(before.get("coverage", {}).get("complete") and after.get("coverage", {}).get("complete"))
    return {
        "schema_version": SCHEMA_VERSION,
        "kind": "shared-main-mutation-delta",
        "compared_at": datetime.now(timezone.utc).isoformat(),
        "before_snapshot_fingerprint": before.get("snapshot_fingerprint"),
        "after_snapshot_fingerprint": after.get("snapshot_fingerprint"),
        "binding_checks": binding_fields,
        "coverage_complete": coverage_complete,
        "evidence_equivalent": evidence_equivalent,
        "unchanged": evidence_equivalent and coverage_complete,
        "indeterminate": evidence_equivalent and not coverage_complete,
        "added": [{"path": key[0], "record_type": key[1], "previous_path": key[2]} for key in added],
        "removed": [{"path": key[0], "record_type": key[1], "previous_path": key[2]} for key in removed],
        "changed": changed,
    }


def load_json(path: str) -> dict[str, Any]:
    try:
        with Path(path).open("r", encoding="utf-8") as handle:
            value = json.load(handle)
    except (UnicodeDecodeError, json.JSONDecodeError):
        raise AuditError("input is not valid UTF-8 JSON") from None
    except OSError:
        raise AuditError("input JSON could not be read") from None
    if not isinstance(value, dict):
        raise AuditError("input JSON document must be an object")
    return value


def write_json(value: dict[str, Any], output: str | None, overwrite: bool) -> None:
    rendered = json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n"
    if output is None:
        sys.stdout.write(rendered)
        return
    destination = Path(output)
    if destination.exists() and not overwrite:
        raise AuditError(f"output exists; pass --overwrite to replace it: {destination}")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=destination.parent, delete=False) as handle:
        handle.write(rendered)
        temporary = Path(handle.name)
    os.replace(temporary, destination)


def validate_snapshot(snapshot_value: dict[str, Any]) -> list[str]:
    validate_snapshot_shape(snapshot_value)
    failures: list[str] = []
    tool = snapshot_value.get("tool")
    sampling = snapshot_value.get("sampling")
    if not isinstance(tool, dict):
        tool = {}
        failures.append("snapshot-tool-invalid")
    if not isinstance(sampling, dict):
        sampling = {}
        failures.append("snapshot-sampling-invalid")
    if not isinstance(snapshot_value.get("coverage"), dict):
        failures.append("snapshot-coverage-invalid")
    if not isinstance(snapshot_value.get("summary"), dict):
        failures.append("snapshot-summary-invalid")
    if snapshot_value.get("schema_version") != SCHEMA_VERSION:
        failures.append("snapshot-schema-version-mismatch")
    if snapshot_value.get("kind") != "shared-main-mutation-sentinel" or snapshot_value.get("read_only") is not True:
        failures.append("snapshot-kind-or-read-only-mismatch")
    if tool.get("name") != "scripts/quality/repository_state.py":
        failures.append("snapshot-tool-name-mismatch")
    if tool.get("classification_version") != CLASSIFICATION_VERSION:
        failures.append("snapshot-classification-version-mismatch")
    if (snapshot_value.get("status_shape_hash_algorithm") != "sha256"
            or snapshot_value.get("content_manifest_hash_algorithm") != "sha256"):
        failures.append("snapshot-hash-algorithm-mismatch")
    for field in ("repository_id", "status_shape_sha256", "content_manifest_sha256", "snapshot_fingerprint"):
        if not valid_sha256(snapshot_value.get(field)):
            failures.append(f"snapshot-{field}-invalid")
    if not valid_sha256(tool.get("sha256")):
        failures.append("snapshot-tool-sha256-invalid")
    if not valid_git_oid(snapshot_value.get("head")):
        failures.append("snapshot-head-invalid")
    if sampling != {"stable": True, "passes": 2, "head_and_status_checked_before_between_and_after": True}:
        failures.append("snapshot-sampling-contract-mismatch")
    expected_counting = {
        "status_records": "one porcelain-v2 record per current path; a rename original path is not counted twice",
        "state_counts": "a record may contribute to more than one state when index and worktree columns differ",
        "unique_current_paths": "deduplicated current paths from parsed records",
    }
    if snapshot_value.get("counting_semantics") != expected_counting:
        failures.append("snapshot-counting-semantics-mismatch")
    if not valid_sha256(snapshot_value.get("worktree_list_sha256")):
        failures.append("snapshot-worktree-list-sha256-invalid")
    if "worktree_count" in snapshot_value:
        failures.append("snapshot-unverifiable-worktree-count-present")
    records = snapshot_value.get("records")
    if not isinstance(records, list):
        return failures + ["snapshot-records-invalid"]
    fingerprints: list[str] = []
    content_states: Counter[str] = Counter()
    seen: set[tuple[Any, Any, Any]] = set()
    for record in records:
        if not isinstance(record, dict):
            failures.append("snapshot-record-invalid")
            continue
        try:
            identity_value = identity(record)
            if identity_value in seen:
                failures.append("snapshot-record-duplicate")
            seen.add(identity_value)
        except (KeyError, TypeError):
            failures.append("snapshot-record-identity-invalid")
            continue
        claimed = record.get("record_fingerprint")
        unsigned = dict(record)
        unsigned.pop("record_fingerprint", None)
        try:
            expected = sha256_bytes(canonical_json(unsigned))
        except (TypeError, ValueError):
            failures.append("snapshot-record-unserializable")
            continue
        if claimed != expected:
            failures.append("snapshot-record-fingerprint-mismatch")
        fingerprints.append(str(claimed))
        kind = record.get("record_type")
        states = record.get("states")
        index_status = record.get("index_status")
        worktree_status = record.get("worktree_status")
        if (not isinstance(kind, str) or kind not in {"1", "2", "u", "?", "!"} or not isinstance(states, list) or not all(
                isinstance(state, str) for state in states) or not isinstance(index_status, str) or len(index_status) != 1
                or not isinstance(worktree_status, str) or len(worktree_status) != 1):
            failures.append("snapshot-record-shape-invalid")
            continue
        if states != status_states(kind, index_status + worktree_status) or record.get("tracked") is not (kind not in {"?", "!"}):
            failures.append("snapshot-record-status-mismatch")
        disclosed = record.get("path_disclosure") == "external-evidence-allowed"
        redacted = record.get("path_disclosure") == "redacted-local-only"
        if not ((disclosed and canonical_path(record.get("path"))) or (redacted and valid_redacted_path(record.get("path")))):
            failures.append("snapshot-current-path-invalid")
        expected_origin = (
            "git-reported-previous-path" if kind == "2" else
            "unknown" if kind == "?" or (kind == "1" and "added" in states) else "same-path"
        )
        if record.get("origin_provenance") != expected_origin:
            failures.append("snapshot-origin-provenance-mismatch")
        category = record.get("category")
        owner = record.get("owner")
        if (not isinstance(category, str) or category not in ALL_CATEGORIES or not isinstance(owner, str)
                or owner not in ASSIGNED_OWNERS | {"unassigned"}
                or record.get("sensitivity") != category_sensitivity(category)
                or not isinstance(record.get("port_approved"), bool)):
            failures.append("snapshot-record-classification-invalid")
        if record.get("source_confidence") != "unattributed":
            failures.append("snapshot-source-confidence-invalid")
        if expected_origin == "unknown" and record.get("recommended_disposition") != "quarantine-origin-review":
            failures.append("snapshot-unknown-origin-disposition-invalid")
        previous_fields = (record.get("previous_path"), record.get("previous_category"), record.get("previous_owner"),
                           record.get("previous_sensitivity"))
        if kind != "2" and (any(value is not None for value in previous_fields) or record.get("cross_owner") is not False):
            failures.append("snapshot-non-type2-previous-fields-invalid")
        if kind == "2":
            previous = record.get("previous_path")
            if not ((disclosed and canonical_path(previous)) or (redacted and valid_redacted_path(previous))):
                failures.append("snapshot-type2-previous-path-invalid")
            previous_category = record.get("previous_category")
            previous_owner = record.get("previous_owner")
            if not isinstance(previous_category, str) or previous_category not in ALL_CATEGORIES:
                failures.append("snapshot-type2-previous-category-invalid")
            if not isinstance(previous_owner, str) or previous_owner not in ASSIGNED_OWNERS | {"unassigned"}:
                failures.append("snapshot-type2-previous-owner-invalid")
            if (isinstance(previous_category, str) and previous_category in ALL_CATEGORIES
                    and record.get("previous_sensitivity") != category_sensitivity(previous_category)):
                failures.append("snapshot-type2-previous-sensitivity-invalid")
            if isinstance(record.get("previous_owner"), str) and isinstance(record.get("owner"), str):
                if record.get("cross_owner") is not (record["previous_owner"] != record["owner"]):
                    failures.append("snapshot-type2-cross-owner-invalid")
        content = record.get("content")
        state = content.get("state") if isinstance(content, dict) else None
        if state not in {"captured", "withheld", "not-covered"}:
            failures.append("snapshot-content-state-invalid")
            continue
        content_states[state] += 1
        if expected_origin == "unknown" and (state != "not-covered" or content.get("reason") != "origin-provenance-unknown"):
            failures.append("snapshot-unknown-origin-content-invalid")
        if state == "captured":
            if (not isinstance(category, str) or category not in PORTABLE_CATEGORIES or record.get("sensitivity") != "normal"
                    or not isinstance(owner, str) or owner not in ASSIGNED_OWNERS or not valid_sha256(content.get("sha256"))):
                failures.append("snapshot-captured-record-invalid")
            if not disclosed or not canonical_path(record.get("path")):
                failures.append("snapshot-captured-path-invalid")
        if disclosed and canonical_path(record.get("path")):
            if path_category(record["path"]) != record.get("category") or path_owner(record["path"]) != record.get("owner"):
                failures.append("snapshot-current-classification-mismatch")
            previous = record.get("previous_path")
            if previous and (not canonical_path(previous) or path_category(previous) != record.get("previous_category")
                             or path_owner(previous) != record.get("previous_owner")):
                failures.append("snapshot-previous-classification-mismatch")
            if bool(previous and record.get("previous_owner") != record.get("owner")) != bool(record.get("cross_owner")):
                failures.append("snapshot-cross-owner-mismatch")
        if state == "withheld" and (not redacted or not valid_redacted_path(record.get("path"))
                                    or (record.get("previous_path") and not valid_redacted_path(record["previous_path"]))):
            failures.append("snapshot-withheld-path-invalid")
    expected_coverage = {
        "complete": bool(not records or content_states["captured"] == len(records)), "record_count": len(records),
        "captured": content_states["captured"], "withheld": content_states["withheld"],
        "not_covered": content_states["not-covered"],
        "full_repository_unchanged_claim_allowed": bool(not records or content_states["captured"] == len(records)),
    }
    if snapshot_value.get("coverage") != expected_coverage:
        failures.append("snapshot-coverage-mismatch")
    try:
        expected_summary = {
            "status_records": len(records),
            "unique_current_paths": len({record["path"] for record in records}),
            "states": status_counter(records),
            "categories": counter(records, "category"),
            "owners": counter(records, "owner"),
            "sensitivity": counter(records, "sensitivity"),
            "port_approved": sum(1 for record in records if record["port_approved"]),
        }
        if snapshot_value.get("summary") != expected_summary:
            failures.append("snapshot-summary-mismatch")
    except (KeyError, TypeError):
        failures.append("snapshot-summary-unverifiable")
    if snapshot_value.get("content_manifest_sha256") != sha256_bytes(canonical_json(fingerprints)):
        failures.append("snapshot-content-manifest-mismatch")
    try:
        if snapshot_value.get("snapshot_fingerprint") != snapshot_fingerprint(snapshot_value):
            failures.append("snapshot-fingerprint-mismatch")
    except (AuditError, KeyError, TypeError, ValueError):
        failures.append("snapshot-authority-fields-missing")
    return sorted(set(failures))


def validate_ledger_shape(value: Any) -> None:
    top_strings = {"repository_id", "head", "status_shape_sha256", "content_manifest_sha256", "tool_sha256",
                   "snapshot_fingerprint"}
    ledger = require_object(value, top_strings | {"classification_version", "entries"})
    require_shape(all(is_string(ledger[field]) for field in top_strings)
                  and is_integer(ledger["classification_version"]) and type(ledger["entries"]) is list)
    required = {
        "path", "owner", "category", "source_confidence", "port_approved", "approved_by",
        "source_repository_id", "source_head", "source_status_shape_sha256", "source_content_manifest_sha256",
        "source_tool_sha256", "source_classification_version", "source_snapshot_fingerprint",
        "source_record_fingerprint", "source_content_sha256",
    }
    optional = {"previous_owner", "approved_owners", "cross_owner_approved"}
    string_fields = required - {"port_approved", "source_classification_version"}
    for entry_value in ledger["entries"]:
        entry = require_object(entry_value, required, optional)
        require_shape(all(is_string(entry[field]) for field in string_fields)
                      and is_boolean(entry["port_approved"])
                      and is_integer(entry["source_classification_version"]))
        if "previous_owner" in entry:
            require_shape(is_string(entry["previous_owner"]))
        if "approved_owners" in entry:
            require_shape(is_string_list(entry["approved_owners"]))
        if "cross_owner_approved" in entry:
            require_shape(is_boolean(entry["cross_owner_approved"]))


def validate_request_shape(value: Any) -> None:
    require_shape(type(value) is list and all(is_string(path) for path in value))


def authority_bindings(snapshot_value: dict[str, Any]) -> dict[str, Any]:
    validate_snapshot_shape(snapshot_value)
    return {
        "repository_id": snapshot_value.get("repository_id"), "head": snapshot_value.get("head"),
        "status_shape_sha256": snapshot_value.get("status_shape_sha256"), "content_manifest_sha256": snapshot_value.get("content_manifest_sha256"),
        "tool_sha256": snapshot_value.get("tool", {}).get("sha256"), "classification_version": snapshot_value.get("tool", {}).get("classification_version"),
        "snapshot_fingerprint": snapshot_value.get("snapshot_fingerprint"),
    }


def validate_port(ledger: dict[str, Any], snapshot_value: dict[str, Any], requested_paths: list[str]) -> dict[str, Any]:
    validate_snapshot_shape(snapshot_value)
    validate_ledger_shape(ledger)
    validate_request_shape(requested_paths)
    failures: list[dict[str, str]] = []
    snapshot_failures = validate_snapshot(snapshot_value)
    for reason in snapshot_failures:
        failures.append({"path": "<snapshot>", "reason": reason})
    if snapshot_failures:
        return {"valid": False, "approval_semantics": "controller-recorded; no cryptographic signature is claimed",
                "requested": [disclosed_path(path) for path in requested_paths],
                "snapshot_fingerprint": snapshot_value.get("snapshot_fingerprint") if isinstance(snapshot_value, dict) else None,
                "failures": failures}
    bindings = authority_bindings(snapshot_value)
    for field, expected in bindings.items():
        if ledger.get(field) != expected:
            failures.append({"path": "<ledger>", "reason": f"authority-{field}-mismatch"})
    raw_entries = ledger.get("entries", [])
    if not isinstance(raw_entries, list):
        raw_entries = []
        failures.append({"path": "<ledger>", "reason": "entries-invalid"})
    entries: dict[str, dict[str, Any]] = {}
    duplicate_entries: set[str] = set()
    for raw_entry in raw_entries:
        if not isinstance(raw_entry, dict):
            failures.append({"path": "<ledger>", "reason": "entry-invalid"})
            continue
        path = raw_entry.get("path")
        if not canonical_path(path):
            failures.append({"path": str(path), "reason": "entry-path-non-canonical"})
            continue
        if path in entries:
            duplicate_entries.add(path)
        else:
            entries[path] = raw_entry
    for path in sorted(duplicate_entries):
        failures.append({"path": path, "reason": "duplicate-ledger-entry"})
    snapshot_by_path: dict[str, dict[str, Any]] = {}
    ambiguous_snapshot_paths: set[str] = set()
    for record in snapshot_value.get("records", []):
        path = record.get("path") if isinstance(record, dict) else None
        if not canonical_path(path):
            continue
        if path in snapshot_by_path:
            ambiguous_snapshot_paths.add(path)
        else:
            snapshot_by_path[path] = record
    seen_requests: set[str] = set()
    for path in requested_paths:
        if not canonical_path(path):
            failures.append({"path": str(path), "reason": "requested-path-non-canonical"})
            continue
        if path in seen_requests:
            failures.append({"path": path, "reason": "duplicate-requested-path"})
            continue
        seen_requests.add(path)
        entry = entries.get(path)
        if entry is None:
            failures.append({"path": path, "reason": "missing-ledger-entry"})
            continue
        if path in duplicate_entries or path in ambiguous_snapshot_paths:
            continue
        record = snapshot_by_path.get(path)
        if record is None:
            failures.append({"path": path, "reason": "missing-authoritative-snapshot-record"})
            continue
        if record.get("content", {}).get("state") != "captured":
            failures.append({"path": path, "reason": "snapshot-content-not-captured"})
        if record.get("origin_provenance") == "unknown":
            failures.append({"path": path, "reason": "snapshot-origin-provenance-unknown"})
        if record.get("category") not in PORTABLE_CATEGORIES or record.get("sensitivity") != "normal":
            failures.append({"path": path, "reason": "snapshot-record-not-portable"})
        if record.get("owner") not in ASSIGNED_OWNERS:
            failures.append({"path": path, "reason": "snapshot-owner-unassigned"})
        if path_category(path) != record.get("category") or path_owner(path) != record.get("owner"):
            failures.append({"path": path, "reason": "snapshot-live-classification-mismatch"})
        if "conflicted" in record.get("states", []):
            failures.append({"path": path, "reason": "snapshot-record-conflicted"})
        if entry.get("owner") != record.get("owner"):
            failures.append({"path": path, "reason": "owner-snapshot-mismatch"})
        if entry.get("category") != record.get("category"):
            failures.append({"path": path, "reason": "category-snapshot-mismatch"})
        if record.get("cross_owner"):
            owners = entry.get("approved_owners")
            expected_owners = sorted({record.get("owner"), record.get("previous_owner")})
            if (record.get("previous_owner") not in ASSIGNED_OWNERS or entry.get("cross_owner_approved") is not True
                    or entry.get("previous_owner") != record.get("previous_owner")
                    or not isinstance(owners, list) or not all(isinstance(owner, str) for owner in owners)
                    or sorted(set(owners)) != expected_owners or len(owners) != 2):
                failures.append({"path": path, "reason": "cross-owner-approval-incomplete"})
        elif any(field in entry for field in ("previous_owner", "approved_owners", "cross_owner_approved")):
            failures.append({"path": path, "reason": "unexpected-cross-owner-approval-fields"})
        if entry.get("source_confidence") not in {"high", "verified"}:
            failures.append({"path": path, "reason": "source-confidence-insufficient"})
        if entry.get("port_approved") is not True:
            failures.append({"path": path, "reason": "port-not-approved"})
        if entry.get("approved_by") != "controller":
            failures.append({"path": path, "reason": "approval-authority-not-controller"})
        entry_bindings = {
            "source_repository_id": bindings["repository_id"],
            "source_head": bindings["head"],
            "source_status_shape_sha256": bindings["status_shape_sha256"],
            "source_content_manifest_sha256": bindings["content_manifest_sha256"],
            "source_tool_sha256": bindings["tool_sha256"],
            "source_classification_version": bindings["classification_version"],
            "source_snapshot_fingerprint": bindings["snapshot_fingerprint"],
            "source_record_fingerprint": record.get("record_fingerprint"),
            "source_content_sha256": record.get("content", {}).get("sha256"),
        }
        for field, expected in entry_bindings.items():
            actual = entry.get(field)
            if actual != expected:
                failures.append({"path": path, "reason": f"{field}-mismatch"})
            if field.endswith("sha256") or field.endswith("fingerprint") or field == "source_repository_id":
                if not valid_sha256(actual):
                    failures.append({"path": path, "reason": f"{field}-invalid"})
    safe_failures = [{**failure, "path": disclosed_path(failure["path"])} for failure in failures]
    return {"valid": not failures, "approval_semantics": "controller-recorded; no cryptographic signature is claimed",
            "requested": [disclosed_path(path) for path in requested_paths],
            "snapshot_fingerprint": snapshot_value.get("snapshot_fingerprint"), "failures": safe_failures}


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    snapshot_parser = subparsers.add_parser("snapshot", help="capture a read-only repository sentinel")
    snapshot_parser.add_argument("--repo", required=True)
    snapshot_parser.add_argument("--label", required=True)
    snapshot_parser.add_argument("--output")
    snapshot_parser.add_argument("--overwrite", action="store_true")

    compare_parser = subparsers.add_parser("compare", help="compare two sentinel JSON documents")
    compare_parser.add_argument("--before", required=True)
    compare_parser.add_argument("--after", required=True)
    compare_parser.add_argument("--output")
    compare_parser.add_argument("--overwrite", action="store_true")

    report_parser = subparsers.add_parser("hygiene-report", help="summarize cleanup and ownership cohorts")
    report_parser.add_argument("--snapshot", required=True)
    report_parser.add_argument("--output")
    report_parser.add_argument("--overwrite", action="store_true")

    validate_parser = subparsers.add_parser("validate-port", help="fail closed unless ledger approvals are complete")
    validate_parser.add_argument("--ledger", required=True)
    validate_parser.add_argument("--snapshot", required=True)
    validate_parser.add_argument("--path", action="append", required=True, dest="paths")
    return parser


def main() -> int:
    args = build_parser().parse_args()
    try:
        if args.command == "snapshot":
            write_json(snapshot(args.repo, args.label), args.output, args.overwrite)
            return 0
        if args.command == "compare":
            write_json(compare(load_json(args.before), load_json(args.after)), args.output, args.overwrite)
            return 0
        if args.command == "hygiene-report":
            write_json(hygiene_report(load_json(args.snapshot)), args.output, args.overwrite)
            return 0
        if args.command == "validate-port":
            ledger = load_json(args.ledger)
            snapshot_value = load_json(args.snapshot)
            if validate_snapshot(snapshot_value):
                raise AuditError("snapshot document is invalid")
            validate_ledger_shape(ledger)
            validate_request_shape(args.paths)
            result = validate_port(ledger, snapshot_value, args.paths)
            write_json(result, None, False)
            return 0 if result["valid"] else 3
    except (AuditError, OSError, subprocess.SubprocessError, json.JSONDecodeError) as exc:
        print(f"repository-state audit failed: {exc}", file=sys.stderr)
        return 2
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
