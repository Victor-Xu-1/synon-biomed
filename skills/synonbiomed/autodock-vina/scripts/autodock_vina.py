from __future__ import annotations

import argparse
import csv
import hashlib
import json
import math
import re
import shutil
import subprocess
import sys
import uuid
from pathlib import Path

import gemmi
from rdkit import Chem
from rdkit.Chem import AllChem

from autodock_vina_inputs import convert_ligand_source
from autodock_vina_outputs import write_docking_report, write_primary_pose_artifacts
from autodock_vina_pockets import load_validated_pocket_selection


VINA_RESULT = re.compile(r"^REMARK VINA RESULT:\s+(-?\d+(?:\.\d+)?)")
PREPARED_LIGAND_INDEX = re.compile(r"(?:^|[-_])(\d+)$")
PDB_CHAIN_CANDIDATES = "ZYXWVUTSRQPONMLKJIHGFEDCBA9876543210"
RECEPTOR_RESIDUE_ALIASES = {"HSD": "HID", "HSE": "HIE", "HSP": "HIP"}
AUTODOCK_ELEMENTS = {
    "A": "C",
    "C": "C",
    "NA": "N",
    "N": "N",
    "OA": "O",
    "O": "O",
    "SA": "S",
    "S": "S",
    "HD": "H",
    "H": "H",
    "F": "F",
    "CL": "Cl",
    "BR": "Br",
    "I": "I",
    "P": "P",
}
PRIMARY_SELECTION_CONTRACT = (
    "minimum_affinity_then_reference_geometry_on_exact_ties_else_stable_run_mode"
)
PRIMARY_SELECTION_BASES = {
    "best_affinity",
    "best_affinity_then_reference_geometry_tiebreak",
    "best_affinity_then_stable_run_mode_tiebreak",
}
EXECUTION_PACK_ID = "molecular-docking.autodock-vina"
OUTPUT_OWNERSHIP_MARKER = ".synon-execution-pack.json"
DEFAULT_OUTPUT_DIR = "out"
OUTPUT_PROMOTION_RECEIPT_PREFIX = "SYNON_EXECUTION_PACK_OUTPUT_RECEIPT="


def pdb_reference_site_scores(source: Path) -> dict[tuple[str, str, int], int]:
    """Return PDB SITE residue counts keyed by their described bound component."""
    if source.suffix.lower() != ".pdb":
        return {}
    site_components: dict[str, tuple[str, str, int]] = {}
    site_sizes: dict[str, int] = {}
    current_site = ""
    for line in source.read_text(encoding="utf-8", errors="replace").splitlines():
        identifier = re.match(r"^REMARK 800 SITE_IDENTIFIER:\s*(\S+)", line)
        if identifier:
            current_site = identifier.group(1).strip()
            continue
        description = re.match(
            r"^REMARK 800 SITE_DESCRIPTION:\s*BINDING SITE FOR RESIDUE\s+(\S+)\s+(\S+)\s+(-?\d+)",
            line,
        )
        if description and current_site:
            site_components[current_site] = (
                description.group(1).upper(), description.group(2), int(description.group(3))
            )
            continue
        site = re.match(r"^SITE\s+\d+\s+(\S+)\s+(\d+)", line)
        if site:
            site_sizes[site.group(1)] = max(site_sizes.get(site.group(1), 0), int(site.group(2)))
    return {
        component: site_sizes.get(site_id, 0)
        for site_id, component in site_components.items()
        if site_sizes.get(site_id, 0) > 0
    }


def heavy_coordinates(atoms: list[dict[str, object]]) -> list[tuple[float, float, float]]:
    coordinates: list[tuple[float, float, float]] = []
    for atom in atoms:
        if str(atom.get("element") or "").strip().upper() == "H":
            continue
        coordinate = (float(atom["x"]), float(atom["y"]), float(atom["z"]))
        if not all(math.isfinite(value) for value in coordinate):
            raise ValueError("pose contains non-finite coordinates")
        coordinates.append(coordinate)
    if not coordinates:
        raise ValueError("pose contains no heavy-atom coordinates")
    return coordinates


def coordinate_centroid(coordinates: list[tuple[float, float, float]]) -> tuple[float, float, float]:
    count = float(len(coordinates))
    return tuple(sum(point[axis] for point in coordinates) / count for axis in range(3))  # type: ignore[return-value]


def principal_extent_axis(
    coordinates: list[tuple[float, float, float]],
) -> tuple[float, float, float] | None:
    if len(coordinates) < 2:
        return None
    best_delta: tuple[float, float, float] | None = None
    best_distance = -1.0
    for index, first in enumerate(coordinates[:-1]):
        for second in coordinates[index + 1 :]:
            delta = tuple(second[axis] - first[axis] for axis in range(3))
            distance = sum(value * value for value in delta)
            if distance > best_distance:
                best_delta = delta  # type: ignore[assignment]
                best_distance = distance
    if best_delta is None or best_distance <= 0:
        return None
    scale = math.sqrt(best_distance)
    return tuple(value / scale for value in best_delta)


def reference_pose_metrics(
    pose_atoms: list[dict[str, object]],
    reference_atoms: list[dict[str, object]] | None,
) -> tuple[float | None, float | None]:
    if reference_atoms is None:
        return None, None
    pose_coordinates = heavy_coordinates(pose_atoms)
    reference_coordinates = heavy_coordinates(reference_atoms)
    pose_centroid = coordinate_centroid(pose_coordinates)
    reference_centroid = coordinate_centroid(reference_coordinates)
    centroid_distance = math.sqrt(
        sum((pose_centroid[axis] - reference_centroid[axis]) ** 2 for axis in range(3))
    )
    pose_axis = principal_extent_axis(pose_coordinates)
    reference_axis = principal_extent_axis(reference_coordinates)
    axis_cosine = None
    if pose_axis is not None and reference_axis is not None:
        axis_cosine = min(1.0, abs(sum(pose_axis[axis] * reference_axis[axis] for axis in range(3))))
    return centroid_distance, axis_cosine


def select_primary_pose(
    samples: list[dict[str, object]],
    reference_atoms: list[dict[str, object]] | None = None,
) -> dict[str, object]:
    ranked: list[dict[str, object]] = []
    for sample in samples:
        affinity = float(sample["affinity_kcal_mol"])
        if not math.isfinite(affinity):
            raise ValueError("pose affinity is non-finite")
        candidate = sample
        centroid_distance, axis_cosine = reference_pose_metrics(list(candidate["atoms"]), reference_atoms)
        candidate["reference_centroid_distance_angstrom"] = centroid_distance
        candidate["reference_axis_cosine"] = axis_cosine
        ranked.append(candidate)
    if not ranked:
        raise ValueError("docking produced no pose samples")

    minimum_affinity = min(float(sample["affinity_kcal_mol"]) for sample in ranked)
    tied = [
        sample
        for sample in ranked
        if float(sample["affinity_kcal_mol"]) == minimum_affinity
    ]
    if len(tied) == 1:
        selected = tied[0]
        selected["selection_basis"] = "best_affinity"
        return selected

    def geometry_key(sample: dict[str, object]) -> tuple[float, float, int, int]:
        centroid_distance = sample.get("reference_centroid_distance_angstrom")
        axis_cosine = sample.get("reference_axis_cosine")
        return (
            float(centroid_distance) if centroid_distance is not None else math.inf,
            -float(axis_cosine) if axis_cosine is not None else math.inf,
            int(sample["run_index"]),
            int(sample["mode_index"]),
        )

    if reference_atoms is not None:
        selected = min(tied, key=geometry_key)
        selected["selection_basis"] = "best_affinity_then_reference_geometry_tiebreak"
        return selected

    selected = min(tied, key=lambda sample: (int(sample["run_index"]), int(sample["mode_index"])))
    selected["selection_basis"] = "best_affinity_then_stable_run_mode_tiebreak"
    return selected


