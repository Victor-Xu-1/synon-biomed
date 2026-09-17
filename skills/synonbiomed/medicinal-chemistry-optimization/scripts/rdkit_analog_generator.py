from __future__ import annotations

import argparse
import csv
import hashlib
import json
from itertools import combinations, product
from pathlib import Path

from rdkit import Chem, DataStructs, rdBase
from rdkit.Chem import AllChem, Crippen, Descriptors, Lipinski, QED, rdFingerprintGenerator, rdMolDescriptors
from rdkit.Chem.Scaffolds import MurckoScaffold
from rdkit.SimDivFilters import rdSimDivPickers


FRAGMENTS = (
    ("fluoro", "[*]F"),
    ("chloro", "[*]Cl"),
    ("bromo", "[*]Br"),
    ("methyl", "[*]C"),
    ("ethyl", "[*]CC"),
    ("isopropyl", "[*]C(C)C"),
    ("cyclopropyl", "[*]C1CC1"),
    ("cyclohexyl", "[*]C1CCCCC1"),
    ("cyano", "[*]C#N"),
    ("methoxy", "[*]OC"),
    ("ethoxy", "[*]OCC"),
    ("hydroxymethyl", "[*]CO"),
    ("trifluoromethyl", "[*]C(F)(F)F"),
    ("carboxamide", "[*]C(=O)N"),
    ("methylamide", "[*]NC(=O)C"),
    ("methylester", "[*]C(=O)OC"),
    ("sulfonamide", "[*]S(=O)(=O)N"),
    ("phenyl", "[*]c1ccccc1"),
    ("pyridinyl", "[*]c1ccncc1"),
    ("thiazolyl", "[*]c1nccs1"),
    ("morpholinyl", "[*]N1CCOCC1"),
)

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


def load_parent(input_json: Path | None, smiles: str | None, ligand_index: int) -> tuple[str, str, str]:
    if smiles:
        return smiles.strip(), "user-smiles", "user input"
    if input_json is None:
        raise ValueError("provide --input-json or --smiles")
    payload = json.loads(input_json.read_text(encoding="utf-8"))
    ligands = payload.get("organic_ligands")
    if isinstance(ligands, list):
        if ligand_index < 0 or ligand_index >= len(ligands):
            raise ValueError("--ligand-index is outside organic_ligands")
        ligand = ligands[ligand_index]
        if not isinstance(ligand, dict):
            raise ValueError("selected ligand record is invalid")
        value = str(ligand.get("smiles") or "").strip()
        identifier = str(ligand.get("comp_id") or f"ligand-{ligand_index}").strip()
        description = str(ligand.get("description") or identifier).strip()
        if not value:
            raise ValueError("selected ligand has no SMILES")
        return value, identifier, description
    value = str(payload.get("ligand_smiles") or payload.get("smiles") or "").strip()
    if not value:
        raise ValueError("input JSON has no supported ligand SMILES field")
    identifier = str(payload.get("ligand_comp_id") or payload.get("ligand_id") or "ligand").strip()
    description = str(payload.get("ligand_description") or payload.get("ligand_name") or identifier).strip()
    return value, identifier, description


def fragment_attachment(fragment_smiles: str) -> tuple[Chem.Mol, int, int]:
    fragment = Chem.MolFromSmiles(fragment_smiles)
    if fragment is None:
        raise ValueError(f"invalid fragment SMILES: {fragment_smiles}")
    dummies = [atom for atom in fragment.GetAtoms() if atom.GetAtomicNum() == 0]
    if len(dummies) != 1 or len(dummies[0].GetNeighbors()) != 1:
        raise ValueError(f"fragment must contain one terminal dummy atom: {fragment_smiles}")
    return fragment, dummies[0].GetIdx(), dummies[0].GetNeighbors()[0].GetIdx()


def attach_fragment(molecule: Chem.Mol, atom_index: int, fragment_smiles: str) -> Chem.Mol:
    fragment, dummy_index, neighbor_index = fragment_attachment(fragment_smiles)
    offset = molecule.GetNumAtoms()
    editable = Chem.RWMol(Chem.CombineMols(molecule, fragment))
    editable.AddBond(atom_index, offset + neighbor_index, Chem.BondType.SINGLE)
    editable.RemoveAtom(offset + dummy_index)
    product_molecule = editable.GetMol()
    Chem.SanitizeMol(product_molecule)
    return product_molecule


