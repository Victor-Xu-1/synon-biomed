"""Regression tests for complete partitions and real Go process outcomes."""

import contextlib
import io
import json
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

from scripts.quality import runtime_test_shards as shards


class PartitionTests(unittest.TestCase):
    def test_race_has_independent_complete_matrix_and_bounded_process_batches(self):
        self.assertNotEqual(shards.matrix(race=True), shards.matrix())
        names = [f"TestCase{number}" for number in range(19)]
        batches = shards.test_batches(names, race=True)
        self.assertEqual([name for batch in batches for name in batch], sorted(names))
        self.assertLessEqual(max(map(len, batches)), 8)
        self.assertEqual(shards.test_batches(names, race=False), [sorted(names)])

    def test_partition_is_complete_disjoint_balanced_and_order_independent(self):
        names = [f"TestCase{number:04d}" for number in range(2380)] + ["ExampleDisplay", "FuzzSeed"]
        groups = shards.partition(names, 4)
        self.assertEqual(groups, shards.partition(list(reversed(names)), 4))
        flattened = [name for group in groups for name in group]
        self.assertCountEqual(flattened, names)
        self.assertEqual(len(flattened), len(set(flattened)))
        self.assertLessEqual(max(map(len, groups)) - min(map(len, groups)), 1)
        for group in groups:
            pattern = re.compile("^(?:" + "|".join(re.escape(name) for name in group) + ")$")
            self.assertEqual([name for name in sorted(names) if pattern.fullmatch(name)], group)
            self.assertIsNone(pattern.fullmatch(group[0] + "NotSelected"))

    def test_invalid_count_and_duplicate_inventory_fail(self):
        for names, count in [(["TestA"], 0), (["TestA", "TestA"], 1)]:
            with self.assertRaises(ValueError):
                shards.partition(names, count)

    def test_matrix_contains_every_configured_partition_once(self):
        actual = [(entry["group"], entry["index"]) for entry in shards.matrix()["include"]]
        expected = [("core", 0)] + [(group, index) for group, (_, count) in shards.PACKAGE_SHARDS.items()
                                     for index in range(count)]
        self.assertEqual(actual, expected)
        self.assertEqual(len(actual), len(set(actual)))


class GoExecutionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.directory = tempfile.TemporaryDirectory(prefix="synon-shard-regression-")
        cls.repo = Path(cls.directory.name) / "source"
        cls.repo.mkdir()
        cls.repo.joinpath("go.mod").write_text("module fixture\n\ngo 1.22\n")
        cls.core_packages = ["fixture/core", *(f"fixture/core{index}" for index in range(1, 8))]
        for relative in [*(package.removeprefix("fixture/") for package in cls.core_packages),
                         *(path for path, _ in shards.PACKAGE_SHARDS.values())]:
            folder = cls.repo / relative
            folder.mkdir(parents=True)
            source = '''package fixture
import ("testing"; "fmt")
func TestAlpha(t *testing.T) {}
func TestNested(t *testing.T) { t.Run("child", func(t *testing.T) {}) }
func TestGamma(t *testing.T) {}
func TestDelta(t *testing.T) {}
func TestEpsilon(t *testing.T) {}
func TestSkipped(t *testing.T) { t.Skip("explicit platform condition") }
func TestFailure(t *testing.T) { t.Fatal("real failing assertion") }
func FuzzSeed(f *testing.F) { f.Add("seed"); f.Fuzz(func(t *testing.T, s string) {}) }
func Example() { fmt.Println("real example"); /* Output: real example */ }
var batchCount int
'''
            for index in range(16):
                source += f'func TestBatch{index:02d}(t *testing.T) {{ batchCount++; if batchCount > 8 {{ t.Fatal("process batch was not bounded") }} }}\n'
            folder.joinpath("runtime_test.go").write_text(source)
        cls.initial_plan = shards.plan(cls.repo, "server", 0, False)

    @classmethod
    def tearDownClass(cls):
        cls.directory.cleanup()

    def test_go_discovery_includes_tests_examples_and_fuzz_seeds(self):
        plans = [shards.plan(self.repo, "server", index, False) for index in range(4)]
        names = [name for plan in plans for values in plan["selected"].values() for name in values]
        self.assertEqual(len(names), 25)
        self.assertEqual(len(names), len(set(names)))
        self.assertIn("Example", names)
        self.assertIn("FuzzSeed", names)
        for plan in plans:
            self.assertIn("-timeout=30m", plan["commands"][0])
            self.assertIn("-count=1", plan["commands"][0])
            self.assertEqual(plan["inventory_sha256"], self.initial_plan["inventory_sha256"])
        core = shards.plan(self.repo, "core", 0, False)
        self.assertEqual(core["packages"], self.core_packages)

    def test_real_process_retains_nested_tests_examples_fuzz_and_failures(self):
        for names, want in [(["TestNested", "Example", "FuzzSeed", "TestSkipped"], 0),
                            (["TestFailure"], 1)]:
            test_plan = dict(self.initial_plan)
            test_plan["selected"] = {"fixture/internal/server": names}
            test_plan["commands"] = [["go", "test", "-json", "-count=1", "-timeout=1m", "-run",
                                     "^(?:" + "|".join(names) + ")$", "fixture/internal/server"]]
            with tempfile.TemporaryDirectory(prefix="synon-shard-events-") as log_dir:
                with contextlib.redirect_stdout(io.StringIO()):
                    result = shards.execute(self.repo, test_plan, Path(log_dir))
                self.assertEqual(result, want)
                summary = json.loads(Path(log_dir, "summary.json").read_text())
                self.assertEqual(summary["missing_tests"], [])
                self.assertEqual(summary["missing_packages"], [])
                self.assertEqual(summary["executed_top_level"], len(names))
                log = Path(log_dir, "events.jsonl").read_text()
                self.assertIn("TestFailure" if want else "TestNested/child", log)

    def test_missing_inventory_and_in_source_logs_are_errors(self):
        test_plan = dict(self.initial_plan)
        test_plan["selected"] = {"fixture/internal/server": ["TestNonexistent"]}
        test_plan["commands"] = [["go", "test", "-json", "-count=1", "-run", "^TestNonexistent$", "fixture/internal/server"]]
        with self.assertRaises(ValueError):
            shards.execute(self.repo, test_plan, self.repo / "logs")
        with tempfile.TemporaryDirectory(prefix="synon-shard-missing-") as log_dir:
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(shards.execute(self.repo, test_plan, Path(log_dir)), 1)

    def test_race_inventory_matches_regular_inventory_with_finer_partitions(self):
        result = subprocess.run(["go", "env", "CGO_ENABLED"], cwd=self.repo, text=True, capture_output=True, check=True)
        if result.stdout.strip() != "1":
            self.skipTest("Go race requires the platform C toolchain")
        race = shards.plan(self.repo, "server", 0, True)
        self.assertEqual(race["inventory_sha256"], self.initial_plan["inventory_sha256"])
        self.assertIn("-race", race["commands"][0])
        selected = []
        core_packages = []
        for entry in shards.matrix(race=True)["include"]:
            if entry["group"] not in {"server", "core"}:
                continue
            planned = shards.plan(self.repo, entry["group"], entry["index"], True)
            if entry["group"] == "server":
                selected.extend(planned["selected"]["fixture/internal/server"])
            else:
                core_packages.extend(planned["packages"])
        self.assertEqual(len(selected), 25)
        self.assertEqual(len(selected), len(set(selected)))
        self.assertCountEqual(core_packages, self.core_packages)

    def test_real_batches_use_new_processes_and_accumulate_failures(self):
        names = [f"TestBatch{index:02d}" for index in range(16)]
        prefix = ["go", "test", "-json", "-count=1", "-timeout=1m", "-run"]
        def command(batch):
            return [*prefix, "^(?:" + "|".join(batch) + ")$", "fixture/internal/server"]
        unbounded = subprocess.run(command(names), cwd=self.repo, capture_output=True)
        self.assertNotEqual(unbounded.returncode, 0)
        test_plan = dict(self.initial_plan, selected={"fixture/internal/server": names},
                         commands=[command(batch) for batch in shards.test_batches(names, race=True)])
        with tempfile.TemporaryDirectory(prefix="synon-shard-batches-") as log_dir:
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(shards.execute(self.repo, test_plan, Path(log_dir)), 0)
            summary = json.loads(Path(log_dir, "summary.json").read_text())
            self.assertEqual(summary["batch_exit_codes"], [0, 0])
            self.assertEqual(summary["executed_top_level"], 16)
        test_plan.update(selected={"fixture/internal/server": ["TestFailure", "TestNested"]},
                         commands=[command(["TestFailure"]), command(["TestNested"])])
        with tempfile.TemporaryDirectory(prefix="synon-shard-failure-batches-") as log_dir:
            with contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(shards.execute(self.repo, test_plan, Path(log_dir)), 1)
            summary = json.loads(Path(log_dir, "summary.json").read_text())
            self.assertEqual(summary["batch_exit_codes"], [1, 0])
            self.assertEqual(summary["packages"]["fixture/internal/server"], "fail")
            self.assertEqual(summary["missing_tests"], [])

    def test_duplicate_and_equal_names_across_packages_cannot_hide_gaps(self):
        command = ["go", "test", "-json", "-count=1", "-run", "^TestAlpha$", "fixture/internal/server"]
        cases = [dict(self.initial_plan, selected={"fixture/internal/server": ["TestAlpha"]},
                      commands=[command, command]),
                 dict(self.initial_plan, selected={"fixture/internal/server": ["TestAlpha"],
                                                   "fixture/core": ["TestAlpha"]}, commands=[command])]
        for test_plan in cases:
            with tempfile.TemporaryDirectory(prefix="synon-shard-identity-") as log_dir:
                with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(shards.execute(self.repo, test_plan, Path(log_dir)), 1)


if __name__ == "__main__":
    unittest.main()
