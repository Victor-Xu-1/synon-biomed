"""Reproduce the kernel/compute integrity inventory without changing licenses."""
import argparse
import hashlib
import json
from pathlib import Path


def render(root):
    asset_root = root / "assets/optional"
    target = asset_root / "kernel-compute.manifest.json"
    manifest = json.loads(target.read_text(encoding="utf-8"))
    entries = []
    for directory in ("kernels", "compute"):
        for path in sorted((asset_root / directory).rglob("*")):
            if "__pycache__" in path.parts:
                continue
            if path.is_symlink():
                raise ValueError("kernel assets must not contain symbolic links")
            if not path.is_file():
                continue
            if path.suffix not in (".py", ".R", ".in", ".lock", ".tmpl"):
                raise ValueError("unexpected kernel asset type: " + path.name)
            raw = path.read_bytes()
            entries.append({"path": path.relative_to(asset_root).as_posix(),
                            "sha256": hashlib.sha256(raw).hexdigest(), "bytes": len(raw)})
    manifest["files"] = sorted(entries, key=lambda entry: entry["path"])
    return target, json.dumps(manifest, indent=2) + "\n"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument("--write", action="store_true")
    args = parser.parse_args()
    target, rendered = render(args.root.resolve(strict=True))
    if args.write:
        target.write_text(rendered, encoding="utf-8")
    elif target.read_text(encoding="utf-8") != rendered:
        parser.exit(1, "kernel asset manifest is stale; run with --write\n")
    print("kernel asset manifest verified")


if __name__ == "__main__":
    main()
