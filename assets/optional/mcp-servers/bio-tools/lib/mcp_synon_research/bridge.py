"""Bounded execution bridge for an operator-installed Synon-research pack.

The external package remains outside Synon Biomed source.  This module exposes
its compact JSON helpers through one MCP domain without registering every
external SKILL.md as a model-visible Skill.
"""

from __future__ import annotations

import hashlib
import json
import os
from dataclasses import dataclass, field
from contextlib import contextmanager
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import threading
from typing import Any

import anyio


ROOT_ENV = "SYNON_RESEARCH_ROOT"
VERSION_ENV = "SYNON_RESEARCH_EXPECTED_VERSION"
MAX_STDOUT_BYTES = 10 * 1024 * 1024
MAX_STDERR_BYTES = 64 * 1024
MAX_INPUT_BYTES = 1024 * 1024
MAX_CONTRACT_CHARS = 12_000
DEFAULT_TIMEOUT_SECONDS = 60
MAX_TIMEOUT_SECONDS = 600
PUBLIC_NAME = "synon-research"
UPSTREAM_PLUGIN_NAME = "life-science-research"

_FRONTMATTER = re.compile(r"\A---\s*\n(.*?)\n---\s*(?:\n|\Z)", re.DOTALL)
_FIELD = re.compile(r"^([A-Za-z][A-Za-z0-9_-]*):\s*(.*?)\s*$")
_SOURCE_ID = re.compile(r"^[a-z0-9][a-z0-9-]{0,127}$")
_OPERATION = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$")


class PackError(RuntimeError):
    """A stable, user-actionable external-pack error."""

    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code


@dataclass(frozen=True)
class Operation:
    name: str
    path: Path
    sha256: str
    input_mode: str


@dataclass(frozen=True)
class Source:
    source_id: str
    name: str
    description: str
    skill_path: Path
    skill_sha256: str
    input_contract: str
    output_contract: str
    operations: tuple[Operation, ...]


@dataclass(frozen=True)
class Pack:
    root: Path
    version: str
    manifest_sha256: str
    sources: tuple[Source, ...]


@dataclass
class _BoundedCapture:
    """Thread/task-safe bounded child output buffers."""

    stdout: bytearray = field(default_factory=bytearray)
    stderr: bytearray = field(default_factory=bytearray)
    error_code: str | None = None
    lock: threading.Lock = field(default_factory=threading.Lock)

    def append(self, stream_name: str, chunk: bytes, limit: int, error_code: str) -> bool:
        with self.lock:
            if self.error_code is not None:
                return False
            target = self.stdout if stream_name == "stdout" else self.stderr
            remaining = limit - len(target)
            if len(chunk) > remaining:
                if remaining > 0:
                    target.extend(chunk[:remaining])
                self.error_code = error_code
                return False
            target.extend(chunk)
            return True


@dataclass(frozen=True)
class _ProcessResult:
    returncode: int
    stdout: bytes
    stderr: bytes


