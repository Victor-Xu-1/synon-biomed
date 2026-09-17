#!/usr/bin/env python3
"""Verify the reused v1.1 ChEMBL ADMET tool and optionally call ChEMBL live."""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path


sys.dont_write_bytecode = True


ROOT = Path(__file__).resolve().parents[2]
BIO_ROOT = ROOT / "assets" / "optional" / "mcp-servers" / "bio-tools"
LIB_ROOT = BIO_ROOT / "lib"
MANIFEST_PATH = BIO_ROOT.parent / "bio-tools.manifest.json"


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def verify_manifest_paths(relative_paths: list[str]) -> None:
    manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
    entries = {entry["path"]: entry for entry in manifest["files"]}
    for relative in relative_paths:
        path = BIO_ROOT / relative
        entry = entries.get(relative)
        if entry is None:
            raise AssertionError(f"manifest entry missing: {relative}")
        if entry["bytes"] != path.stat().st_size:
            raise AssertionError(f"manifest byte count mismatch: {relative}")
        if entry["sha256"] != sha256(path):
            raise AssertionError(f"manifest digest mismatch: {relative}")


def verify_local_contract() -> None:
    sys.path.insert(0, str(LIB_ROOT))
    from mcp_chembl import marshal  # pylint: disable=import-outside-toplevel
    from mcp_chembl import server  # pylint: disable=import-outside-toplevel

    schema_path = LIB_ROOT / "mcp_chembl" / "schemas.json"
    schema = json.loads(schema_path.read_text(encoding="utf-8"))
    names = {tool["name"] for tool in schema["tools"]}
    if "get_admet" not in names or "get_admet" not in server.HANDLERS:
        raise AssertionError("get_admet is not registered in schema and handler catalog")

    fixture = {
        "molecule_chembl_id": "CHEMBL25",
        "molecule_properties": {
            "alogp": "1.31",
            "full_mwt": "180.16",
            "psa": "63.60",
            "hba": 3,
            "hbd": 1,
            "full_molformula": "C9H8O4",
        },
    }
    result = marshal.admet_response(fixture, "CHEMBL25")
    if result["properties"]["molecular_weight"] != 180.16:
        raise AssertionError(f"unexpected ADMET projection: {result}")

    verify_manifest_paths(
        [
            "lib/mcp_chembl/marshal.py",
            "lib/mcp_chembl/schemas.json",
            "lib/mcp_chembl/server.py",
        ]
    )


def verify_live_contract() -> dict[str, object]:
    sys.path.insert(0, str(LIB_ROOT))
    from mcp_chembl.server import get_admet  # pylint: disable=import-outside-toplevel

    result = json.loads(get_admet({"molecule_chembl_id": "CHEMBL25"}))
    properties = result.get("properties") or {}
    if result.get("found") is not True:
        raise AssertionError(f"live ChEMBL molecule was not found: {result}")
    if properties.get("molecule_chembl_id") != "CHEMBL25":
        raise AssertionError(f"live ChEMBL identity mismatch: {result}")
    if not isinstance(properties.get("molecular_weight"), (int, float)):
        raise AssertionError(f"live ChEMBL molecular weight missing: {result}")
    return {
        "found": True,
        "molecule": properties["molecule_chembl_id"],
        "molecularWeight": properties["molecular_weight"],
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--live", action="store_true", help="call the public ChEMBL API")
    args = parser.parse_args()

    verify_local_contract()
    report: dict[str, object] = {"local": "pass", "manifest": "pass"}
    if args.live:
        report["live"] = verify_live_contract()
    print(json.dumps(report, ensure_ascii=True, sort_keys=True))


if __name__ == "__main__":
    main()
