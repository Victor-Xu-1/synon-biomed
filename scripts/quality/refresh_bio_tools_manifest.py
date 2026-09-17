#!/usr/bin/env python3
"""Refresh the integrity fields in the vendored bio-tools asset manifest.

The manifest is an allowlist, not a directory snapshot: this command updates
only files already declared by the manifest and fails when a declared file is
missing. New vendored files must therefore be reviewed and added explicitly.
"""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path


def refresh(manifest_path: Path) -> int:
    manifest_path = manifest_path.resolve()
    suffix = ".manifest.json"
    if not manifest_path.name.endswith(suffix):
        raise ValueError("bio-tools manifest filename must end with .manifest.json")
    asset_root = manifest_path.parent / manifest_path.name.removesuffix(suffix)
    document = json.loads(manifest_path.read_text(encoding="utf-8"))
    files = document.get("files")
    if not isinstance(files, list) or not files:
        raise ValueError("bio-tools manifest must contain a non-empty files array")

    seen: set[str] = set()
    for entry in files:
        if not isinstance(entry, dict) or not isinstance(entry.get("path"), str):
            raise ValueError("every bio-tools manifest entry must declare a path")
        relative = entry["path"].replace("\\", "/").strip("/")
        if not relative or relative in seen or ".." in Path(relative).parts:
            raise ValueError(f"invalid or duplicate manifest path: {relative!r}")
        seen.add(relative)
        source = (asset_root / relative).resolve()
        if asset_root not in source.parents or not source.is_file():
            raise FileNotFoundError(f"declared bio-tools asset is missing: {relative}")
        payload = source.read_bytes()
        entry["path"] = relative
        entry["sha256"] = hashlib.sha256(payload).hexdigest()
        entry["bytes"] = len(payload)

    manifest_path.write_text(
        json.dumps(document, indent=4, ensure_ascii=False) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    return len(files)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "manifest",
        nargs="?",
        type=Path,
        default=Path("assets/optional/mcp-servers/bio-tools.manifest.json"),
    )
    args = parser.parse_args()
    count = refresh(args.manifest)
    print(f"refreshed {count} bio-tools manifest entries")


if __name__ == "__main__":
    main()
