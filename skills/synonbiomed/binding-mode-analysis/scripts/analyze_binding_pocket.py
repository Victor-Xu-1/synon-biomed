#!/usr/bin/env python3
"""Derive a chain-aware protein-ligand contact table and ligand-centered box."""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
from pathlib import Path
from typing import Iterable

import gemmi


def heavy_atoms(residue: gemmi.Residue) -> list[gemmi.Atom]:
    return [atom for atom in residue if atom.element.name != "H"]


def residue_identity(chain_name: str, residue: gemmi.Residue) -> tuple[str, int, str, str]:
    return (
        chain_name,
        int(residue.seqid.num),
        str(residue.seqid.icode).strip(),
        residue.name,
    )


def find_ligand(
    model: gemmi.Model,
    component_id: str,
    chain_name: str | None,
    residue_number: int | None,
) -> tuple[str, gemmi.Residue]:
    matches: list[tuple[str, gemmi.Residue]] = []
    for chain in model:
        if chain_name is not None and chain.name != chain_name:
            continue
        for residue in chain:
            if residue.entity_type != gemmi.EntityType.NonPolymer:
                continue
            if residue.name.upper() != component_id.upper():
                continue
            if residue_number is not None and residue.seqid.num != residue_number:
                continue
            if heavy_atoms(residue):
                matches.append((chain.name, residue))
    if not matches:
        qualifier = f" chain={chain_name!r}" if chain_name else ""
        raise ValueError(f"ligand {component_id!r}{qualifier} was not found")
    if len(matches) > 1:
        identities = ", ".join(
            f"{chain}:{residue.seqid.num}{str(residue.seqid.icode).strip()}"
            for chain, residue in matches
        )
        raise ValueError(
            "ligand selection is ambiguous; pass --ligand-chain and, if needed, "
            f"--ligand-residue-number. Matches: {identities}"
        )
    return matches[0]


def minimum_distance(
    protein_atoms: Iterable[gemmi.Atom], ligand_atoms: list[gemmi.Atom]
) -> float:
    return min(
        protein_atom.pos.dist(ligand_atom.pos)
        for protein_atom in protein_atoms
        for ligand_atom in ligand_atoms
    )


def ligand_box(
    atoms: list[gemmi.Atom], padding: float, minimum_size: float
) -> tuple[list[float], list[float]]:
    coordinates = [
        [float(atom.pos.x), float(atom.pos.y), float(atom.pos.z)] for atom in atoms
    ]
    lower = [min(row[axis] for row in coordinates) for axis in range(3)]
    upper = [max(row[axis] for row in coordinates) for axis in range(3)]
    center = [(lower[axis] + upper[axis]) / 2.0 for axis in range(3)]
    size = [
        max(upper[axis] - lower[axis] + 2.0 * padding, minimum_size)
        for axis in range(3)
    ]
    return center, size


def write_contacts(path: Path, rows: list[dict[str, object]]) -> None:
    fields = [
        "protein_chain",
        "protein_residue_number",
        "protein_insertion_code",
        "protein_residue_name",
        "minimum_distance_angstrom",
    ]
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        writer.writerows(rows)


