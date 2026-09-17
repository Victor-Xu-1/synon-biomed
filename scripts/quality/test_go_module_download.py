import subprocess
import unittest
from unittest import mock

import go_module_download


class GoModuleDownloadTests(unittest.TestCase):
    def test_retries_a_transient_failure_and_succeeds(self):
        results = [
            subprocess.CompletedProcess([], 1, b"", b"signed-url-must-not-leak"),
            subprocess.CompletedProcess([], 0, b"", b""),
        ]
        with mock.patch.object(subprocess, "run", side_effect=results) as runner, mock.patch.object(
            go_module_download.time, "sleep"
        ) as sleeper:
            self.assertEqual(go_module_download.download(attempts=3, timeout_seconds=60), 0)
        self.assertEqual(runner.call_count, 2)
        sleeper.assert_called_once_with(1)

    def test_stops_after_the_bounded_attempt_count(self):
        failure = subprocess.CompletedProcess([], 1, b"", b"signed-url-must-not-leak")
        with mock.patch.object(subprocess, "run", return_value=failure) as runner, mock.patch.object(
            go_module_download.time, "sleep"
        ) as sleeper:
            self.assertEqual(go_module_download.download(attempts=3, timeout_seconds=60), 1)
        self.assertEqual(runner.call_count, 3)
        self.assertEqual(sleeper.call_args_list, [mock.call(1), mock.call(2)])

    def test_treats_timeout_as_a_retryable_transport_failure(self):
        timeout = subprocess.TimeoutExpired(["go", "mod", "download"], 60)
        success = subprocess.CompletedProcess([], 0, b"", b"")
        with mock.patch.object(subprocess, "run", side_effect=[timeout, success]), mock.patch.object(
            go_module_download.time, "sleep"
        ):
            self.assertEqual(go_module_download.download(attempts=2, timeout_seconds=60), 0)


if __name__ == "__main__":
    unittest.main()
