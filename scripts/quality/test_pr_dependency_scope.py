"""Real local Go modules prove version updates retain affected consumers."""
from pathlib import Path
import subprocess
import tempfile
import unittest

from scripts.quality import pr_dependency_scope as dependency
from scripts.quality import pr_fast_scope as scope


class DependencyScopeTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="synon-module-scope-")
        self.addCleanup(self.temporary.cleanup)
        self.repo = Path(self.temporary.name) / "source"
        self.repo.mkdir()
        for name, body in {
            "target": "package target\nconst Value = 1\n",
            "wrapper": 'package wrapper\nimport _ "example.org/target"\n',
        }.items():
            directory = self.repo.parent / "modules" / name
            directory.mkdir(parents=True)
            (directory / "go.mod").write_text(f"module example.org/{name}\n\ngo 1.22\n")
            (directory / "lib.go").write_text(body)
        self.write("go.mod", """module example.org/product

go 1.22

require (
    example.org/target v0.1.0
    example.org/wrapper v0.1.0
)

replace example.org/target => ../modules/target
replace example.org/wrapper => ../modules/wrapper
""")
        self.write("lib/lib.go", 'package lib\nimport _ "example.org/wrapper"\n')
        self.write("app/app.go", 'package app\nimport _ "example.org/product/lib"\n')
        self.write("onlytest/lib.go", "package onlytest\n")
        self.write("onlytest/lib_test.go", 'package onlytest_test\nimport _ "example.org/wrapper"\n')
        self.write("unrelated/lib.go", "package unrelated\n")
        self.git("init", "-q", "-b", "main")
        self.base = self.commit("baseline")

    def write(self, name, content):
        path = self.repo / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)

    def git(self, *arguments):
        return subprocess.check_output([
            "git", "-C", str(self.repo), "-c", "user.name=scope",
            "-c", "user.email=scope@example.invalid", *arguments,
        ], text=True).strip()

    def commit(self, message):
        self.git("add", ".")
        self.git("commit", "-qm", message)
        return self.git("rev-parse", "HEAD")

    def test_version_change_covers_transitive_and_external_test_imports(self):
        path = self.repo / "go.mod"
        path.write_text(path.read_text().replace("target v0.1.0", "target v0.2.0"))
        head = self.commit("update dependency")
        self.assertEqual(dependency.changed_modules(self.repo, self.base, head), {"example.org/target"})
        selected, paths = scope.selected_packages(self.repo, self.base, head)
        self.assertEqual(paths, ["go.mod"])
        self.assertEqual(selected, ["example.org/product/app", "example.org/product/lib",
                                    "example.org/product/onlytest"])

    def test_checksum_only_change_tracks_its_module(self):
        self.write("go.sum", "example.org/target v0.1.0 h1:before\n")
        base = self.commit("baseline checksum")
        self.write("go.sum", "example.org/target v0.1.0 h1:after\n")
        head = self.commit("changed checksum")
        self.assertEqual(dependency.changed_modules(self.repo, base, head), {"example.org/target"})

    def test_directive_and_replacement_changes_retain_conservative_coverage(self):
        original = (self.repo / "go.mod").read_text()
        for before, after in [("go 1.22", "go 1.23"), ("../modules/target", "../modules/other"),
                              ("module example.org/product", "module example.org/renamed")]:
            with self.subTest(after=after):
                self.write("go.mod", original.replace(before, after))
                head = self.commit("authority update")
                self.assertIsNone(dependency.changed_modules(self.repo, self.base, head))

    def test_zero_base_is_not_interpreted_as_no_changes(self):
        self.assertIsNone(dependency.changed_modules(self.repo, "0" * 40, self.base))

    def test_comment_only_change_selects_no_dependency_consumers(self):
        path = self.repo / "go.mod"
        path.write_text(path.read_text() + "\n// module comment\n")
        head = self.commit("comment")
        self.assertEqual(scope.selected_packages(self.repo, self.base, head)[0], [])

    def test_removed_requirement_is_not_lost(self):
        original = (self.repo / "go.mod").read_text()
        self.write("go.mod", original.replace("    example.org/target v0.1.0\n", ""))
        head = self.commit("remove requirement")
        self.assertEqual(dependency.changed_modules(self.repo, self.base, head), {"example.org/target"})

    def test_module_names_require_component_boundaries(self):
        graph = [
            {"ImportPath": "example.org/target/sub", "Module": {"Path": "example.org/target"}},
            {"ImportPath": "example.org/target-extra", "Module": {"Path": "example.org/target-extra"}},
            {"ImportPath": "product/affected", "Imports": ["example.org/target/sub"]},
            {"ImportPath": "product/untouched", "Imports": ["example.org/target-extra"]},
            {"ImportPath": "product/tests", "TestImports": ["product/affected"]},
        ]
        affected = dependency.consumers(graph, {"example.org/target"})
        self.assertIn("product/affected", affected)
        self.assertIn("product/tests", affected)
        self.assertNotIn("product/untouched", affected)

    def test_invalid_manifest_and_unresolved_revision_fail_closed(self):
        self.write("go.mod", "not a valid module declaration\n")
        head = self.commit("broken manifest")
        with self.assertRaises(RuntimeError):
            dependency.changed_modules(self.repo, self.base, head)
        with self.assertRaises(RuntimeError):
            dependency.changed_modules(self.repo, "f" * 40, head)


if __name__ == "__main__":
    unittest.main()
