#!/usr/bin/env python3
"""Generate a bounded APBS OpenDX potential map for the unified structure viewer."""

from __future__ import annotations

import argparse
import hashlib
import importlib.metadata
import json
import math
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tempfile
import time
from typing import Any


ENGINE = "APBS"
CONTRACT_VERSION = "2.0"
CALCULATION_MODE = "separate-components"
FORCE_FIELD = "AMBER"
POTENTIAL_UNIT = "kT/e"
COLOR_RANGE = [-5.0, 5.0]
MESH_SPACING_ANGSTROM = 0.65
MAX_INPUT_PDB_ATOMS = 99_999
MAX_PREPARED_PROTEIN_ATOMS = 200_000
MAX_LIGAND_ATOMS = 2_048
MAX_LIGANDS = 8
MAX_TOTAL_LIGAND_ATOMS = 8_192
MAX_GRID_VALUES = 6_000_000
MAX_TOTAL_GRID_VALUES = 24_000_000
MAX_DX_BYTES = 64 << 20
MAX_TOTAL_DX_BYTES = 128 << 20
COMMAND_TIMEOUT_SECONDS = 420
TOTAL_COMMAND_BUDGET_SECONDS = 660
APBS_GRID_MEMORY_CEILING_MIB = 512
LIGAND_KEY_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
INPUT_DIGEST_DOMAIN = b"synon-biomed:structure-electrostatic-input:v1"


class ElectrostaticError(RuntimeError):
    """A bounded, user-safe electrostatic calculation failure."""

    def __init__(self, error_type: str, message: str) -> None:
        super().__init__(message)
        self.error_type = error_type
        self.safe_message = message