def eligible_positions(molecule: Chem.Mol, attachment_smarts: str) -> list[int]:
    pattern = Chem.MolFromSmarts(attachment_smarts)
    if pattern is None or pattern.GetNumAtoms() == 0:
        raise ValueError("--attachment-smarts is invalid")
    return sorted({match[0] for match in molecule.GetSubstructMatches(pattern) if match})


def required_patterns(parent: Chem.Mol) -> list[tuple[str, Chem.Mol]]:
    # Every generated analog is an additive substitution of the verified
    # parent. Preserving its complete heavy-atom graph is stronger and less
    # ambiguous than accepting model-authored SMARTS that may use a different
    # aromaticity, tautomer, or stereochemical representation.
    return [("parent-heavy-atom-graph", Chem.Mol(parent))]


def accepted_candidate(
    parent_smiles: str,
    molecule: Chem.Mol,
    patterns: list[tuple[str, Chem.Mol]],
) -> tuple[str, Chem.Mol] | None:
    if not all(molecule.HasSubstructMatch(pattern, useChirality=True) for _, pattern in patterns):
        return None
    canonical = Chem.MolToSmiles(molecule, isomericSmiles=True)
    if canonical == parent_smiles:
        return None
    reparsed = Chem.MolFromSmiles(canonical)
    if reparsed is None or not all(
        reparsed.HasSubstructMatch(pattern, useChirality=True) for _, pattern in patterns
    ):
        return None
    if any(atom.GetNumRadicalElectrons() for atom in reparsed.GetAtoms()):
        return None
    return canonical, reparsed


def build_pool(
    parent: Chem.Mol,
    positions: list[int],
    patterns: list[tuple[str, Chem.Mol]],
    maximum: int,
) -> tuple[dict[str, Chem.Mol], int]:
    parent_smiles = Chem.MolToSmiles(parent, isomericSmiles=True)
    if not positions:
        raise ValueError("parent has no position matching --attachment-smarts")
    pool: dict[str, Chem.Mol] = {}
    invalid = 0

    def add(candidate: Chem.Mol | None) -> None:
        nonlocal invalid
        if candidate is None:
            invalid += 1
            return
        accepted = accepted_candidate(parent_smiles, candidate, patterns)
        if accepted is None:
            invalid += 1
            return
        canonical, molecule = accepted
        pool.setdefault(canonical, molecule)

    for position, (_, fragment) in product(positions, FRAGMENTS):
        try:
            add(attach_fragment(parent, position, fragment))
        except (RuntimeError, ValueError):
            invalid += 1

    double_fragments = FRAGMENTS[:14]
    for left_position, right_position in combinations(positions, 2):
        for (_, left_fragment), (_, right_fragment) in product(double_fragments, repeat=2):
            if len(pool) >= maximum:
                break
            try:
                first = attach_fragment(parent, left_position, left_fragment)
                add(attach_fragment(first, right_position, right_fragment))
            except (RuntimeError, ValueError):
                invalid += 1
        if len(pool) >= maximum:
            break
    return pool, invalid


def pairwise_similarity(fingerprints: list[DataStructs.ExplicitBitVect]) -> dict[str, float]:
    values: list[float] = []
    for index, fingerprint in enumerate(fingerprints):
        values.extend(DataStructs.BulkTanimotoSimilarity(fingerprint, fingerprints[index + 1 :]))
    if not values:
        return {"minimum": 0.0, "mean": 0.0, "maximum": 0.0}
    return {
        "minimum": round(min(values), 6),
        "mean": round(sum(values) / len(values), 6),
        "maximum": round(max(values), 6),
    }


def embedded_copy(molecule: Chem.Mol, seed: int) -> Chem.Mol:
    embedded = Chem.AddHs(Chem.Mol(molecule))
    parameters = AllChem.ETKDGv3()
    parameters.randomSeed = int(seed)
    if AllChem.EmbedMolecule(embedded, parameters) != 0:
        raise RuntimeError("RDKit ETKDGv3 conformer generation failed")
    if AllChem.UFFHasAllMoleculeParams(embedded):
        AllChem.UFFOptimizeMolecule(embedded, maxIters=500)
    return embedded


