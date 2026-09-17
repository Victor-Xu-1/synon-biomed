#!/usr/bin/env python3
"""Run pinned P2Rank pocket prediction and emit a validated docking handoff."""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import math
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import uuid


PACK_ID = "binding-pocket-prediction.p2rank"
P2RANK_VERSION = "2.5.1"
P2RANK_ARCHIVE_SHA256 = "d243f2d9036ac053fefb9407b5fe1c85f4fe077c519fd975ac585e995feab274"
OUTPUT_MARKER = ".synon-execution-pack.json"
MAX_ARCHIVE_MEMBERS = 50_000
MAX_ARCHIVE_EXPANDED_BYTES = 2_000_000_000


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def atomic_text(path: Path, content: str) -> None:
    temporary = path.with_name(f".{path.name}.{uuid.uuid4().hex}.tmp")
    temporary.write_text(content, encoding="utf-8")
    os.replace(temporary, path)


def task_file(root: Path, raw: str, label: str) -> Path:
    path = (root / raw).resolve(strict=True)
    if root != path and root not in path.parents:
        raise ValueError(f"{label} escapes the authorized working directory")
    if not path.is_file() or path.stat().st_size <= 0:
        raise ValueError(f"{label} is missing or empty")
    return path


def output_target(root: Path, raw: str, inputs: tuple[Path, ...]) -> Path:
    target = root / raw
    if target.is_symlink():
        raise ValueError("output directory must not be a symbolic link")
    target = target.resolve()
    if target == root or root not in target.parents:
        raise ValueError("output directory must stay inside the authorized working directory")
    for source in inputs:
        if target == source or target in source.parents or source in target.parents:
            raise ValueError("output directory must not overlap an input path or its ancestors")
    if target.exists():
        marker = target / OUTPUT_MARKER
        try:
            owner = json.loads(marker.read_text(encoding="utf-8"))
        except (OSError, ValueError, TypeError):
            raise ValueError("existing output directory is not owned by the P2Rank execution pack") from None
        if owner != {"execution_pack_id": PACK_ID, "schema": "synon.execution-pack-output-owner.v1"}:
            raise ValueError("existing output directory has conflicting execution ownership")
    return target


def safe_extract_archive(archive: Path, destination: Path) -> None:
    with tarfile.open(archive, "r:gz") as bundle:
        members = bundle.getmembers()
        if not members or len(members) > MAX_ARCHIVE_MEMBERS:
            raise ValueError("P2Rank archive has an invalid member count")
        expanded = 0
        for member in members:
            candidate = Path(member.name)
            if candidate.is_absolute() or ".." in candidate.parts or member.isdev() or member.issym() or member.islnk():
                raise ValueError("P2Rank archive contains an unsafe member")
            expanded += max(0, int(member.size))
            if expanded > MAX_ARCHIVE_EXPANDED_BYTES:
                raise ValueError("P2Rank archive exceeds the expanded-size limit")
        bundle.extractall(destination)


def find_prank_launcher(runtime: Path) -> Path:
    launchers = [path for path in runtime.rglob("prank") if path.is_file()]
    if len(launchers) != 1:
        raise ValueError("P2Rank archive must contain exactly one production prank launcher")
    return launchers[0]


def java_major() -> tuple[int, str]:
    java = shutil.which("java")
    if java is None:
        raise RuntimeError("Java is unavailable; P2Rank requires Java 17 or newer")
    completed = subprocess.run(
        [java, "-version"], check=False, capture_output=True, text=True, timeout=30
    )
    version_text = (completed.stderr or completed.stdout).strip()
    match = re.search(r'version\s+"(?P<major>\d+)', version_text)
    if completed.returncode != 0 or match is None:
        raise RuntimeError("Java version could not be verified")
    major = int(match.group("major"))
    if major < 17:
        raise RuntimeError(f"P2Rank requires Java 17 or newer; observed Java {major}")
    return major, version_text.splitlines()[0]


