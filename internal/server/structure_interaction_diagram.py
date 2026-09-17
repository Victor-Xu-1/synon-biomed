#!/usr/bin/env python3
"""Compute publication-grade 2D protein-ligand interaction diagrams."""

from __future__ import annotations

import argparse
import hashlib
import io
import json
import math
import pathlib
import sys
import warnings
from collections import defaultdict, deque
from typing import Any

import cairosvg
import MDAnalysis as mda
import numpy as np
import prolif as plf
from PIL import Image
from prolif.io import MoleculeStandardizer
from rdkit import Chem
from rdkit.Chem import AllChem

from structure_interaction_renderer import render_publication_svg
from structure_interaction_solvent import receptor_surface_exposure

MAX_INPUT_BYTES = 24 << 20
MAX_ATOMS = 50_000
MAX_LIGAND_ATOMS = 512
MAX_INTERACTIONS = 256
MAX_SOLVENT_GRID_CELLS = 250_000
POCKET_RADIUS = 4.5
WATER_RESIDUES = {"DOD", "H2O", "HOH", "WAT"}
SOLVENT_PROBE_RADIUS = 1.4
SOLVENT_SPHERE_SAMPLES = 96
SOLVENT_GRID_PADDING = 8.0
SOLVENT_GRID_SPACING = 1.0

INTERACTION_KIND = {
    "HBAcceptor": ("hydrogen-bond", "protein-to-ligand"),
    "ImplicitHBAcceptor": ("hydrogen-bond", "protein-to-ligand"),
    "HBDonor": ("hydrogen-bond", "ligand-to-protein"),
    "ImplicitHBDonor": ("hydrogen-bond", "ligand-to-protein"),
    "Hydrophobic": ("hydrophobic", None),
    "Anionic": ("ionic", None),
    "Cationic": ("ionic", None),
    "CationPi": ("cation-pi", None),
    "PiCation": ("cation-pi", None),
    "PiStacking": ("pi-stacking", None),
    "FaceToFace": ("pi-stacking", None),
    "EdgeToFace": ("pi-stacking", None),
    "XBAcceptor": ("halogen-bond", None),
    "XBDonor": ("halogen-bond", None),
    "MetalAcceptor": ("metal-coordination", None),
    "MetalDonor": ("metal-coordination", None),
    "VdWContact": ("vdw-contact", None),
}


def bounded_text(path: pathlib.Path) -> str:
    if not path.is_file() or path.stat().st_size <= 0 or path.stat().st_size > MAX_INPUT_BYTES:
        raise ValueError("structure input is missing or exceeds the bounded size limit")
    return path.read_text(encoding="utf-8")


def normalized_element(line: str) -> str:
    current = line[76:78].strip().upper()
    atom_name = line[12:16].strip().lstrip("0123456789")
    # Some docking exporters write only the first letter in the PDB element
    # column (for example atom name ``Cl`` with element ``C``).  A genuinely
    # mixed-case two-letter atom name is unambiguous, unlike protein ``CA``
    # which denotes an alpha carbon.  Recover only that bounded case and keep
    # all other inputs governed by the authoritative element column.
    if len(atom_name) >= 2 and atom_name[0].isupper() and atom_name[1].islower():
        named_candidate = atom_name[:2]
        if (
            Chem.GetPeriodicTable().GetAtomicNumber(named_candidate) > 0
            and (not current or current == "A" or current == named_candidate[0].upper())
        ):
            return named_candidate
    if current == "A":
        return "C"
    if current:
        return current.title()
    if not atom_name:
        raise ValueError("PDB atom element is unavailable")
    candidate = atom_name[0].upper() + (
        atom_name[1].lower() if len(atom_name) > 1 and atom_name[1].islower() else ""
    )
    if Chem.GetPeriodicTable().GetAtomicNumber(candidate) <= 0:
        raise ValueError("PDB atom element is invalid")
    return candidate


def split_complex(
    pdb: str, ligand_residue_name: str
) -> tuple[str, str, list[tuple[float, float, float]]]:
    protein: list[str] = []
    ligand_lines: list[str] = []
    ligand_coordinates: list[tuple[float, float, float]] = []
    atom_count = 0
    for raw in pdb.splitlines():
        if not raw.startswith(("ATOM  ", "HETATM")):
            continue
        atom_count += 1
        if atom_count > MAX_ATOMS:
            raise ValueError("structure contains too many atoms")
        line = raw.ljust(80)
        residue = line[17:20].strip().upper()
        element = normalized_element(line)
        if residue == ligand_residue_name:
            ligand_lines.append(f"{line[:76]}{element:>2}{line[78:]}")
            if element.upper() != "H":
                ligand_coordinates.append((float(line[30:38]), float(line[38:46]), float(line[46:54])))
                if len(ligand_coordinates) > MAX_LIGAND_ATOMS:
                    raise ValueError("selected ligand contains too many heavy atoms")
            continue
        if raw.startswith("ATOM  ") and residue not in WATER_RESIDUES:
            protein.append(f"{line[:76]}{element:>2}{line[78:]}")
    if not protein:
        raise ValueError("protein atoms are unavailable in the complex")
    if not ligand_coordinates:
        raise ValueError("the selected ligand residue is unavailable in the complex")
    protein.append("END")
    ligand_lines.append("END")
    return "\n".join(protein) + "\n", "\n".join(ligand_lines) + "\n", ligand_coordinates


