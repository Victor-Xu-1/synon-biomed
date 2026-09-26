"""Real Git/Go regressions for pull-request test scope and checkout depth."""

from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

from scripts.quality import pr_fast_scope as scope
from scripts.quality.runtime_test_inventory import execution_plan


def git(repo, *arguments):
    return subprocess.check_output(["git", "-C", str(repo), "-c", "user.name=scope",
                                   "-c", "user.email=scope@example.invalid", *arguments], text=True).strip()


class PackageScopeTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix="synon-pr-go-scope-")
        cls.repo = Path(cls.directory.name)
        files = {
            "go.mod": "module example/scope\n\ngo 1.22\n",
            "root.go": 'package scope\nimport _ "embed"\n//go:embed product-identity.json\nvar identity string\nfunc Root() int { return 1 }\n',
            "product-identity.json": "{}\n",
            "scripts/helper.go": "package scripts\nfunc Helper() {}\n",
            "lib/lib.go": "package lib\nfunc Value() int { return 1 }\n",
            "app/app.go": 'package app\nimport ("example/scope"; "example/scope/lib")\nfunc Run() int { return scope.Root()+lib.Value() }\n',
            "integration/integration_test.go": 'package integration\nimport ("testing"; "example/scope/app")\nfunc TestRun(t *testing.T) { if app.Run()!=2 { t.Fatal("result") } }\n',
            "external/external.go": "package external\n",
            "external/external_test.go": 'package external_test\nimport ("testing"; "example/scope/lib")\nfunc TestValue(t *testing.T) { if lib.Value()!=1 { t.Fatal("result") } }\n',
        }
        for name, content in files.items():
            path = cls.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content)
        cls.entries = scope.packages(cls.repo)
        cls.all_packages = sorted(entry["ImportPath"] for entry in cls.entries)

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def selected(self, *paths):
        return scope.select_packages(self.repo, list(paths), self.entries)[0]

    def test_root_package_and_production_consumers_are_included(self):
        self.assertEqual(self.selected("root.go"), ["example/scope", "example/scope/app", "example/scope/integration"])
        self.assertEqual(self.selected("product-identity.json"), self.selected("root.go"))

    def test_test_only_and_external_test_imports_are_included_transitively(self):
        self.assertEqual(self.selected("lib/lib.go"), ["example/scope/app", "example/scope/external",
                                                     "example/scope/integration", "example/scope/lib"])
        self.assertEqual(self.selected("lib/testdata/input.json"), self.selected("lib/lib.go"))

    def test_unknown_runtime_inputs_and_dependency_changes_broaden_instead_of_skip(self):
        for path in ["assets/optional/mcp-servers/tool.py", "skills/worker/SKILL.md",
                     ".github/workflows/quality.yml", "scripts/quality/new.py", "go.mod", "go.sum", "Makefile", ".env.example",
                     "removed/package/file.go"]:
            with self.subTest(path=path):
                self.assertEqual(self.selected(path), self.all_packages)

    def test_documentation_and_frontend_scopes_are_explicit(self):
        self.assertEqual(self.selected("docs/guide.md", "README.md", ".github/pull_request_template.md"), [])
        self.assertEqual(self.selected("frontend/src/App.tsx"), [])
        self.assertFalse(scope.frontend_affected(["docs/guide.md"]))
        self.assertTrue(scope.frontend_affected(["frontend/src/App.tsx"]))
        self.assertTrue(scope.frontend_affected(["product-identity.json"]))
        self.assertEqual(self.selected("frontend/go.mod"), self.all_packages)

    def test_traversal_is_rejected(self):
        for path in ["../outside.go", "/outside.go"]:
            with self.assertRaises(ValueError):
                self.selected(path)

    def test_targeted_plan_uses_existing_complete_batched_executor(self):
        names = [f"TestCase{index:03d}" for index in range(130)]
        inventory = {"example/scope": names}
        planned = execution_plan("pr", 0, False, inventory, inventory, batch_size=64)
        self.assertEqual(len(planned["commands"]), 3)
        self.assertEqual(planned["selected"], inventory)
        for command in planned["commands"]:
            self.assertIn("-count=1", command)
            self.assertIn("-timeout=30m", command)

    def test_vet_uses_the_same_affected_dependency_closure_without_test_evidence(self):
        repo = self.repo.resolve()
        argv = [
            "pr_fast_scope.py", "--repo", str(repo), "--base", "a" * 40,
            "--head", "b" * 40, "--vet",
        ]
        with patch.object(sys, "argv", argv), patch.object(
            scope, "changed_paths", return_value=["lib/lib.go"],
        ), patch.object(scope, "packages", return_value=self.entries), patch.object(
            scope, "run", return_value=subprocess.CompletedProcess([], 0),
        ) as invoke:
            self.assertEqual(scope.main(), 0)
        self.assertEqual(
            invoke.call_args.args,
            (
                repo,
                "go",
                "vet",
                "-buildvcs=false",
                "example/scope/app",
                "example/scope/external",
                "example/scope/integration",
                "example/scope/lib",
            ),
        )


