"""Deterministic selection of the newest experimentally liganded PDB entry."""
from __future__ import annotations

import re
from collections.abc import Iterable
from typing import Any

from .client import PDBClient
from .records import fetch_entry_records, fetch_ligand_records


COMMON_CRYSTALLIZATION_COMPONENTS = {
    "ACE", "ACT", "BME", "CIT", "DMS", "EDO", "FLC", "GOL", "HEP",
    "IPA", "IOH", "MES", "MLI", "MPD", "PEG", "PG4", "PGE", "TRS",
}
ELEMENT_PATTERN = re.compile(r"([A-Z][a-z]?)(\d*)")


def formula_element_counts(formula: str) -> dict[str, int]:
    """Parse the simple element-count notation returned by RCSB chem comps."""
    counts: dict[str, int] = {}
    for element, raw_count in ELEMENT_PATTERN.findall(formula or ""):
        counts[element] = counts.get(element, 0) + int(raw_count or "1")
    return counts


def qualifying_organic_ligands(
    ligands: Iterable[dict[str, Any]],
    *,
    min_formula_weight: float = 120.0,
    min_carbon_atoms: int = 4,
    exclude_common_crystallization_components: bool = True,
) -> list[dict[str, Any]]:
    """Return ligand records that look like substantive organic small molecules."""
    selected: list[dict[str, Any]] = []
    for ligand in ligands:
        comp_id = str(ligand.get("comp_id") or "").strip().upper()
        chemistry = ligand.get("chem_comp") or {}
        if ligand.get("error") or chemistry.get("error"):
            continue
        if exclude_common_crystallization_components and comp_id in COMMON_CRYSTALLIZATION_COMPONENTS:
            continue
        counts = formula_element_counts(str(chemistry.get("formula") or ""))
        if counts.get("C", 0) < min_carbon_atoms:
            continue
        try:
            formula_weight = float(chemistry.get("formula_weight"))
        except (TypeError, ValueError):
            continue
        if formula_weight < min_formula_weight:
            continue
        if not str(chemistry.get("smiles") or "").strip():
            continue
        selected.append(ligand)
    return selected


def select_latest_liganded_from_search(
    client: PDBClient,
    search_result: dict[str, Any],
    *,
    max_candidates: int = 25,
    max_ligands: int = 25,
    min_formula_weight: float = 120.0,
    min_carbon_atoms: int = 4,
    exclude_common_crystallization_components: bool = True,
) -> dict[str, Any]:
    """Inspect release-date-sorted candidates in order and select the first match."""
    if search_result.get("sort_by") != "initial_release_date" or search_result.get("sort_direction") != "desc":
        raise ValueError("search_result must be sorted by initial_release_date desc")
    max_candidates = max(1, min(int(max_candidates), 25))
    pdb_ids = [
        str(record.get("pdb_id") or "").strip().upper()
        for record in search_result.get("records", [])[:max_candidates]
        if str(record.get("pdb_id") or "").strip()
    ]
    structures = fetch_entry_records(client, pdb_ids)
    by_id = {str(record.get("pdb_id") or "").upper(): record for record in structures}
    rejected: list[dict[str, Any]] = []
    for pdb_id in pdb_ids:
        structure = by_id.get(pdb_id) or {"pdb_id": pdb_id, "error": "not_found"}
        if structure.get("error"):
            rejected.append({"pdb_id": pdb_id, "reason": str(structure["error"])})
            continue
        if int(structure.get("nonpolymer_entity_count") or 0) == 0:
            rejected.append({"pdb_id": pdb_id, "reason": "no_nonpolymer_entities"})
            continue
        ligand_result = fetch_ligand_records(client, pdb_id, max_ligands=max_ligands)
        organic = qualifying_organic_ligands(
            ligand_result.get("ligands", []),
            min_formula_weight=min_formula_weight,
            min_carbon_atoms=min_carbon_atoms,
            exclude_common_crystallization_components=exclude_common_crystallization_components,
        )
        if organic:
            return {
                "selected": True,
                "structure": structure,
                "organic_ligands": organic,
                "rejected_newer_candidates": rejected,
                "inspected_candidate_count": len(rejected) + 1,
                "selection_reason": "first qualifying organic-ligand entry in authoritative release-date-descending order",
            }
        rejected.append({
            "pdb_id": pdb_id,
            "reason": "no_qualifying_organic_ligand",
            "observed_comp_ids": [
                ligand.get("comp_id") for ligand in ligand_result.get("ligands", [])
                if ligand.get("comp_id")
            ],
        })
    return {
        "selected": False,
        "structure": None,
        "organic_ligands": [],
        "rejected_newer_candidates": rejected,
        "inspected_candidate_count": len(rejected),
        "search_truncated": bool(search_result.get("truncated")),
        "reason": "no qualifying organic-ligand structure in the inspected release-ordered candidates",
    }
