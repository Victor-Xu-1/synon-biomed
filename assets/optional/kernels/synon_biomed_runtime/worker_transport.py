"""The single JSON-line transport shared by the cell loop and host adapter."""
import io
import json
import os
import sys
import threading

MAX_FRAME_BYTES = 48 * 1024 * 1024


class Transport:
    def __init__(self):
        self.input = io.TextIOWrapper(os.fdopen(os.dup(0), "rb", buffering=0), encoding="utf-8")
        # A raw FileIO write may be short after SIGINT. BufferedWriter retries
        # the remainder, so a large terminal frame cannot lose its newline.
        self.owned_output = io.TextIOWrapper(
            os.fdopen(os.dup(1), "wb"), encoding="utf-8", write_through=True
        )
        output_wrapper = getattr(sys, "_operon_protocol_stdout_wrap", None)
        self.output = output_wrapper(self.owned_output) if callable(output_wrapper) else self.owned_output
        self.lock = threading.RLock()
        self.identity = os.fstat(self.input.fileno())
        self.encode = json.dumps
        self.decode = json.loads
        sys._operon_protocol_stdin = self.input
        sys._operon_protocol_stdout = self.output
        sys._operon_protocol_lock = self.lock
        # Guest stdin cannot steal a request. Native writes between cells cannot
        # inject protocol frames; each cell temporarily receives capture pipes.
        with open(os.devnull, "r+b", buffering=0) as quiet:
            for descriptor in (0, 1, 2):
                os.dup2(quiet.fileno(), descriptor)

    def identity_changed(self, stream):
        if stream is not self.input or stream.closed:
            return True
        try:
            current = os.fstat(stream.fileno())
            return (current.st_dev, current.st_ino) != (self.identity.st_dev, self.identity.st_ino)
        except (OSError, ValueError):
            return True

    def receive(self):
        provider_read = getattr(sys, "_operon_protocol_readline", None)
        line = provider_read() if callable(provider_read) else self.input.readline(MAX_FRAME_BYTES + 1)
        if not line:
            return None
        if not line.endswith("\n") or len(line.encode("utf-8")) > MAX_FRAME_BYTES:
            raise ValueError("kernel request exceeds the frame boundary")
        request = self.decode(line)
        if not isinstance(request, dict):
            raise ValueError("kernel request must be an object")
        return request

    def send(self, value):
        line = self.encode(value, ensure_ascii=True, allow_nan=False)
        if len(line) > MAX_FRAME_BYTES - 1:
            raise ValueError("kernel response exceeds the frame boundary")
        with self.lock:
            self.output.write(line + "\n")
            self.output.flush()

    def close(self):
        self.input.close()
        self.owned_output.close()
