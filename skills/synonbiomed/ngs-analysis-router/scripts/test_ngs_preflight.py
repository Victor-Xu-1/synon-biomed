import importlib.util
import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).with_name("ngs_preflight.py")
SPEC = importlib.util.spec_from_file_location("synon_ngs_preflight", MODULE_PATH)
assert SPEC and SPEC.loader
preflight = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(preflight)


class NGSPreflightTests(unittest.TestCase):
    def setUp(self) -> None:
        self.workspace = tempfile.TemporaryDirectory(prefix="synon-ngs-workspace-")
        self.outside = tempfile.TemporaryDirectory(prefix="synon-ngs-outside-")
        self.root = Path(self.workspace.name)
        self.previous = Path.cwd()
        os.chdir(self.root)

    def tearDown(self) -> None:
        os.chdir(self.previous)
        self.workspace.cleanup()
        self.outside.cleanup()

    def test_inventory_classifies_inputs_without_reading_payloads(self) -> None:
        (self.root / "reads").mkdir()
        (self.root / "reads" / "sample_R1.fastq.gz").write_bytes(b"not decompressed")
        (self.root / "sample.bam").write_bytes(b"not parsed")
        (self.root / "variants.vcf.gz").write_bytes(b"not parsed")
        (self.root / "matrix.mtx").write_text("%%MatrixMarket\n", encoding="utf-8")
        (self.root / "features.tsv").write_text("gene\n", encoding="utf-8")
        (self.root / "barcodes.tsv.gz").write_bytes(b"not decompressed")
        (self.root / "reference.fa").write_text(">chr1\nA\n", encoding="utf-8")
        (self.root / "genes.gtf").write_text("", encoding="utf-8")
        (self.root / "SampleSheet.csv").write_text("[Header]\n", encoding="utf-8")

        report = preflight.inventory(self.root)
        counts = report["summary"]["matchedCounts"]
        self.assertEqual(counts["fastq"], 1)
        self.assertEqual(counts["alignment"], 1)
        self.assertEqual(counts["variant"], 1)
        self.assertEqual(counts["matrix"], 1)
        self.assertEqual(counts["reference"], 1)
        self.assertEqual(counts["annotation"], 1)
        self.assertEqual(counts["tenx"], 3)
        self.assertEqual(counts["illumina"], 1)
        self.assertFalse(report["networkUsed"])
        self.assertFalse(report["payloadsRead"])
        self.assertFalse(report["programsExecuted"])
        self.assertEqual(report["workspace"], ".")
        self.assertEqual(report["scanRoot"], ".")

    def test_skips_symlinks_and_rejects_workspace_escape(self) -> None:
        outside = Path(self.outside.name)
        (outside / "private.fastq.gz").write_bytes(b"private")
        link = self.root / "outside-link"
        try:
            link.symlink_to(outside, target_is_directory=True)
        except (NotImplementedError, OSError) as exc:
            self.skipTest(f"directory symlinks are unavailable: {exc}")
        report = preflight.inventory(self.root)
        self.assertEqual(report["summary"]["skippedSymlinks"], 1)
        self.assertNotIn("fastq", report["summary"]["matchedCounts"])
        with self.assertRaisesRegex(preflight.PreflightError, "inside the current project workspace"):
            preflight.resolve_workspace_path(str(outside), "scan root", must_exist=True)

    def test_cli_writes_only_the_explicit_workspace_report(self) -> None:
        (self.root / "sample.cram").write_bytes(b"not parsed")
        completed = subprocess.run(
            [sys.executable, str(MODULE_PATH), "--root", ".", "--output", "diagnostics/ngs.json"],
            cwd=self.root,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        self.assertEqual(completed.returncode, 0, completed.stderr)
        response = json.loads(completed.stdout)
        report = json.loads((self.root / "diagnostics" / "ngs.json").read_text(encoding="utf-8"))
        self.assertEqual(response["summary"]["matchedCounts"]["alignment"], 1)
        self.assertEqual(report["summary"]["matchedCounts"]["alignment"], 1)
        self.assertEqual(response["output"], "diagnostics/ngs.json")
        self.assertEqual(report["output"], "diagnostics/ngs.json")

    def test_cli_requires_explicit_overwrite(self) -> None:
        output = self.root / "ngs.json"
        output.write_text('{"preserve": true}\n', encoding="utf-8")
        refused = subprocess.run(
            [sys.executable, str(MODULE_PATH), "--output", "ngs.json"],
            cwd=self.root,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        self.assertEqual(refused.returncode, 2)
        self.assertIn("pass --overwrite", refused.stderr)
        self.assertEqual(output.read_text(encoding="utf-8"), '{"preserve": true}\n')

        replaced = subprocess.run(
            [sys.executable, str(MODULE_PATH), "--output", "ngs.json", "--overwrite"],
            cwd=self.root,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        self.assertEqual(replaced.returncode, 0, replaced.stderr)
        self.assertEqual(json.loads(output.read_text(encoding="utf-8"))["output"], "ngs.json")


if __name__ == "__main__":
    unittest.main(verbosity=2)
