#!/usr/bin/env python3
"""Validate the public repository's community and maintenance surfaces."""

from __future__ import annotations

import argparse
import pathlib
import re
import sys


REQUIRED_FILES = (
    "CONTRIBUTING.md",
    "SECURITY.md",
    "CODE_OF_CONDUCT.md",
    ".github/ISSUE_TEMPLATE/bug_report.yml",
    ".github/ISSUE_TEMPLATE/feature_request.yml",
    ".github/ISSUE_TEMPLATE/config.yml",
    "docs/governance/repository-maintenance.md",
    "docs/THIRD_PARTY.md",
)
LOCAL_LINK = re.compile(r"\[[^]]+\]\(([^)#]+)(?:#[^)]*)?\)")


def validate(repo: pathlib.Path) -> list[str]:
    errors: list[str] = []
    for relative in REQUIRED_FILES:
        path = repo / relative
        if not path.is_file() or path.is_symlink():
            errors.append(f"missing required governance file: {relative}")

    readme = repo / "README.md"
    if readme.is_file():
        for target in LOCAL_LINK.findall(readme.read_text(encoding="utf-8")):
            if target.startswith(("http://", "https://", "mailto:")):
                continue
            candidate = (readme.parent / target).resolve()
            try:
                candidate.relative_to(repo.resolve())
            except ValueError:
                errors.append(f"README link escapes repository: {target}")
                continue
            if not candidate.is_file():
                errors.append(f"README link target is missing: {target}")

    for relative in (
        ".github/ISSUE_TEMPLATE/bug_report.yml",
        ".github/ISSUE_TEMPLATE/feature_request.yml",
    ):
        path = repo / relative
        if path.is_file():
            content = path.read_text(encoding="utf-8")
            for key in ("name:", "description:", "body:"):
                if key not in content:
                    errors.append(f"issue template lacks {key}: {relative}")

    return errors


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--repo", type=pathlib.Path, default=pathlib.Path.cwd())
    args = parser.parse_args()
    errors = validate(args.repo.resolve())
    if errors:
        for error in errors:
            print(f"repository-governance: {error}", file=sys.stderr)
        return 1
    print("repository-governance: ok")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