def pocket_residues(
    pdb: str,
    ligand_coordinates: list[tuple[float, float, float]],
    cutoff: float = POCKET_RADIUS,
    limit: int | None = None,
) -> list[dict[str, Any]]:
    grouped: dict[tuple[str, int, str], list[tuple[float, float, float]]] = defaultdict(list)
    for raw in pdb.splitlines():
        if not raw.startswith("ATOM  "):
            continue
        line = raw.ljust(80)
        residue_name = line[17:20].strip().upper()
        if not residue_name or residue_name in WATER_RESIDUES:
            continue
        try:
            residue_number = int(line[22:26])
            coordinate = (float(line[30:38]), float(line[38:46]), float(line[46:54]))
        except ValueError:
            continue
        grouped[(line[21:22].strip(), residue_number, residue_name)].append(coordinate)

    ligand = np.asarray(ligand_coordinates, dtype=float)
    residues: list[dict[str, Any]] = []
    for (chain, number, name), atom_coordinates in grouped.items():
        atoms = np.asarray(atom_coordinates, dtype=float)
        distances = np.linalg.norm(atoms[:, None, :] - ligand[None, :, :], axis=2)
        atom_index, ligand_index = np.unravel_index(int(np.argmin(distances)), distances.shape)
        minimum_distance = float(distances[atom_index, ligand_index])
        if minimum_distance > cutoff:
            continue
        residues.append({
            "key": (chain, number, name),
            "chain": chain,
            "number": number,
            "name": name,
            "centroid_3d": tuple(float(value) for value in atoms.mean(axis=0)),
            "contact_point_3d": tuple(float(value) for value in atoms[atom_index]),
            "closest_ligand_atom": int(ligand_index),
            "minimum_distance": round(minimum_distance, 4),
        })
    residues.sort(key=lambda item: (item["minimum_distance"], item["chain"], item["number"]))
    return residues if limit is None else residues[:limit]


def protein_atom_data(pdb: str) -> tuple[np.ndarray, np.ndarray, list[tuple[str, int, str]]]:
    coordinates: list[tuple[float, float, float]] = []
    radii: list[float] = []
    keys: list[tuple[str, int, str]] = []
    periodic_table = Chem.GetPeriodicTable()
    for raw in pdb.splitlines():
        if not raw.startswith("ATOM  "):
            continue
        line = raw.ljust(80)
        residue_name = line[17:20].strip().upper()
        if residue_name in WATER_RESIDUES:
            continue
        try:
            coordinate = (float(line[30:38]), float(line[38:46]), float(line[46:54]))
            atomic_number = periodic_table.GetAtomicNumber(normalized_element(line))
            residue_number = int(line[22:26])
        except (ValueError, RuntimeError):
            continue
        coordinates.append(coordinate)
        radii.append(float(periodic_table.GetRvdw(atomic_number)))
        keys.append((line[21:22].strip(), residue_number, residue_name))
    if not coordinates:
        raise ValueError("protein atoms are unavailable in the complex")
    return np.asarray(coordinates, dtype=float), np.asarray(radii, dtype=float), keys


def protein_atom_cloud(pdb: str) -> tuple[np.ndarray, np.ndarray]:
    coordinates, radii, _ = protein_atom_data(pdb)
    return coordinates, radii


def fibonacci_sphere(samples: int = SOLVENT_SPHERE_SAMPLES) -> np.ndarray:
    index = np.arange(samples, dtype=float)
    golden_angle = math.pi * (3.0 - math.sqrt(5.0))
    y_value = 1.0 - (index / max(1.0, samples - 1.0)) * 2.0
    radius = np.sqrt(np.maximum(0.0, 1.0 - y_value * y_value))
    theta = golden_angle * index
    return np.column_stack((np.cos(theta) * radius, y_value, np.sin(theta) * radius))