class RevisionTests(unittest.TestCase):
    def test_depth_two_merge_checkout_supports_exact_base_to_merge_diff(self):
        with tempfile.TemporaryDirectory(prefix="synon-pr-revisions-") as directory:
            repo = Path(directory) / "source"
            repo.mkdir()
            git(repo, "init", "-q", "-b", "main")
            (repo / "old.txt").write_text("rename me\n")
            git(repo, "add", ".")
            git(repo, "commit", "-qm", "initial")
            git(repo, "checkout", "-qb", "task")
            (repo / "frontend").mkdir()
            (repo / "frontend/App.tsx").write_text("export {}\n")
            git(repo, "mv", "old.txt", "new.txt")
            git(repo, "add", ".")
            git(repo, "commit", "-qm", "candidate")
            git(repo, "checkout", "-q", "main")
            (repo / "main.txt").write_text("advanced base\n")
            git(repo, "add", ".")
            git(repo, "commit", "-qm", "advanced")
            base = git(repo, "rev-parse", "HEAD")
            git(repo, "merge", "--no-ff", "-qm", "candidate merge", "task")
            head = git(repo, "rev-parse", "HEAD")
            clone = Path(directory) / "shallow"
            subprocess.run(["git", "clone", "-q", "--depth=2", repo.as_uri(), str(clone)], check=True)
            self.assertEqual(git(clone, "rev-parse", "--is-shallow-repository"), "true")
            self.assertEqual(scope.changed_paths(clone, base, head), ["frontend/App.tsx", "new.txt", "old.txt"])

    def test_unresolved_or_mutable_revision_is_rejected(self):
        with self.assertRaises(ValueError):
            scope.changed_paths(Path.cwd(), "main", "HEAD")

    def test_zero_push_base_selects_every_tracked_path(self):
        with tempfile.TemporaryDirectory(prefix="synon-main-zero-base-") as directory:
            repo = Path(directory)
            git(repo, "init", "-q", "-b", "main")
            (repo / "go.mod").write_text("module example/zero\n\ngo 1.22\n")
            (repo / "main.go").write_text("package zero\n")
            git(repo, "add", ".")
            git(repo, "commit", "-qm", "initial")
            head = git(repo, "rev-parse", "HEAD")
            self.assertEqual(
                scope.changed_paths(repo, "0" * 40, head),
                ["go.mod", "main.go"],
            )


class FrontendProvenanceGateTests(unittest.TestCase):
    def test_stale_frontend_provenance_fails_before_install_or_build(self):
        repo = Path.cwd().resolve()
        argv = ['pr_fast_scope.py', '--repo', str(repo), '--base', 'a' * 40,
                '--head', 'b' * 40, '--frontend']
        with patch.object(sys, 'argv', argv), patch.object(scope, 'changed_paths', return_value=['frontend/App.tsx']), patch.object(
            scope, 'run', return_value=subprocess.CompletedProcess([], 23),
        ) as invoke:
            self.assertEqual(scope.main(), 23)
        self.assertEqual(invoke.call_count, 1)
        self.assertEqual(invoke.call_args.args, (
            repo, sys.executable, 'scripts/audit/audit_frontend_migration.py', '--root', str(repo), '--check', '--require-clean',
        ))

    def test_verified_frontend_keeps_every_existing_test_and_build_step(self):
        argv = ['pr_fast_scope.py', '--base', 'a' * 40, '--head', 'b' * 40, '--frontend']
        with patch.object(sys, 'argv', argv), patch.object(scope, 'changed_paths', return_value=['frontend/App.tsx']), patch.object(
            scope, 'run', return_value=subprocess.CompletedProcess([], 0),
        ) as invoke:
            self.assertEqual(scope.main(), 0)
        commands = [call.args[1:] for call in invoke.call_args_list]
        self.assertEqual(len(commands), 10)
        self.assertIn('audit_frontend_migration.py', commands[0][1])
        self.assertEqual(commands[1:], [
            ('npm', 'ci', '--ignore-scripts'),
            ('npx', '--no-install', 'playwright', 'install', 'chromium'),
            ('npm', 'run', 'i18n:types'),
            ('npm', 'run', 'typecheck'), ('npm', 'run', 'lint'),
            ('npm', 'run', 'format:check'), ('npm', 'run', 'test'),
            ('npm', 'run', 'build'), ('npm', 'run', 'test:packaged'),
        ])


if __name__ == "__main__":
    unittest.main()
