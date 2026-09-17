"""Complete partitions and real failing Go processes remain observable."""
from pathlib import Path
import tempfile
import unittest

from scripts.quality import pr_test_partition as shard
from scripts.quality.runtime_test_inventory import discover, execution_plan
from scripts.quality.runtime_test_shards import execute


class AffectedPartitionTests(unittest.TestCase):
    def test_matrix_is_bounded_and_zero_scope_retains_one_checker_job(self):
        self.assertEqual(shard.job_matrix([]), {"include": [{"index": 0, "count": 1}]})
        self.assertEqual(len(shard.job_matrix([str(i) for i in range(200)])["include"]), 4)

    def test_inventory_is_complete_disjoint_and_order_independent(self):
        inventory = {"product/heavy": [f"TestCase{i:04d}" for i in range(1001)],
                     "product/small": ["TestSmall"], "product/empty": []}
        observed = []
        empty = 0
        for index in range(4):
            selected = shard.select(inventory, index, 4)
            reversed_inventory = {name: list(reversed(tests)) for name, tests in inventory.items()}
            self.assertEqual(selected, shard.select(reversed_inventory, index, 4))
            observed.extend((name, test) for name, tests in selected.items() for test in tests)
            empty += "product/empty" in selected
        expected = [(name, test) for name, tests in inventory.items() for test in tests]
        self.assertCountEqual(observed, expected)
        self.assertEqual(len(observed), len(set(observed)))
        self.assertEqual(empty, 1)

    def test_bad_partition_and_duplicate_tests_fail(self):
        for index, count in [(-1, 4), (4, 4), (0, 0), (0, 5)]:
            with self.assertRaises(ValueError):
                shard.select({}, index, count)
        with self.assertRaises(ValueError):
            shard.select({"product": ["TestOne", "TestOne"]}, 0, 4)

    def test_real_go_shards_retain_subtests_seeds_examples_and_failure(self):
        with tempfile.TemporaryDirectory(prefix="synon-pr-partition-") as temporary:
            root = Path(temporary)
            repo = root / "source"
            repo.mkdir()
            (repo / "go.mod").write_text("module fixture\n\ngo 1.22\n")
            (repo / "fixture_test.go").write_text('''package fixture
import ("testing"; "fmt")
func TestNested(t *testing.T) { t.Run("child", func(t *testing.T) {}) }
func TestSkipped(t *testing.T) { t.Skip("explicit condition") }
func TestFailure(t *testing.T) { t.Fatal("real failed assertion") }
func FuzzSeed(f *testing.F) { f.Add("seed"); f.Fuzz(func(t *testing.T, s string) {}) }
func Example() { fmt.Println("example"); /* Output: example */ }
''')
            inventory = discover(repo, ["fixture"], False)
            results = []
            for index in range(4):
                selected = shard.select(inventory, index, 4)
                plan = execution_plan("pr", index, False, inventory, selected, batch_size=64)
                results.append(execute(repo, plan, root / f"evidence-{index}"))
            self.assertEqual(results.count(1), 1)
            self.assertEqual(results.count(0), 3)


if __name__ == "__main__":
    unittest.main()