def external_solvent_grid(
    ligand: np.ndarray,
    ligand_radii: np.ndarray,
    protein: np.ndarray,
    protein_radii: np.ndarray,
) -> tuple[np.ndarray, np.ndarray]:
    """Flood-fill probe-center space connected to bulk solvent.

    The grid is deliberately local to the selected ligand. Boundary cells are
    bulk solvent; occupied cells are the solvent-inflated ligand or protein.
    The returned mask therefore distinguishes a real cavity mouth from a local
    patch that is exposed but still trapped inside a closed protein volume.
    """

    lower = ligand.min(axis=0) - SOLVENT_GRID_PADDING
    upper = ligand.max(axis=0) + SOLVENT_GRID_PADDING
    axes = [
        np.arange(lower[index], upper[index] + SOLVENT_GRID_SPACING * 0.5, SOLVENT_GRID_SPACING)
        for index in range(3)
    ]
    grid_cell_count = math.prod(len(axis) for axis in axes)
    if grid_cell_count > MAX_SOLVENT_GRID_CELLS:
        raise ValueError("solvent-connectivity grid exceeds the bounded cell limit")
    mesh = np.stack(np.meshgrid(*axes, indexing="ij"), axis=-1)
    points = mesh.reshape(-1, 3)
    blocked = np.zeros(len(points), dtype=bool)
    atom_sets = (
        (ligand, ligand_radii + SOLVENT_PROBE_RADIUS - 0.05),
        (protein, protein_radii + SOLVENT_PROBE_RADIUS - 0.05),
    )
    for coordinates, radii in atom_sets:
        for start in range(0, len(points), 1024):
            chunk = points[start:start + 1024]
            distances_squared = np.sum((chunk[:, None, :] - coordinates[None, :, :]) ** 2, axis=2)
            blocked[start:start + len(chunk)] |= np.any(distances_squared < radii[None, :] ** 2, axis=1)
    blocked = blocked.reshape(mesh.shape[:-1])

    external = np.zeros_like(blocked)
    frontier: deque[tuple[int, int, int]] = deque()
    last = tuple(size - 1 for size in blocked.shape)
    for index in np.ndindex(blocked.shape):
        if blocked[index] or not any(index[axis] in (0, last[axis]) for axis in range(3)):
            continue
        external[index] = True
        frontier.append(index)
    neighbours = ((1, 0, 0), (-1, 0, 0), (0, 1, 0), (0, -1, 0), (0, 0, 1), (0, 0, -1))
    while frontier:
        current = frontier.popleft()
        for offset in neighbours:
            following = tuple(current[axis] + offset[axis] for axis in range(3))
            if any(following[axis] < 0 or following[axis] > last[axis] for axis in range(3)):
                continue
            if blocked[following] or external[following]:
                continue
            external[following] = True
            frontier.append(following)
    return external, lower


def points_connected_to_external_solvent(
    points: np.ndarray,
    external: np.ndarray,
    lower: np.ndarray,
) -> np.ndarray:
    connected = np.zeros(len(points), dtype=bool)
    grid_indices = np.rint((points - lower[None, :]) / SOLVENT_GRID_SPACING).astype(int)
    last = np.asarray(external.shape) - 1
    for point_index, grid_index in enumerate(grid_indices):
        minimum = np.maximum(0, grid_index - 1)
        maximum = np.minimum(last, grid_index + 1)
        connected[point_index] = bool(
            np.any(external[
                minimum[0]:maximum[0] + 1,
                minimum[1]:maximum[1] + 1,
                minimum[2]:maximum[2] + 1,
            ])
        )
    return connected


