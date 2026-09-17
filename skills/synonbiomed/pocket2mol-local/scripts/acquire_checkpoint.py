#!/usr/bin/env python3
"""Acquire and validate the official Pocket2Mol checkpoint folder."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
from urllib.parse import urlparse


FOLDER_PATH = re.compile(r"^/drive/folders/([A-Za-z0-9_-]+)$")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Download the exact official Pocket2Mol Google Drive folder with resume support."
    )
    parser.add_argument("--folder-url", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--expected-filename", default="pretrained.pt")
    parser.add_argument(
        "--validate-only",
        action="store_true",
        help="Validate an already acquired checkpoint without network access.",
    )
    return parser.parse_args()


def require_folder_url(raw: str) -> str:
    parsed = urlparse(raw.strip())
    if parsed.scheme != "https" or parsed.netloc.lower() != "drive.google.com":
        raise ValueError("checkpoint source must be the exact official HTTPS drive.google.com folder URL")
    match = FOLDER_PATH.fullmatch(parsed.path.rstrip("/"))
    if match is None:
        raise ValueError(
            "checkpoint source must retain /drive/folders/<folder-id>; a folder ID is not a file ID"
        )
    return raw.strip()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def validate_checkpoint(root: Path, expected_filename: str) -> Path:
    if Path(expected_filename).name != expected_filename or expected_filename in {"", ".", ".."}:
        raise ValueError("expected filename must be one path-free name")
    matches = sorted(path.resolve() for path in root.rglob(expected_filename) if path.is_file())
    if not matches:
        model_suffixes = {".pt", ".pth", ".ckpt"}
        matches = sorted(
            path.resolve()
            for path in root.rglob("*")
            if path.is_file() and path.suffix.lower() in model_suffixes
        )
    if len(matches) != 1:
        raise RuntimeError(
            f"checkpoint folder must contain exactly one {expected_filename}; found {len(matches)}"
        )
    checkpoint = matches[0]
    if checkpoint.stat().st_size <= 0:
        raise RuntimeError("checkpoint file is empty")
    with checkpoint.open("rb") as handle:
        prefix = handle.read(512).lstrip().lower()
    if prefix.startswith((b"<!doctype html", b"<html", b"<?xml", b"<error")):
        raise RuntimeError("checkpoint response is HTML/XML rather than model data")
    return checkpoint


def main() -> None:
    args = parse_args()
    folder_url = require_folder_url(args.folder_url)
    output_dir = Path(args.output_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    if not args.validate_only:
        try:
            import gdown
        except ImportError as exc:
            raise RuntimeError(
                "gdown is not installed in the selected managed environment; add it as a support package"
            ) from exc
        downloaded = gdown.download_folder(
            url=folder_url,
            output=str(output_dir) + "/",
            quiet=False,
            use_cookies=True,
            resume=True,
        )
        if not downloaded:
            raise RuntimeError(
                "folder download returned no files; retain partial bytes, admit the exact redirect host, and rerun"
            )
    checkpoint = validate_checkpoint(output_dir, args.expected_filename)
    receipt = {
        "schema": "synon.pocket2mol-checkpoint-acquisition.v1",
        "status": "passed",
        "source_kind": "google_drive_folder",
        "source_url": folder_url,
        "checkpoint": str(checkpoint),
        "filename": checkpoint.name,
        "size_bytes": checkpoint.stat().st_size,
        "sha256": sha256_file(checkpoint),
        "resume_enabled": True,
    }
    receipt_path = output_dir / "checkpoint_acquisition.json"
    receipt_path.write_text(json.dumps(receipt, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    receipt["receipt"] = str(receipt_path.resolve())
    print(json.dumps(receipt, ensure_ascii=False, sort_keys=True))


if __name__ == "__main__":
    main()