def primary_selection_evidence(
    primary_poses: dict[str, dict[str, object]],
) -> dict[str, object]:
    observed = sorted({str(pose.get("selection_basis") or "") for pose in primary_poses.values()})
    if not observed or any(basis not in PRIMARY_SELECTION_BASES for basis in observed):
        raise ValueError("primary pose selection evidence is missing or invalid")
    return {
        "selection_contract": PRIMARY_SELECTION_CONTRACT,
        "observed_selection_bases": observed,
    }


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def atomic_text(path: Path, text: str) -> None:
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(text, encoding="utf-8")
    temporary.replace(path)


def run_checked(argv: list[str], log: list[dict[str, object]]) -> subprocess.CompletedProcess[str]:
    completed = subprocess.run(argv, text=True, capture_output=True, check=False)
    log.append(
        {
            "argv": [Path(argv[0]).name, *argv[1:]],
            "returncode": completed.returncode,
            "stdout": completed.stdout,
            "stderr": completed.stderr,
        }
    )
    if completed.returncode != 0:
        raise RuntimeError(f"documented invocation failed: {Path(argv[0]).name} exit={completed.returncode}")
    return completed


def load_ligands(path: Path) -> list[Chem.Mol]:
    suffix = path.suffix.lower()
    if suffix == ".sdf":
        molecules = [mol for mol in Chem.SDMolSupplier(str(path), removeHs=False) if mol is not None]
    elif suffix == ".mol":
        molecule = Chem.MolFromMolFile(str(path), removeHs=False)
        molecules = [molecule] if molecule is not None else []
    elif suffix == ".mol2":
        molecule = Chem.MolFromMol2File(str(path), removeHs=False)
        molecules = [molecule] if molecule is not None else []
    else:
        raise ValueError(f"unsupported ligand format: {suffix}")
    if not molecules:
        raise ValueError("ligand input contains no valid molecules")
    return molecules


def normalize_ligands(source: Path, destination: Path, seed: int) -> list[str]:
    molecules = load_ligands(source)
    candidate_ids: list[str] = []
    writer = Chem.SDWriter(str(destination))
    try:
        for index, original in enumerate(molecules, start=1):
            molecule = Chem.AddHs(original, addCoords=True)
            needs_conformer = molecule.GetNumConformers() == 0 or not molecule.GetConformer().Is3D()
            if needs_conformer:
                parameters = AllChem.ETKDGv3()
                parameters.randomSeed = int(seed)
                if AllChem.EmbedMolecule(molecule, parameters) != 0:
                    raise ValueError(f"3D conformer generation failed for ligand {index}")
                AllChem.UFFOptimizeMolecule(molecule, maxIters=500)
            name = molecule.GetProp("_Name").strip() if molecule.HasProp("_Name") else ""
            candidate_id = name or f"ligand-{index:04d}"
            if candidate_id in candidate_ids:
                raise ValueError(f"ligand input contains duplicate candidate ID: {candidate_id}")
            molecule.SetProp("_Name", candidate_id)
            candidate_ids.append(candidate_id)
            writer.write(molecule)
    finally:
        writer.close()
    return candidate_ids


def pdb_atom_record(line: str) -> dict[str, object]:
    if len(line) < 54 or line[:6].strip() not in {"ATOM", "HETATM"}:
        raise ValueError("invalid PDB/PDBQT atom record")
    atom_name = line[12:16].strip() or "X"
    element = line[76:78].strip() if len(line) >= 78 else ""
    if not element:
        autodock_type = line.split()[-1].upper() if line.split() else ""
        element = AUTODOCK_ELEMENTS.get(autodock_type, re.sub(r"[^A-Za-z]", "", atom_name)[:2].title())
    return {
        "record": line[:6].strip(),
        "atom_name": atom_name,
        "residue_name": line[17:20].strip() or "UNK",
        "chain_id": (line[21:22].strip() or "A")[:1],
        "residue_number": int(line[22:26].strip() or "1"),
        "x": float(line[30:38]),
        "y": float(line[38:46]),
        "z": float(line[46:54]),
        "occupancy": float(line[54:60].strip() or "1.0") if len(line) >= 60 else 1.0,
        "b_iso": float(line[60:66].strip() or "0.0") if len(line) >= 66 else 0.0,
        "element": element,
    }


def format_pdb_atom(
    serial: int,
    atom: dict[str, object],
    *,
    record: str | None = None,
    residue_name: str | None = None,
    chain_id: str | None = None,
    residue_number: int | None = None,
) -> str:
    atom_name = str(atom["atom_name"])[:4]
    return (
        f"{(record or str(atom.get('record') or 'HETATM')):<6}{serial:>5} "
        f"{atom_name:<4} {(residue_name or str(atom.get('residue_name') or 'UNK'))[:3]:>3} "
        f"{(chain_id or str(atom.get('chain_id') or 'A'))[:1]}"
        f"{int(residue_number if residue_number is not None else atom.get('residue_number') or 1):>4}    "
        f"{float(atom['x']):>8.3f}{float(atom['y']):>8.3f}{float(atom['z']):>8.3f}"
        f"{float(atom.get('occupancy') or 1.0):>6.2f}{float(atom.get('b_iso') or 0.0):>6.2f}          "
        f"{str(atom.get('element') or '')[:2]:>2}\n"
    )


def pdb_atom_records(path: Path) -> list[dict[str, object]]:
    return [
        pdb_atom_record(line)
        for line in path.read_text(encoding="utf-8", errors="replace").splitlines()
        if line[:6].strip() in {"ATOM", "HETATM"}
    ]


def write_receptor_pdb_from_pdbqt(source: Path, destination: Path) -> None:
    records = pdb_atom_records(source)
    if not records:
        raise ValueError("PDBQT receptor contains no atom records")
    atomic_text(
        destination,
        "REMARK 900 FIXED RECEPTOR CONVERTED FROM PDBQT\n"
        + "".join(format_pdb_atom(index, atom, record="ATOM") for index, atom in enumerate(records, start=1))
        + "END\n",
    )