def pdb_atoms(path: Path) -> tuple[dict[int, tuple[float, float, float, str]], list[float]]:
    atoms: dict[int, tuple[float, float, float, str]] = {}
    b_factors: list[float] = []
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        if line[:6].strip() not in {"ATOM", "HETATM"}:
            continue
        try:
            serial = int(line[6:11])
            coordinates = (float(line[30:38]), float(line[38:46]), float(line[46:54]))
        except (ValueError, IndexError):
            continue
        atoms[serial] = (*coordinates, line)
        try:
            b_factors.append(float(line[60:66]))
        except (ValueError, IndexError):
            pass
    if not atoms:
        raise ValueError("structure contains no readable PDB atom coordinates")
    return atoms, b_factors


def pdb_profile_evidence(path: Path) -> tuple[str | None, str]:
    header_lines: list[str] = []
    records: dict[str, list[str]] = {
        "HEADER": [],
        "TITLE": [],
        "COMPND": [],
        "SOURCE": [],
        "EXPDTA": [],
        "REMARK": [],
    }
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        record = line[:6].strip().upper()
        if record in {"ATOM", "HETATM"}:
            break
        if record in records:
            normalized = line.upper()
            header_lines.append(normalized)
            records[record].append(normalized[10:].strip())
        if len(header_lines) >= 400:
            break

    experiment = " ".join(records["EXPDTA"])
    crystallographic = any(value in experiment for value in ("X-RAY DIFFRACTION", "NEUTRON DIFFRACTION"))
    bfactor_independent = any(
        value in experiment
        for value in (
            "NMR",
            "ELECTRON MICROSCOPY",
            "CRYO-EM",
            "THEORETICAL MODEL",
            "PREDICTED MODEL",
        )
    )
    if crystallographic and bfactor_independent:
        return None, f"PDB EXPDTA contains conflicting profile evidence: {experiment}"
    if crystallographic:
        return "default", f"PDB EXPDTA identifies crystallography: {experiment}"
    if bfactor_independent:
        return "alphafold", f"PDB EXPDTA requires the B-factor-independent profile: {experiment}"
    if experiment:
        return None, f"PDB EXPDTA does not map to a supported automatic profile: {experiment}"

    title = " ".join(records["TITLE"])
    remarks = " ".join(records["REMARK"])
    alpha_title = bool(re.match(r"^ALPHAFOLD(?:\s|[-_])", title)) and "PREDICTION" in title
    alpha_disclaimer = "ALPHAFOLD DATA" in remarks and "THEORETICAL MODELLING ONLY" in remarks
    if alpha_title or alpha_disclaimer:
        return "alphafold", "PDB provenance records identify an AlphaFold prediction"
    if re.match(r"^(?:THEORETICAL|PREDICTED) MODEL(?:\s|$)", title):
        return "alphafold", "PDB title identifies a predicted/theoretical model"
    return None, "PDB provenance/method metadata does not determine a safe P2Rank profile"


def choose_profile(requested: str, structure: Path) -> tuple[str, str]:
    if requested != "auto":
        return requested, "explicit execution argument"
    profile, reason = pdb_profile_evidence(structure)
    if profile is None:
        raise ValueError(
            "--profile auto is ambiguous for this structure; provide verified crystallographic, "
            "AlphaFold/predicted, NMR, or cryo-EM provenance and select --profile default or alphafold; "
            f"observed evidence: {reason}"
        )
    return profile, reason


def validated_internal_state_directory(root: Path, path: Path, label: str) -> Path:
    root = root.resolve(strict=True)
    path = Path(os.path.abspath(path))
    if path == root or root not in path.parents:
        raise ValueError(f"{label} directory escapes the authorized working directory")
    relative = path.relative_to(root)
    current = root
    for part in relative.parts:
        current = current / part
        if current.exists() or current.is_symlink():
            if current.is_symlink() or not current.is_dir():
                raise ValueError(f"{label} path contains a symbolic link or non-directory component")
        else:
            current.mkdir()
        resolved = current.resolve(strict=True)
        if resolved != current or (resolved != root and root not in resolved.parents):
            raise ValueError(f"{label} directory changed during validation")
    return path


def finite_float(row: dict[str, str], key: str) -> float:
    try:
        value = float(row[key])
    except (KeyError, TypeError, ValueError):
        raise ValueError(f"P2Rank output is missing a numeric {key} value") from None
    if not math.isfinite(value):
        raise ValueError(f"P2Rank output contains a non-finite {key} value")
    return value


