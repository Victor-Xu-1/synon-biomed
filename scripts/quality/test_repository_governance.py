import pathlib
import unittest

from . import repository_governance


ROOT = pathlib.Path(__file__).parents[2]


class RepositoryGovernanceTests(unittest.TestCase):
    def test_public_governance_surfaces_are_complete(self):
        self.assertEqual(repository_governance.validate(ROOT), [])

    def test_required_files_are_explicit_and_stable(self):
        self.assertEqual(
            repository_governance.REQUIRED_FILES,
            (
                "CONTRIBUTING.md",
                "SECURITY.md",
                "CODE_OF_CONDUCT.md",
                ".github/CODEOWNERS",
                ".github/ISSUE_TEMPLATE/bug_report.yml",
                ".github/ISSUE_TEMPLATE/feature_request.yml",
                ".github/ISSUE_TEMPLATE/config.yml",
                "docs/governance/repository-maintenance.md",
            ),
        )


if __name__ == "__main__":
    unittest.main()
