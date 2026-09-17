"""Independent publication renderer for Synon Biomed 2D interaction diagrams."""

from __future__ import annotations

import math
import re
from collections import defaultdict
from html import escape
from typing import Any

import numpy as np
from rdkit import Chem
from rdkit.Chem import rdDepictor
from rdkit.Chem.Draw import rdMolDraw2D

from structure_interaction_pocket_geometry import (
    boundary_paths, drawing_points, solvent_halos, solvent_boundary_segments, solvent_opening_band,
)


CONTACT_STYLE = {
    "hydrogen-bond": ("#bd39df", "", "Hydrogen bond"),
    "hydrophobic": ("#78bc2f", "", "Hydrophobic contact"),
    "ionic": ("#e35d50", "11 6", "Ionic / salt bridge"),
    "cation-pi": ("#dc872f", "9 6", "Cation–π"),
    "pi-stacking": ("#6968d8", "9 6", "π stacking"),
    "halogen-bond": ("#24a8a3", "9 6", "Halogen bond"),
    "metal-coordination": ("#65758a", "4 6", "Metal coordination"),
    "vdw-contact": ("#98a4b2", "4 7", "van der Waals contact"),
}

HYDROPHOBIC_RESIDUES = {"ALA", "VAL", "LEU", "ILE", "MET", "PHE", "TRP", "PRO", "CYS"}
ACIDIC_RESIDUES = {"ASP", "GLU"}
BASIC_RESIDUES = {"ARG", "LYS", "HIS", "HIE", "HIP", "HID"}


def ligand_drawing(molecule: Chem.Mol, width: int, height: int) -> tuple[str, dict[int, tuple[float, float]]]:
    drawing_molecule = Chem.Mol(molecule)
    drawing_molecule.RemoveAllConformers()
    rdDepictor.Compute2DCoords(drawing_molecule, canonOrient=True, clearConfs=True)
    drawing_molecule = rdMolDraw2D.PrepareMolForDrawing(drawing_molecule, kekulize=True, addChiralHs=True)
    drawer = rdMolDraw2D.MolDraw2DSVG(width, height)
    options = drawer.drawOptions()
    options.clearBackground = False
    options.padding = 0.045
    options.bondLineWidth = 3.5
    options.multipleBondOffset = 0.18
    options.minFontSize = 20
    options.maxFontSize = 34
    options.addStereoAnnotation = True
    options.annotationFontScale = 0.82
    drawer.DrawMolecule(drawing_molecule)
    coordinates = {
        index: (float(drawer.GetDrawCoords(index).x), float(drawer.GetDrawCoords(index).y))
        for index in range(drawing_molecule.GetNumAtoms())
    }
    drawer.FinishDrawing()
    svg = drawer.GetDrawingText()
    body = re.sub(r"^.*?<svg[^>]*>", "", svg, count=1, flags=re.DOTALL)
    body = re.sub(r"</svg>\s*$", "", body, count=1, flags=re.DOTALL)
    return body, coordinates


def projected_residue_geometry(
    ligand_coordinates: list[tuple[float, float, float]],
    atom_points: dict[int, tuple[float, float]],
    residues: list[dict[str, Any]],
) -> dict[tuple[str, int, str], dict[str, Any]]:
    projection, center_3d, center_2d, rank = fitted_pose_projection(ligand_coordinates, atom_points)
    geometry: dict[tuple[str, int, str], dict[str, Any]] = {}
    for residue in residues:
        centroid_vector = np.asarray(residue["centroid_3d"], dtype=float) - center_3d
        projected = centroid_vector @ projection if rank >= 2 else np.zeros(2, dtype=float)
        contact_vector = np.asarray(residue.get("contact_point_3d", residue["centroid_3d"]), dtype=float) - center_3d
        projected_contact = contact_vector @ projection if rank >= 2 else projected
        if float(np.linalg.norm(projected_contact)) < 1.0:
            projected_contact = projected
        if float(np.linalg.norm(projected)) < 1.0:
            anchor = np.asarray(atom_points[residue["closest_ligand_atom"]], dtype=float) - center_2d
            projected = anchor
        if float(np.linalg.norm(projected)) < 1.0:
            projected = np.asarray([1.0, 0.0])
        geometry[residue["key"]] = {
            "angle": float(math.atan2(projected[1], projected[0])),
            "contact": tuple(float(value) for value in center_2d + projected_contact),
        }
    return geometry


def fitted_pose_projection(
    ligand_coordinates: list[tuple[float, float, float]],
    atom_points: dict[int, tuple[float, float]],
) -> tuple[np.ndarray, np.ndarray, np.ndarray, int]:
    """Fit one shared pose-to-depiction transform for every geometric overlay."""

    ordered_points = np.asarray([atom_points[index] for index in range(len(ligand_coordinates))], dtype=float)
    ligand_3d = np.asarray(ligand_coordinates, dtype=float)
    source = ligand_3d - ligand_3d.mean(axis=0)
    target = ordered_points - ordered_points.mean(axis=0)
    projection, _, rank, _ = np.linalg.lstsq(source, target, rcond=None)
    return projection, ligand_3d.mean(axis=0), ordered_points.mean(axis=0), int(rank)