def ligand_solvent_exposure(
    molecule: Chem.Mol,
    ligand_coordinates: list[tuple[float, float, float]],
    pdb: str,
    *, accessible_patches: dict[int, list[list[float]]] | None = None,
) -> list[dict[str, Any]]:
    """Compute pose-dependent solvent accessibility with a 1.4 Å probe."""

    ligand = np.asarray(ligand_coordinates, dtype=float)
    if ligand.shape != (molecule.GetNumAtoms(), 3):
        raise ValueError("selected ligand coordinates do not match the candidate structure")
    protein, protein_radii = protein_atom_cloud(pdb)
    periodic_table = Chem.GetPeriodicTable()
    ligand_radii = np.asarray(
        [float(periodic_table.GetRvdw(atom.GetAtomicNum())) for atom in molecule.GetAtoms()],
        dtype=float,
    )
    # Only atoms that can intersect a probe ray from this ligand are relevant.
    # This preserves the full-protein occlusion result while bounding the
    # vectorized ray test for large receptors.
    local_cutoff = SOLVENT_GRID_PADDING + SOLVENT_PROBE_RADIUS + float(np.max(ligand_radii)) + 4.0
    local_mask = np.min(
        np.linalg.norm(protein[:, None, :] - ligand[None, :, :], axis=2),
        axis=1,
    ) <= local_cutoff
    protein = protein[local_mask]
    protein_radii = protein_radii[local_mask]
    external_solvent, solvent_grid_lower = external_solvent_grid(
        ligand,
        ligand_radii,
        protein,
        protein_radii,
    )
    directions = fibonacci_sphere()
    atom_indices = np.arange(molecule.GetNumAtoms())
    exposure: list[dict[str, Any]] = []
    for atom_index, atom in enumerate(molecule.GetAtoms()):
        sample_radius = ligand_radii[atom_index] + SOLVENT_PROBE_RADIUS
        sample_points = ligand[atom_index] + directions * sample_radius
        ligand_distances = np.linalg.norm(sample_points[:, None, :] - ligand[None, :, :], axis=2)
        ligand_limits = ligand_radii[None, :] + SOLVENT_PROBE_RADIUS
        ligand_occluded = np.any(
            (ligand_distances < ligand_limits - 0.05)
            & (atom_indices[None, :] != atom_index),
            axis=1,
        )
        protein_distances = np.linalg.norm(sample_points[:, None, :] - protein[None, :, :], axis=2)
        protein_occluded = np.any(
            protein_distances < protein_radii[None, :] + SOLVENT_PROBE_RADIUS - 0.05,
            axis=1,
        )
        locally_accessible = ~(ligand_occluded | protein_occluded)
        reaches_bulk = locally_accessible & points_connected_to_external_solvent(
            sample_points,
            external_solvent,
            solvent_grid_lower,
        )
        accessible_count = int(np.count_nonzero(reaches_bulk))
        if accessible_patches is not None:
            # Private rendering data: do not add sample arrays to report JSON.
            accessible_patches[atom_index] = directions[reaches_bulk].tolist()
        accessible_fraction = float(np.mean(reaches_bulk))
        direction = np.sum(directions[reaches_bulk], axis=0) if accessible_count else np.zeros(3, dtype=float)
        direction_norm = float(np.linalg.norm(direction))
        if direction_norm > 1e-9:
            direction /= direction_norm
        nearest_protein_distance = float(np.min(np.linalg.norm(protein - ligand[atom_index], axis=1)))
        exposure.append(
            {
                "atom_index": atom_index,
                "element": atom.GetSymbol(),
                "degree": atom.GetDegree(),
                "coordinate_3d": [round(float(value), 4) for value in ligand[atom_index]],
                "accessible_fraction": round(accessible_fraction, 4),
                "accessible_sample_count": accessible_count,
                "accessible_direction_3d": [round(float(value), 6) for value in direction],
                "nearest_protein_distance": round(nearest_protein_distance, 4),
                "classification": (
                    "solvent-exposed"
                    if accessible_fraction >= 0.20
                    else "partially-exposed"
                    if accessible_fraction >= 0.08
                    else "buried"
                ),
            }
        )
    return exposure


def pocket_receptor_exposure(molecule: Chem.Mol, ligand_coordinates: list,
                            pdb: str, residues: list[dict[str, Any]]) -> dict:
    """Reuse the bulk-solvent criterion on a box containing whole pocket residues."""
    if not residues:
        return {}
    protein, radii, keys = protein_atom_data(pdb)
    selected_keys = {r["key"] for r in residues}
    selected = np.asarray([key in selected_keys for key in keys])
    ligand = np.asarray(ligand_coordinates, dtype=float)
    ligand_radii = np.asarray([Chem.GetPeriodicTable().GetRvdw(a.GetAtomicNum()) for a in molecule.GetAtoms()])
    # Pocket residues determine the bounds only; ALL nearby receptor atoms
    # remain occluders. Repeated target spheres are idempotent in the voxel mask.
    targets = np.vstack((ligand, protein[selected]))
    target_radii = np.concatenate((ligand_radii, radii[selected]))
    lower, upper = targets.min(axis=0)-SOLVENT_GRID_PADDING, targets.max(axis=0)+SOLVENT_GRID_PADDING
    expanded = radii + SOLVENT_PROBE_RADIUS
    local = np.all((protein+expanded[:,None] >= lower) & (protein-expanded[:,None] <= upper), axis=1)
    external, grid_lower = external_solvent_grid(targets,target_radii,protein[local],radii[local])
    return receptor_surface_exposure(
        protein,radii,keys,ligand,ligand_radii,selected_keys,fibonacci_sphere(512),
        lambda points: points_connected_to_external_solvent(points,external,grid_lower),
    )


