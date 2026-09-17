#!/usr/bin/env python3
"""Measure comparable local runtime costs for Synon Go and SynonBiomed v1.1.

The harness uses only read-only HTTP contracts that exist in both runtimes. It
starts each runtime with an isolated home/data directory and never inherits
provider or channel credentials from the caller.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import hashlib
import json
import os
import pathlib
import platform
import signal
import socket
import statistics
import subprocess
import tempfile
import time
import urllib.request
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import Any


READ_PATHS = {
    "health": "/health",
    "sessionDiscovery": "/api/frames?project_id=benchmark-missing&root_only=true&limit=10",
    "eventStatus": "/api/status/update",
    "providerControl": "/api/compute/providers",
}


def percentile(values: list[float], quantile: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    index = min(len(ordered) - 1, max(0, int(round((len(ordered) - 1) * quantile))))
    return round(ordered[index], 3)


def summarize(values: list[float]) -> dict[str, float]:
    return {
        "min": round(min(values), 3),
        "median": round(statistics.median(values), 3),
        "p95": percentile(values, 0.95),
        "p99": percentile(values, 0.99),
        "max": round(max(values), 3),
    }


def free_port() -> int:
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def file_sha256(path: pathlib.Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def git_revision(root: pathlib.Path) -> str:
    result = subprocess.run(
        ["git", "-C", str(root), "rev-parse", "HEAD"],
        check=False,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip() if result.returncode == 0 else "unversioned"


def git_dirty(root: pathlib.Path) -> bool | None:
    result = subprocess.run(
        ["git", "-C", str(root), "status", "--porcelain", "--untracked-files=normal"],
        check=False,
        capture_output=True,
        text=True,
    )
    return bool(result.stdout.strip()) if result.returncode == 0 else None


def clean_environment(home: pathlib.Path) -> dict[str, str]:
    allowed = ("PATH", "LANG", "LC_ALL", "TZ", "TMPDIR")
    env = {key: os.environ[key] for key in allowed if key in os.environ}
    env.update({"HOME": str(home), "NO_PROXY": "127.0.0.1,localhost,::1", "no_proxy": "127.0.0.1,localhost,::1"})
    return env


def http_latency_ms(url: str, timeout: float = 5.0) -> float:
    request = urllib.request.Request(url, headers={"Accept": "application/json"})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    started = time.perf_counter()
    with opener.open(request, timeout=timeout) as response:
        response.read()
        if response.status != 200:
            raise RuntimeError(f"{url} returned HTTP {response.status}")
    return (time.perf_counter() - started) * 1000


def process_tree_rss_kb(root_pid: int) -> int:
    processes: dict[int, tuple[int, int]] = {}
    for entry in pathlib.Path("/proc").iterdir():
        if not entry.name.isdigit():
            continue
        try:
            status: dict[str, str] = {}
            for line in (entry / "status").read_text().splitlines():
                if ":" in line:
                    key, value = line.split(":", 1)
                    status[key] = value.strip()
            processes[int(entry.name)] = (int(status.get("PPid", "0")), int(status.get("VmRSS", "0 kB").split()[0]))
        except (FileNotFoundError, PermissionError, ValueError):
            continue
    descendants = {root_pid}
    changed = True
    while changed:
        changed = False
        for pid, (parent, _) in processes.items():
            if parent in descendants and pid not in descendants:
                descendants.add(pid)
                changed = True
    return sum(processes.get(pid, (0, 0))[1] for pid in descendants)


@dataclass
class RuntimeSpec:
    name: str
    argv: list[str]
    env: dict[str, str]
    base_url: str


class RuntimeProcess:
    def __init__(self, spec: RuntimeSpec, log_path: pathlib.Path) -> None:
        self.spec = spec
        self.log = log_path.open("wb")
        self.process: subprocess.Popen[bytes] | None = None

    def start(self, timeout: float) -> float:
        started = time.perf_counter()
        self.process = subprocess.Popen(
            self.spec.argv,
            env=self.spec.env,
            stdout=self.log,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
        while time.perf_counter() - started < timeout:
            if self.process.poll() is not None:
                raise RuntimeError(f"{self.spec.name} exited with {self.process.returncode}; see {self.log.name}")
            try:
                http_latency_ms(self.spec.base_url + READ_PATHS["health"], timeout=0.5)
                return (time.perf_counter() - started) * 1000
            except Exception:
                time.sleep(0.025)
        raise TimeoutError(f"{self.spec.name} did not become healthy within {timeout}s")

    def stop(self) -> None:
        if self.process is not None and self.process.poll() is None:
            os.killpg(self.process.pid, signal.SIGTERM)
            try:
                self.process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                os.killpg(self.process.pid, signal.SIGKILL)
                self.process.wait(timeout=5)
        self.log.close()

    @property
    def pid(self) -> int:
        if self.process is None:
            raise RuntimeError("runtime has not started")
        return self.process.pid


def request_series(base_url: str, path: str, count: int) -> list[float]:
    return [http_latency_ms(base_url + path) for _ in range(count)]


def concurrent_series(base_url: str, path: str, count: int, concurrency: int) -> tuple[list[float], float]:
    started = time.perf_counter()
    with concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as executor:
        values = list(executor.map(lambda _: http_latency_ms(base_url + path), range(count)))
    elapsed = time.perf_counter() - started
    return values, round(count / elapsed, 3)


def measure_runtime(spec: RuntimeSpec, root: pathlib.Path, args: argparse.Namespace) -> dict[str, Any]:
    startup: list[float] = []
    recovery: list[float] = []
    rss: list[float] = []
    for attempt in range(args.startup_repeats):
        runtime = RuntimeProcess(spec, root / f"{spec.name}-startup-{attempt}.log")
        try:
            startup.append(runtime.start(args.startup_timeout))
            time.sleep(args.idle_seconds)
            rss.append(float(process_tree_rss_kb(runtime.pid)))
        finally:
            runtime.stop()
        if attempt + 1 < args.startup_repeats:
            restarted = RuntimeProcess(spec, root / f"{spec.name}-recovery-{attempt}.log")
            try:
                recovery.append(restarted.start(args.startup_timeout))
            finally:
                restarted.stop()

    runtime = RuntimeProcess(spec, root / f"{spec.name}-requests.log")
    try:
        runtime.start(args.startup_timeout)
        time.sleep(args.idle_seconds)
        health = request_series(spec.base_url, READ_PATHS["health"], args.serial_requests)
        sessions, sessions_rps = concurrent_series(
            spec.base_url, READ_PATHS["sessionDiscovery"], args.concurrent_requests, args.concurrency
        )
        events, events_rps = concurrent_series(
            spec.base_url, READ_PATHS["eventStatus"], args.concurrent_requests, args.concurrency
        )
        provider = request_series(spec.base_url, READ_PATHS["providerControl"], args.serial_requests)
    finally:
        runtime.stop()
    return {
        "startupMs": summarize(startup),
        "recoveryMs": summarize(recovery or startup),
        "idleProcessTreeRssKiB": summarize(rss),
        "healthLatencyMs": summarize(health),
        "concurrentSessionDiscovery": {"requestsPerSecond": sessions_rps, "latencyMs": summarize(sessions)},
        "eventStatusUnderLoad": {"requestsPerSecond": events_rps, "lagMs": summarize(events)},
        "providerControlLatencyMs": summarize(provider),
    }


def improvement(lower_candidate: float, lower_baseline: float, lower_is_better: bool = True) -> float:
    if lower_baseline == 0:
        return 0.0
    value = (lower_baseline - lower_candidate) / lower_baseline * 100 if lower_is_better else (lower_candidate - lower_baseline) / lower_baseline * 100
    return round(value, 2)


def build_specs(args: argparse.Namespace, root: pathlib.Path) -> tuple[RuntimeSpec, RuntimeSpec]:
    go_port = free_port()
    v1_port = free_port()
    while v1_port == go_port:
        v1_port = free_port()
    go_home, v1_home = root / "go-home", root / "v1-home"
    go_home.mkdir()
    v1_home.mkdir()
    go_env = clean_environment(go_home)
    go_env.update({"SYNON_HOME": str(go_home), "SYNON_ADDRESS": f"127.0.0.1:{go_port}"})
    v1_env = clean_environment(v1_home)
    v1_env.update({"SYNON_DISABLE_NETWORK_GUARD": "1", "SYNON_DATA_DIR": str(v1_home / "data")})
    v1_root = pathlib.Path(args.v11_root).resolve()
    return (
        RuntimeSpec("synon-go-v4.0.2", [str(pathlib.Path(args.go_binary).resolve())], go_env, f"http://127.0.0.1:{go_port}"),
        RuntimeSpec(
            "synonbiomed-v1.1",
            [
                str(pathlib.Path(args.bun).expanduser().resolve()),
                str(v1_root / "runtime/server/synonbiomed.bundle.js"),
                "serve", "--config", str(v1_root / "synonbiomed.config.toml"),
                "--data-dir", str(v1_home / "data"), "--assets-root", str(v1_root / "runtime/assets"),
                "--host", "127.0.0.1", "--port", str(v1_port),
            ],
            v1_env,
            f"http://127.0.0.1:{v1_port}",
        ),
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--go-binary", default="dist/synon-go")
    parser.add_argument("--go-root", default=".")
    parser.add_argument("--v11-root", default="/home/victor_1/synonbiomed-v1.1")
    parser.add_argument("--bun", default="~/.bun/bin/bun")
    parser.add_argument("--output", default="docs/compatibility/evidence/runtime-performance-v1.1-vs-go.json")
    parser.add_argument("--startup-repeats", type=int, default=5)
    parser.add_argument("--startup-timeout", type=float, default=30)
    parser.add_argument("--idle-seconds", type=float, default=1)
    parser.add_argument("--serial-requests", type=int, default=100)
    parser.add_argument("--concurrent-requests", type=int, default=400)
    parser.add_argument("--concurrency", type=int, default=32)
    args = parser.parse_args()
    for value in (args.startup_repeats, args.serial_requests, args.concurrent_requests, args.concurrency):
        if value < 1:
            parser.error("repeat, request, and concurrency values must be positive")
    with tempfile.TemporaryDirectory(prefix="synon-runtime-benchmark-") as temporary:
        root = pathlib.Path(temporary)
        go_spec, v1_spec = build_specs(args, root)
        candidate = measure_runtime(go_spec, root, args)
        baseline = measure_runtime(v1_spec, root, args)
    comparison = {
        "startupMedianPercent": improvement(candidate["startupMs"]["median"], baseline["startupMs"]["median"]),
        "idleRssMedianPercent": improvement(candidate["idleProcessTreeRssKiB"]["median"], baseline["idleProcessTreeRssKiB"]["median"]),
        "healthP95Percent": improvement(candidate["healthLatencyMs"]["p95"], baseline["healthLatencyMs"]["p95"]),
        "sessionThroughputPercent": improvement(candidate["concurrentSessionDiscovery"]["requestsPerSecond"], baseline["concurrentSessionDiscovery"]["requestsPerSecond"], False),
        "eventLagP95Percent": improvement(candidate["eventStatusUnderLoad"]["lagMs"]["p95"], baseline["eventStatusUnderLoad"]["lagMs"]["p95"]),
        "providerControlP95Percent": improvement(candidate["providerControlLatencyMs"]["p95"], baseline["providerControlLatencyMs"]["p95"]),
        "recoveryMedianPercent": improvement(candidate["recoveryMs"]["median"], baseline["recoveryMs"]["median"]),
    }
    superior = sorted(key for key, value in comparison.items() if value >= 5)
    regressed = sorted(key for key, value in comparison.items() if value <= -5)
    v1_bundle = pathlib.Path(args.v11_root).resolve() / "runtime/server/synonbiomed.bundle.js"
    report = {
        "schemaVersion": 1,
        "capturedAt": datetime.now(timezone.utc).isoformat(),
        "environment": {
            "platform": platform.platform(),
            "machine": platform.machine(),
            "cpuCount": os.cpu_count(),
            "goRevision": git_revision(pathlib.Path(args.go_root).resolve()),
            "goSourceDirty": git_dirty(pathlib.Path(args.go_root).resolve()),
            "v11Revision": git_revision(pathlib.Path(args.v11_root).resolve()),
            "v11SourceDirty": git_dirty(pathlib.Path(args.v11_root).resolve()),
            "goBinarySha256": file_sha256(pathlib.Path(args.go_binary).resolve()),
            "bunBinarySha256": file_sha256(pathlib.Path(args.bun).expanduser().resolve()),
            "v11BundleSha256": file_sha256(v1_bundle),
        },
        "method": {
            "isolation": "fresh temporary home/data directory; credentials and proxy variables not inherited",
            "paths": READ_PATHS,
            "startupRepeats": args.startup_repeats,
            "serialRequests": args.serial_requests,
            "concurrentRequests": args.concurrent_requests,
            "concurrency": args.concurrency,
            "providerScope": "local provider-control route only; paid/live provider invocation remains credential-gated",
        },
        "candidate": candidate,
        "baseline": baseline,
        "candidateImprovementPercent": comparison,
        "assessment": {
            "thresholdPercent": 5,
            "superiorMetrics": superior,
            "regressedMetrics": regressed,
            "note": "Differences within +/-5% are reported as approximately equivalent; this threshold is operational, not a statistical confidence interval.",
        },
    }
    output = pathlib.Path(args.output)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(report, indent=2, sort_keys=True) + "\n")
    print(output)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