def projected_opening_direction(
    direction_3d: list[float] | tuple[float, float, float],
    projection: np.ndarray,
) -> tuple[float, float] | None:
    direction = np.asarray(direction_3d, dtype=float)
    projected = direction @ projection if direction.shape == (3,) else np.zeros(2, dtype=float)
    norm = float(np.linalg.norm(projected))
    if not math.isfinite(norm) or norm < 1e-9:
        return None
    projected /= norm
    return float(projected[0]), float(projected[1])


def pocket_wall_radius(
    atom_values: list[tuple[float, float]],
    center: tuple[float, float],
    item: dict[str, Any],
    angle: float,
) -> float:
    unit = (math.cos(angle), math.sin(angle))
    support = max(
        (point[0] - center[0]) * unit[0] + (point[1] - center[1]) * unit[1]
        for point in atom_values
    )
    contact = item.get("projected_contact", center)
    contact_radius = (contact[0] - center[0]) * unit[0] + (contact[1] - center[1]) * unit[1]
    return max(support + 28.0, min(support + 124.0, contact_radius + 14.0))


def projected_pocket_paths(
    atom_points: dict[int, tuple[float, float]],
    center: tuple[float, float],
    residues: list[dict[str, Any]],
    opening_points: list[tuple[float, float]] | None = None,
    glyph_points: list[tuple[float, float]] | None = None,
    opening_clearance: float = 42.0,
) -> list[str]:
    """Contact-supported schematic arcs; the ligand envelope only prevents overlap."""
    return boundary_paths(
        list(atom_points.values()) + (glyph_points or []),
        center,
        [float(item.get("wall_angle", item["angle"])) for item in residues],
        opening_points or [],
        [item["position"] for item in residues if "position" in item],
        opening_clearance,
    )


def petal_path(x_value: float, y_value: float, angle: float) -> str:
    degrees = math.degrees(angle) + 90
    return (
        '<path d="M -53 -31 C -22 -50 25 -49 56 -25 L 42 35 C 7 49 -32 45 -52 22 Z" '
        f'transform="translate({x_value:.1f} {y_value:.1f}) rotate({degrees:.1f})"/>'
    )


def residue_fill(name: str, interacting: bool, hydrophobic_contact: bool) -> str:
    if hydrophobic_contact:
        return "url(#residue-hydrophobic)"
    if name in ACIDIC_RESIDUES:
        return "url(#residue-acidic)"
    if name in BASIC_RESIDUES:
        return "url(#residue-basic)"
    if name in HYDROPHOBIC_RESIDUES:
        return "url(#residue-hydrophobic-soft)"
    return "url(#residue-polar)" if interacting else "url(#residue-neutral)"


def representative_interactions(records: list[dict[str, Any]]) -> list[dict[str, Any]]:
    """Select uncluttered figure representatives without discarding evidence.

    The full ProLIF occurrence list remains in the JSON report.  Publication
    figures need one shortest representative for a repeated semantic contact,
    otherwise equivalent donor/acceptor matches produce overlapping arrows.
    Every interaction kind uses its strongest single representative.
    """

    has_specific_interaction = any(
        record["kind"] not in {"hydrophobic", "vdw-contact"} for record in records
    )
    unique: dict[tuple[str, str | None, tuple[int, ...]], dict[str, Any]] = {}
    for record in records:
        if record["kind"] == "hydrophobic":
            continue
        if record["kind"] == "vdw-contact" and has_specific_interaction:
            continue
        key = (
            record["kind"],
            record.get("direction"),
            tuple(sorted(int(value) for value in record["ligand_atom_indices"])),
        )
        current = unique.get(key)
        if current is None or float(record["distance"]) < float(current["distance"]):
            unique[key] = record

    selected: list[dict[str, Any]] = []
    by_kind: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for record in unique.values():
        by_kind[record["kind"]].append(record)
    for kind, candidates in by_kind.items():
        selected.extend(
            sorted(
                candidates,
                key=lambda record: (
                    -float(record.get("strength_index", 0.0)),
                    float(record["distance"]),
                ),
            )[:1]
        )
    return sorted(selected, key=lambda record: (record["kind"], float(record["distance"])))


def interaction_identity(item: dict[str, Any], record: dict[str, Any]) -> tuple[Any, ...]:
    return (
        item["key"],
        tuple(record.get("ligand_atom_indices", [])),
        tuple(record.get("protein_atom_indices", [])),
    )


def selected_vdw_identities(
    items: list[dict[str, Any]],
    limit: int = 3,
) -> set[tuple[Any, ...]]:
    candidates: list[tuple[float, tuple[Any, ...]]] = []
    for item in items:
        for record in representative_interactions(item.get("records", [])):
            if record["kind"] == "vdw-contact":
                candidates.append(
                    (float(record.get("strength_index", 0.0)), interaction_identity(item, record))
                )
    return {
        identity
        for _, identity in sorted(candidates, key=lambda candidate: candidate[0], reverse=True)[:limit]
    }


