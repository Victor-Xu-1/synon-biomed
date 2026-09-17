#!/usr/bin/env python3
"""Create or verify the build-once manifest for exact release artifacts."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
import stat
import sys
from typing import Any

sys.dont_write_bytecode = True

if __package__:
    from . import product_identity_gate
    from .release_contract_io import (
        CandidateError,
        atomic_new_json,
        digest,
        read_json,
        read_regular,
        run,
        same_tree_json,
        validate_source,
    )
else:
    import product_identity_gate
    from release_contract_io import (
        CandidateError,
        atomic_new_json,
        digest,
        read_json,
        read_regular,
        run,
        same_tree_json,
        validate_source,
    )


SCHEMA = "synon.release-candidate.v1"
MANIFEST_NAME = "RELEASE_CANDIDATE.json"
SHA = re.compile(r"^[0-9a-f]{40,64}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
SEMVER = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
SLUG = re.compile(r"^[a-z0-9]+(?:-[a-z0-9]+)*$")




def load_controls(repo: Path) -> tuple[dict[str, Any], dict[str, Any]]:
    identity = same_tree_json(repo, "product-identity.json", "candidate_identity_invalid")
    policy = same_tree_json(repo, "docs/governance/release-policy.json", "candidate_policy_invalid")
    try:
        product_identity_gate.validate_authority(identity)
        product_identity_gate.validate_release_policy(policy)
    except product_identity_gate.IdentityError as error:
        raise CandidateError(str(error)) from error
    return identity, policy


def expected_artifacts(identity: dict[str, Any], policy: dict[str, Any]) -> list[tuple[str, str, str]]:
    slug = identity["machine_slug"]
    version = identity["version"]
    if not SLUG.fullmatch(slug) or not SEMVER.fullmatch(version):
        raise CandidateError("candidate_identity_invalid")
    result: list[tuple[str, str, str]] = []
    for platform in policy["candidate_manifest"]["required_platforms"]:
        archive = f"{slug}-v{version}-{platform}.tar.gz"
        result.append((archive, platform, "archive"))
        result.append((f"{archive}.sha256", platform, "checksum"))
    return result


def validate_artifact_directory(
    artifact_dir: Path,
    identity: dict[str, Any],
    policy: dict[str, Any],
    *,
    manifest_present: bool,
) -> list[dict[str, Any]]:
    artifact_dir = artifact_dir.resolve()
    if not artifact_dir.is_dir() or artifact_dir.is_symlink():
        raise CandidateError("candidate_artifact_root_invalid")
    expected = expected_artifacts(identity, policy)
    expected_names = {name for name, _, _ in expected}
    actual: set[str] = set()
    for path in artifact_dir.iterdir():
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or stat.S_ISLNK(info.st_mode) or info.st_nlink != 1:
            raise CandidateError("candidate_artifact_entry_invalid")
        actual.add(path.name)
    if manifest_present:
        expected_names.add(MANIFEST_NAME)
    if actual != expected_names:
        raise CandidateError("candidate_artifact_set_invalid")

    entries: list[dict[str, Any]] = []
    archives: dict[str, dict[str, Any]] = {}
    for name, platform, kind in expected:
        path = artifact_dir / name
        info = path.lstat()
        entry = {
            "name": name,
            "platform": platform,
            "kind": kind,
            "bytes": info.st_size,
            "sha256": digest(path),
        }
        entries.append(entry)
        if kind == "archive":
            if info.st_size <= 0:
                raise CandidateError("candidate_artifact_empty")
            archives[name] = entry
    for name, _, kind in expected:
        if kind != "checksum":
            continue
        archive_name = name.removesuffix(".sha256")
        checksum = read_regular(artifact_dir / name, "candidate_checksum_invalid", max_bytes=256)
        expected_text = f"{archives[archive_name]['sha256']}  {archive_name}\n".encode("ascii")
        if checksum != expected_text:
            raise CandidateError("candidate_checksum_invalid")
    return sorted(entries, key=lambda item: item["name"])


def artifact_set_digest(entries: list[dict[str, Any]]) -> str:
    encoded = json.dumps(entries, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def source_tree_digest(repo: Path) -> str:
    value = run(
        repo,
        "bash",
        "scripts/source-tree-digest.sh",
        ".",
        code="candidate_source_digest_failed",
    ).decode("ascii", errors="strict").strip()
    if not SHA256.fullmatch(value):
        raise CandidateError("candidate_source_digest_invalid")
    return value


def candidate_document(
    repo: Path,
    artifact_dir: Path,
    run_id: int,
    run_attempt: int,
) -> dict[str, Any]:
    repo = repo.resolve()
    artifact_dir = artifact_dir.resolve()
    if artifact_dir == repo or repo in artifact_dir.parents:
        raise CandidateError("candidate_artifacts_inside_source")
    if type(run_id) is not int or run_id <= 0 or type(run_attempt) is not int or run_attempt <= 0:
        raise CandidateError("candidate_workflow_identity_invalid")
    commit, tree, committed_at = validate_source(repo)
    identity, policy = load_controls(repo)
    artifacts = validate_artifact_directory(
        artifact_dir,
        identity,
        policy,
        manifest_present=False,
    )
    return {
        "schema": SCHEMA,
        "repository": policy["repository"],
        "workflow": policy["candidate_manifest"]["workflow"],
        "candidate_run_id": run_id,
        "candidate_run_attempt": run_attempt,
        "source_commit": commit,
        "source_tree": tree,
        "source_tree_sha256": source_tree_digest(repo),
        "source_committed_at": committed_at,
        "product_identity": identity,
        "artifacts": artifacts,
        "artifact_set_sha256": artifact_set_digest(artifacts),
        "status": "candidate-not-release-authorization",
    }


def validate_candidate_shape(value: dict[str, Any]) -> None:
    if set(value) != {
        "schema",
        "repository",
        "workflow",
        "candidate_run_id",
        "candidate_run_attempt",
        "source_commit",
        "source_tree",
        "source_tree_sha256",
        "source_committed_at",
        "product_identity",
        "artifacts",
        "artifact_set_sha256",
        "status",
    }:
        raise CandidateError("candidate_manifest_shape_invalid")
    if (
        value.get("schema") != SCHEMA
        or value.get("status") != "candidate-not-release-authorization"
        or type(value.get("candidate_run_id")) is not int
        or value["candidate_run_id"] <= 0
        or type(value.get("candidate_run_attempt")) is not int
        or value["candidate_run_attempt"] <= 0
        or not SHA.fullmatch(str(value.get("source_commit", "")))
        or not SHA.fullmatch(str(value.get("source_tree", "")))
        or not SHA256.fullmatch(str(value.get("source_tree_sha256", "")))
        or not SHA256.fullmatch(str(value.get("artifact_set_sha256", "")))
        or not isinstance(value.get("product_identity"), dict)
        or not isinstance(value.get("artifacts"), list)
    ):
        raise CandidateError("candidate_manifest_shape_invalid")


def create(repo: Path, artifact_dir: Path, run_id: int, run_attempt: int) -> dict[str, Any]:
    output = artifact_dir.resolve() / MANIFEST_NAME
    if output.exists():
        raise CandidateError("candidate_manifest_exists")
    value = candidate_document(repo, artifact_dir, run_id, run_attempt)
    atomic_new_json(output, value)
    return verify(repo, artifact_dir, run_id, run_attempt)


def verify(
    repo: Path,
    artifact_dir: Path,
    run_id: int,
    run_attempt: int,
    *,
    expected_manifest_sha256: str | None = None,
    verify_source_tree_sha256: bool = True,
) -> dict[str, Any]:
    repo = repo.resolve()
    artifact_dir = artifact_dir.resolve()
    manifest_path = artifact_dir / MANIFEST_NAME
    if expected_manifest_sha256 is not None:
        if (
            not SHA256.fullmatch(expected_manifest_sha256)
            or digest(manifest_path) != expected_manifest_sha256
        ):
            raise CandidateError("candidate_manifest_digest_mismatch")
    value = read_json(manifest_path, "candidate_manifest_invalid")
    validate_candidate_shape(value)
    commit, tree, committed_at = validate_source(repo)
    identity, policy = load_controls(repo)
    artifacts = validate_artifact_directory(
        artifact_dir,
        identity,
        policy,
        manifest_present=True,
    )
    expected = {
        "schema": SCHEMA,
        "repository": policy["repository"],
        "workflow": policy["candidate_manifest"]["workflow"],
        "candidate_run_id": run_id,
        "candidate_run_attempt": run_attempt,
        "source_commit": commit,
        "source_tree": tree,
        "source_tree_sha256": (
            source_tree_digest(repo)
            if verify_source_tree_sha256
            else value["source_tree_sha256"]
        ),
        "source_committed_at": committed_at,
        "product_identity": identity,
        "artifacts": artifacts,
        "artifact_set_sha256": artifact_set_digest(artifacts),
        "status": "candidate-not-release-authorization",
    }
    if value != expected:
        raise CandidateError("candidate_manifest_mismatch")
    return value


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("create", "verify"))
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    parser.add_argument("--run-id", type=int, required=True)
    parser.add_argument("--run-attempt", type=int, required=True)
    parser.add_argument("--expected-manifest-sha256")
    parser.add_argument(
        "--source-tree-digest-mode",
        choices=("verify", "manifest-only"),
        default="verify",
    )
    arguments = parser.parse_args(argv)
    try:
        if arguments.mode == "create":
            if (
                arguments.expected_manifest_sha256 is not None
                or arguments.source_tree_digest_mode != "verify"
            ):
                raise CandidateError("candidate_arguments_invalid")
            result = create(
                arguments.repo,
                arguments.artifact_dir,
                arguments.run_id,
                arguments.run_attempt,
            )
        else:
            result = verify(
                arguments.repo,
                arguments.artifact_dir,
                arguments.run_id,
                arguments.run_attempt,
                expected_manifest_sha256=arguments.expected_manifest_sha256,
                verify_source_tree_sha256=arguments.source_tree_digest_mode == "verify",
            )
    except (CandidateError, OSError) as error:
        print(json.dumps({"ok": False, "code": str(error)}, sort_keys=True))
        return 2
    print(
        json.dumps(
            {
                "ok": True,
                "source_commit": result["source_commit"],
                "source_tree": result["source_tree"],
                "artifact_set_sha256": result["artifact_set_sha256"],
                "artifact_count": len(result["artifacts"]),
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