def _sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(64 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _regular_file_under(root: Path, path: Path) -> Path:
    root = root.resolve(strict=True)
    if not path.is_absolute():
        raise PackError("unsafe_path", "External capability path must be absolute.")
    try:
        relative = path.relative_to(root)
    except ValueError as exc:
        raise PackError("unsafe_path", "External capability path escapes its configured root.") from exc
    if relative == Path(".") or any(part in {"", ".", ".."} for part in relative.parts):
        raise PackError("unsafe_path", "External capability path is not canonical.")
    current = root
    for part in relative.parts:
        current = current / part
        if current.is_symlink():
            raise PackError("unsafe_path", "External capability paths may not traverse symbolic links.")
    try:
        resolved = path.resolve(strict=True)
        resolved.relative_to(root)
    except (OSError, ValueError) as exc:
        raise PackError("unsafe_path", "External capability path escapes its configured root.") from exc
    if not resolved.is_file():
        raise PackError("unsafe_path", "External capability files must be regular non-symlink files.")
    return resolved


def _parse_frontmatter(path: Path) -> dict[str, str]:
    text = path.read_text(encoding="utf-8")
    matched = _FRONTMATTER.match(text)
    if not matched:
        raise PackError("invalid_skill", f"{path.parent.name}/SKILL.md has no YAML frontmatter.")
    fields: dict[str, str] = {}
    for line in matched.group(1).splitlines():
        field = _FIELD.match(line)
        if field:
            fields[field.group(1)] = field.group(2).strip().strip("'\"")
    return fields


def _markdown_section(text: str, heading: str) -> str:
    lines = text.splitlines()
    start: int | None = None
    level = 0
    collected: list[str] = []
    wanted = heading.casefold()
    for index, line in enumerate(lines):
        match = re.match(r"^(#{1,6})\s+(.+?)\s*$", line)
        if start is None:
            if match and match.group(2).strip().casefold() == wanted:
                start = index + 1
                level = len(match.group(1))
            continue
        if match and len(match.group(1)) <= level:
            break
        collected.append(line)
    value = "\n".join(collected).strip()
    if len(value) > MAX_CONTRACT_CHARS:
        return value[:MAX_CONTRACT_CHARS] + "\n[contract truncated]"
    return value


def _source_contract(text: str, names: tuple[str, ...]) -> str:
    sections = [_markdown_section(text, name) for name in names]
    value = "\n\n".join(section for section in sections if section).strip()
    if value:
        return value
    lines = text.splitlines()
    for line in lines:
        match = re.match(r"^(#{1,6})\s+(.+?)\s*$", line)
        if match and any(match.group(2).strip().casefold().startswith(name.casefold()) for name in names):
            return _markdown_section(text, match.group(2).strip())
    return ""


def _operation_input_mode(script: Path) -> str:
    content = script.read_text(encoding="utf-8", errors="replace")
    if "--input-json" in content and "--print-result" in content:
        return "input-json-cli"
    return "stdin-json"


def load_pack() -> Pack:
    configured = os.environ.get(ROOT_ENV, "").strip()
    if not configured:
        raise PackError(
            "package_unavailable",
            f"Set {ROOT_ENV} to an operator-verified Synon-research upstream package directory.",
        )
    raw_root = Path(configured).expanduser()
    if not raw_root.is_absolute():
        raise PackError("invalid_configuration", f"{ROOT_ENV} must be an absolute path.")
    root = raw_root.resolve(strict=True)
    manifest = _regular_file_under(root, root / ".codex-plugin" / "plugin.json")
    try:
        metadata = json.loads(manifest.read_text(encoding="utf-8"))
    except (OSError, ValueError) as exc:
        raise PackError("invalid_manifest", "The external plugin manifest is not valid JSON.") from exc
    if metadata.get("name") != UPSTREAM_PLUGIN_NAME:
        raise PackError("invalid_manifest", f"Configured Synon-research upstream package has an unexpected identity.")
    version = str(metadata.get("version") or "").strip()
    if not version:
        raise PackError("invalid_manifest", "The external plugin manifest has no version.")
    expected_version = os.environ.get(VERSION_ENV, "").strip()
    if expected_version and version != expected_version:
        raise PackError(
            "version_mismatch",
            f"Configured plugin version {version!r} does not match expected version {expected_version!r}.",
        )

    skills_root = root / "skills"
    if skills_root.is_symlink() or not skills_root.is_dir():
        raise PackError("invalid_manifest", "The external plugin skills directory is unavailable.")
    skills_root = skills_root.resolve(strict=True)
    sources: list[Source] = []
    for skill_dir in sorted(skills_root.iterdir(), key=lambda item: item.name):
        if not skill_dir.is_dir() or skill_dir.is_symlink():
            continue
        source_id = skill_dir.name
        if not _SOURCE_ID.fullmatch(source_id):
            continue
        skill_path = skill_dir / "SKILL.md"
        if not skill_path.exists():
            continue
        skill_path = _regular_file_under(root, skill_path)
        skill_text = skill_path.read_text(encoding="utf-8")
        fields = _parse_frontmatter(skill_path)
        scripts_dir = skill_dir / "scripts"
        operations: list[Operation] = []
        if scripts_dir.is_dir() and not scripts_dir.is_symlink():
            for script in sorted(scripts_dir.glob("*.py"), key=lambda item: item.name):
                if script.name.startswith(("test_", "_")):
                    continue
                script = _regular_file_under(root, script)
                operation = script.stem
                if _OPERATION.fullmatch(operation):
                    operations.append(
                        Operation(operation, script, _sha256(script), _operation_input_mode(script))
                    )
        if not operations:
            continue
        sources.append(
            Source(
                source_id=source_id,
                name=fields.get("name") or source_id,
                description=(fields.get("description") or "").strip(),
                skill_path=skill_path,
                skill_sha256=_sha256(skill_path),
                input_contract=_source_contract(skill_text, ("Input", "Required Inputs", "Optional Inputs")),
                output_contract=_source_contract(skill_text, ("Output", "Output Contract")),
                operations=tuple(operations),
            )
        )
    if not sources:
        raise PackError("invalid_manifest", "The external plugin exposes no executable JSON helpers.")
    return Pack(root, version, _sha256(manifest), tuple(sources))


def list_sources(query: str = "", max_results: int = 20) -> dict[str, Any]:
    if max_results < 1 or max_results > 100:
        raise ValueError("max_results must be between 1 and 100")
    pack = load_pack()
    needle = query.strip().casefold()
    matched = [
        source
        for source in pack.sources
        if not needle
        or needle in source.source_id.casefold()
        or needle in source.name.casefold()
        or needle in source.description.casefold()
    ]
    returned = matched[:max_results]
    return {
        "source": PUBLIC_NAME,
        "version": pack.version,
        "manifest_sha256": pack.manifest_sha256,
        "query": query.strip(),
        "total": len(matched),
        "returned": len(returned),
        "truncated": len(matched) > len(returned),
        "sources": [
            {
                "source_id": source.source_id,
                "name": source.name,
                "description": source.description,
                "operations": [operation.name for operation in source.operations],
            }
            for source in returned
        ],
    }


def describe_source(source_id: str) -> dict[str, Any]:
    source_id = source_id.strip()
    if not _SOURCE_ID.fullmatch(source_id):
        raise ValueError("source_id must be a canonical lowercase source identifier")
    pack = load_pack()
    source = next((item for item in pack.sources if item.source_id == source_id), None)
    if source is None:
        raise PackError("source_not_found", f"Unknown Synon-research source {source_id!r}.")
    return {
        "source": PUBLIC_NAME,
        "version": pack.version,
        "source_id": source.source_id,
        "name": source.name,
        "description": source.description,
        "skill_sha256": source.skill_sha256,
        "input_contract": source.input_contract or "No separate Input section is declared; inspect the selected operation's narrow example before calling it.",
        "output_contract": source.output_contract or "The operation returns one JSON object; preserve its source-specific status and provenance fields.",
        "operations": [
            {"name": operation.name, "sha256": operation.sha256, "input_mode": operation.input_mode}
            for operation in source.operations
        ],
        "usage": (
            "Call life_science_source with one listed operation and a JSON input object. "
            "Use a narrow representative query first and preserve the returned source receipt."
        ),
    }


def _child_environment() -> dict[str, str]:
    # Source helpers are untrusted operator content. Keep public HTTPS access
    # without inheriting credentials; secrets require a separate audited,
    # source-specific integration.
    allowed = {
        "PATH",
        "HOME",
        "LANG",
        "LC_ALL",
        "SSL_CERT_FILE",
        "REQUESTS_CA_BUNDLE",
        "HTTP_PROXY",
        "HTTPS_PROXY",
        "NO_PROXY",
    }
    return {key: value for key, value in os.environ.items() if key in allowed}


def call_source(
    source_id: str,
    operation: str,
    input: dict[str, Any],
    timeout_seconds: int = DEFAULT_TIMEOUT_SECONDS,
) -> dict[str, Any]:
    pack, source, selected, encoded = _prepare_source_call(
        source_id, operation, input, timeout_seconds
    )
    with _source_process(selected, source, encoded) as (command, process_input, cwd):
        completed = _run_bounded_process(command, process_input, cwd, timeout_seconds)
    return _source_response(pack, source, selected, completed)


async def call_source_async(
    source_id: str,
    operation: str,
    input: dict[str, Any],
    timeout_seconds: int = DEFAULT_TIMEOUT_SECONDS,
) -> dict[str, Any]:
    """Run a source operation with cancellation propagated to the child process."""
    pack, source, selected, encoded = _prepare_source_call(
        source_id, operation, input, timeout_seconds
    )
    with _source_process(selected, source, encoded) as (command, process_input, cwd):
        completed = await _run_bounded_process_async(command, process_input, cwd, timeout_seconds)
    return _source_response(pack, source, selected, completed)


def _capture_error(capture: _BoundedCapture) -> PackError | None:
    if capture.error_code == "source_output_too_large":
        return PackError(capture.error_code, "Source output exceeded the MCP response boundary.")
    if capture.error_code == "source_stderr_too_large":
        return PackError(capture.error_code, "Source diagnostic output exceeded the MCP response boundary.")
    return None


def _terminate_process(process: subprocess.Popen[bytes]) -> None:
    if process.poll() is not None:
        return
    try:
        process.terminate()
        process.wait(timeout=0.5)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=1)
    except (OSError, ProcessLookupError):
        return


