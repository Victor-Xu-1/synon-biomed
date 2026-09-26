from __future__ import annotations

import shutil
import subprocess
from pathlib import Path


def read_smiles_records(source: Path) -> list[tuple[str, str]]:
    """Read ordinary whitespace-delimited SMILES records with stable IDs."""
    records: list[tuple[str, str]] = []
    with source.open("r", encoding="utf-8", errors="strict") as handle:
        for raw_line in handle:
            line = raw_line.strip()
            if not line or line.startswith("#"):
                continue
            fields = line.split(maxsplit=1)
            records.append((fields[0], fields[1].strip() if len(fields) == 2 else ""))
    if not records:
        raise ValueError("SMILES input contains no valid records")
    normalized: list[tuple[str, str]] = []
    for index, (smiles, name) in enumerate(records, start=1):
        if not name:
            name = source.stem if len(records) == 1 else f"{source.stem}-{index:04d}"
        normalized.append((smiles, name))
    return normalized


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
