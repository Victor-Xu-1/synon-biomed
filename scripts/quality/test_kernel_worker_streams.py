"""Exercise capture shutdown against real pipes and controlled poll scheduling."""
from pathlib import Path
import os
import subprocess
import sys
import textwrap
import unittest


@unittest.skipUnless(os.name == "posix", "stream capture requires POSIX descriptors")
class WorkerStreamShutdownTests(unittest.TestCase):
    def test_shutdown_after_empty_poll_drains_already_written_output(self):
        root = Path(__file__).resolve().parents[2] / "assets/optional/kernels"
        code = textwrap.dedent('''
            import os
            import sys
            import threading
            from unittest.mock import patch
            from synon_biomed_runtime import worker_streams

            for descriptor in (1, 2):
                entered = threading.Event()
                resume = threading.Event()
                real_select = worker_streams.select.select
                events = []

                class Transport:
                    def send(self, frame):
                        events.append(frame)

                def delayed_empty_poll(*args):
                    if not entered.is_set():
                        entered.set()
                        assert resume.wait(2), "test did not release the completed poll"
                        return [], [], []
                    return real_select(*args)

                with patch.object(worker_streams.select, "select", delayed_empty_poll):
                    capture = worker_streams.StreamCapture(descriptor, Transport(), "shutdown-cell")
                    assert entered.wait(2), "drain did not reach its poll"
                    payload = "final-output-汉字\\n"
                    os.write(descriptor, payload.encode())
                    result = []
                    failure = []

                    def finish():
                        try:
                            result.append(capture.finish())
                        except BaseException as error:
                            failure.append(error)

                    closer = threading.Thread(target=finish)
                    closer.start()
                    assert capture.done.wait(2), "capture did not close its writer"
                    resume.set()
                    closer.join(3)
                    assert not closer.is_alive(), "capture shutdown hung"
                    assert not failure, failure
                    assert result == [payload], (descriptor, result)
                    if descriptor == 1:
                        assert ''.join(event['data'] for event in events) == payload, events
            print("REAL_PIPE_FINAL_OUTPUT_PRESERVED")
        ''')
        result = subprocess.run([sys.executable, "-B", "-c", code], cwd=root,
                                text=True, capture_output=True, timeout=12)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn("REAL_PIPE_FINAL_OUTPUT_PRESERVED", result.stdout)


if __name__ == "__main__":
    unittest.main()