def run_checked(
    arguments: list[str],
    cwd: Path,
    failure_type: str,
    failure_message: str,
    deadline: float | None = None,
) -> subprocess.CompletedProcess[str]:
    environment = os.environ.copy()
    environment["PWD"] = str(cwd)
    timeout = float(COMMAND_TIMEOUT_SECONDS)
    if deadline is not None:
        timeout = min(timeout, deadline - time.monotonic())
        if timeout <= 0:
            raise ElectrostaticError(failure_type, failure_message)
    try:
        result = subprocess.run(
            arguments,
            cwd=cwd,
            env=environment,
            check=False,
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise ElectrostaticError(failure_type, failure_message) from exc
    if result.returncode != 0:
        raise ElectrostaticError(failure_type, failure_message)
    return result


def executable_version(executable: str, cwd: Path) -> str:
    path = shutil.which(executable)
    if not path:
        return "unavailable"
    try:
        result = subprocess.run(
            [path, "--version"],
            cwd=cwd,
            env={**os.environ, "PWD": str(cwd)},
            check=False,
            capture_output=True,
            text=True,
            timeout=20,
        )
    except (OSError, subprocess.TimeoutExpired):
        return "unavailable"
    output = "\n".join(part for part in (result.stdout.strip(), result.stderr.strip()) if part)
    match = re.search(r"\b\d+\.\d+(?:\.\d+)?\b", output)
    return match.group(0) if match else "unknown"


def split_protein_pdb(source: Path, destination: Path) -> int:
    protein_lines: list[str] = []
    atom_serials: set[int] = set()
    atom_count = 0
    for raw_line in source.read_text(encoding="utf-8").splitlines():
        line = raw_line.rstrip("\r\n")
        if line.startswith("ATOM  "):
            if len(line) < 54:
                raise ElectrostaticError("invalid_input", "structure contains no supported protein or ligand atoms")
            try:
                serial = int(line[6:11])
                residue_number = int(line[22:26])
                coordinates = tuple(float(line[start : start + 8]) for start in (30, 38, 46))
            except ValueError as exc:
                raise ElectrostaticError("invalid_input", "structure contains no supported protein or ligand atoms") from exc
            if (
                serial <= 0
                or serial > MAX_INPUT_PDB_ATOMS
                or serial in atom_serials
                or not -999 <= residue_number <= 9999
                or not all(math.isfinite(value) for value in coordinates)
            ):
                raise ElectrostaticError("invalid_input", "structure contains no supported protein or ligand atoms")
            atom_serials.add(serial)
            atom_count += 1
            if atom_count > MAX_INPUT_PDB_ATOMS:
                raise ElectrostaticError("structure_too_large", "structure contains no supported protein or ligand atoms")
            protein_lines.append(line)
        elif line.startswith("TER") and protein_lines:
            protein_lines.append("TER")
    if protein_lines:
        destination.write_text("\n".join(protein_lines + ["END", ""]), encoding="utf-8")
    return atom_count


def prepare_protein_pqr(protein_pdb: Path, output_pqr: Path, ph: float, deadline: float) -> int:
    run_checked(
        [
            "pdb2pqr",
            "--ff=AMBER",
            "--titration-state-method=propka",
            f"--with-ph={ph:.2f}",
            "--keep-chain",
            "--drop-water",
            protein_pdb.name,
            output_pqr.name,
        ],
        protein_pdb.parent,
        "protein_charge_preparation_failed",
        "protein charge preparation failed",
        deadline,
    )
    if not output_pqr.is_file():
        raise ElectrostaticError("protein_charge_preparation_failed", "protein charge preparation failed")
    return count_pqr_atoms(output_pqr, MAX_PREPARED_PROTEIN_ATOMS)


def prepare_ligand_pqr(ligand_path: Path, output_pqr: Path) -> tuple[int, float]:
    try:
        from rdkit import Chem
        from rdkit.Chem import rdPartialCharges
    except ImportError as exc:
        raise ElectrostaticError("ligand_charge_preparation_failed", "ligand charge preparation failed") from exc

    molecule = Chem.MolFromMolFile(str(ligand_path), removeHs=False, sanitize=True)
    if molecule is None or molecule.GetNumAtoms() == 0 or molecule.GetNumConformers() == 0:
        raise ElectrostaticError("ligand_charge_preparation_failed", "ligand charge preparation failed")
    if molecule.GetNumAtoms() > MAX_LIGAND_ATOMS:
        raise ElectrostaticError("ligand_charge_preparation_failed", "ligand charge preparation failed")
    try:
        molecule = Chem.AddHs(molecule, addCoords=True)
        rdPartialCharges.ComputeGasteigerCharges(molecule, nIter=16, throwOnParamFailure=True)
    except Exception as exc:
        raise ElectrostaticError("ligand_charge_preparation_failed", "ligand charge preparation failed") from exc
    if molecule.GetNumAtoms() > MAX_LIGAND_ATOMS:
        raise ElectrostaticError("ligand_charge_preparation_failed", "ligand charge preparation failed")

    conformer = molecule.GetConformer()
    periodic_table = Chem.GetPeriodicTable()
    element_counts: dict[str, int] = {}
    lines: list[str] = []
    total_charge = 0.0
    for index, atom in enumerate(molecule.GetAtoms(), start=1):
        element = atom.GetSymbol().upper()
        element_counts[element] = element_counts.get(element, 0) + 1
        atom_name = f"{element}{element_counts[element]}"[:4]
        try:
            charge = float(atom.GetProp("_GasteigerCharge"))
            radius = float(periodic_table.GetRvdw(atom.GetAtomicNum()))
        except (KeyError, TypeError, ValueError) as exc:
            raise ElectrostaticError("ligand_charge_preparation_failed", "ligand charge preparation failed") from exc
        if not math.isfinite(charge) or not math.isfinite(radius) or radius <= 0:
            raise ElectrostaticError("ligand_charge_preparation_failed", "ligand charge preparation failed")
        position = conformer.GetAtomPosition(index - 1)
        if not all(math.isfinite(value) for value in (position.x, position.y, position.z)):
            raise ElectrostaticError("ligand_charge_preparation_failed", "ligand charge preparation failed")
        total_charge += charge
        lines.append(
            f"HETATM{index:5d} {atom_name:<4s} LIG L{1:4d}    "
            f"{position.x:8.3f}{position.y:8.3f}{position.z:8.3f} {charge:8.4f} {radius:7.4f}"
        )
    output_pqr.write_text("\n".join(lines + ["END", ""]), encoding="utf-8")
    return len(lines), total_charge


def count_pqr_atoms(path: Path, maximum: int) -> int:
    count = sum(1 for line in path.read_text(encoding="utf-8").splitlines() if line.startswith(("ATOM", "HETATM")))
    if count <= 0 or count > maximum:
        raise ElectrostaticError("invalid_pqr", "structure contains no supported protein or ligand atoms")
    return count


def merge_pqr(sources: list[Path], output_pqr: Path) -> int:
    lines: list[str] = []
    for source in sources:
        for line in source.read_text(encoding="utf-8").splitlines():
            if line.startswith(("ATOM", "HETATM", "TER")):
                lines.append(line)
    if not lines:
        raise ElectrostaticError("empty_structure", "structure contains no supported protein or ligand atoms")
    output_pqr.write_text("\n".join(lines + ["END", ""]), encoding="utf-8")
    return count_pqr_atoms(output_pqr, MAX_PREPARED_PROTEIN_ATOMS + MAX_TOTAL_LIGAND_ATOMS)


def generate_apbs_input(
    molecule_pqr: Path,
    sizing_pqr: Path,
    output_dx: Path,
    ionic_strength: float,
) -> tuple[Path, int]:
    try:
        from pdb2pqr import inputgen, psize

        size = psize.Psize(
            space=MESH_SPACING_ANGSTROM,
            gmemceil=APBS_GRID_MEMORY_CEILING_MIB,
        )
        size.run_psize(str(sizing_pqr))
        grid_counts = [int(value) for value in size.ngrid]
        grid_value_count = math.prod(grid_counts)
        if any(value <= 0 for value in grid_counts) or grid_value_count > MAX_GRID_VALUES:
            raise ElectrostaticError(
                "grid_too_large",
                "electrostatic potential output exceeded the interactive limit",
            )

        # PDB2PQR 3.7.1's inputgen CLI leaves --istrng as a string (or None),
        # causing Elec.__str__ to fail before it can write an APBS input file.
        # The supported Python API accepts the required numeric value directly
        # and also lets us apply the configured mesh and memory bounds.
        generated = inputgen.Input(
            molecule_pqr.name,
            size,
            "mg-auto",
            False,
            float(ionic_strength),
            potdx=True,
        )
        shared_center = " ".join(f"{value:.4f}" for value in size.center)
        for calculation in generated.elecs:
            if not calculation:
                continue
            calculation.cgcent = shared_center
            calculation.fgcent = shared_center
            calculation.gcent = shared_center
        content = str(generated)
    except ElectrostaticError:
        raise
    except (ImportError, OSError, TypeError, ValueError) as exc:
        raise ElectrostaticError("grid_generation_failed", "electrostatic grid generation failed") from exc

    input_path = molecule_pqr.with_suffix(".in")
    content, replacements = re.subn(
        r"(?m)^\s*write\s+pot\s+dx\s+\S+\s*$",
        f"    write pot dx {output_dx.stem}",
        content,
    )
    if replacements != 1:
        raise ElectrostaticError("grid_generation_failed", "electrostatic grid generation failed")
    input_path.write_text(content, encoding="utf-8")
    return input_path, grid_value_count


def calculate_apbs(input_path: Path, output_dx: Path, deadline: float) -> None:
    run_checked(
        ["apbs", input_path.name],
        input_path.parent,
        "apbs_failed",
        "electrostatic potential calculation failed",
        deadline,
    )
    if not output_dx.is_file():
        candidates = sorted(input_path.parent.glob(f"{output_dx.stem}*.dx"))
        if len(candidates) == 1:
            candidates[0].replace(output_dx)
    if not output_dx.is_file():
        raise ElectrostaticError("apbs_failed", "electrostatic potential calculation failed")
    if output_dx.stat().st_size > MAX_DX_BYTES:
        raise ElectrostaticError("dx_too_large", "electrostatic potential output exceeded the interactive limit")


def parse_dx(path: Path) -> dict[str, Any]:
    text = path.read_text(encoding="utf-8")
    count_match = re.search(r"object\s+1\s+class\s+gridpositions\s+counts\s+(\d+)\s+(\d+)\s+(\d+)", text)
    origin_match = re.search(r"(?m)^origin\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)\s*$", text)
    delta_matches = re.findall(r"(?m)^delta\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)\s+([-+0-9.eE]+)\s*$", text)
    data_match = re.search(r"object\s+3\s+class\s+array\s+type\s+\S+\s+rank\s+0\s+items\s+(\d+)\s+data\s+follows\s*\n", text)
    if not count_match or not origin_match or len(delta_matches) < 3 or not data_match:
        raise ElectrostaticError("invalid_dx", "electrostatic potential calculation failed")
    counts = [int(value) for value in count_match.groups()]
    expected = counts[0] * counts[1] * counts[2]
    item_count = int(data_match.group(1))
    if item_count != expected or item_count <= 0 or item_count > MAX_GRID_VALUES:
        raise ElectrostaticError("invalid_dx", "electrostatic potential output exceeded the interactive limit")
    values_text = text[data_match.end():]
    values: list[float] = []
    for token in values_text.split():
        if len(values) == item_count:
            break
        try:
            value = float(token)
        except ValueError as exc:
            raise ElectrostaticError("invalid_dx", "electrostatic potential calculation failed") from exc
        if not math.isfinite(value):
            raise ElectrostaticError("invalid_dx", "electrostatic potential calculation failed")
        values.append(value)
    if len(values) != item_count:
        raise ElectrostaticError("invalid_dx", "electrostatic potential calculation failed")
    delta_vectors = [[float(value) for value in group] for group in delta_matches[:3]]
    axis_steps = [math.sqrt(sum(component * component for component in vector)) for vector in delta_vectors]
    return {
        "counts": counts,
        "origin": [float(value) for value in origin_match.groups()],
        "delta": axis_steps,
        "value_count": item_count,
        "minimum": min(values),
        "maximum": max(values),
    }


def grids_share_frame(grids: dict[str, dict[str, Any]]) -> bool:
    values = list(grids.values())
    if len(values) < 2:
        return True
    reference = values[0]
    for candidate in values[1:]:
        if candidate["counts"] != reference["counts"]:
            return False
        for key in ("origin", "delta"):
            if len(candidate[key]) != len(reference[key]):
                return False
            if any(abs(float(left) - float(right)) > 1e-6 for left, right in zip(candidate[key], reference[key])):
                return False
    return True


def base_report(input_sha256: str, ph: float, ionic_strength: float, workdir: Path) -> dict[str, Any]:
    return {
        "contract": True,
        "contract_version": CONTRACT_VERSION,
        "ok": False,
        "engine": ENGINE,
        "calculation_mode": CALCULATION_MODE,
        "grid_alignment": "shared-frame",
        "engine_version": executable_version("apbs", workdir),
        "preparation_engine": "PDB2PQR + RDKit",
        "preparation_engine_version": f"pdb2pqr {distribution_version('pdb2pqr')}; RDKit {distribution_version('rdkit')}",
        "protein_charge_method": "PDB2PQR AMBER with PROPKA protonation",
        "ligand_charge_method": "RDKit Gasteiger with explicit hydrogens",
        "force_field": FORCE_FIELD,
        "ph": ph,
        "ionic_strength_molar": ionic_strength,
        "potential_unit": POTENTIAL_UNIT,
        "color_range": COLOR_RANGE,
        "input_sha256": input_sha256,
        "protein_atom_count": 0,
        "ligand_atom_count": 0,
        "total_atom_count": 0,
        "mesh_spacing_angstrom": MESH_SPACING_ANGSTROM,
        "grids": {},
        "warnings": [],
    }


def distribution_version(name: str) -> str:
    try:
        return importlib.metadata.version(name)
    except importlib.metadata.PackageNotFoundError:
        return "unavailable"


def resolve_workspace_path(value: str, workdir: Path, label: str) -> Path:
    candidate = Path(value)
    if candidate.name != value:
        raise ElectrostaticError("invalid_input", f"{label} is invalid")
    resolved = (workdir / candidate).resolve()
    if resolved.parent != workdir:
        raise ElectrostaticError("invalid_input", f"{label} is invalid")
    return resolved


def ligand_components(args: argparse.Namespace, workdir: Path) -> tuple[bool, list[tuple[str | None, Path, Path]]]:
    raw_components = list(args.ligand_component or [])
    if args.ligand and raw_components:
        raise ElectrostaticError("invalid_input", "ligand inputs are mutually exclusive")
    if len(raw_components) > MAX_LIGANDS:
        raise ElectrostaticError("invalid_input", "ligand charge preparation failed")
    components: list[tuple[str | None, Path, Path]] = []
    normalized_keys: set[str] = set()
    total_input_bytes = 0
    if args.ligand:
        if not args.ligand_dx:
            raise ElectrostaticError("invalid_input", "ligand charge preparation failed")
        components.append(
            (
                None,
                resolve_workspace_path(args.ligand, workdir, "ligand input"),
                resolve_workspace_path(args.ligand_dx, workdir, "ligand output"),
            )
        )
    for raw_key, raw_input, raw_output in raw_components:
        if not LIGAND_KEY_PATTERN.fullmatch(raw_key) or raw_key.upper() in normalized_keys:
            raise ElectrostaticError("invalid_input", "ligand charge preparation failed")
        normalized_keys.add(raw_key.upper())
        components.append(
            (
                raw_key,
                resolve_workspace_path(raw_input, workdir, "ligand input"),
                resolve_workspace_path(raw_output, workdir, "ligand output"),
            )
        )
    for _, ligand_path, _ in components:
        if not ligand_path.is_file() or ligand_path.stat().st_size <= 0 or ligand_path.stat().st_size > (4 << 20):
            raise ElectrostaticError("invalid_input", "ligand charge preparation failed")
        ligand_bytes = ligand_path.read_bytes()
        total_input_bytes += len(ligand_bytes)
        if b"M  END" not in ligand_bytes or total_input_bytes > (8 << 20):
            raise ElectrostaticError("invalid_input", "ligand charge preparation failed")
    return bool(raw_components), components


def electrostatic_input_sha256(
    source_bytes: bytes,
    components: list[tuple[str | None, Path, Path]],
    batch_mode: bool,
) -> str:
    digest = hashlib.sha256()
    values = [INPUT_DIGEST_DOMAIN, b"batch" if batch_mode else b"legacy", source_bytes]
    if batch_mode:
        for key, path, _ in sorted(
            components,
            key=lambda component: ((component[0] or "").upper(), component[0] or ""),
        ):
            if key is None:
                raise ElectrostaticError("invalid_input", "ligand charge preparation failed")
            values.extend((key.encode("utf-8"), path.read_bytes()))
    else:
        values.append(components[0][1].read_bytes() if components else b"")
    for value in values:
        digest.update(len(value).to_bytes(8, "little"))
        digest.update(value)
    return digest.hexdigest()


def calculate(args: argparse.Namespace) -> dict[str, Any]:
    source = Path(args.input).resolve()
    if not source.is_file() or source.stat().st_size <= 0 or source.stat().st_size > (24 << 20):
        raise ElectrostaticError("invalid_input", "structure contains no supported protein or ligand atoms")
    workdir = source.parent
    protein_dx = resolve_workspace_path(args.protein_dx, workdir, "protein output")
    report_path = resolve_workspace_path(args.report, workdir, "report output")
    batch_mode, ligand_specs = ligand_components(args, workdir)
    source_bytes = source.read_bytes()
    report = base_report(
        electrostatic_input_sha256(source_bytes, ligand_specs, batch_mode),
        args.ph,
        args.ionic_strength,
        workdir,
    )
    protein_pdb = workdir / "electrostatic-protein.pdb"
    protein_pqr = workdir / "electrostatic-protein.pqr"
    system_pqr = workdir / "electrostatic-system.pqr"
    deadline = time.monotonic() + TOTAL_COMMAND_BUDGET_SECONDS
    try:
        raw_protein_count = split_protein_pdb(source, protein_pdb)
        prepared_protein_count = (
            prepare_protein_pqr(protein_pdb, protein_pqr, args.ph, deadline) if raw_protein_count else 0
        )
        prepared_ligands: list[tuple[str | None, Path, Path, int, float]] = []
        total_ligand_count = 0
        for index, (key, ligand_path, ligand_dx) in enumerate(ligand_specs, start=1):
            ligand_pqr = workdir / f"electrostatic-ligand-{index}.pqr"
            ligand_count, ligand_charge = prepare_ligand_pqr(ligand_path, ligand_pqr)
            total_ligand_count += ligand_count
            if total_ligand_count > MAX_TOTAL_LIGAND_ATOMS:
                raise ElectrostaticError("ligand_charge_preparation_failed", "ligand charge preparation failed")
            prepared_ligands.append((key, ligand_pqr, ligand_dx, ligand_count, ligand_charge))
        if prepared_protein_count == 0 and total_ligand_count == 0:
            raise ElectrostaticError("empty_structure", "structure contains no supported protein or ligand atoms")
        sizing_components = ([protein_pqr] if prepared_protein_count else []) + [item[1] for item in prepared_ligands]
        total_count = merge_pqr(sizing_components, system_pqr)
        component_jobs: list[tuple[str, str | None, Path, Path]] = []
        if prepared_protein_count:
            component_jobs.append(("protein", None, protein_pqr, protein_dx))
        component_jobs.extend(("ligand", key, ligand_pqr, ligand_dx) for key, ligand_pqr, ligand_dx, _, _ in prepared_ligands)
        grids: dict[str, dict[str, Any]] = {}
        keyed_ligand_grids: dict[str, dict[str, Any]] = {}
        alignment_grids: dict[str, dict[str, Any]] = {}
        prepared_jobs: list[tuple[str, str | None, Path, Path]] = []
        predicted_grid_values = 0
        for role, key, molecule_pqr, output_dx in component_jobs:
            input_path, component_grid_values = generate_apbs_input(
                molecule_pqr, system_pqr, output_dx, args.ionic_strength
            )
            predicted_grid_values += component_grid_values
            if predicted_grid_values > MAX_TOTAL_GRID_VALUES:
                raise ElectrostaticError(
                    "grid_too_large", "electrostatic potential output exceeded the interactive limit"
                )
            prepared_jobs.append((role, key, input_path, output_dx))
        total_grid_values = 0
        total_dx_bytes = 0
        for role, key, input_path, output_dx in prepared_jobs:
            calculate_apbs(input_path, output_dx, deadline)
            total_dx_bytes += output_dx.stat().st_size
            if total_dx_bytes > MAX_TOTAL_DX_BYTES:
                raise ElectrostaticError("dx_too_large", "electrostatic potential output exceeded the interactive limit")
            grid = parse_dx(output_dx)
            total_grid_values += int(grid["value_count"])
            if total_grid_values > MAX_TOTAL_GRID_VALUES:
                raise ElectrostaticError("grid_too_large", "electrostatic potential output exceeded the interactive limit")
            alignment_key = role if key is None else f"ligand:{key}"
            alignment_grids[alignment_key] = grid
            if key is None:
                grids[role] = grid
            else:
                keyed_ligand_grids[key] = grid
        if not grids_share_frame(alignment_grids):
            raise ElectrostaticError("grid_alignment_failed", "electrostatic component grids could not be aligned")
        report.update(
            {
                "ok": True,
                "protein_atom_count": prepared_protein_count,
                "ligand_atom_count": total_ligand_count,
                "total_atom_count": total_count,
                "grids": grids,
            }
        )
        if batch_mode:
            report["ligand_atom_counts"] = {key: count for key, _, _, count, _ in prepared_ligands if key is not None}
            report["grids"]["ligands"] = keyed_ligand_grids
        if prepared_protein_count == 0:
            report["protein_charge_method"] = "not applicable"
        if total_ligand_count == 0:
            report["ligand_charge_method"] = "not applicable"
        for key, _, _, _, ligand_charge in prepared_ligands:
            if abs(ligand_charge - round(ligand_charge)) > 0.15:
                report["warnings"].append(
                    "ligand partial charges do not sum closely to an integer formal charge"
                    if key is None
                    else f"ligand {key} partial charges do not sum closely to an integer formal charge"
                )
        hetero_atom_count = sum(1 for line in source_bytes.splitlines() if line.startswith(b"HETATM"))
        if hetero_atom_count:
            warning = "non-protein HETATM records were excluded from the protein potential"
            if prepared_ligands:
                warning += "; supplied ligand mol blocks were calculated as separate components"
            report["warnings"].append(warning)
        report_path.write_text(json.dumps(report, ensure_ascii=False, sort_keys=True), encoding="utf-8")
        return report
    except ElectrostaticError as exc:
        report["error_type"] = exc.error_type
        report["message"] = exc.safe_message
        report_path.write_text(json.dumps(report, ensure_ascii=False, sort_keys=True), encoding="utf-8")
        raise


def run_self_test() -> None:
    with tempfile.TemporaryDirectory(prefix="synon-electrostatic-self-test-") as raw_dir:
        root = Path(raw_dir)
        pdb = root / "fixture.pdb"
        protein = root / "protein.pdb"
        pdb.write_text(
            "ATOM      1  N   ALA A   1       0.000   0.000   0.000  1.00  0.00           N\n"
            "HETATM    2  C1  LIG L   1       1.000   0.000   0.000  1.00  0.00           C\nEND\n",
            encoding="utf-8",
        )
        if split_protein_pdb(pdb, protein) != 1 or "HETATM" in protein.read_text(encoding="utf-8"):
            raise AssertionError("protein extraction contract failed")
        dx = root / "fixture.dx"
        dx.write_text(
            "object 1 class gridpositions counts 2 2 2\n"
            "origin 0 0 0\n"
            "delta 1 0 0\n"
            "delta 0 1 0\n"
            "delta 0 0 1\n"
            "object 2 class gridconnections counts 2 2 2\n"
            "object 3 class array type double rank 0 items 8 data follows\n"
            "-2 -1 0 1 2 3 4 5\n",
            encoding="utf-8",
        )
        grid = parse_dx(dx)
        if grid["value_count"] != 8 or grid["minimum"] != -2 or grid["maximum"] != 5:
            raise AssertionError("OpenDX validation contract failed")
        if not grids_share_frame({"protein": grid, "ligand": dict(grid)}):
            raise AssertionError("shared APBS grid contract failed")
        shifted = dict(grid)
        shifted["origin"] = [1.0, 0.0, 0.0]
        if grids_share_frame({"protein": grid, "ligand": shifted}):
            raise AssertionError("misaligned APBS grids were accepted")
        legacy_ligand = root / "legacy.mol"
        legacy_ligand.write_bytes(b"ligand\nM  END\n")
        if (
            electrostatic_input_sha256(
                b"ATOM\n",
                [(None, legacy_ligand, root / "legacy.dx")],
                False,
            )
            != "7f1faffd7cfa9e6101a8da2164dd42ca309e319540f684e26c2ed4f5dbbad9b0"
        ):
            raise AssertionError("legacy input digest contract failed")
        batch_one = root / "batch-one.mol"
        batch_two = root / "batch-two.mol"
        batch_one.write_bytes(b"one\nM  END\n")
        batch_two.write_bytes(b"two\nM  END\n")
        if (
            electrostatic_input_sha256(
                b"ATOM\n",
                [
                    ("D02", batch_two, root / "batch-two.dx"),
                    ("D01", batch_one, root / "batch-one.dx"),
                ],
                True,
            )
            != "58bc763cbb1dd505055834ac4c95f1c9a9829d32017dd88c8f929bc807178b37"
        ):
            raise AssertionError("batch input digest contract failed")
    print(json.dumps({"ok": True, "contract": CONTRACT_VERSION}, sort_keys=True))


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input")
    parser.add_argument("--ligand")
    parser.add_argument("--ligand-component", nargs=3, action="append", default=[], metavar=("KEY", "MOL", "DX"))
    parser.add_argument("--protein-dx")
    parser.add_argument("--ligand-dx")
    parser.add_argument("--report")
    parser.add_argument("--ph", type=float, default=7.4)
    parser.add_argument("--ionic-strength", type=float, default=0.15)
    parser.add_argument("--self-test", action="store_true")
    return parser


def main() -> int:
    args = build_parser().parse_args()
    if args.self_test:
        run_self_test()
        return 0
    if (
        not args.input
        or not args.protein_dx
        or not args.ligand_dx
        or not args.report
        or not 0 <= args.ph <= 14
        or not 0 <= args.ionic_strength <= 1
    ):
        print("invalid electrostatic calculation arguments", file=sys.stderr)
        return 2
    try:
        report = calculate(args)
    except ElectrostaticError as exc:
        print(json.dumps({"ok": False, "error_type": exc.error_type, "message": exc.safe_message}, sort_keys=True))
        return 2
    maps = sorted(key for key in report["grids"] if key != "ligands")
    maps.extend(f"ligand:{key}" for key in sorted(report["grids"].get("ligands", {})))
    print(json.dumps({"ok": True, "engine": report["engine"], "maps": maps}, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