def pose_atom_records(path: Path) -> list[list[dict[str, object]]]:
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    saw_model = False
    poses: list[list[dict[str, object]]] = []
    atoms: list[dict[str, object]] = []
    for line in lines:
        if line.startswith("MODEL"):
            if atoms:
                poses.append(atoms)
                atoms = []
            saw_model = True
            continue
        if line.startswith("ENDMDL"):
            if atoms:
                poses.append(atoms)
                atoms = []
            continue
        if line[:6].strip() in {"ATOM", "HETATM"}:
            atoms.append(pdb_atom_record(line))
    if atoms:
        poses.append(atoms)
    if not poses:
        raise ValueError(f"docking poses contain no atom records: {path.name}")
    if not saw_model and len(poses) != 1:
        raise ValueError(f"unmodelled pose file produced multiple atom groups: {path.name}")
    return poses


def pose_model_texts(path: Path) -> list[str]:
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines(keepends=True)
    models: list[str] = []
    current: list[str] = []
    saw_model = False
    for line in lines:
        if line.startswith("MODEL"):
            if current:
                raise ValueError(f"unterminated pose model in {path.name}")
            saw_model = True
            current = [line]
            continue
        if not saw_model:
            continue
        current.append(line)
        if line.startswith("ENDMDL"):
            models.append("".join(current))
            current = []
    if current:
        raise ValueError(f"unterminated pose model in {path.name}")
    if models:
        return models
    text = "".join(lines)
    if not text.strip():
        raise ValueError(f"docking pose file is empty: {path.name}")
    return [text if text.endswith("\n") else text + "\n"]


def component_residue_name(rank: int) -> str:
    alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
    if rank < 1 or rank >= len(alphabet) ** 2:
        raise ValueError("PDB component rank exceeds the two-character base36 range")
    return "D" + alphabet[rank // len(alphabet)] + alphabet[rank % len(alphabet)]


def build_docking_complex_ensemble(
    receptor_pdb: Path,
    reference_component: dict[str, object] | None,
    rows: list[dict[str, object]],
    primary_poses: dict[str, dict[str, object]],
    pdb_path: Path,
    components_path: Path,
) -> dict[str, object]:
    receptor_atoms = pdb_atom_records(receptor_pdb)
    if not receptor_atoms:
        raise ValueError("selected receptor contains no atom records")
    protein_chains = sorted({str(atom["chain_id"]) for atom in receptor_atoms})
    component_chain = next((chain for chain in PDB_CHAIN_CANDIDATES if chain not in protein_chains), "Z")
    output: list[str] = [
        "REMARK 900 SYNON BIOMED DOCKING COMPLEX ENSEMBLE\n",
        "REMARK 900 FIXED RECEPTOR APPEARS ONCE; EACH LIGAND IS A SEPARATE RESIDUE COMPONENT\n",
    ]
    component_rows: list[dict[str, object]] = []
    serial = 1
    for atom in receptor_atoms:
        output.append(format_pdb_atom(serial, atom, record="ATOM"))
        serial += 1
    output.append("TER\n")
    for chain in protein_chains:
        component_rows.append(
            {
                "component_kind": "protein",
                "candidate_id": "",
                "rank": "",
                "pose_rank": "",
                "chain_id": chain,
                "residue_name": "",
                "residue_number": "",
                "best_affinity_kcal_mol": "",
                "affinity_kcal_mol": "",
                "sample_run": "",
                "source_mode": "",
                "reference_centroid_distance_angstrom": "",
                "reference_axis_cosine": "",
                "source": receptor_pdb.name,
            }
        )

    reference_count = 0
    if reference_component is not None:
        reference_atoms = list(reference_component.get("atoms") or [])
        if not reference_atoms:
            raise ValueError("reference ligand component contains no atoms")
        output.append(
            "REMARK 900 REFERENCE LIGAND REF "
            f"SOURCE {reference_component['component_id']} CHAIN {reference_component['source_chain']} "
            f"RESIDUE {reference_component['source_residue_number']}\n"
        )
        for atom in reference_atoms:
            output.append(
                format_pdb_atom(
                    serial,
                    atom,
                    record="HETATM",
                    residue_name="REF",
                    chain_id=component_chain,
                    residue_number=1,
                )
            )
            serial += 1
        output.append("TER\n")
        reference_count = 1
        component_rows.append(
            {
                "component_kind": "reference_ligand",
                "candidate_id": reference_component["component_id"],
                "rank": 0,
                "pose_rank": 0,
                "chain_id": component_chain,
                "residue_name": "REF",
                "residue_number": 1,
                "best_affinity_kcal_mol": "",
                "affinity_kcal_mol": "",
                "sample_run": "",
                "source_mode": "",
                "reference_centroid_distance_angstrom": "",
                "reference_axis_cosine": "",
                "source": (
                    f"{reference_component['component_id']}:{reference_component['source_chain']}:"
                    f"{reference_component['source_residue_number']}"
                ),
            }
        )

    docked_pose_count = 0
    for row in rows:
        rank = int(row["rank"])
        candidate_id = str(row["ligand_id"])
        primary = primary_poses[candidate_id]
        pose_atoms = list(primary["atoms"])
        affinity = float(primary["affinity_kcal_mol"])
        residue_name = component_residue_name(rank)
        residue_number = 100 + rank
        output.append(
            f"REMARK 900 DOCKED LIGAND {residue_name} CANDIDATE {candidate_id} "
            f"RANK {rank} POSE 1 AFFINITY {affinity} KCAL/MOL "
            f"RUN {primary['run_index']} SOURCE_MODE {primary['mode_index']}\n"
        )
        for atom in pose_atoms:
            output.append(
                format_pdb_atom(
                    serial,
                    atom,
                    record="HETATM",
                    residue_name=residue_name,
                    chain_id=component_chain,
                    residue_number=residue_number,
                )
            )
            serial += 1
        output.append("TER\n")
        component_rows.append(
            {
                "component_kind": "docked_ligand",
                "candidate_id": candidate_id,
                "rank": rank,
                "pose_rank": 1,
                "chain_id": component_chain,
                "residue_name": residue_name,
                "residue_number": residue_number,
                "best_affinity_kcal_mol": row["best_affinity_kcal_mol"],
                "affinity_kcal_mol": affinity,
                "sample_run": primary["run_index"],
                "source_mode": primary["mode_index"],
                "reference_centroid_distance_angstrom": primary["reference_centroid_distance_angstrom"],
                "reference_axis_cosine": primary["reference_axis_cosine"],
                "source": primary["source_pose_file"],
            }
        )
        docked_pose_count += 1
    output.append("END\n")
    atomic_text(pdb_path, "".join(output))
    with components_path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(component_rows[0]))
        writer.writeheader()
        writer.writerows(component_rows)
    selection_evidence = primary_selection_evidence(primary_poses)
    return {
        "protein_atom_count": len(receptor_atoms),
        "protein_chain_count": len(protein_chains),
        "reference_ligand_count": reference_count,
        "docked_ligand_count": len(rows),
        "docked_pose_count": docked_pose_count,
        "poses_per_candidate": 1,
        **selection_evidence,
        "component_chain": component_chain,
    }


def initialize_receptor_entities(structure: gemmi.Structure) -> None:
    # PDB writers commonly omit SEQRES/entity annotations even when ATOM
    # records contain ordinary amino-acid chains. Gemmi leaves those residues
    # Unknown until entity setup; initialize once before every polymer test.
    structure.setup_entities()