def solvent_opening_regions(
    molecule: Chem.Mol,
    exposure: list[dict[str, Any]],
) -> list[dict[str, Any]]:
    exposed = {
        int(item["atom_index"]): item
        for item in exposure
        if float(item["accessible_fraction"]) >= 0.08
    }
    unseen = set(exposed)

    def direction(item: dict[str, Any]) -> np.ndarray:
        values = np.asarray(item.get("accessible_direction_3d", [0.0, 0.0, 0.0]), dtype=float)
        norm = float(np.linalg.norm(values))
        return values / norm if norm > 1e-9 else values

    def same_mouth(first: int, second: int) -> bool:
        first_item, second_item = exposed[first], exposed[second]
        first_direction, second_direction = direction(first_item), direction(second_item)
        has_directions = float(np.linalg.norm(first_direction)) > 0 and float(np.linalg.norm(second_direction)) > 0
        direction_matches = not has_directions or float(np.dot(first_direction, second_direction)) >= 0.50
        if not direction_matches:
            return False
        if molecule.GetBondBetweenAtoms(first, second) is not None:
            return True
        first_coordinate = np.asarray(first_item.get("coordinate_3d", []), dtype=float)
        second_coordinate = np.asarray(second_item.get("coordinate_3d", []), dtype=float)
        return (
            first_coordinate.shape == (3,)
            and second_coordinate.shape == (3,)
            and float(np.linalg.norm(first_coordinate - second_coordinate)) <= 4.5
        )

    regions: list[dict[str, Any]] = []
    while unseen:
        seed = unseen.pop()
        component = {seed}
        frontier = [seed]
        while frontier:
            current = frontier.pop()
            for neighbour_index in list(unseen):
                if not same_mouth(current, neighbour_index):
                    continue
                unseen.remove(neighbour_index)
                component.add(neighbour_index)
                frontier.append(neighbour_index)
        fractions = [float(exposed[index]["accessible_fraction"]) for index in component]
        sample_count = sum(int(exposed[index].get("accessible_sample_count", 0)) for index in component)
        weighted_direction = np.sum(
            [direction(exposed[index]) * max(1, int(exposed[index].get("accessible_sample_count", 0))) for index in component],
            axis=0,
        )
        weighted_norm = float(np.linalg.norm(weighted_direction))
        if weighted_norm > 1e-9:
            weighted_direction /= weighted_norm
        regions.append(
            {
                "atom_indices": sorted(component),
                "mean_accessible_fraction": round(float(np.mean(fractions)), 4),
                "maximum_accessible_fraction": round(max(fractions), 4),
                "direction_3d": [round(float(value), 6) for value in weighted_direction],
                "accessible_sample_count": sample_count,
            }
        )
    regions.sort(key=lambda item: item["maximum_accessible_fraction"], reverse=True)
    return regions


def localized_protein_block(pdb: str, residue_keys: set[tuple[str, int, str]]) -> str:
    lines: list[str] = []
    for raw in pdb.splitlines():
        if not raw.startswith("ATOM  "):
            continue
        line = raw.ljust(80)
        try:
            key = (line[21:22].strip(), int(line[22:26]), line[17:20].strip().upper())
        except ValueError:
            continue
        if key not in residue_keys:
            continue
        element = normalized_element(line)
        lines.append(f"{line[:76]}{element:>2}{line[78:]}")
    if not lines:
        raise ValueError("protein pocket atoms are unavailable in the complex")
    lines.append("END")
    return "\n".join(lines) + "\n"


def ligand_from_smiles(
    smiles: str,
    ligand_block: str,
    coordinates: list[tuple[float, float, float]],
) -> tuple[Chem.Mol, plf.Molecule]:
    template = Chem.MolFromSmiles(smiles)
    if template is None:
        raise ValueError("candidate SMILES could not be parsed")
    if template.GetNumAtoms() != len(coordinates):
        raise ValueError(
            f"candidate SMILES has {template.GetNumAtoms()} heavy atoms but the selected pose has {len(coordinates)}"
        )

    pose = Chem.MolFromPDBBlock(ligand_block, removeHs=True, sanitize=False, proximityBonding=True)
    molecule: Chem.Mol | None = None
    if pose is not None:
        pose = Chem.RemoveHs(pose, sanitize=False)
    if pose is not None and pose.GetNumAtoms() == template.GetNumAtoms():
        try:
            molecule = AllChem.AssignBondOrdersFromTemplate(template, pose)
            Chem.SanitizeMol(molecule)
        except (ValueError, RuntimeError):
            molecule = None
    if molecule is None:
        template_elements = [atom.GetAtomicNum() for atom in template.GetAtoms()]
        block_elements = [
            Chem.GetPeriodicTable().GetAtomicNumber(normalized_element(line.ljust(80)))
            for line in ligand_block.splitlines()
            if line.startswith(("ATOM  ", "HETATM")) and normalized_element(line.ljust(80)).upper() != "H"
        ]
        if template_elements != block_elements:
            raise ValueError("candidate SMILES could not be mapped to the selected PDB pose")
        molecule = Chem.Mol(template)
        conformer = Chem.Conformer(molecule.GetNumAtoms())
        for index, coordinate in enumerate(coordinates):
            conformer.SetAtomPosition(index, coordinate)
        molecule.AddConformer(conformer)
    return molecule, plf.Molecule.from_rdkit(molecule, resname="LIG", resnumber=1, chain="Z")


