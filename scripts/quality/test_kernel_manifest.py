"""Verify that integrity generation preserves metadata and covers shipped bytes."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from kernel_manifest import render


class KernelManifestTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="synon-manifest-")
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        self.assets = self.root / "assets/optional"
        (self.assets / "kernels").mkdir(parents=True)
        (self.assets / "compute").mkdir()
        self.manifest = self.assets / "kernel-compute.manifest.json"
        self.metadata = {"license": "preserved-component-notice", "source": {"commit": "example"}, "files": []}
        self.manifest.write_text(json.dumps(self.metadata), encoding="utf-8")
        self.worker = self.assets / "kernels/worker.py"
        self.worker.write_bytes(b"print('scientific computation')\n")

    def result(self):
        target, contents = render(self.root)
        self.assertEqual(target, self.manifest)
        return json.loads(contents)

    def test_preserves_metadata_and_hashes_actual_bytes(self):
        result = self.result()
        self.assertEqual(result["license"], self.metadata["license"])
        self.assertEqual(result["source"], self.metadata["source"])
        self.assertEqual(result["files"], [{"path": "kernels/worker.py",
                         "bytes": len(self.worker.read_bytes()),
                         "sha256": hashlib.sha256(self.worker.read_bytes()).hexdigest()}])

    def test_changed_bytes_change_inventory(self):
        previous = self.result()["files"]
        self.worker.write_bytes(b"print(42)\n")
        self.assertNotEqual(self.result()["files"], previous)

    def test_removed_file_cannot_survive_in_inventory(self):
        self.manifest.write_text(json.dumps(self.result()), encoding="utf-8")
        self.worker.unlink()
        self.assertEqual(self.result()["files"], [])

    def test_cache_is_not_a_shipped_asset(self):
        cache = self.assets / "kernels/__pycache__"
        cache.mkdir()
        (cache / "worker.pyc").write_bytes(b"generated cache")
        self.assertEqual(len(self.result()["files"]), 1)

    def test_rejects_symlink(self):
        (self.assets / "compute/linked.py").symlink_to(self.worker)
        with self.assertRaisesRegex(ValueError, "symbolic links"):
            self.result()

    def test_rejects_unexpected_files(self):
        (self.assets / "compute/untracked.bin").write_bytes(b"unreviewed")
        with self.assertRaisesRegex(ValueError, "unexpected kernel asset"):
            self.result()

    def test_render_is_sorted_and_reproducible(self):
        (self.assets / "compute/runtime.py").write_bytes(b"pass\n")
        first = render(self.root)[1]
        self.assertEqual(render(self.root)[1], first)
        paths = [entry["path"] for entry in json.loads(first)["files"]]
        self.assertEqual(paths, sorted(paths))


if __name__ == "__main__":
    unittest.main()