def _run_bounded_process(
    command: list[str], process_input: bytes | None, cwd: str, timeout_seconds: int
) -> _ProcessResult:
    try:
        process = subprocess.Popen(
            command,
            stdin=subprocess.PIPE if process_input is not None else subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=cwd,
            env=_child_environment(),
        )
    except OSError as exc:
        raise PackError("source_start_failed", "Source operation could not be started.") from exc
    capture = _BoundedCapture()

    def read_stream(stream_name: str, stream: Any, limit: int, error_code: str) -> None:
        try:
            while chunk := stream.read(64 * 1024):
                if not capture.append(stream_name, chunk, limit, error_code):
                    _terminate_process(process)
                    return
        finally:
            stream.close()

    def write_stdin() -> None:
        assert process.stdin is not None
        try:
            process.stdin.write(process_input or b"")
            process.stdin.flush()
        except (BrokenPipeError, OSError):
            pass
        finally:
            process.stdin.close()

    assert process.stdout is not None and process.stderr is not None
    workers = [
        threading.Thread(
            target=read_stream,
            args=("stdout", process.stdout, MAX_STDOUT_BYTES, "source_output_too_large"),
            daemon=True,
        ),
        threading.Thread(
            target=read_stream,
            args=("stderr", process.stderr, MAX_STDERR_BYTES, "source_stderr_too_large"),
            daemon=True,
        ),
    ]
    if process_input is not None:
        workers.append(threading.Thread(target=write_stdin, daemon=True))
    for worker in workers:
        worker.start()
    try:
        returncode = process.wait(timeout=timeout_seconds)
    except subprocess.TimeoutExpired as exc:
        _terminate_process(process)
        raise PackError("source_timeout", f"Source operation exceeded {timeout_seconds} seconds.") from exc
    finally:
        for worker in workers:
            worker.join(timeout=2)
        if process.poll() is None:
            _terminate_process(process)
    capture_failure = _capture_error(capture)
    if capture_failure is not None:
        raise capture_failure
    return _ProcessResult(returncode, bytes(capture.stdout), bytes(capture.stderr))


