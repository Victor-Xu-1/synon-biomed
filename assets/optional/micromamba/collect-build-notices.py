#!/usr/bin/env python3
"""Emit notices from the pinned build environment without copying host paths."""
import contextlib
import hashlib
import json
import os
from pathlib import Path
import sys


def main():
    environment = Path(sys.argv[1]).resolve(strict=True)
    cache = Path(sys.argv[2]).resolve()
    caches = [cache] + [Path(p).resolve(strict=True) for p in os.environ.get("CONDA_PKGS_DIRS", "").split(os.pathsep) if p]
    texts = {}
    print("Micromamba controlled-build dependency notices\n")
    print("Includes the complete locked build closure, including build-only tools.")
    print("Source distributions and hashes are recorded in build-linux-64.lock.\n")
    for metadata in sorted((environment / "conda-meta").glob("*.json")):
        record = json.loads(metadata.read_text(encoding="utf-8"))
        extracted = Path(record["extracted_package_dir"]).resolve(strict=True)
        if not any(extracted.is_relative_to(root) for root in caches):
            raise ValueError("dependency cache record escapes the build cache")
        print(f"=== {record['name']} {record['version']} ({record.get('license', 'see distribution')}) ===")
        print(record["url"])
        licenses = extracted / "info" / "licenses"
        for entry in sorted(licenses.rglob("*")) if licenses.is_dir() else []:
            if entry.is_file():
                if not entry.resolve().is_relative_to(extracted):
                    raise ValueError("dependency notice escapes its distribution")
                text = entry.read_text(encoding="utf-8", errors="strict")
                digest = hashlib.sha256(text.encode("utf-8")).hexdigest()
                print(f"Notice {entry.relative_to(licenses)}: SHA256 {digest}")
                texts[digest] = text
        print()
    for digest, text in sorted(texts.items()):
        print(f"=== Notice SHA256 {digest} ===")
        print(text)


if __name__ == "__main__":
    if len(sys.argv) == 4:
        with Path(sys.argv[3]).open("w", encoding="utf-8", newline="\n") as output:
            with contextlib.redirect_stdout(output):
                main()
    elif len(sys.argv) == 3:
        main()
    else:
        raise SystemExit("Usage: collect-build-notices.py ENVIRONMENT CACHE [OUTPUT]")
