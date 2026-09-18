#!/usr/bin/env python3
"""Wrap an immutable, full-quality-verified release archive in a GHCR image."""

from __future__ import annotations

import json
import os
from pathlib import Path, PurePosixPath
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.error
import urllib.request

sys.dont_write_bytecode = True
ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT))

from scripts.quality import release_candidate_manifest as candidate
from scripts.packaging.container_smoke import smoke

REPOSITORY = "Victor-Xu-1/synon-biomed"
REPOSITORY_ID = 1374130212


def validate_event(event: dict, identity: dict) -> str:
    release = event.get("release", {})
    repository = event.get("repository", {})
    tag = "v" + identity["version"]
    if (
        event.get("action") != "published"
        or repository.get("full_name") != REPOSITORY
        or repository.get("id") != REPOSITORY_ID
        or release.get("tag_name") != tag
        or release.get("draft") is not False
        or release.get("immutable") is not True
    ):
        raise ValueError("An immutable published release matching product-identity.json is required")
    return tag


def validate_run(run: dict, manifest: dict) -> None:
    if (
        run.get("id") != manifest["candidate_run_id"]
        or run.get("run_attempt") != manifest["candidate_run_attempt"]
        or run.get("head_sha") != manifest["source_commit"]
        or run.get("head_branch") != "main"
        or run.get("status") != "completed"
        or run.get("conclusion") != "success"
        or run.get("path") != ".github/workflows/quality.yml"
        or run.get("repository", {}).get("id") != REPOSITORY_ID
    ):
        raise ValueError("Release archives require a successful full-quality main run for this exact source")


def extract_archive(archive: Path, destination: Path, identity: dict) -> Path:
    prefix = f'{identity["machine_slug"]}-v{identity["version"]}-linux-amd64'
    destination.mkdir(parents=True, exist_ok=False)
    with tarfile.open(archive, "r:gz") as tar:
        members = tar.getmembers()
        names: set[str] = set()
        for member in members:
            path = PurePosixPath(member.name)
            if (
                path.is_absolute() or ".." in path.parts or "\\" in member.name
                or not path.parts or path.parts[0] != prefix
                or not (member.isdir() or member.isfile()) or member.name in names
                or member.mode & 0o7000
            ):
                raise ValueError("Unsafe release archive member")
            names.add(member.name)
        if shutil.disk_usage(destination).free < sum(member.size for member in members):
            raise ValueError("Insufficient disk space for the release archive")
        tar.extractall(destination, members=members, filter="data")
    return destination / prefix


def api_json(path: str, *, absent_ok: bool = False):
    request = urllib.request.Request(
        "https://api.github.com/" + path,
        headers={
            "Authorization": "Bearer " + os.environ["GH_TOKEN"],
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=60) as response:
            return json.load(response)
    except urllib.error.HTTPError as error:
        if error.code == 404 and absent_ok:
            return None
        raise ValueError(f"GitHub metadata request failed (HTTP {error.code})") from None


def ensure_unused_image_tag(tag: str) -> None:
    page = 1
    while True:
        versions = api_json(
            f"users/Victor-Xu-1/packages/container/synon-biomed/versions?per_page=100&page={page}",
            absent_ok=True,
        )
        if versions is None:
            return
        for version in versions:
            if tag in version.get("metadata", {}).get("container", {}).get("tags", []):
                raise ValueError("This image version already exists; do not overwrite a published version")
        if len(versions) < 100:
            return
        page += 1


def prepare(event: dict, root: Path) -> tuple[str, str, Path]:
    identity, _ = candidate.load_controls(ROOT)
    tag = validate_event(event, identity)
    # Re-read the release: event payloads are not a substitute for current state.
    release = api_json(f"repos/{REPOSITORY}/releases/tags/{tag}")
    validate_event({**event, "release": release}, identity)
    artifact_dir = root / "candidate"
    subprocess.run(
        ["gh", "release", "download", tag, "--repo", REPOSITORY, "--dir", str(artifact_dir),
         "--pattern", "*.tar.gz", "--pattern", "*.sha256", "--pattern", candidate.MANIFEST_NAME],
        check=True,
    )
    manifest = candidate.read_json(artifact_dir / candidate.MANIFEST_NAME, "invalid_release_manifest")
    candidate.validate_candidate_shape(manifest)
    run = api_json(f'repos/{REPOSITORY}/actions/runs/{manifest["candidate_run_id"]}')
    validate_run(run, manifest)
    manifest = candidate.verify(
        ROOT, artifact_dir, manifest["candidate_run_id"], manifest["candidate_run_attempt"],
    )
    if os.environ.get("GITHUB_SHA") != manifest["source_commit"]:
        raise ValueError("Published tag, checked-out source and archive source must match")
    revision = manifest["source_commit"]
    ensure_unused_image_tag(tag)
    archive = artifact_dir / f'{identity["machine_slug"]}-{tag}-linux-amd64.tar.gz'
    extracted = extract_archive(archive, root / "extracted", identity)
    binary = str(extracted / "synon-go")
    subprocess.run([binary, "release-supply-chain", "verify", "--root", str(extracted)], check=True)
    subprocess.run([binary, "release-manifest", "verify", "--root", str(extracted)], check=True)
    context = root / "context"
    context.mkdir()
    extracted.rename(context / "synon-biomed")
    shutil.copyfile(ROOT / "scripts/packaging/Dockerfile", context / "Dockerfile")
    return tag, revision, context


def main() -> None:
    if os.environ.get("GITHUB_EVENT_NAME") != "release":
        raise ValueError("Container publication only accepts the release event")
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    with tempfile.TemporaryDirectory(prefix="synon-container-") as temp:
        root = Path(temp)
        tag, revision, context = prepare(event, root)
        image = f"ghcr.io/{REPOSITORY.lower()}:{tag}"
        subprocess.run(
            ["docker", "build", "--platform", "linux/amd64",
             "--build-arg", f"RELEASE_VERSION={tag[1:]}",
             "--build-arg", f"SOURCE_REVISION={revision}", "--tag", image, str(context)],
            check=True,
        )
        smoke(image, tag[1:])
        # Credentials are short-lived and isolated from the caller's Docker config.
        docker_env = {**os.environ, "DOCKER_CONFIG": str(root / "docker-auth")}
        subprocess.run(
            ["docker", "login", "ghcr.io", "--username", os.environ["GITHUB_ACTOR"], "--password-stdin"],
            input=os.environ["GH_TOKEN"], text=True, env=docker_env, check=True,
        )
        ensure_unused_image_tag(tag)
        subprocess.run(["docker", "push", image], env=docker_env, check=True)
        digest = subprocess.check_output(
            ["docker", "inspect", "--format", "{{index .RepoDigests 0}}", image], text=True,
        ).strip()
        with Path(os.environ["GITHUB_STEP_SUMMARY"]).open("a") as summary:
            summary.write(f"## Published container\n\n- Image: `{image}`\n- Digest: `{digest}`\n- Source: `{revision}`\n")
        print(f"Published {digest}")


if __name__ == "__main__":
    main()
