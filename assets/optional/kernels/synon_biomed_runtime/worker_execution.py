"""A persistent Python namespace with explicit preflight and execution phases."""
import builtins
import linecache
import os
import signal
import sys
import time
import traceback

import synon_host_bridge
from .matplotlib_runtime import configure_matplotlib_runtime
from .worker_compile import prepare
from .worker_streams import CellStreams
from .worker_transport import Transport
from .worker_safety import harden_process
from .worker_reads import ExecutionReadWitness


class Worker:
    def __init__(self, transport):
        self.transport = transport
        self.namespace = {"__name__": "__main__", "__builtins__": builtins}
        self.index = 0
        self.executing = False
        self.source_names = []
        self.execution_reads = ExecutionReadWitness()
        signal.signal(signal.SIGINT, self.interrupt)

    def interrupt(self, _signum, _frame):
        # The manager retains pre-ack cancellation and resends it after the
        # execution acknowledgement, or terminates a stuck preflight process.
        if self.executing:
            raise KeyboardInterrupt("Interrupted")

    def execute(self, request):
        cell_id, source = request.get("id"), request.get("code")
        if not isinstance(cell_id, str) or not cell_id or not isinstance(source, str):
            raise ValueError("kernel execution requires an id and Python source")
        result = {"id": cell_id, "stdout": "", "stderr": "", "error": None,
                  "interrupted": False, "preflight": None,
                  "trace": {"error_lineno": None, "error_call": None}, "usage": {}}
        self.index += 1
        filename = "<synon-cell-" + str(self.index) + ">"
        linecache.cache[filename] = (len(source), None, source.splitlines(True), filename)
        self.source_names.append(filename)
        if len(self.source_names) > 64:
            linecache.cache.pop(self.source_names.pop(0), None)
        started, cpu = time.monotonic(), time.process_time()
        streams = CellStreams(self.transport, cell_id)
        try:
            if request.get("host_enabled") is True:
                synon_host_bridge.bind_cell(cell_id, request.get("fresh") is True,
                                            self.namespace, self.transport.identity_changed)
            else:
                synon_host_bridge.disable_cell(self.namespace)
            if request.get("working_dir"):
                os.chdir(request["working_dir"])
            compiled, result["preflight"] = prepare(source, self.namespace, filename)
            if compiled is not None:
                self.execution_reads.begin(request.get("workspace_dir"), request.get("working_dir"))
                self.executing = True
                self.transport.send({"type": "execution_started", "id": cell_id})
                exec(compiled, self.namespace, self.namespace)
        except BaseException as error:
            result["error"] = "".join(traceback.format_exception(type(error), error, error.__traceback__))[-1024 * 1024:]
            result["interrupted"] = isinstance(error, KeyboardInterrupt)
            frames = [frame for frame in traceback.extract_tb(error.__traceback__) if frame.filename == filename]
            if frames:
                result["trace"] = {"error_lineno": frames[-1].lineno, "error_call": frames[-1].line}
        finally:
            self.executing = False
            result["trace"]["execution_reads"] = self.execution_reads.finish()
            synon_host_bridge.finish_cell()
            result["stdout"], result["stderr"] = streams.finish()
            result["usage"] = {"wall_s": time.monotonic() - started, "cpu_s": time.process_time() - cpu}
            try:
                import resource
                peak = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss
                result["usage"]["peak_rss_kb"] = peak / 1024 if sys.platform == "darwin" else peak
            except ImportError:
                result["usage"]["peak_rss_kb"] = None
        self.transport.send(result)


def run():
    harden_process()
    transport = Transport()
    worker = Worker(transport)
    try:
        configure_matplotlib_runtime()
        while True:
            request = transport.receive()
            if request is None:
                break
            if request.get("type") in ("host_ack", "host_result"):
                continue
            worker.execute(request)
    finally:
        transport.close()
