"""Lossless OCI transport for already verified release archives."""

import hashlib
import json
from pathlib import Path
import re
import subprocess

ARTIFACT_TYPE = "application/vnd.synon-biomed.release.v1"


def files(root: Path) -> list[Path]:
    result = sorted(root.iterdir())
    if not result or any(not p.is_file() or p.is_symlink() for p in result):
        raise ValueError("Release bundle must contain regular files only")
    return result


def digest(path: Path) -> str:
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def build_layout(artifacts: Path, layout: Path, tag: str, revision: str, repository: str) -> str:
    names = [p.name for p in files(artifacts)]
    if not re.fullmatch(r"v\d+\.\d+\.\d+", tag) or not re.fullmatch(r"[0-9a-f]{40}", revision):
        raise ValueError("Invalid release identity")
    command = [
        "oras", "push", "--oci-layout", f"{layout}:{tag}", "--artifact-type", ARTIFACT_TYPE,
        "--annotation", f"org.opencontainers.image.source=https://github.com/{repository}",
        "--annotation", f"org.opencontainers.image.version={tag[1:]}",
        "--annotation", f"org.opencontainers.image.revision={revision}",
        "--annotation", "org.opencontainers.image.description=Verified Linux and Windows installation archives; not a runnable container",
        "--format", "json",
    ]
    command.extend(f"{name}:application/octet-stream" for name in names)
    result = json.loads(subprocess.check_output(command, cwd=artifacts, text=True))
    value = result.get("digest", "")
    if not re.fullmatch(r"sha256:[0-9a-f]{64}", value):
        raise ValueError("ORAS did not return a valid content digest")
    return value


def verify_roundtrip(
    original: Path, destination: Path, reference: str, *, local: bool = False,
    registry_config: str | None = None,
) -> None:
    command = ["oras", "pull", reference, "--output", str(destination)]
    if local:
        command.append("--oci-layout")
    if registry_config is not None:
        command.extend(["--registry-config", registry_config])
    subprocess.run(command, check=True)
    expected = {p.name: digest(p) for p in files(original)}
    actual = {p.name: digest(p) for p in files(destination)}
    if actual != expected:
        raise ValueError("OCI roundtrip changed the release artifact set or bytes")
