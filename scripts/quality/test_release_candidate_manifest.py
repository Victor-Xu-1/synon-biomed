from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

from scripts.quality import release_candidate_manifest as candidate
from scripts.quality.test_product_identity_gate import identity, release_policy


PROJECT_ROOT = Path(__file__).resolve().parents[2]


def git(repo: Path, *arguments: str) -> str:
    return subprocess.check_output(
        [
            "git",
            "-C",
            str(repo),
            "-c",
            "user.name=release-candidate-test",
            "-c",
            "user.email=release-candidate@example.invalid",
            *arguments,
        ],
        text=True,
    ).strip()


def sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


class CandidateFixture:
    def __init__(self, root: Path) -> None:
        self.repo = root / "source"
        self.artifacts = root / "artifacts"
        self.repo.mkdir()
        self.artifacts.mkdir()
        git(self.repo, "init", "-q", "-b", "main")
        (self.repo / "docs/governance").mkdir(parents=True)
        (self.repo / "scripts").mkdir()
        (self.repo / "product-identity.json").write_text(
            json.dumps(identity(), indent=2) + "\n",
            encoding="utf-8",
        )
        (self.repo / "docs/governance/release-policy.json").write_text(
            json.dumps(release_policy(), indent=2) + "\n",
            encoding="utf-8",
        )
        shutil.copy2(
            PROJECT_ROOT / "scripts/source-tree-digest.sh",
            self.repo / "scripts/source-tree-digest.sh",
        )
        git(self.repo, "add", ".")
        git(self.repo, "commit", "-qm", "candidate source")
        self.create_artifacts()

    def create_artifacts(self) -> None:
        policy = release_policy()
        for name, _, kind in candidate.expected_artifacts(identity(), policy):
            if kind == "archive":
                (self.artifacts / name).write_bytes(("archive:" + name).encode())
        for name, _, kind in candidate.expected_artifacts(identity(), policy):
            if kind == "checksum":
                archive = name.removesuffix(".sha256")
                (self.artifacts / name).write_text(
                    f"{sha256(self.artifacts / archive)}  {archive}\n",
                    encoding="ascii",
                )


