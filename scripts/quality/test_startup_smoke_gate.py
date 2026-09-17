import pathlib
import sys
import tempfile
import unittest

import startup_smoke_gate


SERVER = r"""
import http.server
import os

runtime_dir = os.environ['XDG_RUNTIME_DIR']
if not os.path.isdir(runtime_dir):
    raise SystemExit('missing isolated runtime directory')

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != '/health':
            self.send_error(404)
            return
        body = b'{"status":"healthy","service":"gateway","name":"synon","version":"5.0.0-test","agent_catalog_ready":true,"agents_registered":14}\n'
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *_):
        pass

http.server.ThreadingHTTPServer(('127.0.0.1', int(os.environ['SYNON_PORT'])), Handler).serve_forever()
"""


class StartupSmokeGateTests(unittest.TestCase):
    def test_real_process_health_and_shutdown(self):
        result = startup_smoke_gate.probe([sys.executable, "-c", SERVER], timeout_seconds=10)
        self.assertEqual(result["result"], "PASS")
        self.assertEqual(result["health_status"], 200)
        self.assertGreater(result["health_body_bytes"], 0)
        self.assertRegex(result["health_body_sha256"], r"^[0-9a-f]{64}$")
        self.assertRegex(result["stderr_sha256"], r"^[0-9a-f]{64}$")

    def test_large_output_is_drained_without_deadlock_or_disclosure(self):
        noisy = "import sys; sys.stdout.write('x' * 2000000); sys.stdout.flush(); " + SERVER
        result = startup_smoke_gate.probe([sys.executable, "-c", noisy], timeout_seconds=10)
        self.assertEqual(result["stdout_bytes"], 2000000)
        self.assertNotIn("stdout", str(result).replace("stdout_bytes", "").replace("stdout_sha256", ""))

    def test_early_exit_fails_closed(self):
        with self.assertRaisesRegex(startup_smoke_gate.StartupSmokeError, "process_exited"):
            startup_smoke_gate.probe([sys.executable, "-c", "raise SystemExit(3)"], timeout_seconds=2)

    def test_unrelated_http_200_does_not_satisfy_synon_health(self):
        unrelated = SERVER.replace(
            "{\"status\":\"healthy\",\"service\":\"gateway\",\"name\":\"synon\",\"version\":\"5.0.0-test\",\"agent_catalog_ready\":true,\"agents_registered\":14}",
            "{\"ok\":true}",
        )
        with self.assertRaisesRegex(startup_smoke_gate.StartupSmokeError, "health_contract_invalid"):
            startup_smoke_gate.probe([sys.executable, "-c", unrelated], timeout_seconds=5)

    def test_main_rejects_escape_and_missing_binary(self):
        with tempfile.TemporaryDirectory() as directory:
            previous = pathlib.Path.cwd()
            try:
                import os

                os.chdir(directory)
                self.assertEqual(startup_smoke_gate.main(["--binary", "../escape"]), 2)
                self.assertEqual(startup_smoke_gate.main(["--binary", "missing"]), 2)
            finally:
                os.chdir(previous)


if __name__ == "__main__":
    unittest.main()
