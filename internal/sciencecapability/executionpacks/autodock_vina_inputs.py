from __future__ import annotations

import shutil
import subprocess
from pathlib import Path


def convert_ligand_source(
    source: Path,
    work: Path,
    log: list[dict[str, object]],
) -> Path:
    """Normalize container formats while preserving the original input as evidence."""
    if source.suffix.lower() != ".cdx":
        return source
    executable = shutil.which("obabel")
    if executable is None:
        raise RuntimeError("documented CLI is unavailable: obabel")
    converted = work / "converted_ligands.sdf"
    completed = subprocess.run(
        [executable, "-icdx", str(source), "-osdf", "-O", str(converted)],
        text=True,
        capture_output=True,
        check=False,
    )
    log.append(
        {
            "argv": ["obabel", "-icdx", str(source), "-osdf", "-O", str(converted)],
            "returncode": completed.returncode,
            "stdout": completed.stdout,
            "stderr": completed.stderr,
        }
    )
    if completed.returncode != 0:
        raise RuntimeError(f"documented invocation failed: obabel exit={completed.returncode}")
    if not converted.is_file() or converted.stat().st_size == 0:
        raise RuntimeError("CDX conversion did not produce a non-empty SDF")
    return converted
