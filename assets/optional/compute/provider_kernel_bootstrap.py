"""Start one confined provider SDK kernel behind a framed proxy transport.

The host passes two already-open Unix socketpairs through inherited file
descriptors: one for the credential handshake and one for multiplexed network
traffic. The sandbox cannot create Unix sockets and has no external network;
this bootstrap exposes only a loopback proxy whose peer is the host-side
domain allowlist.
"""

from __future__ import annotations

import os
import runpy
import socket
import struct
import sys
import threading
from pathlib import Path


FRAME_OPEN = 1
FRAME_DATA = 2
FRAME_CLOSE = 3
FRAME_HEADER = struct.Struct("!BII")
MAX_FRAME_BYTES = 64 * 1024
MAX_STREAMS = 32
PROXY_PORT = 1080


class ProxyRelay:
    def __init__(self, transport_fd: int) -> None:
        if transport_fd < 3 or transport_fd > 1024:
            raise RuntimeError("provider proxy descriptor is invalid")
        self._transport = socket.socket(fileno=transport_fd)
        self._transport.setblocking(True)
        self._writer_lock = threading.Lock()
        self._streams_lock = threading.Lock()
        self._streams: dict[int, socket.socket] = {}
        self._next_id = 1

    def start(self) -> None:
        listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        listener.bind(("127.0.0.1", PROXY_PORT))
        listener.listen(MAX_STREAMS)
        threading.Thread(target=self._read_transport, daemon=True).start()
        threading.Thread(target=self._accept, args=(listener,), daemon=True).start()

    def _accept(self, listener: socket.socket) -> None:
        while True:
            connection, _ = listener.accept()
            with self._streams_lock:
                if len(self._streams) >= MAX_STREAMS:
                    connection.close()
                    continue
                stream_id = self._next_id
                self._next_id = 1 if stream_id == 0xFFFFFFFF else stream_id + 1
                while stream_id in self._streams:
                    stream_id = self._next_id
                    self._next_id = 1 if stream_id == 0xFFFFFFFF else stream_id + 1
                self._streams[stream_id] = connection
            self._send(FRAME_OPEN, stream_id)
            threading.Thread(
                target=self._pump_local, args=(stream_id, connection), daemon=True
            ).start()

    def _pump_local(self, stream_id: int, connection: socket.socket) -> None:
        try:
            while True:
                payload = connection.recv(MAX_FRAME_BYTES)
                if not payload:
                    return
                self._send(FRAME_DATA, stream_id, payload)
        finally:
            self._drop(stream_id)
            try:
                self._send(FRAME_CLOSE, stream_id)
            except OSError:
                pass

    def _read_transport(self) -> None:
        try:
            while True:
                header = self._read_exact(FRAME_HEADER.size)
                kind, stream_id, length = FRAME_HEADER.unpack(header)
                if (
                    stream_id == 0
                    or length > MAX_FRAME_BYTES
                    or kind not in (FRAME_DATA, FRAME_CLOSE)
                    or (kind == FRAME_CLOSE and length != 0)
                ):
                    raise RuntimeError("provider proxy frame is invalid")
                payload = self._read_exact(length) if length else b""
                with self._streams_lock:
                    connection = self._streams.get(stream_id)
                if connection is None:
                    continue
                if kind == FRAME_DATA:
                    connection.sendall(payload)
                else:
                    self._drop(stream_id)
        except BaseException:
            try:
                os.write(2, b"synon-provider: proxy transport closed or returned an invalid frame\n")
            except OSError:
                pass
            os._exit(70)

    def _send(self, kind: int, stream_id: int, payload: bytes = b"") -> None:
        if stream_id <= 0 or len(payload) > MAX_FRAME_BYTES:
            raise RuntimeError("provider proxy frame is invalid")
        frame = FRAME_HEADER.pack(kind, stream_id, len(payload)) + payload
        with self._writer_lock:
            self._transport.sendall(frame)

    def _drop(self, stream_id: int) -> None:
        with self._streams_lock:
            connection = self._streams.pop(stream_id, None)
        if connection is not None:
            try:
                connection.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
            connection.close()

    def _read_exact(self, length: int) -> bytes:
        chunks = bytearray()
        while len(chunks) < length:
            chunk = self._transport.recv(length - len(chunks))
            if not chunk:
                raise EOFError("provider proxy transport closed")
            chunks.extend(chunk)
        return bytes(chunks)


def _bounded_fd(name: str) -> int:
    try:
        value = int(os.environ[name])
    except (KeyError, ValueError) as exc:
        raise RuntimeError(f"{name} is unavailable") from exc
    if value < 3 or value > 1024:
        raise RuntimeError(f"{name} is invalid")
    return value


def main() -> None:
    if len(sys.argv) < 4 or sys.argv[1] not in ("repl", "oneshot"):
        raise RuntimeError("provider bootstrap requires mode, entrypoint, and provider")
    mode = sys.argv[1]
    entrypoint = Path(sys.argv[2])
    provider = Path(sys.argv[3])
    rest = sys.argv[4:]
    if mode == "repl" and rest:
        raise RuntimeError("provider repl bootstrap received trailing arguments")
    if mode == "oneshot" and len(rest) != 3:
        raise RuntimeError("provider oneshot bootstrap requires operation, stage, and confinement flag")
    if not entrypoint.is_absolute() or not provider.is_absolute():
        raise RuntimeError("provider bootstrap paths must be absolute")
    if not entrypoint.is_file() or not provider.is_file():
        raise RuntimeError("provider bootstrap path is unavailable")

    ProxyRelay(_bounded_fd("SYNON_PROVIDER_PROXY_FD")).start()
    for key in (
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "ALL_PROXY",
        "http_proxy",
        "https_proxy",
        "all_proxy",
    ):
        os.environ.pop(key, None)
    proxy = f"http://127.0.0.1:{PROXY_PORT}"
    os.environ["HTTP_PROXY"] = proxy
    os.environ["HTTPS_PROXY"] = proxy
    os.environ["http_proxy"] = proxy
    os.environ["https_proxy"] = proxy
    no_proxy = "" if os.environ.get("SYNON_PROVIDER_PROXY_LOCAL_TARGET") == "1" else "localhost,127.0.0.1"
    os.environ["NO_PROXY"] = no_proxy
    os.environ["no_proxy"] = no_proxy
    os.environ["OPERON_BYOC_PROXY_SOCKS"] = str(PROXY_PORT)

    sys.argv = [str(entrypoint), mode, str(provider), *rest]
    runpy.run_path(str(entrypoint), run_name="__main__")


if __name__ == "__main__":
    main()
