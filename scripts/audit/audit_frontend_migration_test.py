from __future__ import annotations

import importlib.util
from pathlib import Path
import unittest


SCRIPT = Path(__file__).with_name("audit_frontend_migration.py")
SPEC = importlib.util.spec_from_file_location("audit_frontend_migration", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(MODULE)


class FrontendMigrationPolicyTests(unittest.TestCase):
    def test_text_publication_coverage_is_an_exact_addition_not_a_directory_grant(self) -> None:
        self.assertIsNotNone(MODULE.addition_reason("packages/desktop/src/common/chat/textPublicationCoverage.ts"))
        self.assertIsNone(MODULE.addition_reason("packages/desktop/src/common/chat/unreviewedPublicationCoverage.ts"))

    def test_addition_rules_are_scoped_and_exact_manifest_is_still_supported(self) -> None:
        self.assertIsNotNone(MODULE.addition_reason("package-lock.json"))
        self.assertIsNotNone(
            MODULE.addition_reason("packages/desktop/src/renderer/services/currentAuthority.ts")
        )
        self.assertIsNotNone(MODULE.addition_reason("tests/unit/currentAuthority.test.ts"))
        self.assertIsNone(MODULE.addition_reason("packages/desktop/src/common/unreviewedAuthority.ts"))
        self.assertIsNone(MODULE.addition_reason("unreviewed-root-file.txt"))

    def test_addition_fingerprint_binds_path_and_content_hash(self) -> None:
        baseline = [{"path": "tests/unit/a.test.ts", "sha256": "a" * 64}]
        changed_path = [{"path": "tests/unit/b.test.ts", "sha256": "a" * 64}]
        changed_content = [{"path": "tests/unit/a.test.ts", "sha256": "b" * 64}]
        self.assertNotEqual(MODULE.addition_fingerprint(baseline), MODULE.addition_fingerprint(changed_path))
        self.assertNotEqual(MODULE.addition_fingerprint(baseline), MODULE.addition_fingerprint(changed_content))


if __name__ == "__main__":
    unittest.main()
