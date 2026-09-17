import os
import subprocess
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import python_test_dependencies


class PythonTestDependenciesTests(unittest.TestCase):
    def test_installs_into_isolated_home_user_site(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home = root / "home"
            home.mkdir()
            requirements = root / python_test_dependencies.REQUIREMENTS
            requirements.parent.mkdir(parents=True)
            requirements.write_text("example==1.0\n", encoding="utf-8")
            success = subprocess.CompletedProcess([], 0, b"", b"")
            with mock.patch.dict(os.environ, {"HOME": str(home)}), mock.patch.object(
                Path, "cwd", return_value=root
            ), mock.patch.object(subprocess, "run", return_value=success) as runner:
                self.assertEqual(
                    python_test_dependencies.install(home=home, attempts=3, timeout_seconds=60),
                    0,
                )
            argv = runner.call_args.args[0]
            self.assertIn("--break-system-packages", argv)
            self.assertIn("--user", argv)
            self.assertIn(str(requirements), argv)

    def test_retries_without_exposing_captured_transport_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home = root / "home"
            home.mkdir()
            requirements = root / python_test_dependencies.REQUIREMENTS
            requirements.parent.mkdir(parents=True)
            requirements.write_text("example==1.0\n", encoding="utf-8")
            failure = subprocess.CompletedProcess([], 1, b"", b"signed-url-must-not-leak")
            success = subprocess.CompletedProcess([], 0, b"", b"")
            with mock.patch.dict(os.environ, {"HOME": str(home)}), mock.patch.object(
                Path, "cwd", return_value=root
            ), mock.patch.object(
                subprocess, "run", side_effect=[failure, success]
            ), mock.patch.object(python_test_dependencies.time, "sleep") as sleeper:
                self.assertEqual(
                    python_test_dependencies.install(home=home, attempts=3, timeout_seconds=60),
                    0,
                )
            sleeper.assert_called_once_with(1)

    def test_rejects_relative_existing_or_nonisolated_home(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            home = root / "home"
            home.mkdir()
            with mock.patch.dict(os.environ, {"HOME": str(root / "different")}):
                self.assertEqual(
                    python_test_dependencies.install(home=home, attempts=1, timeout_seconds=60),
                    2,
                )
        self.assertEqual(
            python_test_dependencies.install(home=Path("relative"), attempts=1, timeout_seconds=60),
            2,
        )


if __name__ == "__main__":
    unittest.main()