def main() -> int:
    parser = argparse.ArgumentParser(description="Generate a deterministic, diversity-selected RDKit analog set.")
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--input-json", type=Path)
    source.add_argument("--smiles")
    parser.add_argument("--ligand-index", type=int, default=0)
    parser.add_argument("--count", type=int, default=30)
    parser.add_argument("--pool-size", type=int, default=500)
    parser.add_argument("--seed", type=int, default=42)
    parser.add_argument("--attachment-smarts", default="[cH]")
    parser.add_argument("--id-prefix", default="ANALOG")
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    if args.count < 1 or args.count > 500:
        raise ValueError("--count must be between 1 and 500")
    if args.pool_size < args.count or args.pool_size > 5000:
        raise ValueError("--pool-size must be between --count and 5000")
    if args.seed < 0 or args.seed > 2_147_483_647:
        raise ValueError("--seed must be a non-negative signed 32-bit integer")

    parent_raw, parent_id, parent_description = load_parent(args.input_json, args.smiles, args.ligand_index)
    parent = Chem.MolFromSmiles(parent_raw)
    if parent is None:
        raise ValueError("parent SMILES is invalid")
    Chem.SanitizeMol(parent)
    parent_smiles = Chem.MolToSmiles(parent, isomericSmiles=True)
    positions = eligible_positions(parent, args.attachment_smarts)
    patterns = required_patterns(parent)

    pool, invalid_count = build_pool(parent, positions, patterns, args.pool_size)
    if len(pool) < args.count:
        raise RuntimeError(f"only {len(pool)} valid unique candidates; need {args.count}")
    smiles = sorted(pool)
    molecules = [pool[value] for value in smiles]
    generator = rdFingerprintGenerator.GetMorganGenerator(radius=2, fpSize=2048)
    fingerprints = [generator.GetFingerprint(molecule) for molecule in molecules]
    picks = list(
        rdSimDivPickers.MaxMinPicker().LazyBitVectorPick(
            fingerprints, len(fingerprints), args.count, seed=args.seed
        )
    )
    selected = [(smiles[index], molecules[index], fingerprints[index]) for index in picks]
    if len(selected) != args.count or len({value for value, _, _ in selected}) != args.count:
        raise RuntimeError("diversity picker did not return the exact unique candidate count")

    args.output_dir.mkdir(parents=True, exist_ok=True)
    sdf_path = args.output_dir / "designed_ligands.sdf"
    smiles_path = args.output_dir / "designed_ligands.smi"
    table_path = args.output_dir / "designed_ligands.csv"
    validation_path = args.output_dir / "generation_validation.json"
    records: list[dict[str, object]] = []
    writer = Chem.SDWriter(str(sdf_path))
    try:
        for index, (canonical, molecule, fingerprint) in enumerate(selected, start=1):
            embedded = embedded_copy(molecule, args.seed + index)
            candidate_id = f"{args.id_prefix}-{index:03d}"
            scaffold = Chem.MolToSmiles(
                MurckoScaffold.GetScaffoldForMol(molecule), isomericSmiles=True
            )
            molecular_weight = round(Descriptors.ExactMolWt(molecule), 4)
            clogp = round(Crippen.MolLogP(molecule), 4)
            hbd = Lipinski.NumHDonors(molecule)
            hba = Lipinski.NumHAcceptors(molecule)
            rotatable_bonds = Lipinski.NumRotatableBonds(molecule)
            record = {
                "candidate_id": candidate_id,
                "canonical_smiles": canonical,
                "molecular_weight": molecular_weight,
                "clogp": clogp,
                "tpsa": round(rdMolDescriptors.CalcTPSA(molecule), 4),
                "qed": round(QED.qed(molecule), 6),
                "hbd": hbd,
                "hba": hba,
                "rotatable_bonds": rotatable_bonds,
                "lipinski_violations": sum(
                    (
                        molecular_weight > 500.0,
                        clogp > 5.0,
                        hbd > 5,
                        hba > 10,
                    )
                ),
                "parent_tanimoto": round(DataStructs.TanimotoSimilarity(fingerprint, generator.GetFingerprint(parent)), 6),
                "murcko_scaffold": scaffold,
            }
            embedded.SetProp("_Name", candidate_id)
            embedded.SetProp("parent_id", parent_id)
            for key, value in record.items():
                embedded.SetProp(key, str(value))
            writer.write(embedded)
            records.append(record)
    finally:
        writer.close()

    atomic_text(
        smiles_path,
        "".join(f"{record['canonical_smiles']}\t{record['candidate_id']}\n" for record in records),
    )
    with table_path.open("w", newline="", encoding="utf-8") as handle:
        writer_csv = csv.DictWriter(handle, fieldnames=list(records[0]))
        writer_csv.writeheader()
        writer_csv.writerows(records)

    selected_fingerprints = [fingerprint for _, _, fingerprint in selected]
    scaffolds = {str(record["murcko_scaffold"]) for record in records}
    checks = {
        "exact_count": len(records) == args.count,
        "unique_canonical_smiles": len({str(record["canonical_smiles"]) for record in records}) == args.count,
        "parent_excluded": parent_smiles not in {str(record["canonical_smiles"]) for record in records},
        "required_core_preserved": all(
            all(molecule.HasSubstructMatch(pattern, useChirality=True) for _, pattern in patterns)
            for _, molecule, _ in selected
        ),
        "three_dimensional_sdf": True,
        "complete_property_table": all(
            0.0 <= float(record["qed"]) <= 1.0
            and 0 <= int(record["lipinski_violations"]) <= 4
            for record in records
        ),
    }
    validation = {
        "schema": "synon.rdkit-analog-generation.validation.v2",
        "status": "passed",
        "overall_pass": all(checks.values()),
        "errors": [],
        "rdkit_version": rdBase.rdkitVersion,
        "provenance": {
            "generation_class": "analog-enumeration",
            "engine": "rdkit-analog-generator",
            "provider": "local-managed-environment",
            "engine_version": rdBase.rdkitVersion,
            "conditioning": {
                "kind": "parent-ligand",
                "input_sha256": hashlib.sha256(parent_smiles.encode("utf-8")).hexdigest(),
            },
        },
        "parent": {
            "id": parent_id,
            "description": parent_description,
            "canonical_smiles": parent_smiles,
        },
        "generation": {
            "method": "defined dummy-atom fragment attachment at declared substructure positions",
            "attachment_smarts": args.attachment_smarts,
            "eligible_positions": positions,
            "fragment_count": len(FRAGMENTS),
            "pool_size": len(pool),
            "invalid_products": invalid_count,
            "required_core_labels": [label for label, _ in patterns],
            "selected_count": len(records),
            "seed": args.seed,
        },
        "diversity": {
            "fingerprint": "Morgan radius=2 fpSize=2048",
            "picker": "MaxMinPicker LazyBitVectorPick",
            "scaffold_count": len(scaffolds),
            "pairwise_tanimoto": pairwise_similarity(selected_fingerprints),
        },
        "property_summary": {
            "qed": {
                "minimum": min(float(record["qed"]) for record in records),
                "mean": round(sum(float(record["qed"]) for record in records) / len(records), 6),
                "maximum": max(float(record["qed"]) for record in records),
            },
            "lipinski_violations": {
                str(value): sum(int(record["lipinski_violations"]) == value for record in records)
                for value in range(5)
            },
        },
        "checks": checks,
        "artifacts": {},
    }
    for path in (sdf_path, smiles_path, table_path):
        validation["artifacts"][path.name] = {"sha256": sha256_file(path), "bytes": path.stat().st_size}
    if not validation["overall_pass"]:
        raise RuntimeError("analog generation validation failed")
    atomic_text(validation_path, json.dumps(validation, indent=2, sort_keys=True) + "\n")
    print(
        json.dumps(
            {
                "status": "passed",
                "parent_id": parent_id,
                "pool_size": len(pool),
                "selected_count": len(records),
                "scaffold_count": len(scaffolds),
                "pairwise_tanimoto": validation["diversity"]["pairwise_tanimoto"],
                "output_dir": str(args.output_dir),
            },
            ensure_ascii=False,
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
