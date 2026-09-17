"""Focused license inventory tests using actual temporary workspace files."""

import importlib.util
import hashlib
import json
from pathlib import Path
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("audit_frontend_migration.py")
SPEC = importlib.util.spec_from_file_location("frontend_license_report", SCRIPT)
audit = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(audit)


class LicenseInventoryTest(unittest.TestCase):
    def test_bundled_notices_are_bound_to_the_current_lockfile(self):
        root = SCRIPT.parents[2]
        lock = (root / "frontend/package-lock.json").read_bytes()
        notices = (root / "docs/licenses/frontend-bundle/NOTICE.txt").read_text()
        self.assertIn("Package lock SHA-256: " + hashlib.sha256(lock).hexdigest(), notices)
        self.assertIn("@rdkit/rdkit@", notices)
        self.assertIn("Permission is hereby granted", notices)
        self.assertIn("Redistribution and use in source and binary forms", notices)
        self.assertIn("Apache License", notices)

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.frontend = Path(self.temp.name)
        self.package = {"name": "test-app", "workspaces": ["packages/*"]}
        self.target = {"name": "test-renderer", "version": "1.0.0"}
        (self.frontend / "packages/renderer").mkdir(parents=True)
        (self.frontend / "packages/renderer/package.json").write_text(json.dumps(self.target))
        self.lock = {"packages": {
            "packages/renderer": dict(self.target),
            "node_modules/test-renderer": {"link": True, "resolved": "packages/renderer"},
            "node_modules/retained-library": {"version": "2.0.0", "license": "MIT"},
        }}

    def report(self):
        (self.frontend / "package-lock.json").write_text(json.dumps(self.lock))
        return audit.license_report(self.frontend, self.package, self.lock)

    def test_local_workspace_is_distinct_from_third_party_dependencies(self):
        report = self.report()
        self.assertEqual(report["summary"]["records"], 1)
        self.assertEqual(report["summary"]["unresolvedTransitiveRecords"], 0)
        self.assertEqual(report["summary"]["workspaceLinkRecords"], 1)
        self.assertEqual(report["workspaceLinks"], [{
            "name": "test-renderer", "version": "1.0.0", "path": "packages/renderer",
            "basis": "local workspace package; source licensing reviewed separately",
        }])
        self.assertEqual(report["records"][0]["license"], "MIT")

    def test_real_unlicensed_dependency_is_not_suppressed(self):
        self.lock["packages"]["node_modules/unknown"] = {"version": "1.0.0"}
        report = self.report()
        self.assertEqual(report["summary"]["unresolvedTransitiveRecords"], 1)

    def test_unlicensed_direct_dependency_still_fails(self):
        self.package["dependencies"] = {"unknown": "1.0.0"}
        self.lock["packages"]["node_modules/unknown"] = {"version": "1.0.0"}
        with self.assertRaisesRegex(RuntimeError, "direct dependency licenses unresolved"):
            self.report()

    def test_link_cannot_escape_workspace(self):
        self.lock["packages"]["node_modules/test-renderer"]["resolved"] = "packages/../../outside"
        with self.assertRaisesRegex(RuntimeError, "invalid local workspace link"):
            self.report()

    def test_link_requires_matching_lock_identity(self):
        self.lock["packages"]["packages/renderer"]["name"] = "other"
        with self.assertRaisesRegex(RuntimeError, "invalid local workspace link"):
            self.report()

    def test_link_requires_existing_local_identity(self):
        (self.frontend / "packages/renderer/package.json").write_text('{"name":"other"}')
        with self.assertRaisesRegex(RuntimeError, "workspace package identity mismatch"):
            self.report()

    def test_missing_target_is_not_silently_excluded(self):
        (self.frontend / "packages/renderer/package.json").unlink()
        with self.assertRaises(FileNotFoundError):
            self.report()

    def test_symlink_outside_workspace_is_rejected(self):
        with tempfile.TemporaryDirectory() as external:
            (Path(external) / "package.json").write_text(json.dumps(self.target))
            (self.frontend / "packages/renderer/package.json").unlink()
            (self.frontend / "packages/renderer").rmdir()
            (self.frontend / "packages/renderer").symlink_to(external, target_is_directory=True)
            with self.assertRaisesRegex(RuntimeError, "invalid local workspace link"):
                self.report()


if __name__ == "__main__":
    unittest.main()
