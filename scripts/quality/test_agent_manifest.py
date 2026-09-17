from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest

import agent_manifest


class AgentManifestTests(unittest.TestCase):
    def test_manifest_is_sorted_complete_and_content_addressed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            repo = Path(directory)
            root = repo / agent_manifest.AGENT_ROOT
            (root / "zeta").mkdir(parents=True)
            (root / "alpha").mkdir()
            (root / "zeta/metadata.yaml").write_text("agent_name: ZETA\n", encoding="utf-8")
            (root / "alpha/metadata.yaml").write_text("agent_name: ALPHA\n", encoding="utf-8")
            (root / "capabilities.json").write_text("{}\n", encoding="utf-8")
            manifest = agent_manifest.build_manifest(repo)
            self.assertEqual(manifest["agents"], ["alpha", "zeta"])
            paths = [item["path"] for item in manifest["files"]]
            self.assertEqual(paths, ["alpha/metadata.yaml", "capabilities.json", "zeta/metadata.yaml"])
            for item in manifest["files"]:
                self.assertRegex(item["sha256"], r"^[0-9a-f]{64}$")
                self.assertGreater(item["bytes"], 0)

    def test_encoded_manifest_is_stable_json(self) -> None:
        payload = {"schemaVersion": 1, "source": "source", "agents": ["agent"], "files": []}
        raw = agent_manifest.encoded_manifest(payload)
        self.assertTrue(raw.endswith(b"\n"))
        self.assertEqual(json.loads(raw), payload)


if __name__ == "__main__":
    unittest.main()
