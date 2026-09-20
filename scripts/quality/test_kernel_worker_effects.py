"""Exercise observation obligations through the shipped native worker protocols."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest

ROOT = Path(__file__).resolve().parents[2]
PYTHON_WORKER = ROOT / 'assets/optional/kernels/kernel_worker.py'
R_WORKER = ROOT / 'assets/optional/kernels/kernel_worker.R'
RSCRIPT = os.environ.get('SYNON_TEST_RSCRIPT') or shutil.which('Rscript')


def observation(language, source):
    parser = ROOT / 'internal/executionprep' / ('python.py' if language == 'python' else 'r.R')
    command = [sys.executable, '-I', str(parser)] if language == 'python' else [RSCRIPT, '--vanilla', str(parser)]
    output = subprocess.run(command, input=source, capture_output=True, text=True, timeout=15, check=True).stdout
    if language == 'python':
        facts = json.loads(output)
    else:
        facts = []
        for line in output.splitlines():
            fields = line.split('\t')
            values = [bytes.fromhex(value).decode('utf-8') for value in fields[1:]]
            facts.append(dict(kind=fields[0], name=values[0], args=values[1:]))
    fact = next(fact for fact in facts if fact['kind'] == 'observation')
    digest = hashlib.sha256(source.encode('utf-8')).hexdigest()
    return dict(observation=dict(schema=fact['name'], language=language, source_sha256=digest, operations=fact['args']),
                observation_code_sha256=digest)


class NativeWorker:
    def __init__(self, language):
        self.directory = tempfile.TemporaryDirectory(prefix='synon-observation-')
        command = [sys.executable, '-u', str(PYTHON_WORKER)] if language == 'python' else [RSCRIPT, '--vanilla', str(R_WORKER)]
        self.process = subprocess.Popen(command, cwd=self.directory.name, stdin=subprocess.PIPE,
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, encoding='utf-8',
            env=dict(os.environ, PYTHONDONTWRITEBYTECODE='1', MPLBACKEND='Agg', OPERON_R_OPLOG_PATH=''))
        self.frames = queue.Queue()
        self.reader = threading.Thread(target=self.read, daemon=True)
        self.reader.start()
        self.index = 0

    def read(self):
        try:
            for line in self.process.stdout:
                try:
                    self.frames.put(json.loads(line))
                except ValueError:
                    self.frames.put({'invalid': line})
        finally:
            self.frames.put({'eof': True})

    def cell(self, code, **fields):
        self.index += 1
        identity = 'observation-' + str(self.index)
        request = dict(id=identity, code=code, origin='agent', host_enabled=False,
                       workspace_dir=self.directory.name, **fields)
        self.process.stdin.write(json.dumps(request) + '\n')
        self.process.stdin.flush()
        events = []
        while True:
            frame = self.frames.get(timeout=15)
            if frame.get('id') != identity:
                raise AssertionError(frame)
            if 'type' not in frame:
                return frame, events
            events.append(frame)

    def close(self):
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


class ObservationWorkerTests(unittest.TestCase):
    def worker(self, language):
        worker = NativeWorker(language)
        self.addCleanup(worker.close)
        return worker

    def assert_declined(self, response):
        result, events = response
        self.assertFalse(any(event['type'] == 'execution_started' for event in events), events)
        self.assertEqual(result['stdout'], '')
        self.assertFalse(result.get('error'), result)
        self.assertEqual(result['preflight']['status'], 'implementation_selection_required')
        self.assertIs(result['preflight']['executed'], False)
        self.assertIs(result['preflight']['decision_required'], True)

    def test_python_observation_reuses_the_actual_namespace(self):
        worker = self.worker('python')
        for source, expected in [
            ('print(1)', '1\n'),
            ("# observation\nprint('quoted $(touch x)', [1, 2], sep='|')\nprint('ready')", 'quoted $(touch x)|[1, 2]\nready\n'),
            ("1\n('constant', 2, True)", ''),
        ]:
            result, events = worker.cell(source, **observation('python', source))
            self.assertFalse(result.get('error'), result)
            self.assertFalse(result.get('preflight'), result)
            self.assertEqual(result['stdout'], expected)
            self.assertTrue(any(event['type'] == 'execution_started' for event in events))
        # Ordinary source retains the same namespace; observation never resets it.
        result, _ = worker.cell("retained = 31; print(retained)")
        self.assertEqual(result['stdout'], '31\n')
        self.assert_declined(worker.cell('print(1)', **observation('python', 'print(1)')))
        result, _ = worker.cell('print(retained)')
        self.assertEqual(result['stdout'], '31\n')

    def test_python_rebinding_in_a_previous_cell_declines(self):
        worker = self.worker('python')
        result, _ = worker.cell("print = lambda *args: __import__('sys').stdout.write('shadow-ran\\n')")
        self.assertFalse(result.get('error'), result)
        self.assert_declined(worker.cell('print(1)', **observation('python', 'print(1)')))

    def test_python_changed_source_and_unknown_operation_decline(self):
        worker = self.worker('python')
        self.assert_declined(worker.cell('print(2)', **observation('python', 'print(1)')))
        proof = observation('python', 'print(1)')
        proof['observation']['operations'] = ['python.unknown']
        self.assert_declined(worker.cell('print(1)', **proof))
        result, _ = worker.cell('print(1)', **observation('python', 'print(1)'))
        self.assertEqual(result['stdout'], '1\n')

    def test_python_runtime_identity_without_invoking_shadow(self):
        path = ROOT / 'assets/optional/kernels/synon_biomed_runtime/worker_effects.py'
        spec = importlib.util.spec_from_file_location('observation_guard_test', path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        invoked = []
        namespace = {'__builtins__': __import__('builtins'), 'print': lambda *args: invoked.append(args)}
        request = dict(code='print(1)', **observation('python', 'print(1)'))
        self.assertEqual(module.ObservationGuard().validate(request, namespace), 'diagnostic_binding_unproved')
        self.assertEqual(invoked, [])

    @unittest.skipUnless(RSCRIPT, 'native Rscript unavailable')
    def test_r_observation_and_previous_binding(self):
        worker = self.worker('r')
        for source in ('getwd()', "# observation\ngetwd()\n'ready'\nSys.getpid()\nSys.info()", "1\n'ready'\nNULL"):
            result, events = worker.cell(source, **observation('r', source))
            self.assertFalse(result.get('preflight'), result)
            self.assertFalse(result.get('error'), result)
            self.assertTrue(any(event['type'] == 'execution_started' for event in events))
        result, _ = worker.cell("getwd <- function() 'shadow-ran'")
        self.assertFalse(result.get('error'), result)
        self.assert_declined(worker.cell('getwd()', **observation('r', 'getwd()')))

    @unittest.skipUnless(RSCRIPT, 'native Rscript unavailable')
    def test_r_active_binding_is_never_evaluated_by_identity_check(self):
        worker = self.worker('r')
        # Simulate a tampered guest-local provenance flag. The host has an
        # independent fence; the native binding check also rejects without
        # invoking the active getter. All test state belongs to this process.
        result, _ = worker.cell("makeActiveBinding('getwd', function() stop('active-getter-ran'), environment()); assign('.observation_tainted', FALSE, envir=.GlobalEnv)")
        self.assertFalse(result.get('error'), result)
        response = worker.cell('getwd()', **observation('r', 'getwd()'))
        self.assert_declined(response)
        self.assertEqual(response[0]['preflight']['reason'], 'diagnostic_binding_unproved')


if __name__ == '__main__':
    unittest.main()
