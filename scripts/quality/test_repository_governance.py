import pathlib
import tempfile
import unittest

from . import repository_governance as gate


ROOT = pathlib.Path(__file__).parents[2]


class RepositoryGovernanceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.repo = pathlib.Path(temporary.name)
        for relative in gate.REQUIRED_FILES:
            target = self.repo / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text((ROOT / relative).read_text(encoding="utf-8"), encoding="utf-8")
        (self.repo / "README.md").write_text("[Contributing](CONTRIBUTING.md)\n", encoding="utf-8")

    def test_current_repository_passes(self):
        self.assertEqual(gate.validate(ROOT), [])

    def test_each_missing_surface_fails(self):
        for relative in gate.REQUIRED_FILES:
            with self.subTest(path=relative):
                target = self.repo / relative
                original = target.read_text(encoding="utf-8")
                target.unlink()
                self.assertIn(f"missing required governance file: {relative}", gate.validate(self.repo))
                target.write_text(original, encoding="utf-8")

    def test_links_must_resolve_inside_source(self):
        for target, error in (("absent.md", "target is missing"), ("../outside.md", "escapes repository")):
            with self.subTest(target=target):
                (self.repo / "README.md").write_text(f"[link]({target})\n", encoding="utf-8")
                self.assertTrue(any(error in failure for failure in gate.validate(self.repo)))

    def test_symlink_does_not_substitute_required_document(self):
        target = self.repo / "SECURITY.md"
        target.unlink()
        try:
            target.symlink_to("CONTRIBUTING.md")
        except OSError as error:
            self.skipTest(f"symlinks are unavailable: {error}")
        self.assertIn("missing required governance file: SECURITY.md", gate.validate(self.repo))

    def test_completed_provenance_can_replace_historical_warning(self):
        (self.repo / "docs/THIRD_PARTY.md").write_text(
            "# Third-party inventory\nUpdated source and license evidence.\n", encoding="utf-8"
        )
        self.assertEqual(gate.validate(self.repo), [])


if __name__ == "__main__":
    unittest.main()
