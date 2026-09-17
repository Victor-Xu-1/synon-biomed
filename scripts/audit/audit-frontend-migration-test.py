#!/usr/bin/env python3
"""Real-filesystem regression tests for the frontend migration boundary."""

from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("audit_frontend_migration.py")
SPEC = importlib.util.spec_from_file_location("audit_frontend_migration", SCRIPT)
assert SPEC is not None and SPEC.loader is not None
audit = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(audit)


class FrontendMigrationAuditTest(unittest.TestCase):
    def test_default_root_is_the_repository_containing_the_script(self) -> None:
        self.assertEqual(audit.parse_args([]).root.resolve(), SCRIPT.resolve().parents[2])

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.frontend = Path(self.temporary.name) / "frontend"
        self.frontend.mkdir()

        self.original_policy = {
            "adaptations": audit.ALLOWED_ADAPTATIONS,
            "adaptation_rules": audit.ALLOWED_ADAPTATION_RULES,
            "additions": audit.ALLOWED_ADDITIONS,
            "removals": audit.ALLOWED_REMOVALS,
            "removal_rules": audit.ALLOWED_REMOVAL_RULES,
            "adaptation_fingerprint": audit.APPROVED_ADAPTATION_FINGERPRINT,
            "addition_fingerprint": audit.APPROVED_ADDITION_FINGERPRINT,
            "removal_fingerprint": audit.APPROVED_REMOVAL_FINGERPRINT,
        }

        source_bytes = b"source version\n"
        removed_bytes = b"obsolete source\n"
        self.source = {
            "schemaVersion": 1,
            "source": "/read-only/reference/frontend",
            "files": [
                self.record("source.ts", source_bytes),
                self.record("removed.ts", removed_bytes),
            ],
        }
        (self.frontend / audit.SOURCE_MANIFEST).write_text(
            json.dumps(self.source),
            encoding="utf-8",
        )
        (self.frontend / "source.ts").write_bytes(b"adapted target\n")
        (self.frontend / "added.ts").write_bytes(b"target addition\n")

        audit.ALLOWED_ADAPTATIONS = {"source.ts": "Test adaptation."}
        audit.ALLOWED_ADAPTATION_RULES = ()
        audit.ALLOWED_ADDITIONS = {"added.ts": "Test addition."}
        audit.ALLOWED_REMOVALS = {"removed.ts": "Test removal."}
        audit.ALLOWED_REMOVAL_RULES = ()
        audit.APPROVED_ADDITION_FINGERPRINT = audit.addition_fingerprint(
            [
                {
                    "path": "added.ts",
                    "sha256": audit.sha256_file(self.frontend / "added.ts"),
                }
            ]
        )
        audit.APPROVED_ADAPTATION_FINGERPRINT = audit.adaptation_fingerprint(
            [
                {
                    "path": "source.ts",
                    "sourceSHA256": self.source["files"][0]["sha256"],
                    "targetSHA256": audit.sha256_file(self.frontend / "source.ts"),
                }
            ]
        )
        audit.APPROVED_REMOVAL_FINGERPRINT = audit.removal_fingerprint(
            [
                {
                    "path": "removed.ts",
                    "sourceSHA256": self.source["files"][1]["sha256"],
                }
            ]
        )

    def tearDown(self) -> None:
        audit.ALLOWED_ADAPTATIONS = self.original_policy["adaptations"]
        audit.ALLOWED_ADAPTATION_RULES = self.original_policy["adaptation_rules"]
        audit.ALLOWED_ADDITIONS = self.original_policy["additions"]
        audit.ALLOWED_REMOVALS = self.original_policy["removals"]
        audit.ALLOWED_REMOVAL_RULES = self.original_policy["removal_rules"]
        audit.APPROVED_ADAPTATION_FINGERPRINT = self.original_policy["adaptation_fingerprint"]
        audit.APPROVED_ADDITION_FINGERPRINT = self.original_policy["addition_fingerprint"]
        audit.APPROVED_REMOVAL_FINGERPRINT = self.original_policy["removal_fingerprint"]
        self.temporary.cleanup()

    def record(self, relative: str, content: bytes) -> dict[str, object]:
        path = self.frontend / relative
        path.write_bytes(content)
        digest = audit.sha256_file(path)
        path.unlink()
        return {
            "path": relative,
            "sha256": digest,
            "bytes": len(content),
            "mode": 0o644,
        }

    def test_records_adaptations_removals_and_additions(self) -> None:
        manifest = audit.migration_manifest(self.frontend, self.source)

        self.assertEqual(
            manifest["summary"],
            {
                "sourceFiles": 2,
                "unchangedSourceFiles": 0,
                "adaptedSourceFiles": 1,
                "removedSourceFiles": 1,
                "addedFiles": 1,
            },
        )
        self.assertEqual([entry["path"] for entry in manifest["adaptations"]], ["source.ts"])
        self.assertEqual([entry["path"] for entry in manifest["removals"]], ["removed.ts"])
        self.assertEqual([entry["path"] for entry in manifest["additions"]], ["added.ts"])

    def test_rejects_unclassified_source_adaptation(self) -> None:
        audit.ALLOWED_ADAPTATIONS = {}
        with self.assertRaisesRegex(RuntimeError, "unclassified migrated-source change"):
            audit.migration_manifest(self.frontend, self.source)

    def test_rejects_adaptation_hash_drift(self) -> None:
        (self.frontend / "source.ts").write_bytes(b"changed again\n")
        with self.assertRaisesRegex(RuntimeError, "adaptation fingerprint mismatch"):
            audit.migration_manifest(self.frontend, self.source)

    def test_rejects_unclassified_frontend_addition(self) -> None:
        (self.frontend / "rogue.ts").write_bytes(b"unreviewed\n")
        with self.assertRaisesRegex(RuntimeError, "unclassified migrated frontend additions"):
            audit.migration_manifest(self.frontend, self.source)

    def test_rejects_case_insensitive_extensionless_module_collision(self) -> None:
        (self.frontend / "Widget.tsx").write_bytes(b"export default null;\n")
        (self.frontend / "widget.ts").write_bytes(b"export const value = 1;\n")

        with self.assertRaisesRegex(RuntimeError, "case-insensitive extensionless module collision"):
            audit.audit_module_stem_collisions(self.frontend)

    def test_allows_distinct_case_insensitive_module_stems(self) -> None:
        (self.frontend / "Widget.tsx").write_bytes(b"export default null;\n")
        (self.frontend / "widgetModel.ts").write_bytes(b"export const value = 1;\n")

        audit.audit_module_stem_collisions(self.frontend)


if __name__ == "__main__":
    unittest.main()