def normalize_receptor_residue_aliases(model: gemmi.Model) -> list[dict[str, object]]:
    changes: list[dict[str, object]] = []
    for chain in model:
        for residue in chain:
            source_name = residue.name.upper()
            target_name = RECEPTOR_RESIDUE_ALIASES.get(source_name)
            if target_name is None:
                continue
            residue.name = target_name
            changes.append(
                {
                    "chain_id": chain.name,
                    "residue_number": residue.seqid.num,
                    "source_name": source_name,
                    "target_name": target_name,
                }
            )
    return changes


def select_receptor(
    source: Path,
    destination: Path,
    reference_ligand: str | None,
    requested_chains: list[str],
    contact_cutoff: float,
    log: list[dict[str, object]],
) -> tuple[tuple[float, float, float] | None, dict[str, object] | None]:
    structure = gemmi.read_structure(str(source))
    if len(structure) == 0:
        raise ValueError("receptor structure contains no models")
    initialize_receptor_entities(structure)
    model = structure[0]
    residue_aliases = normalize_receptor_residue_aliases(model)
    selected = {name.strip() for name in requested_chains if name.strip()}
    available = {chain.name for chain in model}
    if selected - available:
        raise ValueError(f"requested receptor chains are unavailable: {sorted(selected - available)}")
    reference_component: dict[str, object] | None = None
    ligand_atoms: list[gemmi.Position] = []
    if reference_ligand:
        requested_component = reference_ligand.strip().upper()
        automatic = requested_component == "AUTO"
        site_scores = pdb_reference_site_scores(source) if automatic else {}
        candidates: list[tuple[int, int, int, str, int, gemmi.Residue]] = []
        for chain in model:
            for residue in chain:
                component = residue.name.upper()
                if residue.entity_type != gemmi.EntityType.NonPolymer or (
                    not automatic and component != requested_component
                ):
                    continue
                heavy_atom_count = sum(1 for atom in residue if atom.element.name.upper() != "H")
                if automatic and heavy_atom_count < 6:
                    continue
                contact_count = 0
                for protein_chain in model:
                    if selected and protein_chain.name not in selected:
                        continue
                    for protein_residue in protein_chain:
                        if protein_residue.entity_type != gemmi.EntityType.Polymer:
                            continue
                        for protein_atom in protein_residue:
                            if any(
                                protein_atom.pos.dist(ligand_atom.pos) <= contact_cutoff
                                for ligand_atom in residue
                            ):
                                contact_count += 1
                candidates.append(
                    (
                        site_scores.get((component, chain.name, residue.seqid.num), 0),
                        contact_count,
                        heavy_atom_count,
                        chain.name,
                        residue.seqid.num,
                        residue,
                    )
                )
        if not candidates:
            if automatic:
                raise ValueError(
                    "no plausible bound organic ligand was found in the raw receptor; "
                    "supply P2Rank pocket receipts or an explicit reference ligand"
                )
            raise ValueError(f"reference ligand {requested_component} was not found in receptor coordinates")
        if automatic:
            annotated = [candidate for candidate in candidates if candidate[0] > 0]
            if annotated:
                best_site_size = max(candidate[0] for candidate in annotated)
                best_site_components = {
                    candidate[5].name.upper() for candidate in annotated if candidate[0] == best_site_size
                }
                if len(best_site_components) != 1:
                    raise ValueError(
                        "raw receptor SITE annotations do not identify one unambiguous reference ligand; "
                        f"supply --reference-ligand from {sorted(best_site_components)}"
                    )
                candidates = [
                    candidate for candidate in annotated if candidate[5].name.upper() in best_site_components
                ]
            else:
                component_ids = {candidate[5].name.upper() for candidate in candidates}
                if len(component_ids) != 1:
                    raise ValueError(
                        "raw receptor contains multiple plausible bound non-polymers without an unambiguous SITE annotation; "
                        f"supply --reference-ligand from {sorted(component_ids)} or use P2Rank"
                    )
        candidates.sort(key=lambda item: (-item[0], -item[1], -item[2], item[3], item[4]))
        site_size, _, _, ligand_chain, ligand_residue_number, ligand_residue = candidates[0]
        component = ligand_residue.name.upper()
        ligand_atoms = [atom.pos for atom in ligand_residue]
        reference_component = {
            "component_id": component,
            "source_chain": ligand_chain,
            "source_residue_number": ligand_residue_number,
            "atoms": [
                {
                    "atom_name": atom.name,
                    "x": atom.pos.x,
                    "y": atom.pos.y,
                    "z": atom.pos.z,
                    "occupancy": atom.occ,
                    "b_iso": atom.b_iso,
                    "element": atom.element.name,
                }
                for atom in ligand_residue
            ],
        }
        log.append(
            {
                "operation": "select_reference_ligand",
                "selection": "pdb_site_annotation"
                if automatic and site_size > 0
                else ("single_plausible_nonpolymer" if automatic else "explicit_component"),
                "component_id": component,
                "chain_id": ligand_chain,
                "residue_number": ligand_residue_number,
                "site_residue_count": site_size,
            }
        )
        if not selected:
            for chain in model:
                contacts = False
                for residue in chain:
                    if residue.entity_type != gemmi.EntityType.Polymer:
                        continue
                    for atom in residue:
                        if any(atom.pos.dist(ligand) <= contact_cutoff for ligand in ligand_atoms):
                            contacts = True
                            break
                    if contacts:
                        break
                if contacts:
                    selected.add(chain.name)
    if not selected:
        selected = {
            chain.name
            for chain in model
            if any(residue.entity_type == gemmi.EntityType.Polymer for residue in chain)
        }
    if not selected:
        raise ValueError("receptor selection contains no polymer chains")

    cleaned = structure.clone()
    for cleaned_model in cleaned:
        for chain_name in [chain.name for chain in cleaned_model if chain.name not in selected]:
            cleaned_model.remove_chain(chain_name)
    cleaned.remove_ligands_and_waters()
    cleaned.remove_hydrogens()
    cleaned.remove_alternative_conformations()
    cleaned.remove_empty_chains()
    cleaned.write_minimal_pdb(str(destination))
    if not destination.is_file() or destination.stat().st_size == 0:
        raise RuntimeError("receptor selection did not produce a non-empty PDB")
    derived_center = None
    if ligand_atoms:
        atom_count = float(len(ligand_atoms))
        derived_center = (
            sum(position.x for position in ligand_atoms) / atom_count,
            sum(position.y for position in ligand_atoms) / atom_count,
            sum(position.z for position in ligand_atoms) / atom_count,
        )
    log.append(
        {
            "operation": "select_receptor",
            "source": source.name,
            "reference_ligand": reference_ligand,
            "reference_ligand_instance": None if reference_component is None else {
                "component_id": reference_component["component_id"],
                "source_chain": reference_component["source_chain"],
                "source_residue_number": reference_component["source_residue_number"],
                "atom_count": len(reference_component["atoms"]),
            },
            "requested_chains": requested_chains,
            "selected_chains": sorted(selected),
            "contact_cutoff": contact_cutoff,
            "output": destination.name,
            "sha256": sha256_file(destination),
            "derived_center": derived_center,
            "normalized_residue_aliases": residue_aliases,
        }
    )
    return derived_center, reference_component


