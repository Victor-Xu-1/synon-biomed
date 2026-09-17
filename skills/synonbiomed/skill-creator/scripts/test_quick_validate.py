import tempfile
import unittest
from pathlib import Path

from quick_validate import validate_skill


class QuickValidateTests(unittest.TestCase):
    def validate_frontmatter(self, frontmatter: str) -> tuple[bool, str]:
        with tempfile.TemporaryDirectory(prefix="synon-skill-validator-") as directory:
            root = Path(directory)
            (root / "SKILL.md").write_text(
                "---\n"
                "name: scientific-skill\n"
                "description: Validate a governed scientific capability.\n"
                f"{frontmatter}"
                "---\n"
                "# Scientific skill\n",
                encoding="utf-8",
            )
            return validate_skill(root)

    def test_accepts_runtime_supported_required_capabilities(self) -> None:
        valid, message = self.validate_frontmatter(
            "required-capabilities:\n"
            "  - molecular-docking\n"
            "  - binding-affinity\n"
        )
        self.assertTrue(valid, message)

    def test_rejects_invalid_required_capability_contracts(self) -> None:
        cases = {
            "empty": "required-capabilities: []\n",
            "scalar": "required-capabilities: molecular-docking\n",
            "uppercase": "required-capabilities:\n  - Molecular-Docking\n",
            "leading-digit": "required-capabilities:\n  - 2d-docking\n",
            "double-hyphen": "required-capabilities:\n  - molecular--docking\n",
            "duplicate": "required-capabilities:\n  - molecular-docking\n  - molecular-docking\n",
            "non-string": "required-capabilities:\n  - 42\n",
        }
        for name, frontmatter in cases.items():
            with self.subTest(name=name):
                valid, message = self.validate_frontmatter(frontmatter)
                self.assertFalse(valid, message)

    def test_accepts_runtime_keywords_and_critical_constraints(self) -> None:
        valid, message = self.validate_frontmatter(
            "keywords:\n"
            "  - therapeutic drug monitoring\n"
            "  - 治疗药物监测\n"
            "critical-constraints:\n"
            "  - Keep observed and modelled values separate.\n"
        )
        self.assertTrue(valid, message)

    def test_rejects_invalid_runtime_critical_constraints(self) -> None:
        for frontmatter in (
            "critical-constraints: []\n",
            "critical-constraints:\n  - 42\n",
            "critical-constraints:\n  - ''\n",
        ):
            with self.subTest(frontmatter=frontmatter):
                valid, message = self.validate_frontmatter(frontmatter)
                self.assertFalse(valid, message)


if __name__ == "__main__":
    unittest.main(verbosity=2)
