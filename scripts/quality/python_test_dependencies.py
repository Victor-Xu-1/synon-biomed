#!/usr/bin/env python3
"""Install locked Python test dependencies into the clean-clone private HOME."""

from __future__ import annotations

import argparse
import os
from pathlib import Path
import subprocess
import sys
import time


REQUIREMENTS = Path("assets/optional/mcp-servers/bio-tools/requirements-test.txt")


def install(*, home: Path, attempts: int, timeout_seconds: int) -> int:
    if not home.is_absolute() or home.is_symlink() or not home.is_dir():
        print("python-test-dependencies: home must be an existing absolute directory", file=sys.stderr)
        return 2
    configured_home = Path(os.environ.get("HOME", "")).resolve()
    if configured_home != home.resolve():
        print("python-test-dependencies: target must match the isolated HOME", file=sys.stderr)
        return 2
    requirements = Path.cwd() / REQUIREMENTS
    if not requirements.is_file() or requirements.is_symlink():
        print("python-test-dependencies: locked requirements are unavailable", file=sys.stderr)
        return 2
    for attempt in range(1, attempts + 1):
        completed = None
        try:
            completed = subprocess.run(
                [
                    sys.executable,
                    "-m",
                    "pip",
                    "install",
                    "--disable-pip-version-check",
                    "--no-input",
                    "--break-system-packages",
                    "--user",
                    "--requirement",
                    str(requirements),
                ],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=timeout_seconds,
                check=False,
            )
        except subprocess.TimeoutExpired:
            pass
        if completed is not None and completed.returncode == 0:
            print(f"python-test-dependencies: ok on attempt {attempt}")
            return 0
        if attempt < attempts:
            print(f"python-test-dependencies: attempt {attempt} failed; retrying", file=sys.stderr)
            time.sleep(attempt)
    print(f"python-test-dependencies: failed after {attempts} attempts", file=sys.stderr)
    return 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--home", default=os.environ.get("SYNON_CLEAN_CLONE_PYTHON_HOME", ""))
    parser.add_argument("--attempts", type=int, default=3)
    parser.add_argument("--timeout-seconds", type=int, default=600)
    args = parser.parse_args(argv)
    if not args.home:
        parser.error("--home or SYNON_CLEAN_CLONE_PYTHON_HOME is required")
    if not 1 <= args.attempts <= 3:
        parser.error("--attempts must be between 1 and 3")
    if not 60 <= args.timeout_seconds <= 900:
        parser.error("--timeout-seconds must be between 60 and 900")
    return install(home=Path(args.home).resolve(), attempts=args.attempts, timeout_seconds=args.timeout_seconds)


if __name__ == "__main__":
    raise SystemExit(main())
