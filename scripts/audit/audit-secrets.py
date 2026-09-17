#!/usr/bin/env python3
from __future__ import annotations

import os
import re
import sys
from pathlib import Path

MAX_FILE_BYTES = 5 * 1024 * 1024
EXCLUDED_DIRECTORIES = {
    ".git",
    ".cache",
    ".mypy_cache",
    ".pytest_cache",
    ".ruff_cache",
    ".synon-go-audit",
    "dist",
    "mcp-output",
    "models",
    "node_modules",
    "release",
    "runtime",
    "uploads",
    "users",
    "vendor",
    "workspace",
}
ALLOW_MARKERS = re.compile(
    r"(?i)(?:<redacted>|placeholder|example|dummy|fixture|fake|test[-_ ]secret|"
    r"package[-_ ]secret|legacy[-_ ]secret|changeme|must-not-be-used|"
    r"wrong-static-key|responses-secret|legacy-plaintext-token|synon-go-smoke-key|"
      r"synon-local-core|wechat-config-token|feishu-config-secret|local-integration-key)"
)
ENVIRONMENT_KEY_VALUE = re.compile(
    r"[A-Z][A-Z0-9_]{2,}_(?:API_KEY|ACCESS_TOKEN|BOT_TOKEN|CLIENT_SECRET|APP_SECRET|SECRET_ACCESS_KEY)"
)
PATTERNS = (
    ("private-key", re.compile(r"-----BEGIN (?:RSA |EC |OPENSSH |DSA )?PRIVATE KEY-----")),
    ("aws-access-key", re.compile(r"(?:AKIA|ASIA)[0-9A-Z]{16}")),
    ("google-api-key", re.compile(r"AIza[0-9A-Za-z_-]{35}")),
    ("github-token", re.compile(r"(?:gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{70,})")),
    ("slack-token", re.compile(r"xox[baprs]-[0-9A-Za-z-]{20,}")),
    ("openai-api-key", re.compile(r"sk-[A-Za-z0-9]{32,}")),
    ("anthropic-api-key", re.compile(r"sk-ant-api03-[A-Za-z0-9_-]{40,}")),
    (
        "assigned-secret",
        re.compile(
            r"(?i)(?:api[_-]?key|access[_-]?token|bot[_-]?token|client[_-]?secret|"
            r"app[_-]?secret|secret[_-]?access[_-]?key)\s*[=:]\s*[\"']"
            r"(?P<assigned_value>[^\"'\s$][^\"'\s]{15,})[\"']"
        ),
    ),
)


def iter_files(root: Path):
    auditor = Path(__file__).resolve()
    for directory, names, files in os.walk(root, topdown=True, followlinks=False):
        names[:] = sorted(name for name in names if name not in EXCLUDED_DIRECTORIES)
        for name in sorted(files):
            path = Path(directory, name)
            if path.resolve() == auditor or path.is_symlink():
                continue
            try:
                if path.stat().st_size > MAX_FILE_BYTES:
                    continue
            except OSError as error:
                raise RuntimeError(f"inspect {path}: {error}") from error
            yield path


def scan(root: Path) -> list[str]:
    findings: list[str] = []
    for path in iter_files(root):
        try:
            content = path.read_bytes()
        except OSError as error:
            raise RuntimeError(f"read {path}: {error}") from error
        if b"\x00" in content:
            continue
        text = content.decode("utf-8", errors="replace")
        for line_number, line in enumerate(text.splitlines(), start=1):
            if ALLOW_MARKERS.search(line):
                continue
            for name, pattern in PATTERNS:
                match = pattern.search(line)
                if not match:
                    continue
                if name == "assigned-secret" and ENVIRONMENT_KEY_VALUE.fullmatch(match.group("assigned_value")):
                    continue
                relative = path.relative_to(root).as_posix()
                findings.append(f"{relative}:{line_number}: {name}")
    return findings


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else ".").resolve()
    if not root.is_dir() or root.is_symlink():
        print("audit-secrets: root must be a non-symlink directory", file=sys.stderr)
        return 2
    try:
        findings = scan(root)
    except RuntimeError as error:
        print(f"audit-secrets: {error}", file=sys.stderr)
        return 2
    if findings:
        print("ERROR: high-confidence credential material found", file=sys.stderr)
        print("\n".join(findings), file=sys.stderr)
        return 1
    print("secret-content-audit: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
