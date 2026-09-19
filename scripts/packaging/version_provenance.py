"""Derive provenance for a version-only proposal without approving source changes."""

from __future__ import annotations

import copy
import hashlib
import importlib.util
import json
from pathlib import Path
import re

AUDIT = "scripts/audit/audit_frontend_migration.py"
MIGRATION = "frontend/MIGRATION_MANIFEST.json"
LICENSES = "frontend/THIRD_PARTY_LICENSES.json"
DERIVED = {AUDIT, MIGRATION, LICENSES}
IDENTITY = "product-identity.json"
BOOKKEEPING = ".github/release-please-manifest.json"


def document(raw: bytes) -> dict:
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError("duplicate JSON key")
            result[key] = value
        return result
    value = json.loads(raw, object_pairs_hook=unique)
    if not isinstance(value, dict):
        raise ValueError("expected a JSON object")
    return value


def projections(root: Path) -> dict[str, list[str]]:
    matrix = document((root / "docs/governance/product-identity-consumer-matrix.json").read_bytes())
    result = {IDENTITY: ["/version"], BOOKKEEPING: ["/."]}
    for entry in matrix["product_version_projections"]:
        if entry["kind"] == "json-pointer":
            result.setdefault(entry["path"], []).append(entry["pointer"])
    return result


def replace_pointer(value: dict, pointer: str, old: str, new: str) -> None:
    keys = [key.replace("~1", "/").replace("~0", "~") for key in pointer.split("/")[1:]]
    target = value
    for key in keys[:-1]:
        target = target[key]
    if target[keys[-1]] != old:
        raise ValueError("base version projection is not aligned")
    target[keys[-1]] = new


def plan(root: Path, proposed: dict[str, bytes], changed: set[str]) -> dict[str, bytes]:
    """Check the complete PR delta, then derive only the three audit outputs."""
    projected = projections(root)
    config = document((root / ".github/release-please-config.json").read_bytes())
    changelog = config["packages"]["."]["changelog-path"]
    if changed - (set(projected) | DERIVED | {changelog}):
        raise ValueError("version proposal contains non-version changes")
    old = document((root / IDENTITY).read_bytes())["version"]
    new = document(proposed[IDENTITY])["version"]
    if not re.fullmatch(r"0\.1\.(0|[1-9][0-9]*)", new) or new != f"0.1.{int(old.split('.')[-1]) + 1}":
        raise ValueError("version proposal must advance one patch")
    for path, pointers in projected.items():
        expected = document((root / path).read_bytes())
        for pointer in pointers:
            replace_pointer(expected, pointer, old, new)
        if document(proposed[path]) != expected:
            raise ValueError(f"non-version JSON change: {path}")

    # Only trusted baseline code is imported. Proposal code is never executed.
    spec = importlib.util.spec_from_file_location("version_baseline_audit", root / AUDIT)
    audit = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(audit)
    audit.main(["--root", str(root), "--check"])
    migration = document((root / MIGRATION).read_bytes())
    for category, hash_key in (("adaptations", "targetSHA256"), ("additions", "sha256")):
        for record in migration[category]:
            raw = proposed.get("frontend/" + record["path"])
            if raw is not None:
                record[hash_key] = hashlib.sha256(raw).hexdigest()
                record["bytes"] = len(raw)
    adaptation = audit.adaptation_fingerprint(migration["adaptations"])
    addition = audit.addition_fingerprint(migration["additions"])
    migration["adaptationFingerprintSHA256"] = adaptation
    migration["additionFingerprintSHA256"] = addition
    source = (root / AUDIT).read_text()
    for kind, digest in (("ADAPTATION", adaptation), ("ADDITION", addition)):
        source, count = re.subn(
            rf'(?m)^APPROVED_{kind}_FINGERPRINT = "[0-9a-f]{{64}}"$',
            f'APPROVED_{kind}_FINGERPRINT = "{digest}"', source,
        )
        if count != 1:
            raise ValueError("unexpected audit fingerprint contract")
    licenses = copy.deepcopy(document((root / LICENSES).read_bytes()))
    licenses["lockfileSHA256"] = hashlib.sha256(proposed["frontend/package-lock.json"]).hexdigest()
    for link in licenses["workspaceLinks"]:
        path = "frontend/" + link["path"] + "/package.json"
        if "/version" in projected.get(path, []):
            link["version"] = new
    return {AUDIT: source.encode(), MIGRATION: audit.json_bytes(migration), LICENSES: audit.json_bytes(licenses)}