def place_directional_residues_near_anchors(
    items: list[dict[str, Any]],
    atom_points: dict[int, tuple[float, float]],
    center: tuple[float, float],
    allowed_vdw: set[tuple[Any, ...]],
) -> None:
    """Record interaction-facing preferences without pinning residue labels."""

    for item in items:
        directional = [
            record
            for record in representative_interactions(item.get("records", []))
            if record["kind"] not in {"hydrophobic", "vdw-contact"}
            or (record["kind"] == "vdw-contact" and interaction_identity(item, record) in allowed_vdw)
        ]
        if not directional:
            continue
        anchor_indices = [
            atom
            for record in directional
            for atom in record["ligand_atom_indices"]
            if atom in atom_points
        ]
        if not anchor_indices:
            continue
        anchor_x = sum(atom_points[index][0] for index in anchor_indices) / len(anchor_indices)
        anchor_y = sum(atom_points[index][1] for index in anchor_indices) / len(anchor_indices)
        angle = math.atan2(anchor_y - center[1], anchor_x - center[0])
        item["interaction_anchor_angle"] = angle
        strongest = max(float(record.get("strength_index", 0.0)) for record in directional)
        has_specific_interaction = any(record["kind"] != "vdw-contact" for record in directional)
        item["interaction_anchor_priority"] = strongest * (1.0 if has_specific_interaction else 0.55)


def distribute_residue_slots(
    items: list[dict[str, Any]],
    atom_points: dict[int, tuple[float, float]],
    center: tuple[float, float],
) -> None:
    """Distribute every residue evenly around the wall with weighted preferences.

    Amino-acid readability is the primary constraint. Interaction-facing and
    projected-contact angles only choose the nearest feasible slot, so contact
    lines can be moderately long without pulling labels into local clusters.
    """

    if len(items) < 2:
        return

    def angular_delta(first: float, second: float) -> float:
        return (first - second + math.pi) % math.tau - math.pi

    def preferred_angle(item: dict[str, Any]) -> float:
        return float(item.get("interaction_anchor_angle", item.get("wall_angle", item["angle"]))) % math.tau

    def ellipse_slots(phase_fraction: float) -> list[tuple[float, tuple[float, float]]]:
        radius_x = max(300.0, min(center[0] - 82.0, 1518.0 - center[0]))
        radius_y = max(230.0, min(center[1] - 64.0, 812.0 - center[1]))
        sample_count = 1536
        samples = [
            (
                center[0] + radius_x * math.cos(math.tau * index / sample_count),
                center[1] + radius_y * math.sin(math.tau * index / sample_count),
            )
            for index in range(sample_count)
        ]
        segment_lengths = [
            math.hypot(
                samples[(index + 1) % sample_count][0] - samples[index][0],
                samples[(index + 1) % sample_count][1] - samples[index][1],
            )
            for index in range(sample_count)
        ]
        perimeter = sum(segment_lengths)
        cumulative = [0.0]
        for length in segment_lengths:
            cumulative.append(cumulative[-1] + length)

        def point_at(distance: float) -> tuple[float, float]:
            target = distance % perimeter
            low, high = 0, sample_count
            while low + 1 < high:
                middle = (low + high) // 2
                if cumulative[middle] <= target:
                    low = middle
                else:
                    high = middle
            length = max(1e-9, segment_lengths[low])
            fraction = (target - cumulative[low]) / length
            following = samples[(low + 1) % sample_count]
            return (
                samples[low][0] + (following[0] - samples[low][0]) * fraction,
                samples[low][1] + (following[1] - samples[low][1]) * fraction,
            )

        return [
            (
                math.atan2(point[1] - center[1], point[0] - center[0]) % math.tau,
                point,
            )
            for index in range(len(items))
            for point in [point_at(perimeter * (phase_fraction + index / len(items)))]
        ]

    ordered = sorted(items, key=preferred_angle)
    best_cost = math.inf
    best_assignment: dict[tuple[str, int, str], tuple[float, tuple[float, float]]] = {}
    for phase_index in range(48):
        slots = ellipse_slots(phase_index / (48.0 * len(items)))
        for rotation in range(len(slots)):
            rotated = slots[rotation:] + slots[:rotation]
            cost = 0.0
            for item, (slot_angle, _) in zip(ordered, rotated):
                interaction_priority = float(item.get("interaction_anchor_priority", 0.0))
                weight = 1.0 + interaction_priority * 2.4
                cost += weight * angular_delta(slot_angle, preferred_angle(item)) ** 2
            if cost >= best_cost:
                continue
            best_cost = cost
            best_assignment = {item["key"]: slot for item, slot in zip(ordered, rotated)}

    for item in items:
        assignment = best_assignment.get(item["key"])
        if assignment is None:
            continue
        angle, position = assignment
        item["angle"] = angle
        item["position"] = position