def parse_predictions(path: Path) -> list[dict[str, object]]:
    rows: list[dict[str, object]] = []
    with path.open("r", encoding="utf-8-sig", newline="") as handle:
        reader = csv.DictReader(handle, skipinitialspace=True)
        for raw in reader:
            normalized = {
                str(key).strip(): str(value).strip()
                for key, value in raw.items()
                if key is not None and value is not None
            }
            try:
                rank = int(normalized["rank"])
            except (KeyError, ValueError):
                raise ValueError("P2Rank output is missing an integer rank") from None
            probability = finite_float(normalized, "probability")
            if probability < 0 or probability > 1:
                raise ValueError("P2Rank probability is outside [0, 1]")
            atom_ids = [int(value) for value in re.findall(r"\d+", normalized.get("surf_atom_ids", ""))]
            rows.append(
                {
                    "name": normalized.get("name", f"pocket{rank}"),
                    "rank": rank,
                    "score": finite_float(normalized, "score"),
                    "probability": probability,
                    "center": [
                        finite_float(normalized, "center_x"),
                        finite_float(normalized, "center_y"),
                        finite_float(normalized, "center_z"),
                    ],
                    "residue_ids": normalized.get("residue_ids", ""),
                    "surface_atom_ids": sorted(set(atom_ids)),
                }
            )
    rows.sort(key=lambda row: int(row["rank"]))
    if not rows or [int(row["rank"]) for row in rows] != list(range(1, len(rows) + 1)):
        raise ValueError("P2Rank output must contain contiguous rank-ordered pockets")
    return rows


def build_candidates(
    predictions: list[dict[str, object]],
    atoms: dict[int, tuple[float, float, float, str]],
    minimum_box_size: float,
    box_padding: float,
) -> list[dict[str, object]]:
    candidates: list[dict[str, object]] = []
    for prediction in predictions:
        ids = [int(value) for value in prediction["surface_atom_ids"]]
        coordinates = [atoms[value][:3] for value in ids if value in atoms]
        if not ids or len(coordinates) != len(ids):
            raise ValueError(f"pocket rank {prediction['rank']} has unresolved surface atom identifiers")
        center = [float(value) for value in prediction["center"]]
        sizes = []
        for axis in range(3):
            radius = max(abs(point[axis] - center[axis]) for point in coordinates)
            size = max(minimum_box_size, 2.0 * (radius + box_padding))
            if not math.isfinite(size) or size > 100:
                raise ValueError(f"pocket rank {prediction['rank']} requires an invalid docking-box size")
            sizes.append(round(size, 4))
        candidates.append(
            {
                **prediction,
                "center": [round(value, 4) for value in center],
                "size": sizes,
                "surface_atom_count": len(ids),
            }
        )
    return candidates


def write_candidates(path: Path, candidates: list[dict[str, object]]) -> None:
    fields = [
        "rank", "name", "score", "probability", "center_x", "center_y", "center_z",
        "size_x", "size_y", "size_z", "surface_atom_count", "residue_ids",
    ]
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        for candidate in candidates:
            center = candidate["center"]
            size = candidate["size"]
            writer.writerow(
                {
                    "rank": candidate["rank"], "name": candidate["name"],
                    "score": f"{float(candidate['score']):.6f}",
                    "probability": f"{float(candidate['probability']):.6f}",
                    "center_x": center[0], "center_y": center[1], "center_z": center[2],
                    "size_x": size[0], "size_y": size[1], "size_z": size[2],
                    "surface_atom_count": candidate["surface_atom_count"],
                    "residue_ids": candidate["residue_ids"],
                }
            )


def write_selected_atoms(
    path: Path,
    candidate: dict[str, object],
    atoms: dict[int, tuple[float, float, float, str]],
) -> None:
    lines = [atoms[int(serial)][3] for serial in candidate["surface_atom_ids"]]
    atomic_text(path, "\n".join(lines + ["END", ""]))