async def _terminate_process_async(process: Any) -> None:
    if process.returncode is not None:
        return
    try:
        process.terminate()
    except (OSError, ProcessLookupError):
        return
    with anyio.move_on_after(0.5):
        await process.wait()
    if process.returncode is None:
        try:
            process.kill()
        except (OSError, ProcessLookupError):
            return
        with anyio.move_on_after(1):
            await process.wait()


async def _run_bounded_process_async(
    command: list[str], process_input: bytes | None, cwd: str, timeout_seconds: int
) -> _ProcessResult:
    try:
        process = await anyio.open_process(
            command,
            stdin=subprocess.PIPE if process_input is not None else subprocess.DEVNULL,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            cwd=cwd,
            env=_child_environment(),
        )
    except OSError as exc:
        raise PackError("source_start_failed", "Source operation could not be started.") from exc
    capture = _BoundedCapture()

    async def read_stream(stream_name: str, stream: Any, limit: int, error_code: str) -> None:
        try:
            while True:
                try:
                    chunk = await stream.receive(64 * 1024)
                except anyio.EndOfStream:
                    return
                if not capture.append(stream_name, chunk, limit, error_code):
                    await _terminate_process_async(process)
                    return
        except (anyio.BrokenResourceError, anyio.ClosedResourceError):
            return

    async def write_stdin() -> None:
        assert process.stdin is not None
        try:
            await process.stdin.send(process_input or b"")
        except (anyio.BrokenResourceError, anyio.ClosedResourceError):
            pass
        finally:
            await process.stdin.aclose()

    try:
        assert process.stdout is not None and process.stderr is not None
        async with anyio.create_task_group() as tasks:
            tasks.start_soon(
                read_stream, "stdout", process.stdout, MAX_STDOUT_BYTES, "source_output_too_large"
            )
            tasks.start_soon(
                read_stream, "stderr", process.stderr, MAX_STDERR_BYTES, "source_stderr_too_large"
            )
            if process_input is not None:
                tasks.start_soon(write_stdin)
            try:
                with anyio.fail_after(timeout_seconds):
                    returncode = await process.wait()
            except TimeoutError as exc:
                await _terminate_process_async(process)
                raise PackError(
                    "source_timeout", f"Source operation exceeded {timeout_seconds} seconds."
                ) from exc
    except BaseException:
        with anyio.CancelScope(shield=True):
            await _terminate_process_async(process)
        raise
    finally:
        with anyio.CancelScope(shield=True):
            await process.aclose()
    capture_failure = _capture_error(capture)
    if capture_failure is not None:
        raise capture_failure
    return _ProcessResult(returncode, bytes(capture.stdout), bytes(capture.stderr))


