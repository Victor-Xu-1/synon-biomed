"""Append verified provenance to the version PR, never to main or a release."""

from __future__ import annotations

import base64
import json
import os
from pathlib import Path
import re
import subprocess
from urllib.parse import quote

if __package__:
    from . import version_provenance as provenance
else:
    import version_provenance as provenance


def api(repo: str, path: str, method: str = "GET", body: dict | None = None):
    command = ["gh", "api", "--method", method, f"repos/{repo}/{path}"]
    if body is not None:
        command += ["--input", "-"]
    result = subprocess.run(command, input=json.dumps(body) if body is not None else None,
                            capture_output=True, text=True, timeout=90, check=False)
    if result.returncode:
        raise RuntimeError(f"GitHub request failed: {method} {path.split('?')[0]}")
    return json.loads(result.stdout)


def synchronize(root: Path, repo: str, repo_id: int, base: str, number: int, request=api) -> str:
    checkout = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True, timeout=15).strip()
    if checkout != base:
        raise ValueError("trusted checkout is not the requested main revision")
    identity = request(repo, "")
    if identity["id"] != repo_id or identity["full_name"] != repo:
        raise ValueError("repository identity mismatch")
    pr = request(repo, f"pulls/{number}")
    if (pr["state"] != "open" or pr["base"]["ref"] != "main" or pr["base"]["sha"] != base
            or pr["head"]["repo"]["id"] != repo_id or pr["base"]["repo"]["id"] != repo_id
            or pr["head"]["ref"] != "release-please--branches--main"):
        raise ValueError("proposal is not on the expected main baseline and bot branch")
    head = pr["head"]["sha"]
    delta = request(repo, f"compare/{base}...{head}")
    if delta["merge_base_commit"]["sha"] != base or len(delta["files"]) >= 300:
        raise ValueError("proposal comparison is incomplete or stale")
    if any(f["status"] not in {"added", "modified"} for f in delta["files"]):
        raise ValueError("version proposal cannot rename or remove files")
    changed = {f["filename"] for f in delta["files"]}
    tree = request(repo, f"git/trees/{head}?recursive=1")
    if tree.get("truncated"):
        raise ValueError("proposal tree is incomplete")
    entries = {item["path"]: item for item in tree["tree"]}
    paths = set(provenance.projections(root)) | (changed & provenance.DERIVED)
    content = {}
    for path in sorted(paths):
        entry = entries[path]
        if entry["type"] != "blob" or entry["mode"] != "100644" or entry.get("size", 0) > 4_000_000:
            raise ValueError("proposal input must be a bounded regular file")
        blob = request(repo, f"git/blobs/{entry['sha']}")
        if blob["encoding"] != "base64":
            raise ValueError("unsupported Git blob encoding")
        content[path] = base64.b64decode("".join(blob["content"].split()), validate=True)
        if len(content[path]) > 4_000_000:
            raise ValueError("proposal blob exceeds the size limit")
    outputs = provenance.plan(root, content, changed)
    updates = [{"path": path, "mode": "100644", "type": "blob", "content": raw.decode()}
               for path, raw in outputs.items() if content.get(path) != raw]
    if not updates:
        return head
    # A concurrent main update or PR edit invalidates this exact proposal.
    if (request(repo, "git/ref/heads/main")["object"]["sha"] != base
            or request(repo, f"pulls/{number}")["head"]["sha"] != head):
        raise ValueError("proposal changed during verification")
    new_tree = request(repo, "git/trees", "POST", {"base_tree": tree["sha"], "tree": updates})
    commit = request(repo, "git/commits", "POST", {
        "message": "chore: synchronize version proposal provenance", "tree": new_tree["sha"], "parents": [head],
    })
    # force=false also fences a writer racing after the final comparison.
    request(repo, "git/refs/heads/" + quote(pr["head"]["ref"], safe=""), "PATCH",
            {"sha": commit["sha"], "force": False})
    return commit["sha"]


def main() -> None:
    proposals = json.loads(os.environ.get("VERSION_PRS") or "[]")
    if not isinstance(proposals, list) or len(proposals) > 1:
        raise ValueError("expected at most one product version proposal")
    if not proposals:
        print("No version proposal to synchronize")
        return
    number = proposals[0]["number"]
    repo, base = os.environ["GITHUB_REPOSITORY"], os.environ["GITHUB_SHA"]
    if type(number) is not int or number <= 0 or not re.fullmatch(r"[\w.-]+/[\w.-]+", repo) or not re.fullmatch(r"[0-9a-f]{40}", base):
        raise ValueError("invalid immutable proposal identity")
    root = Path(__file__).resolve().parents[2]
    print("Version provenance synchronized:", synchronize(root, repo, int(os.environ["GITHUB_REPOSITORY_ID"]), base, number))


if __name__ == "__main__":
    main()
