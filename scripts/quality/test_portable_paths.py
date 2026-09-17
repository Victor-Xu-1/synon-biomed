#!/usr/bin/env python3

from __future__ import annotations

from pathlib import Path
import importlib.util
import sys
import unittest


MODULE_PATH = Path(__file__).with_name("portable_paths.py")
SPEC = importlib.util.spec_from_file_location("portable_paths", MODULE_PATH)
if SPEC is None or SPEC.loader is None:
    raise RuntimeError("portable_paths module is unavailable")
portable_paths = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = portable_paths
SPEC.loader.exec_module(portable_paths)


class PortablePathsTests(unittest.TestCase):
    def test_accepts_distinct_portable_files_and_shared_directories(self) -> None:
        paths = [
            "internal/server/testdata/alpha.txt",
            "internal/server/testdata/beta.txt",
            "frontend/package.json",
        ]
        self.assertEqual(3, portable_paths.validate_paths(paths))

    def test_rejects_casefolded_file_and_parent_directory_collisions(self) -> None:
        with self.assertRaisesRegex(portable_paths.AuditError, "case-insensitive tracked path collision"):
            portable_paths.validate_paths(["fixtures/Alpha.txt", "fixtures/alpha.txt"])
        with self.assertRaisesRegex(portable_paths.AuditError, "case-insensitive tracked path collision"):
            portable_paths.validate_paths(["Fixture", "fixture/child.txt"])

    def test_rejects_windows_reserved_invalid_and_trailing_names(self) -> None:
        for path in ("fixtures/CON.txt", "fixtures/bad?.txt", "fixtures/trailing. "):
            with self.subTest(path=path):
                with self.assertRaisesRegex(portable_paths.AuditError, "Windows-invalid tracked paths"):
                    portable_paths.validate_paths([path])


if __name__ == "__main__":
    unittest.main()