def enforce_residues_outside_pocket(
    items: list[dict[str, Any]],
    atom_points: dict[int, tuple[float, float]],
    center: tuple[float, float],
    label_clearance: float = 68.0,
) -> None:
    """Project every residue label beyond its real pocket-wall radius."""

    atom_values = list(atom_points.values())
    for item in items:
        x_value, y_value = item["position"]
        angle = math.atan2(y_value - center[1], x_value - center[0])
        current_radius = math.hypot(x_value - center[0], y_value - center[1])
        minimum_radius = pocket_wall_radius(atom_values, center, item, angle) + label_clearance
        if current_radius >= minimum_radius:
            item["angle"] = angle
            continue
        item["angle"] = angle
        item["position"] = (
            max(72.0, min(1528.0, center[0] + math.cos(angle) * minimum_radius)),
            max(60.0, min(820.0, center[1] + math.sin(angle) * minimum_radius)),
        )


def resolve_outside_residue_collisions(
    items: list[dict[str, Any]],
    atom_points: dict[int, tuple[float, float]],
    center: tuple[float, float],
    minimum_separation: float = 118.0,
) -> None:
    """Separate outer labels tangentially without pushing them into the wall."""

    def spread_boundary_lane(
        lane: list[dict[str, Any]],
        axis: int,
        lower: float,
        upper: float,
        separation: float,
    ) -> None:
        if len(lane) < 2:
            return
        ordered = sorted(lane, key=lambda item: item["position"][axis])
        coordinates = [float(item["position"][axis]) for item in ordered]
        for index in range(1, len(coordinates)):
            coordinates[index] = max(coordinates[index], coordinates[index - 1] + separation)
        overflow = coordinates[-1] - upper
        if overflow > 0:
            coordinates = [coordinate - overflow for coordinate in coordinates]
        for index in range(len(coordinates) - 2, -1, -1):
            coordinates[index] = min(coordinates[index], coordinates[index + 1] - separation)
        underflow = lower - coordinates[0]
        if underflow > 0:
            coordinates = [coordinate + underflow for coordinate in coordinates]
        for item, coordinate in zip(ordered, coordinates):
            x_value, y_value = item["position"]
            item["position"] = (coordinate, y_value) if axis == 0 else (x_value, coordinate)

    for _ in range(36):
        moved = False
        for first_index, first in enumerate(items):
            for second in items[first_index + 1 :]:
                first_x, first_y = first["position"]
                second_x, second_y = second["position"]
                delta_x, delta_y = second_x - first_x, second_y - first_y
                distance = math.hypot(delta_x, delta_y)
                if distance >= minimum_separation:
                    continue
                midpoint_x = (first_x + second_x) / 2
                midpoint_y = (first_y + second_y) / 2
                angle = math.atan2(midpoint_y - center[1], midpoint_x - center[0])
                tangent_x, tangent_y = -math.sin(angle), math.cos(angle)
                orientation = 1.0 if delta_x * tangent_x + delta_y * tangent_y >= 0 else -1.0
                overlap = minimum_separation - distance + 1.0
                first_share = second_share = 0.5
                first["position"] = (
                    max(72.0, min(1528.0, first_x - tangent_x * overlap * first_share * orientation)),
                    max(60.0, min(820.0, first_y - tangent_y * overlap * first_share * orientation)),
                )
                second["position"] = (
                    max(72.0, min(1528.0, second_x + tangent_x * overlap * second_share * orientation)),
                    max(60.0, min(820.0, second_y + tangent_y * overlap * second_share * orientation)),
                )
                moved = True
        enforce_residues_outside_pocket(items, atom_points, center)
        if not moved:
            break
    spread_boundary_lane([item for item in items if item["position"][1] <= 66.0], 0, 72.0, 1528.0, 120.0)
    spread_boundary_lane([item for item in items if item["position"][1] >= 814.0], 0, 72.0, 1528.0, 120.0)
    spread_boundary_lane([item for item in items if item["position"][0] <= 78.0], 1, 60.0, 820.0, 112.0)
    spread_boundary_lane([item for item in items if item["position"][0] >= 1522.0], 1, 60.0, 820.0, 112.0)


