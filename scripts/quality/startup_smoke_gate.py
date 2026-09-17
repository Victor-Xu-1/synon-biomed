#!/usr/bin/env python3
"""Start an isolated Synon binary, probe /health, and stop it deterministically."""

from __future__ import annotations

import argparse
import hashlib
import http.client
import json
import os
import pathlib
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from typing import Any


class StartupSmokeError(Exception):
    """Controlled startup smoke failure."""


class _DigestSink:
    def __init__(self, stream: Any) -> None:
        self.stream = stream
        self.bytes = 0
        self.digest = hashlib.sha256()
        self.thread = threading.Thread(target=self._consume, daemon=True)

    def _consume(self) -> None:
        for chunk in iter(lambda: self.stream.read(64 * 1024), b""):
            self.bytes += len(chunk)
            self.digest.update(chunk)

    def start(self) -> None:
        self.thread.start()

    def finish(self) -> None:
        self.thread.join(timeout=10)
        try:
            if self.thread.is_alive():
                raise StartupSmokeError("startup_output_drain_failed")
        finally:
            self.stream.close()

    def hexdigest(self) -> str:
        return self.digest.hexdigest()


def _port() -> int:
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return int(probe.getsockname()[1])


def _stop(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    try:
        if os.name == "nt":
            process.terminate()
        else:
            os.killpg(process.pid, signal.SIGTERM)
        process.wait(timeout=10)
    except (OSError, subprocess.TimeoutExpired):
        if os.name == "nt":
            process.kill()
        else:
            os.killpg(process.pid, signal.SIGKILL)
        process.wait(timeout=10)


def probe(command: list[str], *, timeout_seconds: int = 30) -> dict[str, Any]:
    if not command or any(type(item) is not str or not item for item in command):
        raise StartupSmokeError("startup_command_invalid")
    port = _port()
    # Detached kernel sockets must stay below the Unix-domain path budget.
    # Keep the private acceptance root deliberately short.
    runtime_parent = "/tmp" if os.name != "nt" and pathlib.Path("/tmp").is_dir() else None
    with (
        tempfile.TemporaryDirectory(prefix="sbs-") as directory,
        tempfile.TemporaryDirectory(prefix="sr-", dir=runtime_parent) as runtime_directory,
    ):
        root = pathlib.Path(directory)
        for name in ("home", "tmp", "cache", "state", "data", "logs"):
            (root / name).mkdir(mode=0o700)
        environment = {
            key: os.environ[key]
            for key in ("PATH", "LANG", "LC_ALL", "TZ", "SYSTEMROOT", "WINDIR")
            if key in os.environ
        }
        environment.update(
            {
                "HOME": str(root / "home"),
                "TMPDIR": str(root / "tmp"),
                "TEMP": str(root / "tmp"),
                "TMP": str(root / "tmp"),
                "XDG_CACHE_HOME": str(root / "cache"),
                "XDG_STATE_HOME": str(root / "state"),
                "XDG_RUNTIME_DIR": runtime_directory,
                "SYNON_HOME": str(root / "data"),
                "SYNON_HOST": "127.0.0.1",
                "SYNON_PORT": str(port),
                "NO_PROXY": "127.0.0.1,localhost,::1",
                "no_proxy": "127.0.0.1,localhost,::1",
            }
        )
        flags = subprocess.CREATE_NEW_PROCESS_GROUP if os.name == "nt" else 0
        try:
            process = subprocess.Popen(
                command,
                env=environment,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                start_new_session=os.name != "nt",
                creationflags=flags,
            )
        except OSError as exc:
            raise StartupSmokeError("startup_process_failed") from exc
        if process.stdout is None or process.stderr is None:
            _stop(process)
            raise StartupSmokeError("startup_output_pipe_failed")
        stdout_sink = _DigestSink(process.stdout)
        stderr_sink = _DigestSink(process.stderr)
        stdout_sink.start()
        stderr_sink.start()
        deadline = time.monotonic() + timeout_seconds
        status: int | None = None
        body = b""
        content_type: str | None = None
        try:
            while time.monotonic() < deadline:
                if process.poll() is not None:
                    raise StartupSmokeError("startup_process_exited")
                connection = http.client.HTTPConnection("127.0.0.1", port, timeout=1)
                try:
                    connection.request("GET", "/health")
                    response = connection.getresponse()
                    status = response.status
                    content_type = response.getheader("Content-Type")
                    body = response.read(65537)
                    if status == 200:
                        break
                except (OSError, http.client.HTTPException):
                    pass
                finally:
                    connection.close()
                time.sleep(0.1)
            if status != 200:
                raise StartupSmokeError("startup_health_timeout")
            if not body or len(body) > 65536:
                raise StartupSmokeError("startup_health_body_invalid")
            if type(content_type) is not str or content_type.split(";", 1)[0].strip().lower() != "application/json":
                raise StartupSmokeError("startup_health_content_type_invalid")
            try:
                health = json.loads(body.decode("utf-8"))
            except (UnicodeDecodeError, json.JSONDecodeError) as exc:
                raise StartupSmokeError("startup_health_json_invalid") from exc
            if (
                type(health) is not dict
                or health.get("status") != "healthy"
                or health.get("service") != "gateway"
                or type(health.get("name")) is not str
                or not health["name"]
                or type(health.get("version")) is not str
                or not health["version"]
                or health.get("agent_catalog_ready") is not True
                or type(health.get("agents_registered")) is not int
                or health["agents_registered"] < 0
            ):
                raise StartupSmokeError("startup_health_contract_invalid")
        finally:
            _stop(process)
            stdout_sink.finish()
            stderr_sink.finish()
        return {
            "schema": "synon.governance.startup-smoke-result.v1",
            "result": "PASS",
            "health_status": status,
            "health_body_bytes": len(body),
            "health_body_sha256": hashlib.sha256(body).hexdigest(),
            "stdout_bytes": stdout_sink.bytes,
            "stdout_sha256": stdout_sink.hexdigest(),
            "stderr_bytes": stderr_sink.bytes,
            "stderr_sha256": stderr_sink.hexdigest(),
        }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--timeout-seconds", type=int, default=30)
    args = parser.parse_args(argv)
    try:
        relative = pathlib.PurePath(args.binary)
        if relative.is_absolute() or ".." in relative.parts:
            raise StartupSmokeError("startup_binary_path_invalid")
        binary = pathlib.Path.cwd().joinpath(*relative.parts).resolve()
        if not binary.is_file() or binary.is_symlink():
            raise StartupSmokeError("startup_binary_invalid")
        if type(args.timeout_seconds) is not int or not 1 <= args.timeout_seconds <= 120:
            raise StartupSmokeError("startup_timeout_invalid")
        result = probe([str(binary), "serve"], timeout_seconds=args.timeout_seconds)
    except StartupSmokeError as exc:
        print(json.dumps({"ok": False, "code": str(exc)}, sort_keys=True))
        return 2
    print(json.dumps({"ok": True, **result}, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