def prepare_receptor(
    source: Path,
    destination: Path,
    work: Path,
    reference_ligand: str | None,
    receptor_chains: list[str],
    receptor_contact_cutoff: float,
    log: list[dict[str, object]],
) -> tuple[tuple[float, float, float] | None, Path, dict[str, object] | None]:
    if source.suffix.lower() == ".pdbqt":
        shutil.copyfile(source, destination)
        selected = work / "selected_receptor.pdb"
        write_receptor_pdb_from_pdbqt(source, selected)
        return None, selected, None
    executable = shutil.which("mk_prepare_receptor.py")
    if executable is None:
        raise RuntimeError("documented CLI is unavailable: mk_prepare_receptor.py")
    selected = work / "selected_receptor.pdb"
    derived_center, reference_component = select_receptor(
        source, selected, reference_ligand, receptor_chains, receptor_contact_cutoff, log
    )
    base = work / "receptor"
    run_checked(
        [executable, "--read_pdb", str(selected), "-o", str(base), "-p", str(destination), "--allow_bad_res"],
        log,
    )
    if not destination.is_file() or destination.stat().st_size == 0:
        raise RuntimeError("receptor preparation did not produce PDBQT")
    return derived_center, selected, reference_component


def prepare_ligands(
    source: Path,
    directory: Path,
    work: Path,
    seed: int,
    log: list[dict[str, object]],
) -> tuple[list[tuple[str, Path]], int]:
    directory.mkdir(parents=True, exist_ok=True)
    if source.suffix.lower() == ".pdbqt":
        destination = directory / "ligand-0001.pdbqt"
        shutil.copyfile(source, destination)
        return [(source.stem, destination)], 1
    normalized = work / "normalized_ligands.sdf"
    candidate_ids = normalize_ligands(source, normalized, seed)
    count = len(candidate_ids)
    executable = shutil.which("mk_prepare_ligand.py")
    if executable is None:
        raise RuntimeError("documented CLI is unavailable: mk_prepare_ligand.py")
    run_checked(
        [
            executable,
            "-i",
            str(normalized),
            "--multimol_outdir",
            str(directory),
            "--multimol_prefix",
            "ligand",
        ],
        log,
    )
    prepared_with_indices: list[tuple[int, Path]] = []
    for path in directory.glob("*.pdbqt"):
        match = PREPARED_LIGAND_INDEX.search(path.stem)
        if match is None:
            raise RuntimeError(f"prepared ligand filename has no stable numeric index: {path.name}")
        prepared_with_indices.append((int(match.group(1)), path))
    prepared_with_indices.sort(key=lambda item: item[0])
    prepared = [path for _, path in prepared_with_indices]
    if len(prepared) != count:
        raise RuntimeError(f"ligand preparation count mismatch: input={count} prepared={len(prepared)}")
    indices = [index for index, _ in prepared_with_indices]
    if indices == list(range(count)):
        offset = 0
    elif indices == list(range(1, count + 1)):
        offset = 1
    else:
        raise RuntimeError(f"prepared ligand indices are not contiguous: {indices[:10]}")
    return [
        (candidate_ids[index - offset], path)
        for index, path in prepared_with_indices
    ], count


def affinities(path: Path) -> list[float]:
    values: list[float] = []
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        match = VINA_RESULT.match(line)
        if match:
            value = float(match.group(1))
            if not math.isfinite(value):
                raise ValueError(f"non-finite affinity in {path.name}")
            values.append(value)
    if not values:
        raise ValueError(f"no Vina affinity records in {path.name}")
    return values


def pose_samples(path: Path, run_index: int, seed: int) -> list[dict[str, object]]:
    atoms = pose_atom_records(path)
    scores = affinities(path)
    model_texts = pose_model_texts(path)
    if len(atoms) != len(scores) or len(atoms) != len(model_texts):
        raise ValueError(
            f"pose model fidelity failed for {path.name}: "
            f"atoms={len(atoms)} scores={len(scores)} blocks={len(model_texts)}"
        )
    return [
        {
            "affinity_kcal_mol": affinity,
            "atoms": pose_atoms,
            "pdbqt_text": model_text,
            "run_index": run_index,
            "mode_index": mode_index,
            "seed": seed,
            "source_pose_file": path.name,
        }
        for mode_index, (pose_atoms, affinity, model_text) in enumerate(
            zip(atoms, scores, model_texts, strict=True), start=1
        )
    ]


def parser() -> argparse.ArgumentParser:
    value = argparse.ArgumentParser(description="Governed AutoDock Vina execution pack")
    value.add_argument("--receptor", required=True)
    value.add_argument("--ligand", required=True)
    value.add_argument("--reference-ligand")
    value.add_argument("--pocket-selection")
    value.add_argument("--pocket-validation")
    value.add_argument("--report-language", choices=["en", "zh"], default="en")
    value.add_argument("--receptor-chain", action="append", default=[])
    value.add_argument("--receptor-contact-cutoff", type=float, default=8.0)
    for axis in ("x", "y", "z"):
        value.add_argument(f"--center-{axis}", type=float)
        value.add_argument(f"--size-{axis}", type=float)
    value.add_argument("--center-authority", choices=["resolved-user-input"])
    value.add_argument("--seed", type=int, default=42)
    value.add_argument("--repeat-count", type=int, default=3)
    value.add_argument("--exhaustiveness", type=int, default=8)
    value.add_argument("--num-modes", type=int, default=9)
    value.add_argument("--output-dir", default=DEFAULT_OUTPUT_DIR)
    return value


def validate_docking_center_contract(
    receptor_source: Path,
    reference_ligand: str | None,
    has_pocket_selection: bool,
    has_pocket_validation: bool,
    explicit_center: tuple[float | None, float | None, float | None],
    center_authority: str | None,
) -> None:
    has_any_explicit = any(value is not None for value in explicit_center)
    has_complete_explicit = all(value is not None for value in explicit_center)
    if has_any_explicit and not has_complete_explicit:
        raise ValueError("docking center requires all three --center-x/--center-y/--center-z values")
    if has_pocket_selection != has_pocket_validation:
        raise ValueError("--pocket-selection and --pocket-validation must be supplied together")
    if has_pocket_selection:
        if receptor_source.suffix.lower() == ".pdbqt":
            raise ValueError("predicted pocket receipts require raw PDB receptor coordinates")
        if reference_ligand or has_any_explicit or center_authority:
            raise ValueError("predicted pocket receipts cannot be combined with another docking-center authority")
        return
    if receptor_source.suffix.lower() == ".pdbqt":
        if reference_ligand:
            raise ValueError("--reference-ligand requires raw PDB or mmCIF receptor coordinates")
        if not has_complete_explicit:
            raise ValueError("a prepared PDBQT receptor requires all three explicit docking-center values")
        if center_authority != "resolved-user-input":
            raise ValueError("explicit docking-center values require resolved user-input authority")
        return
    if reference_ligand:
        if has_any_explicit:
            raise ValueError("raw receptor coordinates with --reference-ligand must use its verified centroid")
        return
    if has_complete_explicit and center_authority == "resolved-user-input":
        return
    raise ValueError(
        "validated binding-site evidence is required for raw receptor coordinates; "
        "supply a P2Rank pocket receipt, --reference-ligand from the receptor, or resolved user-input authority"
    )


