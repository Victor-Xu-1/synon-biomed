"""Internal receptor surface sampling for 2D depiction, never report mutation.

Shrake-Rupley sampling uses the full receptor plus the selected ligand as
occluders. An additional caller-supplied bulk-connectivity test excludes closed
cavities. Fractions use the SAME residue isolated in its bound conformation as
the reference, not a tabulated maximum RSA or a binding-energy approximation.
"""
from __future__ import annotations

import math
from collections import defaultdict
from collections.abc import Callable

import numpy as np

ResidueKey = tuple[str, int, str]


def surface_mask(index: int, coordinates: np.ndarray, radii: np.ndarray,
                 directions: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """Return exposed probe-center samples; radii already include the probe."""
    origin = coordinates[index]
    distances = np.linalg.norm(coordinates - origin, axis=1)
    neighbours = np.flatnonzero((distances < radii + radii[index]) & (np.arange(len(radii)) != index))
    points = origin + directions * radii[index]
    mask = np.ones(len(directions), dtype=bool)
    # Chunk neighbour evaluation so an unusual dense input cannot allocate an
    # unbounded samples-by-protein tensor.
    for start in range(0, len(neighbours), 128):
        indices = neighbours[start:start + 128]
        squared = np.sum((points[:, None, :] - coordinates[indices][None, :, :]) ** 2, axis=2)
        mask &= ~np.any(squared < radii[indices][None, :] ** 2 - 1e-10, axis=1)
    return points, mask


def receptor_surface_exposure(
    protein: np.ndarray, protein_radii: np.ndarray, residue_keys: list[ResidueKey],
    ligand: np.ndarray, ligand_radii: np.ndarray, selected_keys: set[ResidueKey],
    directions: np.ndarray, bulk_connected: Callable[[np.ndarray], np.ndarray],
    probe_radius: float = 1.4,
) -> dict[ResidueKey, dict[str, float]]:
    """Area-weighted, bulk-connected exposure of the selected pocket residues."""
    coordinates = np.vstack((protein, ligand))
    radii = np.concatenate((protein_radii, ligand_radii)) + probe_radius
    if (protein.shape != (len(protein_radii), 3) or ligand.shape != (len(ligand_radii), 3)
            or len(residue_keys) != len(protein) or len(coordinates) > 50512
            or not np.isfinite(coordinates).all() or not np.isfinite(radii).all()
            or np.any(radii <= probe_radius) or directions.ndim != 2 or directions.shape[1] != 3
            or not 32 <= len(directions) <= 4096 or not np.isfinite(directions).all()
            or not np.allclose(np.linalg.norm(directions, axis=1), 1.0)):
        raise ValueError("invalid bounded receptor accessibility input")
    grouped: dict[ResidueKey, list[int]] = defaultdict(list)
    for index, key in enumerate(residue_keys):
        if key in selected_keys:
            grouped[key].append(index)
    if set(grouped) != selected_keys or sum(map(len, grouped.values())) > 4096:
        raise ValueError("pocket residue atoms are unavailable or exceed the sampling limit")
    result = {}
    for key, indices in grouped.items():
        accessible_area = local_area = reference_area = 0.0
        isolated_coordinates, isolated_radii = coordinates[indices], radii[indices]
        for local_index, index in enumerate(indices):
            points, accessible = surface_mask(index, coordinates, radii, directions)
            _, isolated = surface_mask(local_index, isolated_coordinates, isolated_radii, directions)
            connected = np.asarray(bulk_connected(points), dtype=bool)
            if connected.shape != accessible.shape:
                raise ValueError("invalid receptor bulk-connectivity result")
            sphere_area = 4.0 * math.pi * radii[index] ** 2
            accessible_area += sphere_area * float(np.mean(accessible & connected))
            local_area += sphere_area * float(np.mean(accessible))
            reference_area += sphere_area * float(np.mean(isolated))
        if reference_area <= 0 or accessible_area > reference_area + 1e-6:
            raise ValueError("invalid receptor accessibility reference area")
        result[key] = {
            "accessible_area": accessible_area,
            "local_sasa_area": local_area,
            "reference_area": reference_area,
            "fraction": accessible_area / reference_area,
        }
    return result