def json_value(value: Any) -> Any:
    if hasattr(value, "tolist"):
        return value.tolist()
    if isinstance(value, tuple):
        return [json_value(item) for item in value]
    if isinstance(value, dict):
        return {str(key): json_value(item) for key, item in value.items()}
    if isinstance(value, (str, int, float, bool)) or value is None:
        return value
    return str(value)


def bounded_score(value: float) -> float:
    return max(0.0, min(1.0, float(value)))


def interaction_strength(
    kind: str,
    distance: float,
    metadata: dict[str, Any],
    vdw_radius_sum: float | None = None,
) -> dict[str, Any]:
    """Return a transparent relative interaction-strength index.

    The index is not an absolute binding free energy.  Hydrogen bonds use the
    AutoDock Vina potential exposed by ProLIF plus angular quality; other
    interaction classes use their accepted geometric terms.  Raw geometry is
    retained in the report but is not used as the user-facing label.
    """

    basis = "geometry-normalized interaction potential"
    if kind == "hydrogen-bond":
        potential = metadata.get("vina_hbond_potential")
        distance_score = bounded_score(
            float(potential) if isinstance(potential, (int, float)) else (3.7 - distance) / 1.0
        )
        donor_deviation = abs(float(metadata.get("donor_atom_angle_deviation", 0.0)))
        acceptor_deviation = abs(float(metadata.get("acceptor_atom_angle_deviation", 0.0)))
        angular_score = math.sqrt(
            bounded_score(1.0 - donor_deviation / 45.0)
            * bounded_score(1.0 - acceptor_deviation / 60.0)
        )
        score = distance_score * (0.72 + 0.28 * angular_score)
        basis = "ProLIF AutoDock Vina H-bond potential with angular quality"
    elif kind == "halogen-bond":
        distance_score = bounded_score((4.2 - distance) / 1.4)
        axd_score = bounded_score((float(metadata.get("AXD_angle", 120.0)) - 120.0) / 60.0)
        xar_score = bounded_score(1.0 - abs(float(metadata.get("XAR_angle", 120.0)) - 120.0) / 60.0)
        score = (distance_score * max(0.15, axd_score) * max(0.15, xar_score)) ** (1.0 / 3.0)
        basis = "halogen-bond distance and AXD/XAR angular quality"
    elif kind == "pi-stacking":
        distance_score = bounded_score((6.5 - distance) / 2.5)
        plane_angle = abs(float(metadata.get("plane_angle", 0.0)))
        angle_score = max(
            bounded_score(1.0 - plane_angle / 35.0),
            bounded_score(1.0 - abs(plane_angle - 90.0) / 35.0),
        )
        offset = abs(float(metadata.get("normal_to_centroid_angle", 0.0)))
        offset_score = bounded_score(1.0 - offset / 45.0)
        score = (distance_score * max(0.2, angle_score) * max(0.2, offset_score)) ** (1.0 / 3.0)
        basis = "pi-stacking centroid distance, plane angle, and offset"
    elif kind == "cation-pi":
        score = bounded_score((6.0 - distance) / 2.5)
        basis = "cation-pi centroid geometry"
    elif kind == "ionic":
        score = bounded_score((5.2 - distance) / 2.2)
        basis = "charge-assisted contact geometry"
    elif kind == "metal-coordination":
        score = bounded_score((3.0 - distance) / 1.2)
        basis = "metal coordination distance potential"
    elif kind == "hydrophobic":
        score = bounded_score((4.5 - distance) / 1.5)
        basis = "hydrophobic contact complementarity"
    elif kind == "vdw-contact" and vdw_radius_sum and vdw_radius_sum > 0:
        ratio = distance / vdw_radius_sum
        score = math.exp(-((ratio - 0.92) / 0.16) ** 2)
        basis = "van der Waals surface complementarity"
    elif kind == "vdw-contact":
        score = bounded_score((4.2 - distance) / 1.2)
        basis = "van der Waals contact geometry"
    else:
        score = bounded_score((5.0 - distance) / 2.0)

    score = bounded_score(score)
    level = "strong" if score >= 0.72 else "moderate" if score >= 0.45 else "weak"
    return {
        "strength_index": round(score, 4),
        "strength_level": level,
        "strength_basis": basis,
    }


