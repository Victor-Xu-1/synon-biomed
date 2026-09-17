import csv
import importlib.util
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("stability_analysis.py")
SPEC = importlib.util.spec_from_file_location("stability_analysis", SCRIPT)
MODULE = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
sys.modules[SPEC.name] = MODULE
sys.path.insert(0, str(SCRIPT.parent))
SPEC.loader.exec_module(MODULE)


class StabilityAnalysisTest(unittest.TestCase):
    def test_skill_classifies_machine_outputs_as_working_data(self):
        skill_text = (SCRIPT.parent.parent / "SKILL.md").read_text(encoding="utf-8")
        self.assertIn("Save machine-oriented summaries", skill_text)
        self.assertIn("semantic role, not its extension", skill_text)
        self.assertIn("`--summary` output must use a `.json` filename", skill_text)
        self.assertIn("must not appear in the user-facing artifact tray", skill_text)

    def test_summary_rejects_misleading_non_json_extension(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "input.csv"
            source.write_text(
                "batch_id,condition_role,condition,time_month,attribute,value,direction,lower_limit,upper_limit\n"
                "b1,long_term,25C,0,impurity,0.1,increase,,1.0\n"
                "b1,long_term,25C,3,impurity,0.2,increase,,1.0\n",
                encoding="utf-8",
            )
            completed = subprocess.run(
                [
                    sys.executable,
                    str(SCRIPT),
                    "--input",
                    str(source),
                    "--results",
                    str(Path(directory) / "results.csv"),
                    "--summary",
                    str(Path(directory) / "summary.csv"),
                ],
                capture_output=True,
                text=True,
            )
            self.assertNotEqual(completed.returncode, 0)
            self.assertIn("--summary must use a .json filename", completed.stderr)

    def test_single_batch_is_development_only_and_uses_confidence_bound(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "input.csv"
            with source.open("w", newline="", encoding="utf-8") as handle:
                writer = csv.writer(handle)
                writer.writerow(["batch_id", "condition_role", "condition", "time_month", "attribute", "value", "direction", "lower_limit", "upper_limit"])
                for month, impurity, assay in zip([0, 3, 6, 9, 12], [0.10, 0.16, 0.23, 0.32, 0.45], [99.8, 99.6, 99.4, 99.1, 98.8]):
                    writer.writerow(["pilot-1", "long_term", "25C/60RH", month, "total_impurity", impurity, "increase", "", 1.0])
                    writer.writerow(["pilot-1", "long_term", "25C/60RH", month, "assay", assay, "decrease", 95.0, ""])
            observations = MODULE.load_observations(source)
            results = MODULE.analyze(observations)
            summary = Path(directory) / "summary.json"
            report = Path(directory) / "report.md"
            MODULE.write_summary(summary, observations, results)
            MODULE.write_decision_report(report, observations, results, "zh")
            payload = json.loads(summary.read_text(encoding="utf-8"))
            self.assertFalse(payload["formal_shelf_life_supported"])
            self.assertEqual(payload["conclusion_code"], "development_only_insufficient_batches")
            self.assertEqual(payload["long_term_batch_count"], 1)
            self.assertEqual(payload["common_observed_through_month"], 12)
            impurity = next(row for row in results if row["attribute"] == "total_impurity")
            self.assertGreater(impurity["confidence_limit_intersection_month"], 12)
            self.assertLess(impurity["confidence_limit_intersection_month"], 32.1)
            report_text = report.read_text(encoding="utf-8")
            self.assertIn("不能用于给未来批次指定正式或暂定有效期", report_text)
            self.assertIn("12 个月", report_text)
            self.assertIn("## 统计诊断", report_text)
            self.assertIn("增加中间条件考察", report_text)
            self.assertNotIn("## Statistical diagnostics", report_text)
            self.assertNotIn("暂定18个月", report_text)

    def test_rejects_missing_limit_for_increasing_attribute(self):
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "input.csv"
            source.write_text(
                "batch_id,condition_role,condition,time_month,attribute,value,direction,lower_limit,upper_limit\n"
                "b1,long_term,25C,0,impurity,0.1,increase,,\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(ValueError, "require upper_limit"):
                MODULE.load_observations(source)


if __name__ == "__main__":
    unittest.main()
