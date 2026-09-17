#!/usr/bin/env python3
"""Download locked Go modules with bounded retry and redacted failures."""

from __future__ import annotations

import argparse
import subprocess
import sys
import time


def download(*, attempts: int, timeout_seconds: int) -> int:
    for attempt in range(1, attempts + 1):
        try:
            completed = subprocess.run(
                ["go", "mod", "download"],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=timeout_seconds,
                check=False,
            )
        except subprocess.TimeoutExpired:
            completed = None
        if completed is not None and completed.returncode == 0:
            print(f"go-module-download: ok on attempt {attempt}")
            return 0
        if attempt < attempts:
            print(f"go-module-download: attempt {attempt} failed; retrying", file=sys.stderr)
            time.sleep(attempt)
    print(f"go-module-download: failed after {attempts} attempts", file=sys.stderr)
    return 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--attempts", type=int, default=3)
    parser.add_argument("--timeout-seconds", type=int, default=300)
    args = parser.parse_args(argv)
    if not 1 <= args.attempts <= 3:
        parser.error("--attempts must be between 1 and 3")
    if not 30 <= args.timeout_seconds <= 600:
        parser.error("--timeout-seconds must be between 30 and 600")
    return download(attempts=args.attempts, timeout_seconds=args.timeout_seconds)


if __name__ == "__main__":
    raise SystemExit(main())