def promote_output(root: Path, staging: Path, target: Path, token: str) -> dict[str, object]:
    previous: Path | None = None
    if target.exists():
        history = validated_internal_state_directory(
            root, root / ".p2rank-generations" / target.name, "output history"
        )
        previous = history / token
        if previous.exists() or previous.is_symlink():
            raise RuntimeError("P2Rank output history path already exists")
        if target.is_symlink() or not target.is_dir():
            raise RuntimeError("P2Rank output target changed before promotion")
        history = validated_internal_state_directory(root, history, "output history")
        target.rename(previous)
    try:
        staging.rename(target)
    except Exception:
        if previous is not None and not target.exists() and previous.exists():
            previous.rename(target)
        raise
    return {
        "schema": "synon.execution-pack-output-promotion.v1",
        "execution_pack_id": PACK_ID,
        "current": str(target.relative_to(root)),
        "previous": str(previous.relative_to(root)) if previous is not None else None,
    }


def parser() -> argparse.ArgumentParser:
    value = argparse.ArgumentParser(description="Governed P2Rank binding-pocket prediction pack")
    value.add_argument("--structure", required=True)
    value.add_argument("--p2rank-archive", required=True)
    value.add_argument("--method", required=True, choices=["P2Rank"])
    value.add_argument("--profile", choices=["auto", "default", "alphafold"], default="auto")
    value.add_argument("--top-k", type=int, default=5)
    value.add_argument("--threads", type=int, default=4)
    value.add_argument("--minimum-box-size", type=float, default=20.0)
    value.add_argument("--box-padding", type=float, default=6.0)
    value.add_argument("--output-dir", default="pocket_detection")
    return value


