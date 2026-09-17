"""Keep verification-only declarations narrow and fail closed on test failures."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.quality import pr_fast_scope as scope
from scripts.quality import verification_scope as contract


class VerificationOwnershipTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="synon-verification-scope-")
        self.addCleanup(self.directory.cleanup)
        self.repo = Path(self.directory.name)
        self.contract_path = self.repo / contract.CONTRACT
        self.contract_path.parent.mkdir(parents=True)
        self.value = {"schema": "synon.verification-scope.v1", "groups": [{
            "name": "fixture-checker", "paths": ["scripts/check.py", contract.CONTRACT], "frontend": True,
            "checks": [["python3", "-B", "scripts/test_check.py"]],
        }]}
        self.save()

    def save(self):
        self.contract_path.write_text(json.dumps(self.value), encoding="utf-8")

    def test_exact_match_drives_checks_and_frontend(self):
        groups = contract.matched(self.repo, ["scripts/check.py"])
        self.assertEqual(contract.checks(groups + groups), [["python3", "-B", "scripts/test_check.py"]])
        self.assertTrue(scope.frontend_affected(["scripts/check.py"], self.repo))
        for path in ["scripts/check.py.new", "scripts/nested/check.py", "scripts/other.py"]:
            self.assertEqual(contract.matched(self.repo, [path]), [])
            self.assertFalse(scope.frontend_affected([path], self.repo))

    def test_runtime_ownership_wins_and_unknown_inputs_still_expand(self):
        (self.repo / "go.mod").write_text("module example/verification\n\ngo 1.22\n")
        (self.repo / "scripts/check.py").write_text("# embedded runtime input\n")
        (self.repo / "root.go").write_text(
            'package verification\nimport _ "embed"\n//go:embed scripts/check.py\nvar Script string\n')
        (self.repo / "app").mkdir()
        (self.repo / "app/app.go").write_text('package app\nimport _ "example/verification"\n')
        entries = scope.packages(self.repo)
        expected = ["example/verification", "example/verification/app"]
        self.assertEqual(scope.select_packages(self.repo, ["scripts/check.py"], entries)[0], expected)
        self.value["groups"][0]["paths"] = ["scripts/non-runtime.py", contract.CONTRACT]
        self.save()
        self.assertEqual(scope.select_packages(self.repo, ["scripts/non-runtime.py"], entries)[0], [])
        for changed in [["scripts/unknown.py"], ["scripts/non-runtime.py", "assets/runtime.py"],
                        ["scripts/non-runtime.py", "root.go"], ["go.mod"]]:
            self.assertEqual(scope.select_packages(self.repo, changed, entries)[0], expected)

    def test_invalid_paths_cannot_claim_runtime_or_wildcard_ownership(self):
        for path in ["internal/runner.py", "../scripts/check.py", "scripts/../root.py",
                     "scripts/*.py", "scripts/check.go", "scripts/go.mod", "scripts//check.py",
                     ".github/workflows/*.yml", "docs/governance/../runtime.json"]:
            with self.subTest(path=path):
                self.value["groups"][0]["paths"] = [path]
                self.save()
                with self.assertRaises(ValueError):
                    contract.load(self.repo)

    def test_workflow_ownership_runs_mandatory_checks_without_runtime_exemptions(self):
        group = self.value["groups"][0]
        group["paths"] += [".github/workflows/quality.yml", "docs/governance/actions.json"]
        group["checks"] = [["bash", "scripts/check.sh"]]
        self.save()
        entries = [{"ImportPath": "fixture", "Dir": str(self.repo)}]
        self.assertEqual(scope.select_packages(self.repo, [".github/workflows/quality.yml"], entries)[0], [])
        self.assertEqual(scope.select_packages(self.repo, [".github/workflows/unknown.yml"], entries)[0], ["fixture"])
        self.assertEqual(contract.checks(contract.matched(self.repo, ["docs/governance/actions.json"])),
                         [["bash", "scripts/check.sh"]])

    def test_empty_checks_duplicate_ownership_and_malformed_contract_are_rejected(self):
        group = self.value["groups"][0]
        group["checks"] = []
        self.save()
        with self.assertRaises(ValueError):
            contract.load(self.repo)
        group["checks"] = [["python3", "-B", "scripts/test_check.py"]]
        group["paths"] *= 2
        self.save()
        with self.assertRaises(ValueError):
            contract.load(self.repo)
        self.contract_path.write_text("{invalid")
        with self.assertRaises(ValueError):
            contract.load(self.repo)

    def test_required_checker_failure_blocks_scope_execution(self):
        args = ["pr_fast_scope.py", "--repo", str(self.repo), "--base", "a" * 40, "--head", "b" * 40]
        with patch.object(sys, "argv", args), patch.object(scope, "changed_paths", return_value=["scripts/check.py"]), \
                patch.object(scope, "run", return_value=subprocess.CompletedProcess([], 17)) as invoke, \
                patch.object(scope, "packages") as discover:
            self.assertEqual(scope.main(), 17)
        discover.assert_not_called()
        invoke.assert_called_once_with(self.repo, "python3", "-B", "scripts/test_check.py")

    def test_missing_contract_has_no_exemptions(self):
        self.contract_path.unlink()
        self.assertEqual(contract.load(self.repo), [])

    def test_existing_contract_cannot_remove_its_own_verification_group(self):
        for groups in [[dict(self.value["groups"][0], paths=["scripts/check.py"])], []]:
            with self.subTest(groups=groups):
                self.value["groups"] = groups
                self.save()
                with self.assertRaisesRegex(ValueError, "own verification"):
                    contract.load(self.repo)

    def test_repository_contract_contains_real_check_targets(self):
        repo = Path(__file__).resolve().parents[2]
        groups = contract.load(repo)
        self.assertTrue(groups)
        for group in groups:
            for path in group["paths"]:
                self.assertTrue((repo / path).is_file(), path)
            for command in group["checks"]:
                if "-m" in command:
                    for module in command[command.index("unittest") + 1:]:
                        self.assertTrue((repo / (module.replace(".", "/") + ".py")).is_file())
                else:
                    self.assertTrue((repo / command[-1]).is_file())


if __name__ == "__main__":
    unittest.main()