def interaction_records(ifp: Any, ligand_molecule: Any, protein_molecule: Any) -> list[dict[str, Any]]:
    def mean_position(molecule: Any, atom_indices: list[int]) -> list[float] | None:
        if not atom_indices or molecule.GetNumConformers() == 0:
            return None
        conformer = molecule.GetConformer()
        valid_indices = [index for index in atom_indices if 0 <= index < molecule.GetNumAtoms()]
        if not valid_indices:
            return None
        coordinates = [conformer.GetAtomPosition(index) for index in valid_indices]
        return [
            round(sum(float(getattr(point, axis)) for point in coordinates) / len(coordinates), 4)
            for axis in ("x", "y", "z")
        ]

    records: list[dict[str, Any]] = []
    for pair, interactions in ifp.items():
        protein_residue = pair[1]
        residue = {"name": str(protein_residue.name), "number": int(protein_residue.number), "chain": str(protein_residue.chain)}
        for engine_kind, occurrences in interactions.items():
            mapped = INTERACTION_KIND.get(engine_kind)
            if mapped is None:
                continue
            kind, direction = mapped
            for occurrence in occurrences:
                indices = occurrence.get("indices", {})
                ligand_indices = [int(value) for value in indices.get("ligand", ())]
                protein_indices = [int(value) for value in occurrence.get("parent_indices", {}).get("protein", ())]
                distance = float(occurrence.get("distance", 0))
                if not ligand_indices or not math.isfinite(distance):
                    continue
                metadata = json_value(occurrence)
                vdw_radius_sum: float | None = None
                ligand_parent_indices = [
                    int(value) for value in occurrence.get("parent_indices", {}).get("ligand", ())
                ]
                try:
                    ligand_symbol = ligand_molecule.GetAtomWithIdx(ligand_parent_indices[0]).GetSymbol()
                    protein_symbol = protein_molecule.GetAtomWithIdx(protein_indices[0]).GetSymbol()
                    periodic_table = Chem.GetPeriodicTable()
                    vdw_radius_sum = float(
                        periodic_table.GetRvdw(periodic_table.GetAtomicNumber(ligand_symbol))
                        + periodic_table.GetRvdw(periodic_table.GetAtomicNumber(protein_symbol))
                    )
                except (IndexError, KeyError, TypeError, ValueError, RuntimeError):
                    vdw_radius_sum = None
                record = {
                    "kind": kind,
                    "engine_kind": engine_kind,
                    "direction": direction,
                    "distance": round(distance, 4),
                    "ligand_atom_indices": ligand_indices,
                    "protein_atom_indices": protein_indices,
                    "ligand_position": mean_position(ligand_molecule, ligand_parent_indices),
                    "protein_position": mean_position(protein_molecule, protein_indices),
                    "residue": residue,
                    "metadata": metadata,
                }
                if vdw_radius_sum is not None:
                    record["vdw_radius_sum"] = round(vdw_radius_sum, 4)
                record.update(interaction_strength(kind, distance, metadata, vdw_radius_sum))
                records.append(record)
    records.sort(key=lambda item: (item["residue"]["chain"], item["residue"]["number"], item["kind"], item["distance"]))
    if len(records) > MAX_INTERACTIONS:
        raise ValueError("interaction count exceeds the bounded output limit")
    return records