def main() -> int:
    args = parser().parse_args()
    if args.top_k < 1 or args.top_k > 20 or args.threads < 1 or args.threads > 8:
        raise ValueError("--top-k must be 1..20 and --threads must be 1..8")
    if args.minimum_box_size < 8 or args.minimum_box_size > 100 or args.box_padding < 0 or args.box_padding > 30:
        raise ValueError("box-size controls are outside the accepted range")
    root = Path.cwd().resolve()
    structure = task_file(root, args.structure, "structure")
    archive = task_file(root, args.p2rank_archive, "P2Rank archive")
    if structure.suffix.lower() != ".pdb":
        raise ValueError("P2Rank docking handoff v1 requires a PDB structure")
    archive_sha = sha256_file(archive)
    if archive_sha != P2RANK_ARCHIVE_SHA256:
        raise ValueError("P2Rank archive SHA-256 does not match the pinned 2.5.1 release")
    target = output_target(root, args.output_dir, (structure, archive))
    token = uuid.uuid4().hex
    staging = root / f".p2rank-output-{token}"
    runtime = root / f".p2rank-runtime-{token}"
    failure_root = validated_internal_state_directory(root, root / ".p2rank-failures", "failure")
    staging.mkdir()
    runtime.mkdir()
    marker = {"execution_pack_id": PACK_ID, "schema": "synon.execution-pack-output-owner.v1"}
    atomic_text(staging / OUTPUT_MARKER, json.dumps(marker, sort_keys=True) + "\n")
    log_path = staging / "p2rank.log"
    try:
        java_version, java_witness = java_major()
        atoms, _b_factors = pdb_atoms(structure)
        profile, profile_reason = choose_profile(args.profile, structure)
        safe_extract_archive(archive, runtime)
        launcher = find_prank_launcher(runtime)
        raw_output = runtime / "prediction-output"
        command = [
            "bash", str(launcher), "predict", "-f", str(structure), "-o", str(raw_output),
            "-visualizations", "0", "-threads", str(args.threads),
        ]
        if profile == "alphafold":
            command.extend(["-c", "alphafold"])
        with log_path.open("w", encoding="utf-8") as log_handle:
            log_handle.write(json.dumps({"command": command, "profile_reason": profile_reason}, ensure_ascii=False) + "\n")
            log_handle.flush()
            completed = subprocess.run(
                command, check=False, stdout=log_handle, stderr=subprocess.STDOUT,
                text=True, timeout=900, cwd=launcher.parent,
            )
        if completed.returncode != 0:
            tail = log_path.read_text(encoding="utf-8", errors="replace")[-4000:]
            raise RuntimeError(f"P2Rank exited with code {completed.returncode}:\n{tail}")
        prediction_files = list(raw_output.rglob("*_predictions.csv"))
        if len(prediction_files) != 1:
            raise RuntimeError("P2Rank did not produce exactly one predictions CSV")
        raw_predictions = prediction_files[0]
        predictions = parse_predictions(raw_predictions)[: args.top_k]
        candidates = build_candidates(predictions, atoms, args.minimum_box_size, args.box_padding)
        selected = candidates[0]
        candidates_path = staging / "pocket_candidates.csv"
        write_candidates(candidates_path, candidates)
        shutil.copy2(raw_predictions, staging / "p2rank_predictions.csv")
        params = list(raw_output.rglob("params.txt"))
        if len(params) == 1:
            shutil.copy2(params[0], staging / "p2rank_params.txt")
        write_selected_atoms(staging / "selected_pocket_atoms.pdb", selected, atoms)
        source_sha = sha256_file(structure)
        selection = {
            "schema": "synon.binding-pocket-prediction.v1",
            "status": "passed",
            "source": {"file": structure.name, "sha256": source_sha},
            "method": {
                "name": args.method, "version": P2RANK_VERSION, "profile": profile,
                "profile_reason": profile_reason, "archive_sha256": archive_sha,
                "java_major": java_version,
            },
            "selection_policy": "highest P2Rank score; rank 1",
            "selection": {
                "rank": selected["rank"], "name": selected["name"],
                "score": selected["score"], "probability": selected["probability"],
                "center_angstrom": dict(zip(("x", "y", "z"), selected["center"])),
                "size_angstrom": dict(zip(("x", "y", "z"), selected["size"])),
                "box_definition": "P2Rank centroid with surface-atom extent plus explicit padding",
                "surface_atom_count": selected["surface_atom_count"],
                "residue_ids": selected["residue_ids"],
            },
            "candidate_count": len(candidates),
            "outputs": ["pocket_candidates.csv", "p2rank_predictions.csv", "selected_pocket_atoms.pdb", "p2rank.log"],
        }
        selection_path = staging / "pocket_selection.json"
        atomic_text(selection_path, json.dumps(selection, ensure_ascii=False, indent=2) + "\n")
        checks = {
            "source_integrity": len(source_sha) == 64,
            "archive_integrity": archive_sha == P2RANK_ARCHIVE_SHA256,
            "engine_identity": args.method == "P2Rank" and java_version >= 17,
            "rank_integrity": int(selected["rank"]) == 1 and len(candidates) >= 1,
            "probability_integrity": 0 <= float(selected["probability"]) <= 1,
            "box_integrity": all(8 <= float(value) <= 100 for value in selected["size"]),
            "output_integrity": all(
                (staging / name).is_file() and (staging / name).stat().st_size > 0
                for name in selection["outputs"] + ["pocket_selection.json"]
            ),
        }
        validation = {
            "schema": "synon.execution-pack-validation.v4",
            "execution_pack_id": PACK_ID,
            "overall_pass": all(checks.values()),
            "checks": checks,
            "inputs": {"structure": source_sha, "p2rank_archive": archive_sha},
            "pocket_selection_sha256": sha256_file(selection_path),
            "selected_rank": selected["rank"],
            "candidate_count": len(candidates),
            "java_witness": java_witness,
            "errors": [],
        }
        if not validation["overall_pass"]:
            raise RuntimeError("P2Rank execution validation failed")
        atomic_text(staging / "pocket_validation.json", json.dumps(validation, ensure_ascii=False, indent=2) + "\n")
        shutil.rmtree(runtime)
        promotion = promote_output(root, staging, target, token)
        atomic_text(target / "promotion.json", json.dumps(promotion, ensure_ascii=False, indent=2) + "\n")
        print(
            f"P2Rank {P2RANK_VERSION} selected pocket rank 1: probability "
            f"{float(selected['probability']):.3f}; center {selected['center']}; size {selected['size']}"
        )
        print(f"Validated outputs: {target.relative_to(root)}")
        return 0
    except Exception as error:
        failure_root = validated_internal_state_directory(root, root / ".p2rank-failures", "failure")
        failure = failure_root / token
        atomic_text(staging / "failure.json", json.dumps({"error": str(error), "execution_pack_id": PACK_ID}, ensure_ascii=False, indent=2) + "\n")
        failure_root = validated_internal_state_directory(root, root / ".p2rank-failures", "failure")
        failure = failure_root / token
        if not failure.exists() and not failure.is_symlink():
            staging.rename(failure)
        if runtime.exists():
            shutil.rmtree(runtime, ignore_errors=True)
        raise


if __name__ == "__main__":
    raise SystemExit(main())
