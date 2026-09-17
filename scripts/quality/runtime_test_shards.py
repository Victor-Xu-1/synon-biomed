#!/usr/bin/env python3
"""Run the complete Go test inventory in bounded, independently rerunnable jobs.

The timeout remains per test process. Large packages are split by top-level
test name; every subtest stays with its parent. Race work uses smaller jobs and
process batches. No cached result, failure exclusion or shortened assertion is used.
"""

from __future__ import annotations

import argparse
from collections import defaultdict, deque
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile

if __package__:
    from .runtime_test_inventory import PACKAGE_SHARDS, matrix, partition, plan, test_batches
else:
    from runtime_test_inventory import PACKAGE_SHARDS, matrix, partition, plan, test_batches


def execute(repo: Path, test_plan: dict, log_dir: Path) -> int:
    log_dir = log_dir.resolve()
    if log_dir == repo or repo in log_dir.parents:
        raise ValueError("test logs must be outside the source repository")
    log_dir.mkdir(parents=True, exist_ok=True)
    (log_dir / "plan.json").write_text(json.dumps(test_plan, indent=2) + "\n")
    print(f"Runtime shard {test_plan['group']}/{test_plan['index']} race={test_plan['race']}: "
          f"{len(test_plan['packages'])} packages, {sum(map(len, test_plan['selected'].values()))} tests, "
          f"{len(test_plan['commands'])} process batches", flush=True)
    env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1")
    completed: dict[tuple[str, str], dict] = {}
    diagnostics: dict[tuple[str, str], deque] = defaultdict(lambda: deque(maxlen=40))
    package_results: dict[str, str] = {}
    duplicates: set[tuple[str, str]] = set()
    batch_results = []
    with (log_dir / "events.jsonl").open("w") as raw_log:
        for index, command in enumerate(test_plan["commands"]):
            print(f"Process batch {index + 1}/{len(test_plan['commands'])}", flush=True)
            process = subprocess.Popen(command, cwd=repo, env=env, text=True,
                                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, bufsize=1)
            assert process.stdout is not None
            try:
                for line in process.stdout:
                    raw_log.write(line)
                    try:
                        event = json.loads(line)
                    except json.JSONDecodeError:
                        print(line, end="", flush=True)
                        continue
                    action, name = event.get("Action"), event.get("Test", "")
                    key = (event.get("Package", ""), name.split("/")[0])
                    if action in {"pass", "fail", "skip"}:
                        if name and "/" not in name:
                            if key in completed:
                                duplicates.add(key)
                            completed[key] = event
                            if action == "fail":
                                print("".join(diagnostics[key]), end="", flush=True)
                            diagnostics.pop(key, None)
                            print(f"{action.upper()} {name} {event.get('Elapsed', 0):.3f}s", flush=True)
                        elif not name:
                            if package_results.get(key[0]) != "fail":
                                package_results[key[0]] = action
                            if action == "fail":
                                for pending in list(diagnostics):
                                    if pending[0] == key[0]:
                                        print("".join(diagnostics.pop(pending)), end="", flush=True)
                            print(f"{action.upper()} {key[0]} {event.get('Elapsed', 0):.3f}s", flush=True)
                    elif action == "output":
                        output = event.get("Output", "")
                        if not name or "panic:" in output or "WARNING: DATA RACE" in output:
                            print(output, end="", flush=True)
                        if name:
                            diagnostics[key].append(output[-4096:])
                batch_results.append(process.wait())
            finally:
                process.stdout.close()
                if process.poll() is None:
                    process.terminate()
                    process.wait()
    result = 1 if any(batch_results) else 0
    expected = {(package, name) for package, names in test_plan["selected"].items() for name in names}
    observed = set(completed)
    missing = sorted(expected - observed)
    unexpected = sorted(observed - expected)
    missing_packages = sorted(set(test_plan["packages"]) - package_results.keys())
    if result == 0 and (missing or unexpected or missing_packages or duplicates):
        result = 1
        print("ERROR: runtime shard did not execute its complete planned inventory", file=sys.stderr)
    summary = {key: test_plan[key] for key in ("group", "index", "race", "inventory_count", "inventory_sha256")}
    summary.update({"exit_code": result, "executed_top_level": len(completed),
                    "batch_exit_codes": batch_results, "duplicate_tests": sorted(duplicates),
                    "missing_tests": missing, "unexpected_tests": unexpected, "missing_packages": missing_packages,
                    "packages": package_results,
                    "slowest_tests": [{"package": package, "test": name, "elapsed": event.get("Elapsed", 0), "result": event["Action"]}
                                      for (package, name), event in sorted(completed.items(),
                                                                key=lambda pair: pair[1].get("Elapsed", 0), reverse=True)[:20]]})
    (log_dir / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(f"Runtime evidence: {log_dir}", flush=True)
    return result


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=Path.cwd())
    parser.add_argument("--matrix", action="store_true")
    parser.add_argument("--group", choices=["core", *PACKAGE_SHARDS])
    parser.add_argument("--index", type=int, default=0)
    parser.add_argument("--race", action="store_true")
    parser.add_argument("--plan-only", action="store_true")
    parser.add_argument("--log-dir", type=Path)
    args = parser.parse_args()
    if args.matrix:
        print(json.dumps(matrix(args.race), separators=(",", ":")))
        return 0
    if not args.group:
        parser.error("--group is required outside --matrix")
    repo = args.repo.resolve()
    test_plan = plan(repo, args.group, args.index, args.race)
    if args.plan_only:
        print(json.dumps(test_plan, indent=2))
        return 0
    log_dir = args.log_dir or Path(tempfile.mkdtemp(prefix="synon-runtime-tests-"))
    return execute(repo, test_plan, log_dir)


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, RuntimeError, OSError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        sys.exit(1)