def analyze(args: argparse.Namespace) -> dict[str, Any]:
    input_path = pathlib.Path(args.input)
    pdb = bounded_text(input_path)
    ligand_residue_name = args.ligand_residue_name.strip().upper()
    _, ligand_block, ligand_coordinates = split_complex(pdb, ligand_residue_name)
    analysis_residues = pocket_residues(pdb, ligand_coordinates, cutoff=8.0, limit=None)
    residues = [
        residue for residue in analysis_residues
        if residue["minimum_distance"] <= POCKET_RADIUS
    ]
    protein_block = localized_protein_block(pdb, {residue["key"] for residue in analysis_residues})
    ligand_rdkit, ligand = ligand_from_smiles(args.smiles.strip(), ligand_block, ligand_coordinates)
    accessible_patches: dict[int, list[list[float]]] = {}
    solvent_exposure = [] if args.report_only else ligand_solvent_exposure(
        ligand_rdkit, ligand_coordinates, pdb, accessible_patches=accessible_patches,
    )
    solvent_openings = [] if args.report_only else solvent_opening_regions(ligand_rdkit, solvent_exposure)
    protein_path = input_path.with_name("interaction-protein.pdb")
    protein_path.write_text(protein_block, encoding="utf-8", newline="\n")

    with warnings.catch_warnings(record=True) as seen:
        warnings.simplefilter("always")
        try:
            protein = MoleculeStandardizer()(protein_path)
        except ValueError:
            universe = mda.Universe(str(protein_path))
            protein = plf.Molecule.from_mda(universe.select_atoms("protein"), force=True)
        fingerprint = plf.Fingerprint(interactions="all", count=True, implicit_hydrogens=True)
        ifp = fingerprint.generate(ligand, protein, metadata=True)
        captured_warnings = sorted({str(item.message) for item in seen})
    records = interaction_records(ifp, ligand, protein)
    if not records:
        raise ValueError("ProLIF found no supported interactions for the selected pose")

    ligand_label = args.ligand_label.strip() or ligand_residue_name
    if not args.report_only:
        svg = render_publication_svg(
            ligand_rdkit, records, residues, ligand_coordinates, solvent_exposure,
            ligand_label, args.pose_label.strip(),
            plf.__version__, Chem.rdBase.rdkitVersion,
            accessible_patches=accessible_patches,
            opening_membership={atom:index for index,region in enumerate(solvent_openings) for atom in region["atom_indices"]},
            receptor_exposure=pocket_receptor_exposure(ligand_rdkit,ligand_coordinates,pdb,residues),
        )
        pathlib.Path(args.svg).write_text(svg, encoding="utf-8", newline="\n")
        png_bytes = cairosvg.svg2png(bytestring=svg.encode("utf-8"), output_width=3200, output_height=2000)
        image = Image.open(io.BytesIO(png_bytes))
        image.save(args.png, format="PNG", dpi=(300, 300), optimize=True)
    interaction_kind_counts = {
        kind: sum(record["kind"] == kind for record in records)
        for kind in sorted({record["kind"] for record in records})
    }
    return {
        "contract": True,
        "ok": True,
        "engine": "Synon 2D Interaction Engine",
        "engine_release": "1.3.0",
        "analysis_engine": {"name": "ProLIF", "version": plf.__version__},
        "depiction_engine": {"name": "RDKit", "version": Chem.rdBase.rdkitVersion},
        "input_sha256": hashlib.sha256(pdb.encode("utf-8")).hexdigest(),
        "ligand_residue_name": ligand_residue_name,
        "ligand_label": ligand_label,
        "pose_label": args.pose_label.strip(),
        "protein_atom_count": int(protein.GetNumAtoms()),
        "ligand_atom_count": int(ligand.GetNumAtoms()),
        "interaction_count": len(records),
        "residue_count": len(residues),
        "hydrogen_bond_count": sum(record["kind"] == "hydrogen-bond" for record in records),
        "salt_bridge_count": sum(record["kind"] == "ionic" for record in records),
        "width": 1600,
        "height": 1000,
        "png_dpi": 300,
        "svg": not args.report_only,
        "interactions": records,
        "detected_interaction_kinds": sorted(interaction_kind_counts),
        "interaction_kind_counts": interaction_kind_counts,
        "pocket_residues": residues,
        "pocket_radius_angstrom": POCKET_RADIUS,
        "pocket_geometry_method": "shared-4.5A-residue-neighborhood",
        "solvent_exposure": solvent_exposure,
        "solvent_openings": solvent_openings,
        "solvent_opening_method": "1.4A-probe-with-external-solvent-flood-fill",
        "pocket_opening_count": len(solvent_openings),
        "warnings": captured_warnings,
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--svg", required=True)
    parser.add_argument("--png", required=True)
    parser.add_argument("--report", required=True)
    parser.add_argument("--ligand-residue-name", required=True)
    parser.add_argument("--smiles", required=True)
    parser.add_argument("--ligand-label", default="Ligand")
    parser.add_argument("--pose-label", default="")
    parser.add_argument("--report-only", action="store_true")
    args = parser.parse_args()
    report: dict[str, Any] = {"contract": True, "ok": False}
    try:
        report = analyze(args)
    except Exception as error:
        report.update({"message": str(error), "error_type": type(error).__name__})
    pathlib.Path(args.report).write_text(
        json.dumps(report, ensure_ascii=False, sort_keys=True, separators=(",", ":")) + "\n",
        encoding="utf-8",
        newline="\n",
    )
    if not report.get("ok"):
        print(report.get("message", "interaction diagram generation failed"), file=sys.stderr)
        return 1
    print(json.dumps({"ok": True, "interactions": report["interaction_count"], "residues": report["residue_count"]}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
