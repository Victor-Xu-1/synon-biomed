"""Runtime assets retain exact consumers and their full import closure."""

import copy
import json
from pathlib import Path
import tempfile
import unittest

from scripts.quality import pr_fast_scope as scope, runtime_input_scope as contract


class RuntimeInputScopeTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="synon-runtime-scope-")
        self.addCleanup(self.directory.cleanup)
        self.repo = Path(self.directory.name)
        self.path = self.repo / contract.CONTRACT
        self.path.parent.mkdir(parents=True)
        self.value = {"schema": "synon.runtime-input-scope.v1", "groups": [{
            "name": "runtime", "paths": ["assets/runtime.py"], "packages": ["example/scope/lib"],
        }]}
        self.save()
        files = {
            "go.mod": "module example/scope\n\ngo 1.22\n",
            "lib/lib.go": "package lib\n",
            "app/app.go": 'package app\nimport _ "example/scope/lib"\n',
            "consumer/consumer_test.go": 'package consumer\nimport _ "example/scope/app"\n',
            "unrelated/other.go": "package unrelated\n",
        }
        for name, content in files.items():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        self.entries = scope.packages(self.repo)
        self.all_packages = sorted(e["ImportPath"] for e in self.entries)

    def save(self):
        self.path.write_text(json.dumps(self.value))

    def selected(self, *paths):
        return scope.select_packages(self.repo, list(paths), self.entries)[0]

    def test_asset_selects_production_and_test_consumers_not_unrelated_packages(self):
        self.assertEqual(self.selected("assets/runtime.py"), [
            "example/scope/app", "example/scope/consumer", "example/scope/lib",
        ])
        self.assertEqual(self.selected("assets/runtime.py", "unrelated/other.go"), self.all_packages)

    def test_unknown_asset_missing_owner_and_missing_contract_remain_conservative(self):
        self.assertEqual(self.selected("assets/runtime.py.new"), self.all_packages)
        self.value["groups"][0]["packages"] = ["example/scope/deleted"]
        self.save()
        self.assertEqual(self.selected("assets/runtime.py"), self.all_packages)
        self.path.unlink()
        self.assertEqual(self.selected("assets/runtime.py"), self.all_packages)

    def test_invalid_authorities_and_duplicate_paths_fail_closed(self):
        valid = copy.deepcopy(self.value)
        for path in ["go.mod", "assets/go.mod", "internal/runtime.go", "assets/runtime.go",
                     "assets/../outside", "assets//runtime", "assets/*", "assets/./runtime"]:
            with self.subTest(path=path):
                self.value = copy.deepcopy(valid)
                self.value["groups"][0]["paths"] = [path]
                self.save()
                with self.assertRaises(ValueError):
                    contract.load(self.repo)
        for key, value in [("packages", []), ("packages", ["../escape"]),
                           ("paths", ["assets/runtime.py", "assets/runtime.py"])]:
            self.value = copy.deepcopy(valid)
            self.value["groups"][0][key] = value
            self.save()
            with self.assertRaises(ValueError):
                contract.load(self.repo)

    def test_repository_assets_have_real_package_owners_and_keep_core_consumers(self):
        repo = Path(__file__).resolve().parents[2]
        owners = contract.load(repo)
        entries = scope.packages(repo)
        names = {entry["ImportPath"] for entry in entries}
        for path, packages in owners.items():
            self.assertTrue((repo / path).is_file(), path)
            self.assertLessEqual(packages, names)
        selected, _ = scope.select_packages(repo, list(owners), entries)
        for package in ["synon-go/internal/assets", "synon-go/internal/kernel",
                        "synon-go/internal/kernel/detached", "synon-go/internal/server",
                        "synon-go/cmd/synon", "synon-go/internal/compat/v11reuse"]:
            self.assertIn(package, selected)
        self.assertLess(len(selected), len(entries))


if __name__ == "__main__":
    unittest.main()
