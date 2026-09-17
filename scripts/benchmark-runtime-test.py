#!/usr/bin/env python3
import importlib.util
import pathlib
import sys
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("benchmark-runtime.py")
SPEC = importlib.util.spec_from_file_location("benchmark_runtime", MODULE_PATH)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
sys.modules[SPEC.name] = MODULE
SPEC.loader.exec_module(MODULE)


class BenchmarkRuntimeTest(unittest.TestCase):
    def test_percentiles_and_summary_are_deterministic(self):
        self.assertEqual(MODULE.percentile([5, 1, 4, 2, 3], 0.95), 5)
        self.assertEqual(MODULE.summarize([1, 2, 3, 4])["median"], 2.5)

    def test_improvement_handles_lower_and_higher_is_better(self):
        self.assertEqual(MODULE.improvement(50, 100), 50)
        self.assertEqual(MODULE.improvement(150, 100, False), 50)
        self.assertEqual(MODULE.improvement(1, 0), 0)

    def test_clean_environment_does_not_inherit_credentials(self):
        env = MODULE.clean_environment(pathlib.Path("/tmp/isolated"))
        self.assertNotIn("OPENAI_API_KEY", env)
        self.assertNotIn("HTTP_PROXY", env)
        self.assertEqual(env["HOME"], "/tmp/isolated")

    def test_git_dirty_reports_real_repository_state(self):
        self.assertIsInstance(MODULE.git_dirty(MODULE_PATH.parent.parent), bool)


if __name__ == "__main__":
    unittest.main()