def render_publication_svg(
    molecule: Chem.Mol,
    records: list[dict[str, Any]],
    residues: list[dict[str, Any]],
    ligand_coordinates: list[tuple[float, float, float]],
    solvent_exposure: list[dict[str, Any]],
    ligand_label: str,
    pose_label: str,
    analysis_version: str,
    depiction_version: str,
    *, accessible_patches: dict[int, list[list[float]]] | None = None,
    opening_membership: dict[int, int] | None = None,
    receptor_exposure: dict[tuple[str, int, str], dict[str, float]] | None = None,
) -> str:
    width, height = 1600, 1000
    ligand_width, ligand_height = 1180, 760
    ligand_x, ligand_y = 210, 82
    ligand_body, local_coordinates = ligand_drawing(molecule, ligand_width, ligand_height)
    atom_points = {
        index: (point[0] + ligand_x, point[1] + ligand_y) for index, point in local_coordinates.items()
    }
    glyph_points = drawing_points(ligand_body, (ligand_x, ligand_y))
    exclusion_points = list(atom_points.values()) + glyph_points
    center_x = sum(point[0] for point in atom_points.values()) / len(atom_points)
    center_y = sum(point[1] for point in atom_points.values()) / len(atom_points)
    center = (center_x, center_y)
    grouped_interactions: dict[tuple[str, int, str], list[dict[str, Any]]] = defaultdict(list)
    for record in records:
        residue = record["residue"]
        grouped_interactions[(residue["chain"], residue["number"], residue["name"])].append(record)

    residue_by_key = {residue["key"]: dict(residue) for residue in residues}
    for key, residue_records in grouped_interactions.items():
        if key not in residue_by_key:
            ligand_indices = [
                index
                for record in residue_records
                for index in record["ligand_atom_indices"]
                if index in atom_points
            ]
            residue_by_key[key] = {
                "key": key,
                "chain": key[0],
                "number": key[1],
                "name": key[2],
                "centroid_3d": tuple(np.mean(ligand_coordinates, axis=0)),
                "closest_ligand_atom": ligand_indices[0] if ligand_indices else 0,
                "minimum_distance": min((record["distance"] for record in residue_records), default=0.0),
            }

    layout_items = list(residue_by_key.values())
    geometry = projected_residue_geometry(ligand_coordinates, atom_points, layout_items)
    for item in layout_items:
        item["records"] = grouped_interactions.get(item["key"], [])
        projected_angle = float(geometry[item["key"]]["angle"])
        display_records = representative_interactions(item["records"])
        anchor_indices = [
            atom
            for record in display_records
            for atom in record["ligand_atom_indices"]
            if atom in atom_points
        ]
        if anchor_indices:
            anchor_x = sum(atom_points[atom][0] for atom in anchor_indices) / len(anchor_indices)
            anchor_y = sum(atom_points[atom][1] for atom in anchor_indices) / len(anchor_indices)
            anchor_angle = math.atan2(anchor_y - center_y, anchor_x - center_x)
            blend_x = math.cos(projected_angle) * 0.42 + math.cos(anchor_angle) * 0.58
            blend_y = math.sin(projected_angle) * 0.42 + math.sin(anchor_angle) * 0.58
            item["angle"] = math.atan2(blend_y, blend_x)
        else:
            item["angle"] = projected_angle
        # The wall remains tied to the actual projected protein contact. Label
        # distribution and interaction routing may move independently.
        item["wall_angle"] = projected_angle
        item["projected_contact"] = geometry[item["key"]]["contact"]
    allowed_vdw = selected_vdw_identities(layout_items)
    place_directional_residues_near_anchors(layout_items, atom_points, center, allowed_vdw)
    distribute_residue_slots(layout_items, atom_points, center)
    enforce_residues_outside_pocket(layout_items, atom_points, center)
    resolve_outside_residue_collisions(layout_items, atom_points, center)

    backbone: list[str] = []
    by_chain: dict[str, list[dict[str, Any]]] = defaultdict(list)
    for item in layout_items:
        by_chain[item["chain"]].append(item)
    for chain_items in by_chain.values():
        chain_items.sort(key=lambda item: item["number"])
        for first, second in zip(chain_items, chain_items[1:]):
            x1, y1 = first["position"]
            x2, y2 = second["position"]
            if second["number"] - first["number"] > 2 or math.hypot(x2 - x1, y2 - y1) > 245:
                continue
            middle_x, middle_y = (x1 + x2) / 2, (y1 + y2) / 2
            backbone.append(
                f'<path d="M {x1:.1f} {y1:.1f} Q {middle_x:.1f} {middle_y - 18:.1f} {x2:.1f} {y2:.1f}" '
                'fill="none" stroke="#121417" stroke-width="7" stroke-linecap="round"/>'
                f'<circle cx="{middle_x:.1f}" cy="{middle_y - 9:.1f}" r="5" fill="#121417"/>'
            )

    residue_shapes: list[str] = []
    receptor_halos: list[str] = []
    residue_text: list[str] = []
    contacts: list[str] = []
    present_kinds: list[str] = []
    for item in layout_items:
        x_value, y_value = item["position"]
        exposure = (receptor_exposure or {}).get(item["key"])
        if exposure is not None:
            fraction = float(exposure["fraction"])
            if not math.isfinite(fraction) or not 0 <= fraction <= 1:
                raise ValueError("invalid receptor exposure for depiction")
            # The circle encodes a residue total, not a directional opening.
            # Keep it centered on its label; no invented exposed-side bearing.
            if fraction > 0:
                key_label = f'{item["chain"]}:{item["number"]}:{item["name"]}'
                receptor_halos.append(
                    f'<circle cx="{x_value:.2f}" cy="{y_value:.2f}" r="{62+12*math.sqrt(fraction):.2f}" '
                    f'fill="#3f9ff5" opacity="{.10+.65*math.sqrt(fraction):.3f}" '
                    f'data-residue="{escape(key_label,quote=True)}" data-receptor-exposure="{fraction:.6f}" '
                    f'data-accessible-area="{exposure["accessible_area"]:.4f}" '
                    f'data-reference-area="{exposure["reference_area"]:.4f}">'
                    f'<title>{escape(key_label)} · bulk-connected area {exposure["accessible_area"]:.1f} Å²; '
                    f'{fraction:.1%} of isolated same-conformation residue surface</title></circle>'
                )
        kinds = {record["kind"] for record in item["records"]}
        fill = residue_fill(item["name"], bool(kinds), "hydrophobic" in kinds)
        hydrophobic_records = [record for record in item["records"] if record["kind"] == "hydrophobic"]
        hydrophobic_badge = ""
        if hydrophobic_records:
            strongest_hydrophobic = max(
                hydrophobic_records,
                key=lambda record: float(record.get("strength_index", 0.0)),
            )
            hydrophobic_badge = (
                f'<text y="35" font-size="10.5" fill="#44505f">Hph · '
                f'{float(strongest_hydrophobic.get("strength_index", 0.0)):.2f}</text>'
            )
        residue_shapes.append(
            f'<g fill="{fill}" stroke="#b7d3d7" stroke-width="1.1">{petal_path(x_value, y_value, item["angle"])}</g>'
        )
        residue_text.append(
            f'<g transform="translate({x_value:.1f} {y_value:.1f})" text-anchor="middle" fill="#0e1720">'
            f'<text y="-4" font-size="22" font-weight="700">{escape(item["name"])}</text>'
            f'<text y="18" font-size="15.5">{escape(item["chain"] or "·")}: {item["number"]}</text>'
            f'{hydrophobic_badge}</g>'
        )
        for present_kind in sorted(kinds):
            if present_kind not in present_kinds:
                present_kinds.append(present_kind)
        display_records = [
            record
            for record in representative_interactions(item["records"])
            if record["kind"] != "vdw-contact"
            or interaction_identity(item, record) in allowed_vdw
        ]
        for occurrence_index, record in enumerate(display_records):
            kind = record["kind"]
            anchor_indices = [atom for atom in record["ligand_atom_indices"] if atom in atom_points]
            if not anchor_indices:
                continue
            anchor_x = sum(atom_points[atom][0] for atom in anchor_indices) / len(anchor_indices)
            anchor_y = sum(atom_points[atom][1] for atom in anchor_indices) / len(anchor_indices)
            color, dash, label = CONTACT_STYLE[kind]
            strength_index = max(0.0, min(1.0, float(record.get("strength_index", 0.0))))
            strength_level = str(record.get("strength_level", "weak")).title()
            strength_text = f"{strength_level} · {strength_index:.2f}"
            stroke_width = 1.8 + strength_index * 2.8
            stroke_opacity = 0.48 + strength_index * 0.52
            direction = record.get("direction")
            if direction == "protein-to-ligand":
                start_x, start_y, end_x, end_y = x_value, y_value, anchor_x, anchor_y
            else:
                start_x, start_y, end_x, end_y = anchor_x, anchor_y, x_value, y_value
            dx, dy = end_x - start_x, end_y - start_y
            length = max(1.0, math.hypot(dx, dy))
            curve = (occurrence_index - (len(display_records) - 1) / 2) * 22.0
            control_x = (start_x + end_x) / 2 - dy / length * curve
            control_y = (start_y + end_y) / 2 + dx / length * curve
            dash_attribute = f' stroke-dasharray="{dash}"' if dash else ""
            marker = f' marker-end="url(#arrow-{kind})"' if direction else ""
            distance_x = 0.25 * start_x + 0.5 * control_x + 0.25 * end_x
            distance_y = 0.25 * start_y + 0.5 * control_y + 0.25 * end_y
            contacts.append(
                f'<g data-interaction-kind="{escape(kind)}"><path d="M {start_x:.1f} {start_y:.1f} Q '
                f'{control_x:.1f} {control_y:.1f} {end_x:.1f} {end_y:.1f}" fill="none" stroke="{color}" '
                f'stroke-width="{stroke_width:.2f}" stroke-opacity="{stroke_opacity:.2f}" '
                f'stroke-linecap="round"{dash_attribute}{marker}/>'
                f'<rect x="{distance_x - 43:.1f}" y="{distance_y - 11:.1f}" width="86" height="20" rx="10" '
                'fill="white" opacity="0.9"/>'
                f'<text x="{distance_x:.1f}" y="{distance_y + 4:.1f}" text-anchor="middle" font-size="11.5" '
                f'fill="#44505f">{escape(strength_text)}</text>'
                f'<title>{escape(label)} · relative strength {strength_index:.2f} ({escape(strength_level)})</title></g>'
            )

    exposure_by_atom = {int(item["atom_index"]): item for item in solvent_exposure}
    exposure_markup = solvent_halos(atom_points, solvent_exposure)
    projection, _, _, _ = fitted_pose_projection(ligand_coordinates, atom_points)
    patch_directions: dict[int, list[tuple[float, float]]] = {}
    for atom, directions in (accessible_patches or {}).items():
        if atom not in atom_points or float(exposure_by_atom.get(atom,{}).get("accessible_fraction",0)) < .08:
            continue
        projected = [projected_opening_direction(direction,projection) for direction in directions]
        patch_directions[atom] = [direction for direction in projected if direction is not None]
    opening_segments = solvent_boundary_segments(
        exclusion_points,atom_points,patch_directions,[item["position"] for item in layout_items],
    )
    projected_opening_points = [p for segment in opening_segments for p in segment]
    opening_bands = []
    for segment in opening_segments:
        atoms = sorted({min(atom_points,key=lambda i:math.dist(point,atom_points[i])) for point in segment})
        regions = sorted({opening_membership[atom] for atom in atoms if atom in (opening_membership or {})})
        opening_bands.append(solvent_opening_band(
            segment,exclusion_points,[item["position"] for item in layout_items],atoms,regions,
        ))
    opening_markup = "".join(opening_bands)

    interaction_order = {
        "hydrogen-bond": 0,
        "ionic": 1,
        "pi-stacking": 2,
        "cation-pi": 3,
        "halogen-bond": 4,
        "metal-coordination": 5,
        "hydrophobic": 6,
        "vdw-contact": 7,
    }
    present_kinds.sort(key=lambda kind: interaction_order.get(kind, 99))
    legend_entries = ["pocket-wall", "solvent-opening", "receptor-exposure", "ligand-exposure", *present_kinds]
    slot_width = min(260.0, 1520.0 / max(1, len(legend_entries)))
    legend_start = (width - slot_width * len(legend_entries)) / 2
    legend_y = 946.0
    legend: list[str] = []
    legend_labels = {
        "hydrogen-bond": "H-bond strength",
        "ionic": "Ionic strength",
        "pi-stacking": "π–π strength",
        "cation-pi": "Cation–π strength",
        "halogen-bond": "Halogen-bond strength",
        "metal-coordination": "Metal coordination",
        "hydrophobic": "Hydrophobic strength",
        "vdw-contact": "van der Waals strength",
    }
    for index, entry in enumerate(legend_entries):
        x_value = legend_start + index * slot_width
        if entry == "pocket-wall":
            legend.append(
                f'<line x1="{x_value:.1f}" y1="{legend_y:.1f}" x2="{x_value + 28:.1f}" y2="{legend_y:.1f}" '
                'stroke="#888c91" stroke-width="3.5" stroke-dasharray="0.1 9" stroke-linecap="round"/>'
                f'<text x="{x_value + 36:.1f}" y="{legend_y + 5:.1f}" font-size="12" fill="#293442">Pocket boundary (schematic)</text>'
            )
            continue
        if entry == "solvent-opening":
            legend.append(
                f'<line x1="{x_value:.1f}" y1="{legend_y:.1f}" x2="{x_value+28:.1f}" y2="{legend_y:.1f}" '
                'stroke="#97d5eb" stroke-width="12" stroke-opacity="0.35" stroke-linecap="round"/>'
                f'<text x="{x_value+36:.1f}" y="{legend_y+5:.1f}" font-size="12" fill="#293442">Solvent opening (2D)</text>'
            )
            continue
        if entry == "receptor-exposure":
            legend.append(
                f'<circle cx="{x_value+14:.1f}" cy="{legend_y:.1f}" r="14" fill="#3f9ff5" opacity="0.45"/>'
                f'<circle cx="{x_value+14:.1f}" cy="{legend_y:.1f}" r="9" fill="#f4f7fa"/>'
                f'<text x="{x_value+36:.1f}" y="{legend_y+5:.1f}" font-size="12" fill="#293442">Residue exposure</text>'
            )
            continue
        if entry == "ligand-exposure":
            legend.append(
                f'<circle cx="{x_value + 18:.1f}" cy="{legend_y:.1f}" r="18" fill="url(#ligand-exposure)" opacity="0.65"/>'
                f'<text x="{x_value + 44:.1f}" y="{legend_y + 5:.1f}" font-size="13" fill="#293442">Ligand solvent exposure</text>'
            )
            continue
        color, dash, _ = CONTACT_STYLE[entry]
        if entry == "hydrophobic":
            legend.append(
                f'<circle cx="{x_value + 14:.1f}" cy="{legend_y:.1f}" r="9" fill="url(#residue-hydrophobic)" '
                'stroke="#b7d3d7" stroke-width="1"/>'
                f'<text x="{x_value + 34:.1f}" y="{legend_y + 5:.1f}" font-size="13" fill="#293442">{escape(legend_labels[entry])}</text>'
            )
            continue
        dash_attribute = f' stroke-dasharray="{dash}"' if dash else ""
        legend.append(
            f'<line x1="{x_value:.1f}" y1="{legend_y:.1f}" x2="{x_value + 40:.1f}" y2="{legend_y:.1f}" stroke="{color}" '
            f'stroke-width="3"{dash_attribute}/><text x="{x_value + 50:.1f}" y="{legend_y + 5:.1f}" font-size="13" '
            f'fill="#293442">{escape(legend_labels.get(entry, CONTACT_STYLE[entry][2]))}</text>'
        )
    legend.append(
        '<text x="800" y="984" text-anchor="middle" font-size="11.5" fill="#667383">'
        'Line labels are relative interaction-strength indices (0–1), not distances or binding energies.'
        '</text>'
    )

    marker_defs = "".join(
        f'<marker id="arrow-{kind}" markerWidth="8" markerHeight="8" refX="6.4" refY="4" orient="auto">'
        f'<path d="M0,0 L8,4 L0,8 z" fill="{style[0]}"/></marker>'
        for kind, style in CONTACT_STYLE.items()
    )
    pocket_paths = projected_pocket_paths(
        atom_points, center, layout_items, projected_opening_points,
        glyph_points, opening_clearance=4.5,
    )
    pocket_markup = "".join(
        f'<path d="{path}" fill="none" stroke="#888c91" stroke-width="3.5" '
        'stroke-dasharray="0.1 9" stroke-linecap="round" stroke-linejoin="round"/>'
        for path in pocket_paths
    )
    return f'''<svg xmlns="http://www.w3.org/2000/svg" width="{width}" height="{height}" viewBox="-24 -32 1648 1030" role="img" aria-labelledby="title desc">
<title id="title">{escape(ligand_label)} protein-ligand interaction diagram</title>
<desc id="desc">Interactions and solvent accessibility are computed from the selected 3D pose. Blue residue halos show bulk-connected accessible area relative to the same residue isolated in its bound conformation, not tabulated RSA. Pale blue boundary patches project accessible ligand surface directions. Gray dotted curves are schematic proximity boundaries, not measured protein surfaces. Violet atom halos encode ligand exposure, not charge or binding energy.</desc>
<metadata>{{"engine":"Synon 2D Interaction Engine","pose":"{escape(pose_label)}","analysis":"ProLIF {escape(analysis_version)}","depiction":"RDKit {escape(depiction_version)}"}}</metadata>
<defs>
  <linearGradient id="residue-polar" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#f3fdff"/><stop offset="1" stop-color="#a7e2f0"/></linearGradient>
  <linearGradient id="residue-neutral" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#fffef6"/><stop offset="1" stop-color="#eef0d8"/></linearGradient>
  <linearGradient id="residue-hydrophobic" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#f6facb"/><stop offset="1" stop-color="#c7e829"/></linearGradient>
  <linearGradient id="residue-hydrophobic-soft" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#fbfce9"/><stop offset="1" stop-color="#dceca2"/></linearGradient>
  <linearGradient id="residue-acidic" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#fff5ec"/><stop offset="1" stop-color="#ec9b59"/></linearGradient>
  <linearGradient id="residue-basic" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="#f1f9ff"/><stop offset="1" stop-color="#a9d8ee"/></linearGradient>
  <radialGradient id="ligand-exposure"><stop offset="0" stop-color="#7065ed"/><stop offset="0.4" stop-color="#8176ef" stop-opacity="0.82"/><stop offset="0.75" stop-color="#a8a0f5" stop-opacity="0.35"/><stop offset="1" stop-color="#c8c2ff" stop-opacity="0"/></radialGradient>
  {marker_defs}
</defs>
<rect x="-24" y="-32" width="1648" height="1030" fill="white"/>
<g class="solvent-exposure" aria-label="Per-atom bulk-connected probe accessibility, not binding energy">{exposure_markup}</g>
<g class="solvent-opening-regions" aria-label="Local projection of bulk-connected solvent patches">{opening_markup}</g>
<g class="projected-pocket-walls" aria-label="Contact-supported schematic pocket boundary; not a measured surface or electrostatic map">{pocket_markup}</g>
<g transform="translate({ligand_x} {ligand_y})">{ligand_body}</g>
<g class="residue-backbone">{''.join(backbone)}</g>
<g class="receptor-exposure" aria-label="Residue bulk-connected surface area relative to isolated bound conformation">{''.join(receptor_halos)}</g>
<g class="interactions" font-family="Arial, Helvetica, sans-serif">{''.join(contacts)}</g>
<g class="residue-shapes">{''.join(residue_shapes)}</g>
<g class="residue-labels" font-family="Arial, Helvetica, sans-serif">{''.join(residue_text)}</g>
<g class="interaction-legend" aria-label="Pocket and interaction legend" font-family="Arial, Helvetica, sans-serif">{''.join(legend)}</g>
</svg>'''
