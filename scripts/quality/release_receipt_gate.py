#!/usr/bin/env python3
"""Verify an external signed release authorization against exact candidate bytes."""

from __future__ import annotations

import argparse
from datetime import datetime, timezone
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import stat
import sys
from typing import Any
import uuid

sys.dont_write_bytecode = True

if __package__:
    from . import release_candidate_manifest as candidate
else:
    import release_candidate_manifest as candidate


SCHEMA = "synon.release-authorization-receipt.v1"
SHA256 = re.compile(r"^[0-9a-f]{64}$")
SHA = re.compile(r"^[0-9a-f]{40,64}$")
SEMVER = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")


class ReceiptError(RuntimeError):
    """Stable signed-receipt validation failure."""


def canonical_payload(payload: dict[str, Any]) -> bytes:
    return json.dumps(
        payload,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")


def parse_time(value: Any, code: str) -> datetime:
    if not isinstance(value, str) or not value:
        raise ReceiptError(code)
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise ReceiptError(code) from error
    if parsed.tzinfo is None or parsed.utcoffset() is None:
        raise ReceiptError(code)
    return parsed.astimezone(timezone.utc)


def load_key(path: Path, repo: Path) -> bytes:
    path = path.resolve()
    repo = repo.resolve()
    if path == repo or repo in path.parents:
        raise ReceiptError("receipt_key_inside_source")
    try:
        info = path.lstat()
        if (
            not stat.S_ISREG(info.st_mode)
            or stat.S_ISLNK(info.st_mode)
            or info.st_nlink != 1
            or info.st_size < 32
            or info.st_size > 4096
            or info.st_mode & 0o077
        ):
            raise ReceiptError("receipt_key_invalid")
        value = path.read_bytes()
    except OSError as error:
        raise ReceiptError("receipt_key_invalid") from error
    if value.endswith(b"\n"):
        value = value[:-1]
    if len(value) < 32:
        raise ReceiptError("receipt_key_invalid")
    return value


def load_receipt(path: Path, repo: Path) -> dict[str, Any]:
    path = path.resolve()
    repo = repo.resolve()
    if path == repo or repo in path.parents:
        raise ReceiptError("receipt_inside_source")
    try:
        return candidate.read_json(path, "receipt_document_invalid")
    except candidate.CandidateError as error:
        raise ReceiptError(str(error)) from error


def validate_payload(payload: Any, max_ttl_seconds: int, now: datetime) -> dict[str, Any]:
    required = {
        "receipt_id",
        "decision",
        "authorized_by",
        "authorization_reference",
        "repository",
        "candidate_run_id",
        "candidate_run_attempt",
        "source_commit",
        "source_tree",
        "product_version",
        "tag",
        "candidate_manifest_sha256",
        "artifact_set_sha256",
        "issued_at",
        "expires_at",
    }
    if not isinstance(payload, dict) or set(payload) != required:
        raise ReceiptError("receipt_payload_shape_invalid")
    try:
        receipt_id = uuid.UUID(str(payload.get("receipt_id")))
    except (ValueError, AttributeError) as error:
        raise ReceiptError("receipt_id_invalid") from error
    if receipt_id.version != 4 or str(receipt_id) != payload["receipt_id"]:
        raise ReceiptError("receipt_id_invalid")
    if (
        payload.get("decision") != "authorized"
        or payload.get("authorized_by") != "user"
        or not isinstance(payload.get("authorization_reference"), str)
        or not payload["authorization_reference"].strip()
        or len(payload["authorization_reference"]) > 512
        or not isinstance(payload.get("repository"), str)
        or type(payload.get("candidate_run_id")) is not int
        or payload["candidate_run_id"] <= 0
        or type(payload.get("candidate_run_attempt")) is not int
        or payload["candidate_run_attempt"] <= 0
        or not SHA.fullmatch(str(payload.get("source_commit", "")))
        or not SHA.fullmatch(str(payload.get("source_tree", "")))
        or not SEMVER.fullmatch(str(payload.get("product_version", "")))
        or payload.get("tag") != f"v{payload.get('product_version')}"
        or not SHA256.fullmatch(str(payload.get("candidate_manifest_sha256", "")))
        or not SHA256.fullmatch(str(payload.get("artifact_set_sha256", "")))
    ):
        raise ReceiptError("receipt_payload_value_invalid")
    if now.tzinfo is None or now.utcoffset() is None:
        raise ReceiptError("receipt_now_invalid")
    issued = parse_time(payload["issued_at"], "receipt_issued_time_invalid")
    expires = parse_time(payload["expires_at"], "receipt_expiry_time_invalid")
    now = now.astimezone(timezone.utc)
    ttl = (expires - issued).total_seconds()
    if ttl <= 0 or ttl > max_ttl_seconds or now < issued or now > expires:
        raise ReceiptError("receipt_expired_or_not_yet_valid")
    return payload


def verify(
    repo: Path,
    artifact_dir: Path,
    receipt_path: Path,
    key_path: Path,
    now: datetime | None = None,
) -> dict[str, Any]:
    repo = repo.resolve()
    artifact_dir = artifact_dir.resolve()
    receipt = load_receipt(receipt_path, repo)
    if set(receipt) != {"schema", "payload", "signature"} or receipt.get("schema") != SCHEMA:
        raise ReceiptError("receipt_shape_invalid")
    signature = receipt.get("signature")
    if (
        not isinstance(signature, dict)
        or set(signature) != {"algorithm", "value"}
        or signature.get("algorithm") != "hmac-sha256"
        or not SHA256.fullmatch(str(signature.get("value", "")))
    ):
        raise ReceiptError("receipt_signature_shape_invalid")

    identity, policy = candidate.load_controls(repo)
    if policy["authorization_receipt"]["schema"] != SCHEMA:
        raise ReceiptError("receipt_policy_schema_mismatch")
    payload = validate_payload(
        receipt["payload"],
        policy["authorization_receipt"]["max_ttl_seconds"],
        now or datetime.now(timezone.utc),
    )
    key = load_key(key_path, repo)
    expected_signature = hmac.new(key, canonical_payload(payload), hashlib.sha256).hexdigest()
    if not hmac.compare_digest(expected_signature, signature["value"]):
        raise ReceiptError("receipt_signature_invalid")

    manifest = candidate.verify(
        repo,
        artifact_dir,
        payload["candidate_run_id"],
        payload["candidate_run_attempt"],
    )
    manifest_path = artifact_dir / candidate.MANIFEST_NAME
    expected = {
        "repository": manifest["repository"],
        "source_commit": manifest["source_commit"],
        "source_tree": manifest["source_tree"],
        "product_version": manifest["product_identity"]["version"],
        "tag": f"v{manifest['product_identity']['version']}",
        "candidate_manifest_sha256": candidate.digest(manifest_path),
        "artifact_set_sha256": manifest["artifact_set_sha256"],
    }
    if any(payload.get(key_name) != value for key_name, value in expected.items()):
        raise ReceiptError("receipt_candidate_binding_mismatch")
    return {
        "schema": "synon.release-authorization-verification.v1",
        "authorized": True,
        "receipt_id": payload["receipt_id"],
        "authorization_reference": payload["authorization_reference"],
        "repository": payload["repository"],
        "candidate_run_id": payload["candidate_run_id"],
        "candidate_run_attempt": payload["candidate_run_attempt"],
        "source_commit": payload["source_commit"],
        "source_tree": payload["source_tree"],
        "product_version": payload["product_version"],
        "tag": payload["tag"],
        "candidate_manifest_sha256": payload["candidate_manifest_sha256"],
        "artifact_set_sha256": payload["artifact_set_sha256"],
        "expires_at": payload["expires_at"],
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--artifact-dir", type=Path, required=True)
    parser.add_argument("--receipt", type=Path, required=True)
    parser.add_argument("--key-file", type=Path, required=True)
    arguments = parser.parse_args(argv)
    try:
        result = verify(
            arguments.repo,
            arguments.artifact_dir,
            arguments.receipt,
            arguments.key_file,
        )
    except (OSError, ReceiptError) as error:
        print(json.dumps({"ok": False, "code": str(error)}, sort_keys=True))
        return 2
    print(json.dumps({"ok": True, "result": result}, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