def validate_box_size_contract(
    has_pocket_selection: bool,
    explicit_size: tuple[float | None, float | None, float | None],
) -> None:
    has_any = any(value is not None for value in explicit_size)
    has_all = all(value is not None for value in explicit_size)
    if has_pocket_selection:
        if has_any:
            raise ValueError("a predicted pocket receipt owns its docking-box size; do not override --size-*")
        return
    if not has_all:
        raise ValueError("docking box requires all three --size-x/--size-y/--size-z values")
    if any(not math.isfinite(float(value)) or float(value) < 1 or float(value) > 100 for value in explicit_size):
        raise ValueError("docking-box sizes must be finite values between 1 and 100 Angstrom")


def next_default_output_target(root: Path) -> Path:
    for index in range(2, 1000):
        candidate = (root / f"{DEFAULT_OUTPUT_DIR}-{index}").resolve()
        if not candidate.exists() and not candidate.is_symlink():
            return candidate
    raise ValueError("no collision-free default AutoDock Vina output directory is available")


def validate_output_target(
    root: Path,
    output: Path,
    inputs: tuple[Path, ...],
    redirect_default: bool = False,
) -> Path:
    if output == root or root not in output.parents:
        raise ValueError("output directory must stay inside the authorized working directory")
    for source in inputs:
        if output == source or output in source.parents or source in output.parents:
            raise ValueError("output directory must not overlap an input path or its ancestors")
    if output.is_symlink():
        raise ValueError("output directory must not be a symbolic link")
    if not output.exists():
        return output
    if redirect_default:
        return next_default_output_target(root)
    raise ValueError("explicit output directory must not already exist")


def validated_internal_state_directory(root: Path, path: Path, label: str) -> Path:
    if path.is_symlink():
        raise ValueError(f"{label} directory must not be a symbolic link")
    resolved = path.resolve()
    if resolved != root and root not in resolved.parents:
        raise ValueError(f"{label} directory escapes the authorized working directory")
    if path.exists() and not path.is_dir():
        raise ValueError(f"{label} path is not a directory")
    path.mkdir(parents=True, exist_ok=True)
    if path.is_symlink() or path.resolve() != resolved:
        raise ValueError(f"{label} directory changed during validation")
    return path


def promote_execution_output(
    staging: Path, target: Path, token: str, root: Path | None = None
) -> dict[str, str | None]:
    root = root.resolve() if root is not None else target.parent.resolve()
    atomic_text(
        staging / OUTPUT_OWNERSHIP_MARKER,
        json.dumps(
            {"execution_pack_id": EXECUTION_PACK_ID, "schema": "synon.execution-pack-output-owner.v1"},
            sort_keys=True,
        )
        + "\n",
    )
    if target.exists() or target.is_symlink():
        raise RuntimeError("execution output target was created before promotion")
    staging.rename(target)
    return {
        "schema": "synon.execution-pack-output-promotion.v1",
        "execution_pack_id": EXECUTION_PACK_ID,
        "generation_id": token,
        "current": str(target.relative_to(root)),
        "previous": None,
    }


