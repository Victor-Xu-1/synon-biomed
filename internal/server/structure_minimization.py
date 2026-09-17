#!/usr/bin/env python3
"""Bounded RDKit force-field minimization for the unified Synon software runtime."""

import argparse
import json
import os
import sys
from typing import Any

from rdkit import Chem
from rdkit.Chem import AllChem

MAX_MOLECULES = 64
MAX_HEAVY_ATOMS = 512
MAX_CONTEXT_ATOMS = 20000
WATER_RESIDUES = {"DOD", "HOH", "H2O", "WAT"}
VALID_ELEMENTS = frozenset(
    Chem.GetPeriodicTable().GetElementSymbol(atomic_number)
    for atomic_number in range(1, 119)
)


def write_report(path: str, report: dict[str, Any]) -> None:
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        json.dump(report, handle, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
        handle.write("\n")


def base_report(
    force_field: str,
    input_format: str,
    scope: str,
    protein_environment: str,
) -> dict[str, Any]:
    return {
        "contract": True,
        "ok": False,
        "force_field": force_field,
        "format": input_format,
        "scope": scope,
        "protein_environment": protein_environment,
        "atom_count": 0,
        "ligand_atom_count": 0,
        "fixed_atom_count": 0,
        "protein_present": False,
        "ligand_residue": None,
        "molecule_count": 0,
        "generated_conformer": False,
        "before_energy": None,
        "after_energy": None,
        "converged": False,
    }


def normalize_pdb_block(content: str) -> str:
    """Repair element columns emitted by bounded Synon docking previews.

    The docking preview format can retain AutoDock atom types in the PDB
    element column (for example ``A`` for aromatic carbon) and can abbreviate
    ``Cl`` as ``C``.  RDKit correctly rejects those records.  Normalize only
    the element field from the atom-name field; coordinates, residue identity,
    connectivity, and every other column remain unchanged.
    """
    normalized_lines: list[str] = []
    for raw_line in content.splitlines():
        if not raw_line.startswith(("ATOM  ", "HETATM")):
            normalized_lines.append(raw_line)
            continue

        line = raw_line.ljust(78)
        atom_name = line[12:16].strip()
        while atom_name and atom_name[0].isdigit():
            atom_name = atom_name[1:]
        if not atom_name:
            raise ValueError("PDB atom element could not be normalized")

        current_element = line[76:78].strip().title()
        inferred_element = atom_name[0].upper()
        if len(atom_name) > 1 and atom_name[1].islower():
            inferred_element += atom_name[1].lower()

        if (
            inferred_element in VALID_ELEMENTS
            and (current_element not in VALID_ELEMENTS or len(inferred_element) == 2)
        ):
            element = inferred_element
        elif current_element in VALID_ELEMENTS:
            element = current_element
        elif inferred_element in VALID_ELEMENTS:
            element = inferred_element
        else:
            raise ValueError("PDB atom element could not be normalized")
        normalized_lines.append(f"{line[:76]}{element:>2}{line[78:]}")
    return "\n".join(normalized_lines) + "\n"


def parse_molecules(input_path: str, input_format: str) -> list[Chem.Mol]:
    if input_format == "sdf":
        supplier = Chem.SDMolSupplier(input_path, removeHs=False, sanitize=True)
        molecules = []
        for index, molecule in enumerate(supplier):
            if index >= MAX_MOLECULES:
                raise ValueError("input contains too many molecules")
            if molecule is None:
                raise ValueError(f"could not parse molecule {index + 1} from SDF")
            molecules.append(molecule)
        return molecules
    if input_format == "mol":
        molecule = Chem.MolFromMolFile(input_path, removeHs=False, sanitize=True)
    elif input_format == "mol2":
        molecule = Chem.MolFromMol2File(input_path, removeHs=False, cleanupSubstructures=True)
    elif input_format == "pdb":
        with open(input_path, "r", encoding="utf-8") as handle:
            molecule = Chem.MolFromPDBBlock(
                normalize_pdb_block(handle.read()),
                removeHs=False,
                sanitize=True,
            )
    else:
        raise ValueError(f"unsupported input format: {input_format}")
    if molecule is None:
        raise ValueError("input structure could not be parsed")
    return [molecule]


def build_force_field(molecule: Chem.Mol, force_field: str):
    if force_field == "uff":
        if not AllChem.UFFHasAllMoleculeParams(molecule):
            raise ValueError("UFF parameters are unavailable for one or more atoms")
        return AllChem.UFFGetMoleculeForceField(molecule)
    variant = "MMFF94s" if force_field == "mmff94s" else "MMFF94"
    if not AllChem.MMFFHasAllMoleculeParams(molecule):
        raise ValueError(f"{variant} parameters are unavailable for one or more atoms")
    properties = AllChem.MMFFGetMoleculeProperties(molecule, mmffVariant=variant)
    if properties is None:
        raise ValueError(f"{variant} properties are unavailable")
    return AllChem.MMFFGetMoleculeForceField(molecule, properties)


def ensure_3d_conformer(
    molecule: Chem.Mol,
    preserve_existing_coordinates: bool = False,
) -> bool:
    """Ensure force-field optimization receives a real 3D conformer.

    SDF/MOL files commonly contain a single 2D conformer.  RDKit exposes that
    conformer through the same API as a 3D conformer, but passing its zero-Z
    coordinates to the BFGS optimizer can produce an invariant violation.  A
    2D depiction is not a physical pose, so it is safe and necessary to replace
    it with a bounded, deterministic 3D embedding before minimization.

    PDB coordinates are an observed pose and receptor reference frame.  They
    remain authoritative even when every Z coordinate happens to be zero; a
    ligand-only minimization must never replace that existing frame through a
    whole-complex embedding.
    """
    if preserve_existing_coordinates:
        if molecule.GetNumConformers() == 0:
            raise ValueError("PDB input has no coordinate conformer")
        return False
    if molecule.GetNumConformers() > 0:
        try:
            if molecule.GetConformer().Is3D():
                return False
        except (IndexError, ValueError):
            pass

    molecule.RemoveAllConformers()
    embed_status = AllChem.EmbedMolecule(
        molecule,
        randomSeed=0x5EED,
        useRandomCoords=False,
    )
    if embed_status != 0:
        molecule.RemoveAllConformers()
        embed_status = AllChem.EmbedMolecule(
            molecule,
            randomSeed=0x5EED,
            useRandomCoords=True,
        )
    if embed_status != 0:
        raise ValueError("a 3D conformer could not be generated for the input molecule")
    return True


def pdb_residue_key(atom: Chem.Atom) -> tuple[str, str, int, str] | None:
    info = atom.GetPDBResidueInfo()
    if info is None:
        return None
    return (
        info.GetResidueName().strip().upper(),
        info.GetChainId().strip(),
        int(info.GetResidueNumber()),
        info.GetInsertionCode().strip(),
    )


def select_pdb_ligand(
    molecule: Chem.Mol,
    ligand_residue_name: str | None,
) -> tuple[list[int], list[int], bool, str | None]:
    if molecule.GetNumAtoms() > MAX_CONTEXT_ATOMS:
        raise ValueError("protein-ligand context exceeds the bounded atom limit")

    if ligand_residue_name:
        ligand_indices = [
            atom.GetIdx()
            for atom in molecule.GetAtoms()
            if (pdb_residue_key(atom) or ("", "", 0, ""))[0] == ligand_residue_name
        ]
        if not ligand_indices:
            raise ValueError("specified ligand residue is not present in the current pose")
        ligand_index_set = set(ligand_indices)
        fixed_indices = [
            atom.GetIdx()
            for atom in molecule.GetAtoms()
            if atom.GetIdx() not in ligand_index_set
        ]
        protein_present = any(
            atom.GetIdx() not in ligand_index_set
            and atom.GetPDBResidueInfo() is not None
            and not atom.GetPDBResidueInfo().GetIsHeteroAtom()
            for atom in molecule.GetAtoms()
        )
        return ligand_indices, fixed_indices, protein_present, f"{ligand_residue_name}:*"

    protein_indices: list[int] = []
    ligand_groups: dict[tuple[str, str, int, str], list[int]] = {}
    standalone_indices: list[int] = []
    for atom in molecule.GetAtoms():
        index = atom.GetIdx()
        info = atom.GetPDBResidueInfo()
        key = pdb_residue_key(atom)
        residue_name = key[0] if key else ""
        if residue_name in WATER_RESIDUES:
            continue
        if info is not None and not info.GetIsHeteroAtom():
            protein_indices.append(index)
            continue
        standalone_indices.append(index)
        if key is not None:
            ligand_groups.setdefault(key, []).append(index)

    protein_present = bool(protein_indices)
    if not protein_present:
        if not standalone_indices:
            raise ValueError("input contains no ligand")
        ligand_indices = standalone_indices
        ligand_index_set = set(ligand_indices)
        fixed_indices = [
            atom.GetIdx()
            for atom in molecule.GetAtoms()
            if atom.GetIdx() not in ligand_index_set
        ]
        return ligand_indices, fixed_indices, False, None

    candidates = []
    for key, indices in ligand_groups.items():
        heavy_atoms = [index for index in indices if molecule.GetAtomWithIdx(index).GetAtomicNum() > 1]
        has_carbon = any(molecule.GetAtomWithIdx(index).GetAtomicNum() == 6 for index in heavy_atoms)
        if len(heavy_atoms) >= 2 and has_carbon:
            candidates.append((key, indices))
    if not candidates:
        raise ValueError("input contains no ligand")
    if len(candidates) != 1:
        raise ValueError(
            "the protein complex contains multiple ligand candidates; open one ligand pose before minimizing"
        )

    ligand_key, ligand_indices = candidates[0]
    ligand_index_set = set(ligand_indices)
    fixed_indices = [
        atom.GetIdx()
        for atom in molecule.GetAtoms()
        if atom.GetIdx() not in ligand_index_set
    ]
    residue_name, chain_id, residue_number, insertion_code = ligand_key
    descriptor = f"{residue_name}:{chain_id or '-'}:{residue_number}{insertion_code}"
    return ligand_indices, fixed_indices, True, descriptor


def minimization_partition(
    molecule: Chem.Mol,
    input_format: str,
    ligand_residue_name: str | None,
) -> tuple[list[int], list[int], bool, str | None]:
    if input_format == "pdb":
        return select_pdb_ligand(molecule, ligand_residue_name)
    ligand_indices = [atom.GetIdx() for atom in molecule.GetAtoms()]
    if not ligand_indices:
        raise ValueError("input contains no ligand")
    return ligand_indices, [], False, None


def write_pdb_preserving_records(molecule: Chem.Mol, input_path: str, output_path: str) -> None:
    """Update coordinates without letting RDKit rewrite protein PDB semantics.

    RDKit's PDB writer is suitable for small molecules, but rewriting a protein
    complex through it can change ATOM/HETATM and polymer metadata. Mol* then
    stops recognizing the receptor as a polymer and renders disconnected
    residue fragments. Keep the original records byte-for-byte and replace only
    the fixed-width coordinate columns for atoms represented by the force field.
    """

    conformer = molecule.GetConformer()
    coordinates_by_serial: dict[int, tuple[float, float, float]] = {}
    for atom in molecule.GetAtoms():
        info = atom.GetPDBResidueInfo()
        if info is None:
            raise ValueError("PDB atom metadata is unavailable after minimization")
        serial = int(info.GetSerialNumber())
        if serial <= 0 or serial in coordinates_by_serial:
            raise ValueError("PDB atom serials are missing or ambiguous")
        point = conformer.GetAtomPosition(atom.GetIdx())
        coordinates_by_serial[serial] = (float(point.x), float(point.y), float(point.z))

    updated_serials: set[int] = set()
    output_lines: list[str] = []
    with open(input_path, encoding="utf-8") as handle:
        for raw_line in handle.read().splitlines():
            record_name = raw_line[:6].strip()
            if record_name not in {"ATOM", "HETATM"}:
                output_lines.append(raw_line)
                continue
            try:
                serial = int(raw_line[6:11])
            except ValueError as exc:
                raise ValueError("PDB atom serial is invalid") from exc
            coordinates = coordinates_by_serial.get(serial)
            if coordinates is None:
                output_lines.append(raw_line)
                continue
            padded_line = raw_line.ljust(54)
            x, y, z = coordinates
            output_lines.append(f"{padded_line[:30]}{x:8.3f}{y:8.3f}{z:8.3f}{padded_line[54:]}")
            updated_serials.add(serial)

    if updated_serials != set(coordinates_by_serial):
        raise ValueError("PDB output could not preserve every minimized atom")
    with open(output_path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(output_lines))
        handle.write("\n")


def write_output(
    molecules: list[Chem.Mol],
    input_path: str,
    output_path: str,
    output_format: str,
) -> None:
    if output_format == "pdb":
        if len(molecules) != 1:
            raise ValueError("PDB minimization requires exactly one molecule")
        write_pdb_preserving_records(molecules[0], input_path, output_path)
        return

    writer = Chem.SDWriter(output_path)
    try:
        for molecule in molecules:
            writer.write(molecule)
    finally:
        writer.close()


def minimize(args: argparse.Namespace) -> dict[str, Any]:
    report = base_report(args.force_field, args.format, args.scope, args.protein_environment)
    molecules = parse_molecules(args.input, args.format)
    if not molecules:
        raise ValueError("input contains no molecules")
    if len(molecules) != 1:
        raise ValueError("input contains multiple molecules; open one ligand before minimizing")

    total_atoms = 0
    generated_conformer = False
    before_energy = 0.0
    after_energy = 0.0
    converged = True
    ligand_atom_count = 0
    fixed_atom_count = 0
    protein_present = False
    ligand_residue = None

    for molecule in molecules:
        ligand_indices, fixed_indices, molecule_has_protein, molecule_ligand_residue = (
            minimization_partition(molecule, args.format, args.ligand_residue_name or None)
        )
        ligand_heavy_atoms = sum(
            1 for index in ligand_indices if molecule.GetAtomWithIdx(index).GetAtomicNum() > 1
        )
        if ligand_heavy_atoms <= 0 or ligand_heavy_atoms > MAX_HEAVY_ATOMS:
            raise ValueError(
                f"ligand heavy-atom count {ligand_heavy_atoms} is outside the bounded range"
            )
        total_atoms += ligand_heavy_atoms
        ligand_atom_count += ligand_heavy_atoms
        fixed_atom_count += len(fixed_indices)
        protein_present = protein_present or molecule_has_protein
        ligand_residue = molecule_ligand_residue or ligand_residue
        generated_conformer = ensure_3d_conformer(
            molecule,
            preserve_existing_coordinates=args.format == "pdb",
        ) or generated_conformer

        field = build_force_field(molecule, args.force_field)
        if field is None:
            raise ValueError("force field could not be constructed")
        conformer = molecule.GetConformer()
        fixed_coordinates = {
            index: tuple(conformer.GetAtomPosition(index))
            for index in fixed_indices
        }
        for index in fixed_indices:
            field.AddFixedPoint(index)
        before_energy += float(field.CalcEnergy())
        status = int(field.Minimize(maxIts=args.max_iterations, forceTol=args.tolerance))
        if status != 0:
            converged = False
        after_energy += float(field.CalcEnergy())
        for index, point in fixed_coordinates.items():
            conformer.SetAtomPosition(index, point)

    output_format = "pdb" if args.format == "pdb" else "sdf"
    write_output(molecules, args.input, args.output, output_format)
    report.update(
        {
            "ok": True,
            "format": output_format,
            "atom_count": total_atoms,
            "molecule_count": len(molecules),
            "scope": args.scope,
            "protein_environment": args.protein_environment,
            "ligand_atom_count": ligand_atom_count,
            "fixed_atom_count": fixed_atom_count,
            "protein_present": protein_present,
            "ligand_residue": ligand_residue,
            "generated_conformer": generated_conformer,
            "before_energy": before_energy,
            "after_energy": after_energy,
            "converged": converged,
            "message": "force-field minimization completed",
        }
    )
    return report


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--input", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--report", required=True)
    parser.add_argument("--format", choices=("sdf", "mol", "mol2", "pdb"), required=True)
    parser.add_argument("--force-field", choices=("uff", "mmff94", "mmff94s"), required=True)
    parser.add_argument("--scope", choices=("ligand",), required=True)
    parser.add_argument("--protein-environment", choices=("fixed",), required=True)
    parser.add_argument("--ligand-residue-name", default="")
    parser.add_argument("--max-iterations", type=int, default=200)
    parser.add_argument("--tolerance", type=float, default=1e-4)
    args = parser.parse_args()
    args.ligand_residue_name = args.ligand_residue_name.strip().upper()

    for path in (args.input, args.output, args.report):
        if os.path.basename(path) != path or not path or path.startswith(".") or "/" in path or "\\" in path:
            raise ValueError("runtime paths must be simple filenames")
    if args.max_iterations < 1 or args.max_iterations > 1000:
        raise ValueError("max-iterations is outside the bounded range")
    if not 0.0 < args.tolerance <= 1.0:
        raise ValueError("tolerance is outside the bounded range")

    try:
        report = minimize(args)
    except Exception as exc:  # the JSON report is the user-visible diagnostic contract
        report = base_report(
            args.force_field,
            args.format,
            args.scope,
            args.protein_environment,
        )
        report["message"] = str(exc)[:1000]
        print(report["message"], file=sys.stderr)
        write_report(args.report, report)
        return 2

    write_report(args.report, report)
    return 0 if report["ok"] else 2


if __name__ == "__main__":
    raise SystemExit(main())