class ReleaseCandidateManifestTests(unittest.TestCase):
    def test_manifest_verifies_across_filesystem_permission_projection(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-cross-filesystem-") as directory:
            root = Path(directory)
            fixture = CandidateFixture(root)
            git(fixture.repo, "config", "core.fileMode", "false")
            for raw in git(fixture.repo, "ls-files").splitlines():
                path = fixture.repo / raw
                if path.is_file():
                    path.chmod(path.stat().st_mode | 0o111)
            manifest = candidate.create(fixture.repo, fixture.artifacts, 123, 2)
            clone = root / "native-clone"
            subprocess.run(
                ["git", "clone", "-q", "--no-local", str(fixture.repo), str(clone)],
                check=True,
            )
            verified = candidate.verify(clone, fixture.artifacts, 123, 2)
            self.assertEqual(verified["source_tree_sha256"], manifest["source_tree_sha256"])

    def test_release_entrypoints_do_not_create_source_cache(self) -> None:
        source = Path(candidate.__file__).resolve().parent
        with tempfile.TemporaryDirectory(prefix="synon-release-entrypoint-") as directory:
            root = Path(directory)
            for name in (
                "product_identity_gate.py",
                "release_contract_io.py",
                "release_candidate_manifest.py",
                "release_receipt_gate.py",
            ):
                shutil.copy2(source / name, root / name)
            environment = os.environ.copy()
            environment.pop("PYTHONDONTWRITEBYTECODE", None)
            for name in ("release_candidate_manifest.py", "release_receipt_gate.py"):
                result = subprocess.run(
                    [sys.executable, str(root / name), "--help"],
                    cwd=root,
                    env=environment,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    check=False,
                    timeout=10,
                )
                self.assertEqual(result.returncode, 0, result.stderr.decode())
            self.assertFalse((root / "__pycache__").exists())

    def test_create_and_verify_bind_source_and_every_artifact(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            result = candidate.create(fixture.repo, fixture.artifacts, 123, 2)
            self.assertEqual(result["candidate_run_id"], 123)
            self.assertEqual(result["candidate_run_attempt"], 2)
            self.assertEqual(len(result["artifacts"]), 4)
            self.assertEqual(
                result["artifact_set_sha256"],
                candidate.artifact_set_digest(result["artifacts"]),
            )
            self.assertEqual(result["source_commit"], git(fixture.repo, "rev-parse", "HEAD"))
            self.assertEqual(result["source_tree"], git(fixture.repo, "rev-parse", "HEAD^{tree}"))
            candidate.verify(fixture.repo, fixture.artifacts, 123, 2)
            manifest_digest = candidate.digest(
                fixture.artifacts / candidate.MANIFEST_NAME
            )
            candidate.verify(
                fixture.repo,
                fixture.artifacts,
                123,
                2,
                expected_manifest_sha256=manifest_digest,
                verify_source_tree_sha256=False,
            )
            with self.assertRaisesRegex(
                candidate.CandidateError, "manifest_digest_mismatch",
            ):
                candidate.verify(
                    fixture.repo,
                    fixture.artifacts,
                    123,
                    2,
                    expected_manifest_sha256="0" * 64,
                    verify_source_tree_sha256=False,
                )

    def test_artifact_tamper_and_wrong_checksum_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            candidate.create(fixture.repo, fixture.artifacts, 123, 1)
            archive = next(fixture.artifacts.glob("*-linux-amd64.tar.gz"))
            archive.write_bytes(archive.read_bytes() + b"tamper")
            with self.assertRaisesRegex(candidate.CandidateError, "checksum_invalid"):
                candidate.verify(fixture.repo, fixture.artifacts, 123, 1)

        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            checksum = next(fixture.artifacts.glob("*.sha256"))
            checksum.write_text("0" * 64 + "  wrong.tar.gz\n", encoding="ascii")
            with self.assertRaisesRegex(candidate.CandidateError, "checksum_invalid"):
                candidate.create(fixture.repo, fixture.artifacts, 123, 1)

    def test_source_dirty_and_control_tree_drift_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            (fixture.repo / "untracked.txt").write_text("dirty\n", encoding="utf-8")
            with self.assertRaisesRegex(candidate.CandidateError, "source_dirty"):
                candidate.create(fixture.repo, fixture.artifacts, 123, 1)

        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            path = fixture.repo / "docs/governance/release-policy.json"
            changed = release_policy()
            changed["rules"].append("working-tree-only")
            path.write_text(json.dumps(changed) + "\n", encoding="utf-8")
            git(fixture.repo, "update-index", "--assume-unchanged", str(path.relative_to(fixture.repo)))
            with self.assertRaisesRegex(candidate.CandidateError, "control_tree_mismatch"):
                candidate.create(fixture.repo, fixture.artifacts, 123, 1)

    def test_extra_symlink_and_hardlink_artifacts_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            (fixture.artifacts / "extra.txt").write_text("extra\n", encoding="utf-8")
            with self.assertRaisesRegex(candidate.CandidateError, "artifact_set_invalid"):
                candidate.create(fixture.repo, fixture.artifacts, 123, 1)

        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            archive = next(fixture.artifacts.glob("*-linux-amd64.tar.gz"))
            content = archive.read_bytes()
            archive.unlink()
            outside = Path(directory) / "outside"
            outside.write_bytes(content)
            archive.symlink_to(outside)
            with self.assertRaisesRegex(candidate.CandidateError, "artifact_entry_invalid"):
                candidate.create(fixture.repo, fixture.artifacts, 123, 1)

        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            archive = next(fixture.artifacts.glob("*-linux-amd64.tar.gz"))
            os.link(archive, Path(directory) / "second-link")
            with self.assertRaisesRegex(candidate.CandidateError, "artifact_entry_invalid"):
                candidate.create(fixture.repo, fixture.artifacts, 123, 1)

    def test_manifest_duplicate_key_wrong_run_and_existing_output_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            candidate.create(fixture.repo, fixture.artifacts, 123, 1)
            with self.assertRaisesRegex(candidate.CandidateError, "manifest_exists"):
                candidate.create(fixture.repo, fixture.artifacts, 123, 1)
            with self.assertRaisesRegex(candidate.CandidateError, "manifest_mismatch"):
                candidate.verify(fixture.repo, fixture.artifacts, 124, 1)
            manifest = fixture.artifacts / candidate.MANIFEST_NAME
            manifest.write_text('{"schema":"one","schema":"two"}\n', encoding="utf-8")
            with self.assertRaisesRegex(candidate.CandidateError, "json_duplicate_key"):
                candidate.verify(fixture.repo, fixture.artifacts, 123, 1)

    def test_artifact_directory_must_be_outside_source(self) -> None:
        with tempfile.TemporaryDirectory(prefix="synon-release-candidate-") as directory:
            fixture = CandidateFixture(Path(directory))
            inside = fixture.repo / "artifacts"
            inside.mkdir()
            with self.assertRaisesRegex(candidate.CandidateError, "inside_source"):
                candidate.candidate_document(fixture.repo, inside, 123, 1)


if __name__ == "__main__":
    unittest.main()
