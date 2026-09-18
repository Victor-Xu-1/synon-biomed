"""Regression contracts for published-release container inputs."""

import copy
import io
import tarfile
import tempfile
import unittest
from pathlib import Path

from scripts.packaging import release_container as packaging


class ReleaseContainerTest(unittest.TestCase):
    def setUp(self):
        self.identity = {"machine_slug": "synon-biomed", "version": "0.1.1"}
        self.revision = "a" * 40
        self.event = {
            "action": "published",
            "repository": {"full_name": packaging.REPOSITORY, "id": packaging.REPOSITORY_ID},
            "release": {"tag_name": "v0.1.1", "draft": False, "prerelease": False, "immutable": True},
        }

    def test_only_published_immutable_version_is_accepted(self):
        self.assertEqual(packaging.validate_event(self.event, self.identity), "v0.1.1")
        for path, value in [
            (("action",), "edited"),
            (("repository", "id"), 1),
            (("repository", "full_name"), "someone/else"),
            (("release", "draft"), True),
            (("release", "immutable"), False),
            (("release", "tag_name"), "v0.1.2"),
            (("release", "tag_name"), "v0.1.1; touch /tmp/unwanted"),
        ]:
            with self.subTest(path=path, value=value):
                event = copy.deepcopy(self.event)
                target = event
                for key in path[:-1]:
                    target = target[key]
                target[path[-1]] = value
                with self.assertRaises(ValueError):
                    packaging.validate_event(event, self.identity)

    def test_full_quality_must_match_revision_attempt_and_repository(self):
        manifest = {"source_commit": self.revision, "candidate_run_id": 10, "candidate_run_attempt": 2}
        run = {
            "id": 10, "run_attempt": 2, "head_sha": self.revision, "head_branch": "main",
            "status": "completed", "conclusion": "success",
            "path": ".github/workflows/quality.yml",
            "repository": {"id": packaging.REPOSITORY_ID},
        }
        packaging.validate_run(run, manifest)
        for key, value in [
            ("conclusion", "failure"), ("status", "in_progress"), ("head_sha", "b" * 40),
            ("run_attempt", 1), ("head_branch", "task"), ("path", ".github/workflows/quality-pr.yml"),
            ("repository", {"id": 1}), ("id", 11),
        ]:
            with self.subTest(key=key):
                bad = {**run, key: value}
                with self.assertRaises(ValueError):
                    packaging.validate_run(bad, manifest)

    def test_archive_rejects_traversal_links_and_special_files(self):
        for name, kind in [
            ("../escape", tarfile.REGTYPE), ("/absolute", tarfile.REGTYPE),
            ("synon-biomed-v0.1.1-linux-amd64/../../escape", tarfile.REGTYPE),
            ("other/file", tarfile.REGTYPE),
            ("synon-biomed-v0.1.1-linux-amd64/link", tarfile.SYMTYPE),
            ("synon-biomed-v0.1.1-linux-amd64/hard", tarfile.LNKTYPE),
            ("synon-biomed-v0.1.1-linux-amd64/fifo", tarfile.FIFOTYPE),
        ]:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                archive = Path(temp) / "test.tar.gz"
                with tarfile.open(archive, "w:gz") as tar:
                    entry = tarfile.TarInfo(name)
                    entry.type = kind
                    entry.linkname = "/etc/passwd" if kind in (tarfile.SYMTYPE, tarfile.LNKTYPE) else ""
                    tar.addfile(entry)
                with self.assertRaises(ValueError):
                    packaging.extract_archive(archive, Path(temp) / "output", self.identity)

    def test_real_archive_extraction_keeps_binary_and_modes(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            archive = root / "test.tar.gz"
            with tarfile.open(archive, "w:gz") as tar:
                entry = tarfile.TarInfo("synon-biomed-v0.1.1-linux-amd64/synon-go")
                entry.size, entry.mode = 5, 0o755
                tar.addfile(entry, io.BytesIO(b"hello"))
            result = packaging.extract_archive(archive, root / "output", self.identity)
            self.assertEqual((result / "synon-go").read_bytes(), b"hello")
            self.assertEqual((result / "synon-go").stat().st_mode & 0o777, 0o755)


if __name__ == "__main__":
    unittest.main()
