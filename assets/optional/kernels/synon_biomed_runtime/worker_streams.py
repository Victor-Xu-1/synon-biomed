"""Capture Python, native-library and subprocess output without protocol mixing."""
import codecs
import io
import json
import os
import select
import sys
import threading

# Each stream reserves at most 16 MiB of JSON string payload. Both streams,
# traceback and envelope therefore fit the host's 48 MiB terminal frame.
MAX_OUTPUT_JSON_BYTES = 16 * 1024 * 1024
TRUNCATION_NOTICE = "\n[output truncated]\n"
MAX_LIVE_BYTES = 10 * 1024 * 1024
LIVE_NOTICE = "\n…(live stream truncated at 10 MB; full output in tool_result)\n"


class StreamCapture:
    def __init__(self, descriptor, transport, cell_id):
        self.descriptor = descriptor
        self.transport = transport
        self.cell_id = cell_id
        self.parts = []
        self.length = 0
        self.truncated = False
        self.live_bytes = 0
        self.live_closed = False
        self.failure = None
        self.done = threading.Event()
        self.saved = os.dup(descriptor)
        self.reader, writer = os.pipe()
        # Windows pipes do not expose os.set_blocking or select readiness.
        # The dedicated reader thread can block until the writer closes.
        if os.name != "nt":
            os.set_blocking(self.reader, False)
        os.dup2(writer, descriptor)
        os.close(writer)
        self.text = io.TextIOWrapper(
            os.fdopen(os.dup(descriptor), "wb", buffering=0),
            encoding="utf-8", errors="backslashreplace", write_through=True,
        )
        self.thread = threading.Thread(target=self.drain, daemon=True)
        self.thread.start()

    def retain(self, text):
        if self.descriptor == 1 and text:
            self.publish_live(text)
        remaining = max(0, MAX_OUTPUT_JSON_BYTES - self.length)
        size = len(json.dumps(text, ensure_ascii=True)) - 2
        kept = text
        if size > remaining:
            lower, upper = 0, len(text)
            while lower < upper:
                middle = (lower + upper + 1) // 2
                if len(json.dumps(text[:middle], ensure_ascii=True)) - 2 <= remaining:
                    lower = middle
                else:
                    upper = middle - 1
            kept = text[:lower]
            size = len(json.dumps(kept, ensure_ascii=True)) - 2
        self.truncated |= len(kept) < len(text)
        if not kept:
            return
        self.parts.append(kept)
        self.length += size

    def publish_live(self, text):
        if self.live_closed:
            return
        # Keep the existing wire presentation boundary distinct from the larger
        # terminal payload. The manager remains the sole execution identity owner.
        budget = MAX_LIVE_BYTES - len(LIVE_NOTICE.encode("utf-8")) - self.live_bytes
        raw = text.encode("utf-8")
        prefix = raw[:max(0, budget)].decode("utf-8", errors="ignore")
        if prefix:
            self.transport.send({"type": "stdout_chunk", "id": self.cell_id, "data": prefix})
            self.live_bytes += len(prefix.encode("utf-8"))
        if len(raw) > budget:
            self.transport.send({"type": "stdout_chunk", "id": self.cell_id, "data": LIVE_NOTICE})
            self.live_closed = True

    def drain(self):
        decoder = codecs.getincrementaldecoder("utf-8")(errors="replace")
        try:
            while True:
                # Observe writer shutdown before the read, not after a poll
                # timeout. A writer can publish its final bytes and close while
                # this thread is descheduled after an empty poll. Shutdown is
                # complete only after a subsequent nonblocking read is empty.
                finishing = self.done.is_set()
                if os.name != "nt" and not finishing:
                    ready, _, _ = select.select([self.reader], [], [], 0.05)
                    if not ready:
                        continue
                try:
                    block = os.read(self.reader, 16 * 1024)
                except BlockingIOError:
                    if finishing:
                        break
                    continue
                if not block:
                    break
                self.retain(decoder.decode(block))
            self.retain(decoder.decode(b"", final=True))
        except BaseException as error:
            self.failure = error
        finally:
            os.close(self.reader)

    def finish(self):
        if not self.text.closed:
            self.text.close()
        os.dup2(self.saved, self.descriptor)
        os.close(self.saved)
        self.done.set()
        self.thread.join(timeout=2)
        if self.thread.is_alive():
            raise RuntimeError("kernel output drain did not settle")
        if self.failure is not None:
            raise RuntimeError("kernel output transport failed") from self.failure
        return "".join(self.parts) + (TRUNCATION_NOTICE if self.truncated else "")


class CellStreams:
    def __init__(self, transport, cell_id):
        self.previous = sys.stdin, sys.stdout, sys.stderr
        self.stdout = StreamCapture(1, transport, cell_id)
        try:
            self.stderr = StreamCapture(2, transport, cell_id)
        except BaseException:
            self.stdout.finish()
            raise
        self.stdin = open(os.devnull, encoding="utf-8")
        sys.stdin, sys.stdout, sys.stderr = self.stdin, self.stdout.text, self.stderr.text

    def finish(self):
        try:
            try:
                output = self.stdout.finish()
            finally:
                errors = self.stderr.finish()
            return output, errors
        finally:
            self.stdin.close()
            sys.stdin, sys.stdout, sys.stderr = self.previous
