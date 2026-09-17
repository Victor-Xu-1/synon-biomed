from __future__ import annotations

import hashlib
import json
from pathlib import Path

import pytest

from refresh_bio_tools_manifest import refresh


def test_refresh_updates_only_declared_assets(tmp_path: Path) -> None:
    asset_root = tmp_path / "bio-tools"
    asset_root.mkdir()
    payload = b"authoritative asset\n"
    (asset_root / "declared.txt").write_bytes(payload)
    (asset_root / "unreviewed.txt").write_text("not admitted", encoding="utf-8")
    manifest_path = tmp_path / "bio-tools.manifest.json"
    manifest_path.write_text(
        json.dumps({"schemaVersion": 1, "files": [{"path": "declared.txt"}]}),
        encoding="utf-8",
    )

    assert refresh(manifest_path) == 1
    document = json.loads(manifest_path.read_text(encoding="utf-8"))
    assert document["files"] == [
        {
            "path": "declared.txt",
            "sha256": hashlib.sha256(payload).hexdigest(),
            "bytes": len(payload),
        }
    ]


def test_refresh_rejects_missing_or_traversing_assets(tmp_path: Path) -> None:
    (tmp_path / "bio-tools").mkdir()
    manifest_path = tmp_path / "bio-tools.manifest.json"
    manifest_path.write_text(
        json.dumps({"files": [{"path": "missing.txt"}]}), encoding="utf-8"
    )
    with pytest.raises(FileNotFoundError):
        refresh(manifest_path)

    manifest_path.write_text(
        json.dumps({"files": [{"path": "../outside.txt"}]}), encoding="utf-8"
    )
    with pytest.raises(ValueError):
        refresh(manifest_path)
