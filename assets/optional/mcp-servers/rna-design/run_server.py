#!/usr/bin/env python3
"""Small ViennaRNA MCP adapter.

The adapter is part of Synon Biomed source, while the ViennaRNA wheel is
installed into the user-managed MCP directory only after explicit action.
No RNA sequence is sent to an external service by this server.
"""

from __future__ import annotations

import os
import sys
from typing import Any


def _load_runtime() -> Any:
    root = os.environ.get("SYNON_VIENNARNA_PACKAGE_ROOT", "").strip()
    if not root:
        raise RuntimeError("ViennaRNA managed package root is not configured")
    sys.path.insert(0, root)
    try:
        import RNA  # type: ignore
    except ImportError as exc:
        raise RuntimeError("ViennaRNA runtime is not installed") from exc
    return RNA


def _sequence(value: str, field: str = "sequence") -> str:
    sequence = "".join(str(value or "").split()).upper().replace("T", "U")
    if not sequence or any(base not in "ACGUN" for base in sequence):
        raise ValueError(f"{field} must contain only A/C/G/U/N bases")
    if len(sequence) > 10000:
        raise ValueError(f"{field} is too long; maximum length is 10000 bases")
    return sequence


def fold_rna(sequence: str, temperature_c: float = 37.0) -> dict[str, Any]:
    """Predict the minimum-free-energy secondary structure for one RNA."""
    RNA = _load_runtime()
    sequence = _sequence(sequence)
    model = RNA.md()
    model.temperature = float(temperature_c)
    compound = RNA.fold_compound(sequence, model)
    structure, mfe = compound.mfe()
    return {"sequence": sequence, "structure": structure, "mfe_kcal_mol": float(mfe), "temperature_c": float(temperature_c)}


def fold_rna_pair(sequence_a: str, sequence_b: str, temperature_c: float = 37.0) -> dict[str, Any]:
    """Predict a co-folded structure and intermolecular minimum free energy."""
    RNA = _load_runtime()
    sequence_a = _sequence(sequence_a, "sequence_a")
    sequence_b = _sequence(sequence_b, "sequence_b")
    model = RNA.md()
    model.temperature = float(temperature_c)
    compound = RNA.fold_compound(f"{sequence_a}&{sequence_b}", model)
    structure, mfe = compound.mfe()
    return {"sequence_a": sequence_a, "sequence_b": sequence_b, "structure": structure, "mfe_kcal_mol": float(mfe), "temperature_c": float(temperature_c)}


def evaluate_rna_structure(sequence: str, structure: str, temperature_c: float = 37.0) -> dict[str, Any]:
    """Evaluate the free energy of a supplied dot-bracket structure."""
    RNA = _load_runtime()
    sequence = _sequence(sequence)
    structure = "".join(str(structure or "").split())
    if len(structure) != len(sequence) or any(char not in ".()[]{}<>" for char in structure):
        raise ValueError("structure must be dot-bracket notation with the same length as sequence")
    model = RNA.md()
    model.temperature = float(temperature_c)
    energy = RNA.energy_of_struct(sequence, structure, model)
    return {"sequence": sequence, "structure": structure, "energy_kcal_mol": float(energy), "temperature_c": float(temperature_c)}


def design_rna_sequence(target_structure: str, sequence_template: str = "") -> dict[str, Any]:
    """Design a sequence that folds toward a target dot-bracket structure."""
    RNA = _load_runtime()
    target_structure = "".join(str(target_structure or "").split())
    if not target_structure or any(char not in ".()[]{}<>" for char in target_structure):
        raise ValueError("target_structure must be dot-bracket notation")
    template = "".join(str(sequence_template or "").split()).upper().replace("T", "U")
    if template and (len(template) != len(target_structure) or any(base not in "ACGUN" for base in template)):
        raise ValueError("sequence_template must match target_structure length and contain A/C/G/U/N")
    designed, distance = RNA.inverse_fold(template or ("N" * len(target_structure)), target_structure)
    return {"target_structure": target_structure, "sequence": designed, "ensemble_distance": float(distance)}


def main() -> None:
    from mcp.server.fastmcp import FastMCP

    server = FastMCP("Synon Biomed ViennaRNA")
    server.tool()(fold_rna)
    server.tool()(fold_rna_pair)
    server.tool()(evaluate_rna_structure)
    server.tool()(design_rna_sequence)
    server.run()


if __name__ == "__main__":
    main()