def _prepare_source_call(
    source_id: str,
    operation: str,
    input: dict[str, Any],
    timeout_seconds: int,
) -> tuple[Pack, Source, Operation, bytes]:
    source_id = source_id.strip()
    operation = operation.strip()
    if not _SOURCE_ID.fullmatch(source_id):
        raise ValueError("source_id must be a canonical lowercase source identifier")
    if not _OPERATION.fullmatch(operation):
        raise ValueError("operation must be a canonical operation identifier")
    if not isinstance(input, dict):
        raise ValueError("input must be a JSON object")
    if timeout_seconds < 1 or timeout_seconds > MAX_TIMEOUT_SECONDS:
        raise ValueError(f"timeout_seconds must be between 1 and {MAX_TIMEOUT_SECONDS}")
    encoded = json.dumps(input, ensure_ascii=False, separators=(",", ":")).encode("utf-8")
    if len(encoded) > MAX_INPUT_BYTES:
        raise ValueError(f"input exceeds {MAX_INPUT_BYTES} bytes")

    pack = load_pack()
    source = next((item for item in pack.sources if item.source_id == source_id), None)
    if source is None:
        raise PackError("source_not_found", f"Unknown Synon-research source {source_id!r}.")
    selected = next((item for item in source.operations if item.name == operation), None)
    if selected is None:
        raise PackError("operation_not_found", f"Source {source_id!r} has no operation {operation!r}.")
    return pack, source, selected, encoded


@contextmanager
def _source_process(operation: Operation, source: Source, encoded: bytes):
    if operation.input_mode == "stdin-json":
        yield [sys.executable, "-B", str(operation.path)], encoded, str(source.skill_path.parent)
        return
    if operation.input_mode != "input-json-cli":
        raise PackError("unsupported_operation", f"Unsupported input mode {operation.input_mode!r}.")
    with tempfile.TemporaryDirectory(prefix="synon-research-") as temporary:
        request_path = Path(temporary) / "input.json"
        request_path.write_bytes(encoded)
        yield (
            [
                sys.executable,
                "-B",
                str(operation.path),
                "--input-json",
                str(request_path),
                "--print-result",
            ],
            None,
            temporary,
        )


def _source_response(
    pack: Pack,
    source: Source,
    selected: Operation,
    completed: _ProcessResult,
) -> dict[str, Any]:
    stderr = completed.stderr[:MAX_STDERR_BYTES].decode("utf-8", errors="replace").strip()
    if completed.returncode != 0:
        raise PackError(
            "source_failed",
            f"Source operation exited unsuccessfully with status {completed.returncode}.",
        )
    try:
        result = json.loads(completed.stdout.decode("utf-8"))
    except (UnicodeDecodeError, ValueError) as exc:
        raise PackError("invalid_source_output", "Source operation did not return one JSON value.") from exc
    if not isinstance(result, dict):
        raise PackError("invalid_source_output", "Source operation output must be a JSON object.")
    receipt = {
        "plugin": PUBLIC_NAME,
        "upstream_package": UPSTREAM_PLUGIN_NAME,
        "plugin_version": pack.version,
        "manifest_sha256": pack.manifest_sha256,
        "source_id": source.source_id,
        "skill_sha256": source.skill_sha256,
        "operation": selected.name,
        "operation_sha256": selected.sha256,
        "exit_code": completed.returncode,
    }
    response: dict[str, Any] = {"receipt": receipt, "result": result}
    if stderr:
        response["diagnostic"] = stderr
    return response
