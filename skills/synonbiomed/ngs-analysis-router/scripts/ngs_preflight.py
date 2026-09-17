#!/usr/bin/env python3
"""Bounded, read-only NGS input inventory for the current project workspace."""

from __future__ import annotations

import argparse
import json
import os
import sys
from collections import Counter
from pathlib import Path
from typing import Any


SCHEMA_VERSION = 1
MAX_FILES = 100_000
MAX_DEPTH = 12
MAX_REPORTED_PATHS_PER_KIND = 25
MAX_OUTPUT_BYTES = 4 * 1024 * 1024

SUFFIXES: dict[str, tuple[str, ...]] = {
    "fastq": (".fastq", ".fq", ".fastq.gz", ".fq.gz"),
    "alignment": (".bam", ".cram", ".sam"),
    "variant": (".vcf", ".vcf.gz", ".bcf"),
    "matrix": (".mtx", ".mtx.gz", ".h5", ".h5ad", ".loom", ".rds"),
    "reference": (".fa", ".fasta", ".fna", ".fa.gz", ".fasta.gz", ".fai", ".dict"),
    "annotation": (".gtf", ".gtf.gz", ".gff", ".gff3", ".bed", ".bed.gz"),
    "tabular": (".csv", ".tsv", ".txt"),
}

MARKERS: dict[str, tuple[str, ...]] = {
    "illumina": ("runinfo.xml", "runparameters.xml", "samplesheet.csv"),
    "tenx": ("matrix.mtx", "matrix.mtx.gz", "features.tsv", "features.tsv.gz", "barcodes.tsv", "barcodes.tsv.gz"),
    "design": ("samplesheet.csv", "sample_sheet.csv", "metadata.csv", "metadata.tsv", "contrasts.tsv", "design.tsv"),
}


class PreflightError(RuntimeError):
    pass


def workspace_root() -> Path:
    return Path.cwd().resolve(strict=True)


def resolve_workspace_path(value: str, label: str, *, must_exist: bool = False) -> Path:
    root = workspace_root()
    candidate = Path(value).expanduser()
    if not candidate.is_absolute():
        candidate = root / candidate
    try:
        resolved = candidate.resolve(strict=must_exist)
    except OSError as exc:
        raise PreflightError(f"cannot resolve {label} {value}: {exc}") from exc
    try:
        resolved.relative_to(root)
    except ValueError as exc:
        raise PreflightError(f"{label} must stay inside the current project workspace: {value}") from exc
    return resolved


def classify(name: str) -> set[str]:
    lowered = name.lower()
    kinds = {kind for kind, suffixes in SUFFIXES.items() if lowered.endswith(suffixes)}
    for kind, markers in MARKERS.items():
        if lowered in markers:
            kinds.add(kind)
    return kinds


def inventory(root: Path) -> dict[str, Any]:
    workspace = workspace_root()
    counts: Counter[str] = Counter()
    samples: dict[str, list[str]] = {}
    total_files = 0
    total_bytes = 0
    skipped_symlinks = 0
    truncated = False
    errors: list[dict[str, str]] = []

    def record_walk_error(error: OSError) -> None:
        if len(errors) >= 25:
            return
        failing_path = Path(error.filename) if error.filename else root
        try:
            display_path = failing_path.resolve(strict=False).relative_to(workspace).as_posix()
        except (OSError, ValueError):
            display_path = "<unavailable>"
        errors.append({"path": display_path, "error": str(error)})

    for current, directories, files in os.walk(
        root,
        topdown=True,
        onerror=record_walk_error,
        followlinks=False,
    ):
        current_path = Path(current)
        try:
            depth = len(current_path.relative_to(root).parts)
        except ValueError as exc:
            raise PreflightError(f"inventory escaped root: {current_path}") from exc
        if depth >= MAX_DEPTH:
            directories[:] = []
        kept_directories: list[str] = []
        for directory in sorted(directories):
            child = current_path / directory
            if child.is_symlink():
                skipped_symlinks += 1
            else:
                kept_directories.append(directory)
        directories[:] = kept_directories

        for filename in sorted(files):
            path = current_path / filename
            if path.is_symlink():
                skipped_symlinks += 1
                continue
            total_files += 1
            if total_files > MAX_FILES:
                truncated = True
                directories[:] = []
                break
            try:
                stat = path.stat(follow_symlinks=False)
            except OSError as exc:
                if len(errors) < 25:
                    errors.append({"path": str(path.relative_to(workspace)), "error": str(exc)})
                continue
            total_bytes += max(0, stat.st_size)
            relative = path.relative_to(workspace).as_posix()
            for kind in classify(filename):
                counts[kind] += 1
                bucket = samples.setdefault(kind, [])
                if len(bucket) < MAX_REPORTED_PATHS_PER_KIND:
                    bucket.append(relative)
        if truncated:
            break

    relative_root = root.relative_to(workspace).as_posix() or "."
    return {
        "schemaVersion": SCHEMA_VERSION,
        "workspace": ".",
        "scanRoot": relative_root,
        "limits": {"maxFiles": MAX_FILES, "maxDepth": MAX_DEPTH, "maxSamplesPerKind": MAX_REPORTED_PATHS_PER_KIND},
        "summary": {
            "totalFiles": min(total_files, MAX_FILES),
            "totalBytes": total_bytes,
            "matchedCounts": dict(sorted(counts.items())),
            "skippedSymlinks": skipped_symlinks,
            "truncated": truncated,
        },
        "samplePaths": {kind: sorted(paths) for kind, paths in sorted(samples.items())},
        "errors": errors,
        "networkUsed": False,
        "payloadsRead": False,
        "programsExecuted": False,
    }


def atomic_write_json(path: Path, payload: dict[str, Any], *, overwrite: bool = False) -> None:
    encoded = (json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True) + "\n").encode("utf-8")
    if len(encoded) > MAX_OUTPUT_BYTES:
        raise PreflightError(f"preflight report exceeds {MAX_OUTPUT_BYTES} bytes")
    if path.exists() and not overwrite:
        raise PreflightError(f"output already exists; pass --overwrite to replace it: {path.name}")
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        with temporary.open("xb") as handle:
            handle.write(encoded)
            handle.flush()
            os.fsync(handle.fileno())
        if overwrite:
            os.replace(temporary, path)
        else:
            try:
                os.link(temporary, path)
            except FileExistsError as exc:
                raise PreflightError(
                    f"output already exists; pass --overwrite to replace it: {path.name}"
                ) from exc
            except OSError as exc:
                raise PreflightError(f"cannot create output report atomically: {exc}") from exc
    finally:
        temporary.unlink(missing_ok=True)


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--root", default=".", help="workspace-relative directory to inventory")
    result.add_argument("--output", help="optional workspace-relative JSON report path")
    result.add_argument("--overwrite", action="store_true", help="replace an existing output report")
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        root = resolve_workspace_path(args.root, "scan root", must_exist=True)
        if not root.is_dir():
            raise PreflightError(f"scan root must be a directory: {args.root}")
        report = inventory(root)
        if args.output:
            output = resolve_workspace_path(args.output, "output")
            report["output"] = output.relative_to(workspace_root()).as_posix()
            atomic_write_json(output, report, overwrite=args.overwrite)
        print(json.dumps(report, ensure_ascii=False, indent=2, sort_keys=True))
        return 0
    except PreflightError as exc:
        print(json.dumps({"ok": False, "error": str(exc)}, ensure_ascii=False), file=sys.stderr)
        return 2


if __name__ == "__main__":
    raise SystemExit(main())
