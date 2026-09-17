#!/usr/bin/env python3
"""Run bounded argv-only verification from an exact clean local Git clone."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import pathlib
import re
import shutil
import signal
import subprocess
import sys
import tempfile
import time
from typing import Any
from urllib.parse import urlsplit


SCHEMA = "synon.governance.clean-clone-command-set.v1"
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
SAFE_ID_RE = re.compile(r"^[a-z0-9][a-z0-9-]{0,63}$")
SECRET_ARG_RE = re.compile(r"(?i)(api[_-]?key|token|password|secret)\s*[:=]")
SHELL_PROGRAMS = {"bash", "cmd", "cmd.exe", "pwsh", "powershell", "powershell.exe", "sh"}


def _remove_private_tree(path: pathlib.Path) -> None:
    """Remove an owned private tree even when package managers made directories read-only."""

    for current, _, _ in os.walk(path, topdown=True, followlinks=False):
        current_path = pathlib.Path(current)
        if current_path.is_symlink():
            continue
        current_path.chmod(current_path.stat().st_mode | 0o700)
    shutil.rmtree(path)


class CloneGateError(Exception):
    """Controlled clean-clone gate failure."""


def _load(path: pathlib.Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_bytes().decode("utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise CloneGateError("clean_clone_command_json_invalid") from exc
    if type(value) is not dict:
        raise CloneGateError("clean_clone_command_root_invalid")
    return value


def _safe_relative(value: Any, code: str) -> pathlib.PurePosixPath:
    if type(value) is not str or "\\" in value:
        raise CloneGateError(code)
    path = pathlib.PurePosixPath(value or ".")
    if path.is_absolute() or ".." in path.parts or str(path) != value:
        raise CloneGateError(code)
    return path


def validate_command_set(value: dict[str, Any]) -> list[dict[str, Any]]:
    if set(value) != {"schema", "commands"} or value.get("schema") != SCHEMA:
        raise CloneGateError("clean_clone_command_shape_invalid")
    commands = value["commands"]
    if type(commands) is not list or not commands:
        raise CloneGateError("clean_clone_commands_invalid")
    names: set[str] = set()
    for command in commands:
        if type(command) is not dict or set(command) != {"name", "argv", "cwd", "timeout_seconds"}:
            raise CloneGateError("clean_clone_command_shape_invalid")
        name = command["name"]
        if type(name) is not str or not SAFE_ID_RE.fullmatch(name) or name in names:
            raise CloneGateError("clean_clone_command_name_invalid")
        names.add(name)
        argv = command["argv"]
        if type(argv) is not list or not argv or any(type(arg) is not str or not arg for arg in argv):
            raise CloneGateError("clean_clone_command_argv_invalid")
        if any(SECRET_ARG_RE.search(arg) for arg in argv):
            raise CloneGateError("clean_clone_command_secret_arg")
        if pathlib.PurePath(argv[0]).name.lower() in SHELL_PROGRAMS:
            raise CloneGateError("clean_clone_command_shell_forbidden")
        _safe_relative(command["cwd"], "clean_clone_command_cwd_invalid")
        timeout = command["timeout_seconds"]
        if type(timeout) is not int or timeout <= 0 or timeout > 7200:
            raise CloneGateError("clean_clone_command_timeout_invalid")
    return commands


def _run_git(repo: pathlib.Path, *args: str, timeout: int = 60) -> str:
    try:
        completed = subprocess.run(
            ["git", "-C", str(repo), *args],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=timeout,
        )
    except (OSError, subprocess.SubprocessError) as exc:
        raise CloneGateError("clean_clone_git_failed") from exc
    return completed.stdout.strip()


def _resource_snapshot() -> dict[str, Any]:
    result: dict[str, Any] = {"logical_cpu": os.cpu_count(), "unix_time": time.time()}
    try:
        lines = pathlib.Path("/proc/meminfo").read_text(encoding="ascii").splitlines()
        values = {line.split(":", 1)[0]: line.split(":", 1)[1].strip() for line in lines if ":" in line}
        result["mem_available"] = values.get("MemAvailable", "unavailable")
    except OSError:
        result["mem_available"] = "unavailable"
    return result


def _digest(data: bytes) -> str:
    return hashlib.sha256(data).hexdigest()


def _write_new_json(path: pathlib.Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary: pathlib.Path | None = None
    try:
        with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=path.parent, delete=False) as handle:
            temporary = pathlib.Path(handle.name)
            os.chmod(temporary, 0o600)
            json.dump(value, handle, indent=2, sort_keys=True)
            handle.write("\n")
            handle.flush()
            os.fsync(handle.fileno())
        os.link(temporary, path)
    except FileExistsError as exc:
        raise CloneGateError("clean_clone_output_exists") from exc
    except OSError as exc:
        raise CloneGateError("clean_clone_output_write_failed") from exc
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def _terminate_owned_processes(process: subprocess.Popen) -> None:
    """Only stop the process tree created for this verification command."""
    if os.name == "posix":
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
    elif process.poll() is None:
        subprocess.run(["taskkill", "/PID", str(process.pid), "/T", "/F"],
                       capture_output=True, timeout=10, check=False)


def _execute_command(argv: list[str], cwd: pathlib.Path, environment: dict, timeout: int,
                     evidence_dir: pathlib.Path | None = None, name: str = "command") -> tuple:
    options = {"start_new_session": True} if os.name == "posix" else {
        "creationflags": subprocess.CREATE_NEW_PROCESS_GROUP,
    }
    with subprocess.Popen(argv, cwd=cwd, env=environment, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, **options) as process:
        try:
            stdout, stderr = process.communicate(timeout=timeout)
            return process.returncode, stdout, stderr, False
        except subprocess.TimeoutExpired:
            _terminate_owned_processes(process)
            stdout, stderr = process.communicate(timeout=10)
            return None, stdout, stderr, True
        except KeyboardInterrupt as exc:
            _terminate_owned_processes(process)
            stdout, stderr = process.communicate(timeout=10)
            if evidence_dir is not None:
                _save_logs(evidence_dir, name, stdout, stderr)
            raise CloneGateError("clean_clone_command_interrupted") from exc
        finally:
            # Children must not outlive a completed/failed/cancelled command and
            # continue writing after its private verification tree is retired.
            _terminate_owned_processes(process)


def _save_logs(directory: pathlib.Path, name: str, stdout: bytes, stderr: bytes) -> None:
    directory.mkdir(parents=True, mode=0o700, exist_ok=True)
    for suffix, content in (("stdout", stdout), ("stderr", stderr)):
        path = directory / f"{name}.{suffix}.log"
        with path.open("xb") as handle:
            os.chmod(path, 0o600)
            handle.write(content)


def run_gate(
    source: pathlib.Path,
    sha: str,
    command_set: dict[str, Any],
    temp_root: pathlib.Path,
    keep_on_failure: bool = False,
    evidence_dir: pathlib.Path | None = None,
    command_executor=None,
) -> tuple[dict[str, Any], pathlib.Path | None]:
    if not SHA_RE.fullmatch(sha):
        raise CloneGateError("clean_clone_sha_invalid")
    commands = validate_command_set(command_set)
    command_executor = command_executor or _execute_command
    if not source.is_dir() or not temp_root.is_dir():
        raise CloneGateError("clean_clone_path_invalid")
    if evidence_dir is not None:
        evidence_dir = evidence_dir.resolve()
        if evidence_dir == source.resolve() or source.resolve() in evidence_dir.parents:
            raise CloneGateError("clean_clone_evidence_inside_source")
    if _run_git(source, "status", "--porcelain=v1", "--untracked-files=all"):
        raise CloneGateError("clean_clone_source_dirty")
    _run_git(source, "cat-file", "-e", f"{sha}^{{commit}}")
    if _run_git(source, "rev-parse", "HEAD") != sha:
        raise CloneGateError("clean_clone_source_head_mismatch")

    clone_root = pathlib.Path(tempfile.mkdtemp(prefix="synon-clean-clone-", dir=temp_root))
    clone = clone_root / "source"
    isolated = clone_root / "isolated"
    isolated.mkdir(mode=0o700)
    result: dict[str, Any] = {
        "schema": "synon.governance.clean-clone-result.v1",
        "source_sha": sha,
        "started": _resource_snapshot(),
        "commands": [],
        "result": "PASS",
    }
    failed = False
    try:
        subprocess.run(
            ["git", "clone", "--quiet", "--no-local", "--no-checkout", str(source), str(clone)],
            check=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=300,
        )
        _run_git(clone, "checkout", "--quiet", "--detach", sha)
        if _run_git(clone, "rev-parse", "HEAD") != sha or _run_git(clone, "status", "--porcelain=v1", "--untracked-files=all"):
            raise CloneGateError("clean_clone_checkout_mismatch")
        expected_tree = _run_git(clone, "rev-parse", "HEAD^{tree}")
        environment = {
            key: os.environ[key]
            for key in (
                "LANG",
                "PATH",
                "LC_ALL",
                "TZ",
                "SSL_CERT_FILE",
                "SSL_CERT_DIR",
                "HTTP_PROXY",
                "HTTPS_PROXY",
                "ALL_PROXY",
                "NO_PROXY",
                "http_proxy",
                "https_proxy",
                "all_proxy",
                "no_proxy",
            )
            if key in os.environ
        }
        environment.update(
            {
                "HOME": str(isolated / "home"),
                "TMPDIR": str(isolated / "tmp"),
                "XDG_CACHE_HOME": str(isolated / "cache"),
                "XDG_STATE_HOME": str(isolated / "state"),
                "GOCACHE": str(isolated / "cache" / "go-build"),
                "GOMODCACHE": str(isolated / "cache" / "go-mod"),
                "GOPATH": str(isolated / "gopath"),
                "npm_config_cache": str(isolated / "cache" / "npm"),
                "SYNON_CLEAN_CLONE_PYTHON_HOME": str(isolated / "home"),
                "SYNON_TEST_DATA_DIR": str(isolated / "data"),
                "SYNON_TEST_LOG_DIR": str(isolated / "logs"),
            }
        )
        for key, value in environment.items():
            if key.lower() in {"http_proxy", "https_proxy", "all_proxy"}:
                proxy = urlsplit(value)
                if (proxy.scheme not in {"http", "https", "socks5", "socks5h"} or not proxy.hostname
                        or proxy.username is not None or proxy.password is not None or proxy.query or proxy.fragment):
                    raise CloneGateError("authenticated_proxy_requires_separate_ci_configuration")
        for name in ("home", "tmp", "cache", "state", "gopath", "data", "logs"):
            (isolated / name).mkdir(mode=0o700)
        for command in commands:
            if _run_git(clone, "rev-parse", "HEAD") != sha or _run_git(clone, "rev-parse", "HEAD^{tree}") != expected_tree:
                raise CloneGateError("clean_clone_revision_drift")
            if _run_git(clone, "status", "--porcelain=v1", "--untracked-files=no"):
                raise CloneGateError("clean_clone_tracked_source_mutation")
            relative = _safe_relative(command["cwd"], "clean_clone_command_cwd_invalid")
            cwd = clone.joinpath(*relative.parts).resolve()
            try:
                cwd.relative_to(clone.resolve())
            except ValueError as exc:
                raise CloneGateError("clean_clone_command_cwd_escape") from exc
            if not cwd.is_dir():
                raise CloneGateError("clean_clone_command_cwd_missing")
            started = time.monotonic()
            code, stdout, stderr, timed_out = command_executor(
                command["argv"], cwd, environment, command["timeout_seconds"], evidence_dir, command["name"],
            )
            if evidence_dir is not None:
                _save_logs(evidence_dir, command["name"], stdout, stderr)
            record = {
                "name": command["name"],
                "argv": command["argv"],
                "cwd": command["cwd"],
                "timeout_seconds": command["timeout_seconds"],
                "timed_out": timed_out,
                "exit_code": code,
                "wall_seconds": round(time.monotonic() - started, 3),
                "stdout_bytes": len(stdout),
                "stdout_sha256": _digest(stdout),
                "stderr_bytes": len(stderr),
                "stderr_sha256": _digest(stderr),
            }
            result["commands"].append(record)
            if _run_git(clone, "rev-parse", "HEAD") != sha or _run_git(clone, "rev-parse", "HEAD^{tree}") != expected_tree:
                failed = True
                result["result"] = "FAIL"
                result["failure_classification"] = "revision-drift"
                break
            if _run_git(clone, "status", "--porcelain=v1", "--untracked-files=no"):
                failed = True
                result["result"] = "FAIL"
                result["failure_classification"] = "source-mutation"
                break
            if timed_out or code != 0:
                failed = True
                result["result"] = "FAIL"
                result["failure_classification"] = "timeout-unclassified" if timed_out else "test-failure"
                if keep_on_failure:
                    stdout_log = isolated / "logs" / f"{command['name']}.stdout.log"
                    stderr_log = isolated / "logs" / f"{command['name']}.stderr.log"
                    stdout_log.write_bytes(stdout)
                    stderr_log.write_bytes(stderr)
                    stdout_log.chmod(0o600)
                    stderr_log.chmod(0o600)
                    record["diagnostic_logs"] = {
                        "stdout": stdout_log.relative_to(clone_root).as_posix(),
                        "stderr": stderr_log.relative_to(clone_root).as_posix(),
                        "local_only": True,
                    }
                break
        result["finished"] = _resource_snapshot()
        result["verified_tree"] = expected_tree
        result["post_status_clean"] = not bool(_run_git(clone, "status", "--porcelain=v1", "--untracked-files=all"))
        if not result["post_status_clean"]:
            result["result"] = "FAIL"
            result["failure_classification"] = "source-mutation"
            failed = True
        result["source_post_stable"] = (
            _run_git(source, "rev-parse", "HEAD") == sha
            and not bool(_run_git(source, "status", "--porcelain=v1", "--untracked-files=all"))
        )
        if not result["source_post_stable"]:
            result["result"] = "FAIL"
            result["failure_classification"] = "source-drift"
            failed = True
    except BaseException:
        failed = True
        raise
    finally:
        if not (keep_on_failure and failed):
            resolved_root = clone_root.resolve()
            if resolved_root.parent != temp_root.resolve() or not resolved_root.name.startswith("synon-clean-clone-"):
                raise CloneGateError("clean_clone_cleanup_target_invalid")
            _remove_private_tree(resolved_root)
            retained = None
        else:
            retained = clone_root
    if retained is not None:
        result["retained_local_id"] = retained.name
    return result, retained


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--validate-only", action="store_true")
    parser.add_argument("--source")
    parser.add_argument("--sha")
    parser.add_argument("--commands", required=True)
    parser.add_argument("--temp-root")
    parser.add_argument("--output")
    parser.add_argument("--keep-on-failure", action="store_true")
    args = parser.parse_args(argv)
    try:
        command_set = _load(pathlib.Path(args.commands))
        if args.validate_only:
            commands = validate_command_set(command_set)
            print(json.dumps({"ok": True, "valid": True, "command_count": len(commands)}, sort_keys=True))
            return 0
        if not all((args.source, args.sha, args.temp_root, args.output)):
            raise CloneGateError("clean_clone_run_arguments_incomplete")
        output = pathlib.Path(args.output)
        if output.exists() or output.is_symlink():
            raise CloneGateError("clean_clone_output_exists")
        result, retained = run_gate(
            pathlib.Path(args.source),
            args.sha,
            command_set,
            pathlib.Path(args.temp_root),
            args.keep_on_failure,
        )
        _write_new_json(output, result)
        if retained is not None:
            print(json.dumps({"ok": False, "code": "clean_clone_failed_retained", "result": result["result"]}))
        else:
            print(json.dumps({"ok": result["result"] == "PASS", "result": result["result"]}))
        return 0 if result["result"] == "PASS" else 1
    except CloneGateError as exc:
        print(json.dumps({"ok": False, "code": str(exc)}, sort_keys=True))
        return 2


if __name__ == "__main__":
    sys.exit(main())