def write_box(path: Path, center: list[float], size: list[float]) -> None:
    fields = ["center_x", "center_y", "center_z", "size_x", "size_y", "size_z"]
    values = [*center, *size]
    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.writer(handle)
        writer.writerow(fields)
        writer.writerow([f"{value:.4f}" for value in values])


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Create a reproducible, chain-aware binding-pocket handoff."
    )
    parser.add_argument("--structure", required=True)
    parser.add_argument("--ligand-comp-id", required=True)
    parser.add_argument("--ligand-chain")
    parser.add_argument("--ligand-residue-number", type=int)
    parser.add_argument("--model-index", type=int, default=0)
    parser.add_argument("--contact-cutoff", type=float, default=4.5)
    parser.add_argument("--box-padding", type=float, default=6.0)
    parser.add_argument("--minimum-box-size", type=float, default=18.0)
    parser.add_argument("--output-dir", required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if args.contact_cutoff <= 0 or args.box_padding < 0 or args.minimum_box_size <= 0:
        raise ValueError("cutoff and box dimensions must be positive")

    structure_path = Path(args.structure).resolve(strict=True)
    output_dir = Path(args.output_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    structure = gemmi.read_structure(str(structure_path))
    if args.model_index < 0 or args.model_index >= len(structure):
        raise ValueError(f"model index {args.model_index} is outside the structure")
    model = structure[args.model_index]
    ligand_chain, ligand = find_ligand(
        model,
        args.ligand_comp_id,
        args.ligand_chain,
        args.ligand_residue_number,
    )
    ligand_atoms = heavy_atoms(ligand)

    contacts: list[dict[str, object]] = []
    for chain in model:
        for residue in chain:
            if residue.entity_type != gemmi.EntityType.Polymer:
                continue
            protein_atoms = heavy_atoms(residue)
            if not protein_atoms:
                continue
            distance = minimum_distance(protein_atoms, ligand_atoms)
            if distance > args.contact_cutoff:
                continue
            chain_id, residue_number, insertion_code, residue_name = residue_identity(
                chain.name, residue
            )
            contacts.append(
                {
                    "protein_chain": chain_id,
                    "protein_residue_number": residue_number,
                    "protein_insertion_code": insertion_code,
                    "protein_residue_name": residue_name,
                    "minimum_distance_angstrom": f"{distance:.4f}",
                }
            )
    contacts.sort(
        key=lambda row: (
            float(row["minimum_distance_angstrom"]),
            str(row["protein_chain"]),
            int(row["protein_residue_number"]),
            str(row["protein_insertion_code"]),
        )
    )
    if not contacts:
        raise ValueError(
            f"no polymer residue contacts were found within {args.contact_cutoff:.2f} A"
        )

    center, size = ligand_box(ligand_atoms, args.box_padding, args.minimum_box_size)
    contacts_path = output_dir / "pocket_contacts.csv"
    box_path = output_dir / "pocket_box.csv"
    validation_path = output_dir / "pocket_validation.json"
    write_contacts(contacts_path, contacts)
    write_box(box_path, center, size)

    source_sha256 = hashlib.sha256(structure_path.read_bytes()).hexdigest()
    validation = {
        "schema": "synon.binding-pocket-handoff.v1",
        "status": "passed",
        "source_file": structure_path.name,
        "source_sha256": source_sha256,
        "model_index": args.model_index,
        "ligand": {
            "component_id": ligand.name,
            "chain": ligand_chain,
            "residue_number": int(ligand.seqid.num),
            "insertion_code": str(ligand.seqid.icode).strip(),
            "heavy_atom_count": len(ligand_atoms),
        },
        "contact_cutoff_angstrom": args.contact_cutoff,
        "contact_residue_count": len(contacts),
        "box_definition": "ligand heavy-atom bounding box plus explicit padding",
        "box_padding_angstrom": args.box_padding,
        "minimum_box_size_angstrom": args.minimum_box_size,
        "center_angstrom": dict(zip(("x", "y", "z"), center)),
        "size_angstrom": dict(zip(("x", "y", "z"), size)),
        "outputs": [contacts_path.name, box_path.name],
    }
    validation_path.write_text(
        json.dumps(validation, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )

    identity = f"{ligand.name} chain {ligand_chain} residue {ligand.seqid.num}"
    print(f"Validated ligand: {identity}; {len(ligand_atoms)} heavy atoms")
    print(
        f"Contacts: {len(contacts)} residues within {args.contact_cutoff:.2f} A; "
        "chain and residue number retained"
    )
    print(
        "Box center (A): " + ", ".join(f"{value:.2f}" for value in center)
        + "; size (A): "
        + ", ".join(f"{value:.2f}" for value in size)
    )
    print(f"Outputs: {contacts_path.name}, {box_path.name}, {validation_path.name}")


if __name__ == "__main__":
    main()
