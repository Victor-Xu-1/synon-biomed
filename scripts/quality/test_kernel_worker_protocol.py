"""Exercise the shipped worker through real pipes, not a mocked interpreter."""
import json
import importlib.util
import os
from pathlib import Path
import queue
import signal
import subprocess
import sys
import tempfile
import threading
import unittest

WORKER = Path(__file__).resolve().parents[2] / "assets/optional/kernels/kernel_worker.py"


@unittest.skipUnless(os.name == "posix", "the worker requires POSIX descriptors and signals")
class WorkerProtocolTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory(prefix="synon-worker-test-")
        self.process = subprocess.Popen(
            [sys.executable, "-u", str(WORKER)], cwd=self.directory.name,
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            text=True, encoding="utf-8", start_new_session=True,
            env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1", MPLBACKEND="Agg"),
        )
        self.frames = queue.Queue()
        self.reader = threading.Thread(target=self.read_frames, daemon=True)
        self.reader.start()
        self.index = 0

    def read_frames(self):
        try:
            for line in self.process.stdout:
                try:
                    self.frames.put(json.loads(line))
                except ValueError:
                    self.frames.put({"invalid_protocol": line})
        finally:
            self.frames.put({"eof": True})

    def tearDown(self):
        if self.process.poll() is None:
            self.process.terminate()
            try:
                self.process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=3)
        for stream in (self.process.stdin, self.process.stdout, self.process.stderr):
            stream.close()
        self.reader.join(timeout=1)
        self.directory.cleanup()

    def start_cell(self, code, **options):
        self.index += 1
        cell_id = "protocol-cell-" + str(self.index)
        request = dict(id=cell_id, code=code, origin="user", tool_name="python",
                       workspace_dir=self.directory.name, host_enabled=False, **options)
        self.process.stdin.write(json.dumps(request) + "\n")
        self.process.stdin.flush()
        return cell_id

    def terminal(self, cell_id):
        events = []
        while True:
            frame = self.frames.get(timeout=12)
            self.assertNotIn("invalid_protocol", frame)
            self.assertNotIn("eof", frame)
            if frame.get("type") != "stdout_chunk" or "id" in frame:
                self.assertEqual(frame.get("id"), cell_id)
            if "type" not in frame:
                return frame, events
            events.append(frame)

    def cell(self, code, **options):
        return self.terminal(self.start_cell(code, **options))[0]

    def test_persistent_namespace_and_expression(self):
        self.assertFalse(self.cell("retained = 40")["error"])
        result = self.cell("print(retained + 2)")
        self.assertEqual(result["stdout"].strip(), "42")
        self.assertFalse(result["error"])

    def test_python_native_and_child_output_are_not_protocol(self):
        result = self.cell(
            "import os, subprocess, sys\n"
            "print('python-output', flush=True)\n"
            "os.write(1, b'native-output\\n')\n"
            "os.write(2, b'native-error\\n')\n"
            "subprocess.run([sys.executable, '-c', \"print('child-output')\"], check=True)\n"
            "print('last-output')"
        )
        for marker in ("python-output", "native-output", "child-output", "last-output"):
            self.assertIn(marker, result["stdout"])
        self.assertIn("native-error", result["stderr"])
        self.assertFalse(result["error"])

    def test_syntax_rejection_cannot_execute_prefix(self):
        result = self.cell("open('must-not-exist', 'w').write('bad')\nif")
        self.assertFalse(result["preflight"]["executed"])
        self.assertFalse(Path(self.directory.name, "must-not-exist").exists())
        self.assertFalse(self.cell("print('healthy')")["error"])

    def test_runtime_error_preserves_state(self):
        result = self.cell("retained = 9\nraise ValueError('synthetic-error')")
        self.assertIn("synthetic-error", result["error"])
        self.assertEqual(result["trace"]["error_lineno"], 2)
        self.assertEqual(self.cell("print(retained)")["stdout"].strip(), "9")

    def test_guest_stdin_cannot_consume_protocol(self):
        result = self.cell("import sys\nprint(repr(sys.stdin.read()))")
        self.assertEqual(result["stdout"].strip(), "''")
        self.assertEqual(self.cell("print(7)")["stdout"].strip(), "7")

    def test_future_import_compiles_expression_consistently(self):
        result = self.cell(
            "from __future__ import annotations\n"
            "def f(value: NotImportedYet):\n    return value\n"
            "print(f.__annotations__)"
        )
        self.assertFalse(result["error"])
        self.assertIn("NotImportedYet", result["stdout"])

    def test_interrupt_preserves_worker_and_prior_namespace(self):
        self.cell("retained = 37")
        cell_id = self.start_cell("import time\nwhile True:\n    time.sleep(0.02)")
        while self.frames.get(timeout=12).get("type") != "execution_started":
            pass
        self.process.send_signal(signal.SIGINT)
        result, _ = self.terminal(cell_id)
        self.assertTrue(result["interrupted"])
        self.assertEqual(self.cell("print(retained)")["stdout"].strip(), "37")

    def test_working_directory_persists(self):
        self.cell("import os\nos.mkdir('nested')\nos.chdir('nested')")
        self.assertEqual(self.cell("print(os.path.basename(os.getcwd()))")["stdout"].strip(), "nested")

    def test_unicode_output_is_valid_and_complete(self):
        result = self.cell("print('汉字é' * 12000)")
        self.assertEqual(result["stdout"], "汉字é" * 12000 + "\n")

    def test_system_exit_is_a_cell_error_not_a_worker_exit(self):
        self.assertIn("SystemExit", self.cell("raise SystemExit(5)")["error"])
        self.assertEqual(self.cell("print(6)")["stdout"].strip(), "6")

    @unittest.skipUnless(Path("/proc/self/fd").is_dir(), "Linux descriptor accounting is unavailable")
    def test_repeated_cells_release_capture_descriptors(self):
        before = int(self.cell("import os\nprint(len(os.listdir('/proc/self/fd')))")["stdout"].strip())
        for _ in range(25):
            self.assertEqual(self.cell("print(17)")["stdout"].strip(), "17")
        after = int(self.cell("print(len(os.listdir('/proc/self/fd')))")["stdout"].strip())
        self.assertEqual(after, before)

    @unittest.skipUnless(importlib.util.find_spec("matplotlib"), "optional plotting runtime is unavailable")
    def test_real_scientific_plot_is_written(self):
        result = self.cell(
            "import matplotlib.pyplot as plt\nimport numpy as np\n"
            "plt.rcdefaults()\nvalues = np.array([1.0, 2.0, 3.0])\n"
            "figure, axis = plt.subplots()\naxis.plot(values)\n"
            "figure.savefig('scientific-plot.png')\nplt.close(figure)\n"
            "print(float(values.mean()))"
        )
        self.assertFalse(result["error"])
        self.assertEqual(result["stdout"].strip(), "2.0")
        raw = Path(self.directory.name, "scientific-plot.png").read_bytes()
        self.assertTrue(raw.startswith(b"\x89PNG\r\n\x1a\n"))
        self.assertGreater(len(raw), 1000)

    def test_large_stream_interrupt_retains_terminal_output(self):
        cell_id = self.start_cell(
            "import os, signal\n"
            "print('A' * 10486784 + 'TAIL', flush=True)\n"
            "os.kill(os.getpid(), signal.SIGINT)"
        )
        while True:
            frame = self.frames.get(timeout=12)
            self.assertNotIn("eof", frame)
            if frame.get("type") == "stdout_chunk" and "live stream truncated" in frame.get("data", ""):
                break
        result, _ = self.terminal(cell_id)
        self.assertTrue(result["interrupted"])
        self.assertTrue(result["stdout"].endswith("TAIL\n"))


if __name__ == "__main__":
    unittest.main()