def main() -> int:
    args = parser().parse_args()
    if args.repeat_count < 1 or args.repeat_count > 64:
        raise ValueError("--repeat-count must be between 1 and 64")
    if args.num_modes < 1:
        raise ValueError("--num-modes must be positive")
    if args.seed + args.repeat_count - 1 > 2_147_483_647:
        raise ValueError("repeat seed range exceeds Vina's signed integer range")
    root = Path.cwd().resolve()
    receptor_source = (root / args.receptor).resolve()
    ligand_source = (root / args.ligand).resolve()
    for source in (receptor_source, ligand_source):
        if root != source and root not in source.parents:
            raise ValueError("input path escapes the authorized working directory")
        if not source.is_file() or source.stat().st_size == 0:
            raise ValueError(f"input is missing or empty: {source.name}")
    pocket_selection_source = None
    pocket_validation_source = None
    for raw, label in (
        (args.pocket_selection, "pocket selection"),
        (args.pocket_validation, "pocket validation"),
    ):
        if not raw:
            continue
        source = (root / raw).resolve()
        if root != source and root not in source.parents:
            raise ValueError(f"{label} escapes the authorized working directory")
        if not source.is_file() or source.stat().st_size == 0:
            raise ValueError(f"{label} is missing or empty")
        if label == "pocket selection":
            pocket_selection_source = source
        else:
            pocket_validation_source = source
    explicit_center = (args.center_x, args.center_y, args.center_z)
    explicit_size = (args.size_x, args.size_y, args.size_z)
    if (
        receptor_source.suffix.lower() != ".pdbqt"
        and not args.reference_ligand
        and pocket_selection_source is None
        and not any(value is not None for value in explicit_center)
    ):
        args.reference_ligand = "auto"
    validate_docking_center_contract(
        receptor_source,
        args.reference_ligand,
        pocket_selection_source is not None,
        pocket_validation_source is not None,
        explicit_center,
        args.center_authority,
    )
    validate_box_size_contract(pocket_selection_source is not None, explicit_size)
    pocket_evidence = None
    if pocket_selection_source is not None and pocket_validation_source is not None:
        pocket_evidence = load_validated_pocket_selection(
            pocket_selection_source, pocket_validation_source, receptor_source
        )
    target_output = (root / args.output_dir).resolve()
    input_paths = tuple(
        source
        for source in (receptor_source, ligand_source, pocket_selection_source, pocket_validation_source)
        if source is not None
    )
    target_output = validate_output_target(
        root, target_output, input_paths, Path(args.output_dir) == Path(DEFAULT_OUTPUT_DIR)
    )
    run_token = uuid.uuid4().hex
    output = root / f".vina-pack-output-{run_token}"
    work = root / f".vina-pack-work-{run_token}"
    failure_root = validated_internal_state_directory(root, root / ".vina-pack-failures", "failure")
    failure = failure_root / run_token
    invocation_log: list[dict[str, object]] = []
    log_path = output / "vina.log"
    try:
        required_executables = ["vina"]
        if ligand_source.suffix.lower() == ".cdx":
            required_executables.append("obabel")
        if ligand_source.suffix.lower() != ".pdbqt":
            required_executables.append("mk_prepare_ligand.py")
        if receptor_source.suffix.lower() != ".pdbqt":
            required_executables.append("mk_prepare_receptor.py")
        for executable_name in required_executables:
            executable_path = shutil.which(executable_name)
            if executable_path is None:
                raise RuntimeError(f"documented CLI is unavailable: {executable_name}")
            run_checked([executable_path, "--help"], invocation_log)
        output.mkdir(parents=False)
        work.mkdir(parents=False)
        if pocket_selection_source is not None and pocket_validation_source is not None:
            shutil.copy2(pocket_selection_source, output / "binding_site_selection.json")
            shutil.copy2(pocket_validation_source, output / "binding_site_validation.json")
        receptor = work / "receptor.pdbqt"
        ligand_dir = work / "ligands"
        pose_dir = work / "poses"
        pose_dir.mkdir()
        derived_center, selected_receptor_pdb, reference_component = prepare_receptor(
            receptor_source,
            receptor,
            work,
            args.reference_ligand,
            args.receptor_chain,
            args.receptor_contact_cutoff,
            invocation_log,
        )
        if pocket_evidence is not None:
            center_x, center_y, center_z = pocket_evidence["center"]
            args.size_x, args.size_y, args.size_z = pocket_evidence["size"]
            center_source = "p2rank_predicted_pocket"
        elif all(value is not None for value in explicit_center):
            center_x, center_y, center_z = explicit_center
            center_source = "resolved_user_input_explicit"
        elif derived_center is not None:
            center_x, center_y, center_z = derived_center
            center_source = "reference_ligand_centroid"
        else:
            raise ValueError("docking center is required unless a reference ligand is present in raw receptor coordinates")
        invocation_log.append(
            {
                "operation": "resolve_docking_center",
                "source": center_source,
                "center_x": center_x,
                "center_y": center_y,
                "center_z": center_z,
                "size_x": args.size_x,
                "size_y": args.size_y,
                "size_z": args.size_z,
                "pocket_evidence": pocket_evidence,
            }
        )
        prepared_ligand_source = convert_ligand_source(ligand_source, work, invocation_log)
        ligands, input_count = prepare_ligands(prepared_ligand_source, ligand_dir, work, args.seed, invocation_log)
        expected_candidate_ids = {candidate_id for candidate_id, _ in ligands}
        vina = shutil.which("vina")
        if vina is None:
            raise RuntimeError("documented CLI is unavailable: vina")
        rows: list[dict[str, object]] = []
        sampled_poses: dict[str, list[dict[str, object]]] = {}
        primary_poses: dict[str, dict[str, object]] = {}
        reference_atoms = None if reference_component is None else list(reference_component.get("atoms") or [])
        receptor_sha = sha256_file(receptor_source)
        for index, (ligand_id, ligand) in enumerate(ligands, start=1):
            samples: list[dict[str, object]] = []
            for run_index in range(1, args.repeat_count + 1):
                run_seed = args.seed + run_index - 1
                pose = pose_dir / f"pose-{index:04d}-run-{run_index:02d}.pdbqt"
                argv = [
                    vina,
                    "--receptor",
                    str(receptor),
                    "--ligand",
                    str(ligand),
                    "--center_x",
                    str(center_x),
                    "--center_y",
                    str(center_y),
                    "--center_z",
                    str(center_z),
                    "--size_x",
                    str(args.size_x),
                    "--size_y",
                    str(args.size_y),
                    "--size_z",
                    str(args.size_z),
                    "--seed",
                    str(run_seed),
                    "--exhaustiveness",
                    str(args.exhaustiveness),
                    "--num_modes",
                    str(args.num_modes),
                    "--out",
                    str(pose),
                ]
                run_checked(argv, invocation_log)
                samples.extend(pose_samples(pose, run_index, run_seed))
            primary = select_primary_pose(samples, reference_atoms)
            rows.append(
                {
                    "ligand_id": ligand_id,
                    "best_affinity_kcal_mol": primary["affinity_kcal_mol"],
                    "mode_count": len(samples),
                    "repeat_count": args.repeat_count,
                    "receptor_sha256": receptor_sha,
                    "center_x": center_x,
                    "center_y": center_y,
                    "center_z": center_z,
                    "size_x": args.size_x,
                    "size_y": args.size_y,
                    "size_z": args.size_z,
                    "seed": args.seed,
                    "exhaustiveness": args.exhaustiveness,
                    "num_modes": args.num_modes,
                    "poses_per_candidate": 1,
                    "primary_pose_run": primary["run_index"],
                    "primary_pose_mode": primary["mode_index"],
                    "reference_centroid_distance_angstrom": primary["reference_centroid_distance_angstrom"],
                    "reference_axis_cosine": primary["reference_axis_cosine"],
                    "primary_pose_selection": primary["selection_basis"],
                }
            )
            sampled_poses[ligand_id] = samples
            primary_poses[ligand_id] = primary
        rows.sort(key=lambda row: (float(row["best_affinity_kcal_mol"]), str(row["ligand_id"])))
        for rank, row in enumerate(rows, start=1):
            row["rank"] = rank
        scores_path = output / "docking_scores.csv"
        with scores_path.open("w", newline="", encoding="utf-8") as handle:
            writer = csv.DictWriter(handle, fieldnames=list(rows[0]))
            writer.writeheader()
            writer.writerows(rows)
        pose_scores_path = output / "docking_pose_scores.csv"
        pose_score_rows = [
            {
                "candidate_id": row["ligand_id"],
                "candidate_rank": row["rank"],
                "pose_rank": 1,
                "affinity_kcal_mol": primary_poses[str(row["ligand_id"])]["affinity_kcal_mol"],
                "sample_run": primary_poses[str(row["ligand_id"])]["run_index"],
                "source_mode": primary_poses[str(row["ligand_id"])]["mode_index"],
                "reference_centroid_distance_angstrom": primary_poses[str(row["ligand_id"])][
                    "reference_centroid_distance_angstrom"
                ],
                "reference_axis_cosine": primary_poses[str(row["ligand_id"])]["reference_axis_cosine"],
            }
            for row in rows
        ]
        with pose_scores_path.open("w", newline="", encoding="utf-8") as handle:
            writer = csv.DictWriter(handle, fieldnames=list(pose_score_rows[0]))
            writer.writeheader()
            writer.writerows(pose_score_rows)
        pose_samples_path = output / "docking_pose_samples.csv"
        pose_sample_rows = [
            {
                "candidate_id": row["ligand_id"],
                "candidate_rank": row["rank"],
                "sample_run": sample["run_index"],
                "source_mode": sample["mode_index"],
                "seed": sample["seed"],
                "affinity_kcal_mol": sample["affinity_kcal_mol"],
                "reference_centroid_distance_angstrom": sample["reference_centroid_distance_angstrom"],
                "reference_axis_cosine": sample["reference_axis_cosine"],
                "selected_primary": (
                    sample["run_index"] == primary_poses[str(row["ligand_id"])]["run_index"]
                    and sample["mode_index"] == primary_poses[str(row["ligand_id"])]["mode_index"]
                ),
            }
            for row in rows
            for sample in sampled_poses[str(row["ligand_id"])]
        ]
        with pose_samples_path.open("w", newline="", encoding="utf-8") as handle:
            writer = csv.DictWriter(handle, fieldnames=list(pose_sample_rows[0]))
            writer.writeheader()
            writer.writerows(pose_sample_rows)
        ranked_pose = output / "ranked_poses.pdbqt"
        pose_text: list[str] = []
        for row in rows:
            ligand_id = str(row["ligand_id"])
            primary = primary_poses[ligand_id]
            pose_text.append(
                f"REMARK SYNON LIGAND {ligand_id} RANK {row['rank']} PRIMARY RUN {primary['run_index']} "
                f"SOURCE_MODE {primary['mode_index']}\n"
            )
            pose_text.append(str(primary["pdbqt_text"]))
            pose_text.append("\n")
        atomic_text(ranked_pose, "".join(pose_text))
        primary_pose_paths, primary_pose_manifest, primary_pose_manifest_rows = write_primary_pose_artifacts(
            output, rows, primary_poses
        )
        complex_pdb = output / "docking_complex_ensemble.pdb"
        components_path = output / "docking_components.csv"
        complex_summary = build_docking_complex_ensemble(
            selected_receptor_pdb,
            reference_component,
            rows,
            primary_poses,
            complex_pdb,
            components_path,
        )
        selection_evidence = primary_selection_evidence(primary_poses)
        report_path = output / "docking_report.md"
        write_docking_report(
            report_path,
            receptor_source,
            ligand_source,
            rows,
            center_source,
            reference_component,
            pocket_evidence,
            primary_pose_manifest_rows,
            args,
        )
        atomic_text(log_path, "\n".join(json.dumps(item, sort_keys=True) for item in invocation_log) + "\n")
        shutil.rmtree(work)
        pocket_output_paths = ()
        if pocket_evidence is not None:
            pocket_output_paths = (
                output / "binding_site_selection.json",
                output / "binding_site_validation.json",
            )
        checks = {
            "input_fidelity": len(rows) == input_count and len(rows) == len(ligands),
            "candidate_id_fidelity": (
                {str(row["ligand_id"]) for row in rows} == expected_candidate_ids
                and len(expected_candidate_ids) == input_count
            ),
            "source_integrity": len(receptor_sha) == 64 and len(sha256_file(ligand_source)) == 64,
            "output_integrity": all(
                path.stat().st_size > 0
                for path in (
                    scores_path, pose_scores_path, pose_samples_path, ranked_pose,
                    primary_pose_manifest, report_path, complex_pdb, components_path, log_path,
                    *primary_pose_paths, *pocket_output_paths,
                )
            ),
            "report_language": report_path.read_text(encoding="utf-8").startswith(
                "# 分子对接报告" if args.report_language == "zh" else "# Molecular docking report"
            ),
            "binding_site_evidence": (
                pocket_evidence is None
                or (
                    sha256_file(output / "binding_site_selection.json") == pocket_evidence["selection_sha256"]
                    and sha256_file(output / "binding_site_validation.json") == pocket_evidence["validation_sha256"]
                )
            ),
            "pose_count_fidelity": (
                len(pose_score_rows) == input_count
                and all(len(sampled_poses[candidate_id]) >= args.repeat_count for candidate_id in expected_candidate_ids)
                and sum(1 for row in pose_sample_rows if row["selected_primary"]) == input_count
                and len(primary_pose_paths) == input_count
                and len(primary_pose_manifest_rows) == input_count
            ),
            "complex_component_integrity": (
                int(complex_summary["protein_atom_count"]) > 0
                and int(complex_summary["docked_ligand_count"]) == input_count
                and int(complex_summary["docked_pose_count"]) == input_count
                and int(complex_summary["reference_ligand_count"]) == (1 if reference_component is not None else 0)
            ),
            "selection_evidence_consistency": (
                complex_summary["selection_contract"] == selection_evidence["selection_contract"]
                and complex_summary["observed_selection_bases"]
                == selection_evidence["observed_selection_bases"]
                and sorted({str(row["primary_pose_selection"]) for row in rows})
                == selection_evidence["observed_selection_bases"]
            ),
            "process_cleanup": not work.exists(),
        }
        validation = {
            "schema": "synon.execution-pack-validation.v4",
            "execution_pack_id": EXECUTION_PACK_ID,
            "overall_pass": all(checks.values()),
            "checks": checks,
            "inputs": {
                "receptor": receptor_sha,
                "ligand": sha256_file(ligand_source),
                "pocket_selection": None if pocket_selection_source is None else sha256_file(pocket_selection_source),
                "pocket_validation": None if pocket_validation_source is None else sha256_file(pocket_validation_source),
            },
            "binding_site": {
                "source": center_source,
                "prediction": pocket_evidence,
            },
            "report_language": args.report_language,
            "input_count": input_count,
            "output_count": len(rows),
            "pose_output_count": len(pose_score_rows),
            "primary_pose_file_count": len(primary_pose_paths),
            "sampled_pose_count": len(pose_sample_rows),
            "sampling": {
                "repeat_count": args.repeat_count,
                "num_modes_per_run": args.num_modes,
                "primary_poses_per_candidate": 1,
                "primary_selection": selection_evidence["selection_contract"],
                "observed_selection_bases": selection_evidence["observed_selection_bases"],
            },
            "complex_ensemble": {
                **complex_summary,
                "pdb_sha256": sha256_file(complex_pdb),
                "components_sha256": sha256_file(components_path),
            },
            "score_summary": {
                "best_affinity_kcal_mol": min(float(row["best_affinity_kcal_mol"]) for row in rows),
                "mean_affinity_kcal_mol": round(
                    sum(float(row["best_affinity_kcal_mol"]) for row in rows) / len(rows), 6
                ),
                "worst_affinity_kcal_mol": max(float(row["best_affinity_kcal_mol"]) for row in rows),
            },
            "errors": [],
        }
        atomic_text(output / "validation.json", json.dumps(validation, indent=2, sort_keys=True) + "\n")
        if not validation["overall_pass"]:
            raise RuntimeError("execution pack validation failed")
        promotion = promote_execution_output(output, target_output, run_token, root)
        print(OUTPUT_PROMOTION_RECEIPT_PREFIX + json.dumps(promotion, sort_keys=True))
        print(
            "SYNON_EXECUTION_PACK_INPUT_RECEIPT="
            + json.dumps(
                {
                    "schema": "synon.execution-pack-input-receipt.v1",
                    "execution_pack_id": EXECUTION_PACK_ID,
                    "inputs": validation["inputs"],
                },
                sort_keys=True,
            )
        )
        return 0
    except Exception as error:
        if work.exists():
            shutil.rmtree(work)
        invocation_log.append({"failure": type(error).__name__, "message": str(error)})
        failure.mkdir(parents=True, exist_ok=False)
        atomic_text(failure / "vina.log", "\n".join(json.dumps(item, sort_keys=True) for item in invocation_log) + "\n")
        atomic_text(
            failure / "failure.json",
            json.dumps(
                {
                    "schema": "synon.execution-pack-failure.v1",
                    "execution_pack_id": EXECUTION_PACK_ID,
                    "failure_type": type(error).__name__,
                    "message": str(error),
                },
                indent=2,
                sort_keys=True,
            )
            + "\n",
        )
        if output.exists():
            output.rename(failure / "partial-output")
        raise


if __name__ == "__main__":
    sys.exit(main())
