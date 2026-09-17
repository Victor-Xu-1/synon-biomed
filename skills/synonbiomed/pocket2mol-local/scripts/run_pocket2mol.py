#!/usr/bin/env python3
"""Run an official Pocket2Mol checkout and normalize its validated SDF output."""

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
import sys
import time

import torch
import yaml
from rdkit import Chem


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def require_file(path: Path, label: str) -> Path:
    resolved = path.resolve(strict=True)
    if not resolved.is_file():
        raise ValueError(f"{label} must be a regular file")
    return resolved


def load_checkpoint_acquisition(path: Path) -> tuple[Path, Path, dict]:
    receipt_path = require_file(path, "checkpoint acquisition receipt")
    try:
        payload = json.loads(receipt_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError("checkpoint acquisition receipt is not readable JSON") from exc
    if not isinstance(payload, dict) or payload.get("schema") != "synon.pocket2mol-checkpoint-acquisition.v1":
        raise ValueError("checkpoint acquisition receipt has the wrong schema")
    if payload.get("status") != "passed" or payload.get("source_kind") != "google_drive_folder":
        raise ValueError("checkpoint acquisition receipt is not a passing official-folder receipt")
    source_url = str(payload.get("source_url", ""))
    if "drive.google.com/drive/folders/" not in source_url:
        raise ValueError("checkpoint acquisition receipt did not retain the official folder URL")
    checkpoint = require_file(Path(str(payload.get("checkpoint", ""))), "Pocket2Mol checkpoint")
    if int(payload.get("size_bytes", -1)) != checkpoint.stat().st_size:
        raise ValueError("checkpoint size differs from the acquisition receipt")
    if str(payload.get("sha256", "")).lower() != sha256_file(checkpoint):
        raise ValueError("checkpoint hash differs from the acquisition receipt")
    return receipt_path, checkpoint, payload


def validate_cuda_runtime() -> tuple[str, list[int], list[str]]:
    if not torch.cuda.is_available():
        raise RuntimeError("Pocket2Mol local validation requires an available CUDA GPU")
    capability = list(torch.cuda.get_device_capability(0))
    architecture = f"sm_{capability[0]}{capability[1]}"
    compiled = list(torch.cuda.get_arch_list())
    if architecture not in compiled:
        raise RuntimeError(
            f"installed PyTorch does not contain the observed GPU architecture {architecture}; "
            f"compiled architectures: {compiled}"
        )
    left = torch.randn((32, 32), device="cuda")
    right = torch.randn((32, 32), device="cuda")
    product = torch.mm(left, right)
    torch.cuda.synchronize()
    if not torch.isfinite(product).all().item():
        raise RuntimeError("representative compiled CUDA matrix multiplication returned non-finite values")
    return torch.cuda.get_device_name(0), capability, compiled


def load_pocket_handoff(path: Path) -> tuple[Path, dict, list[float], float]:
    resolved = require_file(path, "validated pocket handoff")
    try:
        payload = json.loads(resolved.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValueError("validated pocket handoff is not readable JSON") from exc
    if not isinstance(payload, dict):
        raise ValueError("validated pocket handoff must be a JSON object")
    if payload.get("schema") != "synon.binding-pocket-handoff.v1" or payload.get("status") != "passed":
        raise ValueError("validated pocket handoff must be a passing binding-pocket receipt")

    ligand = payload.get("ligand")
    if not isinstance(ligand, dict):
        raise ValueError("validated pocket handoff is missing ligand identity")
    required_ligand_fields = ("component_id", "chain", "residue_number", "heavy_atom_count")
    if any(ligand.get(field) in (None, "") for field in required_ligand_fields):
        raise ValueError("validated pocket handoff has incomplete ligand identity")
    if int(ligand["heavy_atom_count"]) < 1:
        raise ValueError("validated pocket handoff ligand has no heavy atoms")

    center_record = payload.get("center_angstrom")
    size_record = payload.get("size_angstrom")
    if not isinstance(center_record, dict) or not isinstance(size_record, dict):
        raise ValueError("validated pocket handoff is missing center or box dimensions")
    try:
        center = [float(center_record[axis]) for axis in ("x", "y", "z")]
        dimensions = [float(size_record[axis]) for axis in ("x", "y", "z")]
    except (KeyError, TypeError, ValueError) as exc:
        raise ValueError("validated pocket handoff has invalid center or box dimensions") from exc
    if not all(math.isfinite(value) for value in center):
        raise ValueError("validated pocket handoff center is not finite")
    if not all(math.isfinite(value) and value > 0 for value in dimensions):
        raise ValueError("validated pocket handoff box dimensions must be finite and positive")
    return resolved, payload, center, max(dimensions)


def finite_conformer(molecule: Chem.Mol) -> bool:
    if molecule.GetNumConformers() == 0:
        return False
    conformer = molecule.GetConformer()
    return all(
        math.isfinite(value)
        for atom_index in range(molecule.GetNumAtoms())
        for value in (
            conformer.GetAtomPosition(atom_index).x,
            conformer.GetAtomPosition(atom_index).y,
            conformer.GetAtomPosition(atom_index).z,
        )
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run and validate Pocket2Mol pocket generation.")
    parser.add_argument("--repository", required=True)
    parser.add_argument("--checkpoint-acquisition", required=True)
    parser.add_argument("--protein", required=True)
    parser.add_argument("--source-revision", default="unresolved")
    parser.add_argument("--pocket-validation", required=True)
    parser.add_argument("--required-count", required=True, type=int)
    parser.add_argument("--beam-size", type=int)
    parser.add_argument("--max-steps", type=int, default=50)
    parser.add_argument("--seed", type=int, default=2020)
    parser.add_argument("--device", default="cuda")
    parser.add_argument("--output-dir", required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.required_count < 1 or args.required_count > 10000:
        raise ValueError("required count must be between 1 and 10000")
    if args.max_steps < 1:
        raise ValueError("max steps must be positive")
    if not re.fullmatch(r"[0-9a-fA-F]{40,64}", args.source_revision):
        raise ValueError("source revision must be a verified immutable hexadecimal commit")
    if args.device != "cuda":
        raise RuntimeError("Pocket2Mol local validation requires the CUDA execution route")
    device_name, gpu_capability, compiled_architectures = validate_cuda_runtime()

    repository = Path(args.repository).resolve(strict=True)
    sample_script = require_file(repository / "sample_for_pdb.py", "Pocket2Mol sample script")
    template_config = require_file(repository / "configs" / "sample_for_pdb.yml", "Pocket2Mol config")
    checkpoint_receipt, checkpoint, checkpoint_acquisition = load_checkpoint_acquisition(
        Path(args.checkpoint_acquisition)
    )
    protein = require_file(Path(args.protein), "protein input")
    pocket_handoff_path, pocket_handoff, center_values, box_size = load_pocket_handoff(
        Path(args.pocket_validation)
    )
    output_dir = Path(args.output_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    raw_output = output_dir / "raw"
    raw_output.mkdir(exist_ok=True)

    config = yaml.safe_load(template_config.read_text(encoding="utf-8"))
    if not isinstance(config, dict) or not isinstance(config.get("model"), dict) or not isinstance(config.get("sample"), dict):
        raise ValueError("official Pocket2Mol sample config has an unexpected shape")
    config["model"]["checkpoint"] = str(checkpoint)
    config["sample"]["seed"] = args.seed
    config["sample"]["num_samples"] = args.required_count
    config["sample"]["beam_size"] = args.beam_size or min(300, max(64, args.required_count * 8))
    config["sample"]["max_steps"] = args.max_steps
    run_config = output_dir / "pocket2mol_run.yml"
    run_config.write_text(yaml.safe_dump(config, sort_keys=False), encoding="utf-8")

    before = {path.resolve() for path in raw_output.iterdir()}
    center = ",".join(str(value) for value in center_values)
    if center_values[0] < 0:
        center = " " + center
    command = [
        sys.executable,
        str(sample_script),
        "--pdb_path",
        str(protein),
        "--center",
        center,
        "--bbox_size",
        str(box_size),
        "--config",
        str(run_config),
        "--device",
        args.device,
        "--outdir",
        str(raw_output),
    ]
    environment = os.environ.copy()
    environment["CUDA_VISIBLE_DEVICES"] = environment.get("CUDA_VISIBLE_DEVICES", "0")
    environment["TORCH_FORCE_NO_WEIGHTS_ONLY_LOAD"] = "1"
    started = time.time()
    completed = subprocess.run(
        command,
        cwd=repository,
        env=environment,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        check=False,
    )
    log_path = output_dir / "pocket2mol.log"
    log_path.write_text(completed.stdout, encoding="utf-8")
    if completed.returncode != 0:
        tail = "\n".join(completed.stdout.splitlines()[-30:])
        raise RuntimeError(f"Pocket2Mol exited with code {completed.returncode}:\n{tail}")

    created = [path.resolve() for path in raw_output.iterdir() if path.resolve() not in before]
    search_roots = created or [raw_output]
    sdf_paths = sorted(
        {path.resolve() for root in search_roots for path in root.rglob("*.sdf") if path.is_file()}
    )
    records: list[tuple[str, Chem.Mol, Path]] = []
    seen: set[str] = set()
    for sdf_path in sdf_paths:
        for molecule in Chem.SDMolSupplier(str(sdf_path), removeHs=False):
            if molecule is None or not finite_conformer(molecule):
                continue
            canonical = Chem.MolToSmiles(Chem.RemoveHs(molecule), canonical=True, isomericSmiles=True)
            if not canonical or "." in canonical or canonical in seen:
                continue
            seen.add(canonical)
            records.append((canonical, molecule, sdf_path))
    if len(records) < args.required_count:
        raise RuntimeError(
            f"Pocket2Mol produced {len(records)} unique valid 3D molecules; {args.required_count} required"
        )
    records = records[: args.required_count]

    normalized_sdf = output_dir / "pocket2mol_candidates.sdf"
    smiles_csv = output_dir / "pocket2mol_candidates.csv"
    writer = Chem.SDWriter(str(normalized_sdf))
    rows: list[dict[str, str]] = []
    try:
        for index, (canonical, molecule, source_path) in enumerate(records, start=1):
            candidate_id = f"P2M-{index:04d}"
            molecule.SetProp("_Name", candidate_id)
            molecule.SetProp("candidate_id", candidate_id)
            molecule.SetProp("canonical_smiles", canonical)
            molecule.SetProp("generation_engine", "Pocket2Mol")
            writer.write(molecule)
            rows.append(
                {
                    "candidate_id": candidate_id,
                    "canonical_smiles": canonical,
                    "source_sdf": source_path.name,
                }
            )
    finally:
        writer.close()
    with smiles_csv.open("w", encoding="utf-8", newline="") as handle:
        csv_writer = csv.DictWriter(handle, fieldnames=["candidate_id", "canonical_smiles", "source_sdf"])
        csv_writer.writeheader()
        csv_writer.writerows(rows)

    validation = {
        "schema": "synon.pocket2mol-generation-validation.v1",
        "status": "passed",
        "engine": "Pocket2Mol",
        "source_revision": args.source_revision,
        "source_files_sha256": {
            "sample_for_pdb.py": sha256_file(sample_script),
            "config_template": sha256_file(template_config),
        },
        "checkpoint_sha256": sha256_file(checkpoint),
        "checkpoint_acquisition_sha256": sha256_file(checkpoint_receipt),
        "checkpoint_source_url": checkpoint_acquisition["source_url"],
        "protein_sha256": sha256_file(protein),
        "conditioning": {
            "kind": "protein_pocket_3d",
            "pocket_handoff_sha256": sha256_file(pocket_handoff_path),
            "pocket_handoff_schema": pocket_handoff["schema"],
            "pocket_source_file": pocket_handoff.get("source_file"),
            "pocket_source_sha256": pocket_handoff.get("source_sha256"),
            "ligand": pocket_handoff["ligand"],
            "box_definition": pocket_handoff.get("box_definition"),
            "center_angstrom": center_values,
            "box_size_angstrom": box_size,
        },
        "requested_count": args.required_count,
        "validated_count": len(records),
        "seed": args.seed,
        "beam_size": config["sample"]["beam_size"],
        "max_steps": args.max_steps,
        "device": device_name,
        "gpu_capability": gpu_capability,
        "compiled_architectures": compiled_architectures,
        "torch_version": torch.__version__,
        "cuda_runtime": torch.version.cuda,
        "elapsed_seconds": round(time.time() - started, 3),
        "outputs": [normalized_sdf.name, smiles_csv.name],
    }
    validation_path = output_dir / "pocket2mol_validation.json"
    validation_path.write_text(json.dumps(validation, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    copied_protein = output_dir / protein.name
    if protein != copied_protein.resolve():
        shutil.copy2(protein, copied_protein)

    print(f"Pocket2Mol validated {len(records)} unique 3D molecules on {validation['device']}")
    print(f"Outputs: {normalized_sdf.name}, {smiles_csv.name}, {validation_path.name}")


if __name__ == "__main__":
    main()
