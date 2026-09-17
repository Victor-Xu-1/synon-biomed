import json
import pathlib
import subprocess
import tempfile
import unittest

import dependency_authority


class DependencyAuthorityTest(unittest.TestCase):
    def fixture(self):
        temporary = tempfile.TemporaryDirectory()
        repo = pathlib.Path(temporary.name)
        (repo / "frontend/packages/desktop").mkdir(parents=True)
        (repo / "go.mod").write_text("module example\n", encoding="utf-8")
        (repo / "go.sum").write_text("example v1.0.0 h1:test\n", encoding="utf-8")
        package = {"name": "frontend", "version": "1.0.0", "workspaces": ["packages/desktop"]}
        (repo / "frontend/package.json").write_text(json.dumps(package), encoding="utf-8")
        (repo / "frontend/packages/desktop/package.json").write_text(
            json.dumps({"name": "desktop", "version": "1.0.0"}), encoding="utf-8"
        )
        lock = {"name": "frontend", "version": "1.0.0", "lockfileVersion": 3, "packages": {
            "": {"name": "frontend", "version": "1.0.0"},
            "packages/desktop": {"name": "desktop", "version": "1.0.0"},
        }}
        (repo / "frontend/package-lock.json").write_text(json.dumps(lock), encoding="utf-8")
        subprocess.run(["git", "init", "-q", str(repo)], check=True)
        subprocess.run(["git", "-C", str(repo), "add", "."], check=True)
        spec = {
            "schema": dependency_authority.SCHEMA,
            "go": {"manifest": "go.mod", "lock": "go.sum"},
            "frontend": {
                "root": "frontend", "manager": "npm", "install_argv": ["npm", "ci"],
                "manifest": "frontend/package.json", "lock": "frontend/package-lock.json",
                "lockfile_version": 3,
                "forbidden_paths": ["frontend/pnpm-lock.yaml", "frontend/pnpm-workspace.yaml", "frontend/yarn.lock"],
            },
            "rule": "one authority",
        }
        return temporary, repo, spec

    def test_valid_npm_and_go_authorities(self):
        temporary, repo, spec = self.fixture()
        self.addCleanup(temporary.cleanup)
        result = dependency_authority.check(repo, spec)
        self.assertTrue(result["valid"])
        self.assertEqual(result["workspace_count"], 1)

    def test_rejects_untracked_competing_lock_without_reading_it(self):
        temporary, repo, spec = self.fixture()
        self.addCleanup(temporary.cleanup)
        (repo / "frontend/pnpm-lock.yaml").write_text("secret: must-not-be-read", encoding="utf-8")
        with self.assertRaisesRegex(dependency_authority.DependencyError, "dependency_competing_authority_present"):
            dependency_authority.check(repo, spec)

    def test_rejects_non_npm_package_manager(self):
        temporary, repo, spec = self.fixture()
        self.addCleanup(temporary.cleanup)
        package_path = repo / "frontend/package.json"
        package = json.loads(package_path.read_text(encoding="utf-8"))
        package["packageManager"] = "pnpm@11"
        package_path.write_text(json.dumps(package), encoding="utf-8")
        with self.assertRaisesRegex(dependency_authority.DependencyError, "dependency_package_manager_mismatch"):
            dependency_authority.check(repo, spec)

    def test_rejects_lock_root_or_workspace_drift(self):
        temporary, repo, spec = self.fixture()
        self.addCleanup(temporary.cleanup)
        lock_path = repo / "frontend/package-lock.json"
        lock = json.loads(lock_path.read_text(encoding="utf-8"))
        lock["packages"].pop("packages/desktop")
        lock_path.write_text(json.dumps(lock), encoding="utf-8")
        with self.assertRaisesRegex(dependency_authority.DependencyError, "dependency_workspace_lock_missing"):
            dependency_authority.check(repo, spec)

    def test_rejects_bool_lockfile_version(self):
        temporary, repo, spec = self.fixture()
        self.addCleanup(temporary.cleanup)
        spec["frontend"]["lockfile_version"] = True
        with self.assertRaisesRegex(dependency_authority.DependencyError, "dependency_frontend_spec_invalid"):
            dependency_authority.check(repo, spec)


if __name__ == "__main__":
    unittest.main()
