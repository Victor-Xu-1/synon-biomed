"""Validated binding-pocket receipt consumption for the AutoDock Vina pack."""

from __future__ import annotations

import hashlib
import json
import math
from pathlib import Path


P2RANK_ARCHIVE_SHA256 = "d243f2d9036ac053fefb9407b5fe1c85f4fe077c519fd975ac585e995feab274"


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for block in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def _load_object(path: Path, label: str) -> dict[str, object]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError, TypeError) as error:
        raise ValueError(f"{label} is not readable JSON") from error
    if not isinstance(value, dict):
        raise ValueError(f"{label} must be a JSON object")
    return value


def _axis_values(value: object, label: str) -> tuple[float, float, float]:
    if not isinstance(value, dict):
        raise ValueError(f"{label} is missing")
    result: list[float] = []
    for axis in ("x", "y", "z"):
        try:
            number = float(value[axis])
        except (KeyError, TypeError, ValueError):
            raise ValueError(f"{label}.{axis} is invalid") from None
        if not math.isfinite(number):
            raise ValueError(f"{label}.{axis} is non-finite")
        result.append(number)
    return result[0], result[1], result[2]


def load_validated_pocket_selection(
    selection_path: Path,
    validation_path: Path,
    receptor_path: Path,
) -> dict[str, object]:
    selection = _load_object(selection_path, "pocket selection")
    validation = _load_object(validation_path, "pocket validation")
    if selection.get("schema") != "synon.binding-pocket-prediction.v1" or selection.get("status") != "passed":
        raise ValueError("pocket selection does not use the passing prediction schema")
    if validation.get("schema") != "synon.execution-pack-validation.v4" or validation.get("execution_pack_id") != "binding-pocket-prediction.p2rank" or validation.get("overall_pass") is not True:
        raise ValueError("pocket validation is not a passing P2Rank execution receipt")
    checks = validation.get("checks")
    required_checks = {
        "source_integrity", "archive_integrity", "engine_identity", "rank_integrity",
        "probability_integrity", "box_integrity", "output_integrity",
    }
    if not isinstance(checks, dict) or any(checks.get(name) is not True for name in required_checks):
        raise ValueError("pocket validation checks are incomplete")
    method = selection.get("method")
    source = selection.get("source")
    chosen = selection.get("selection")
    if not isinstance(method, dict) or not isinstance(source, dict) or not isinstance(chosen, dict):
        raise ValueError("pocket selection is incomplete")
    if method.get("name") != "P2Rank" or method.get("version") != "2.5.1" or method.get("archive_sha256") != P2RANK_ARCHIVE_SHA256:
        raise ValueError("pocket selection engine identity is not the pinned P2Rank release")
    source_sha = _sha256_file(receptor_path)
    if source.get("sha256") != source_sha:
        raise ValueError("pocket selection source hash does not match the receptor")
    validation_inputs = validation.get("inputs")
    if not isinstance(validation_inputs, dict) or validation_inputs.get("structure") != source_sha or validation_inputs.get("p2rank_archive") != P2RANK_ARCHIVE_SHA256:
        raise ValueError("pocket validation input hashes conflict with the selection")
    if validation.get("pocket_selection_sha256") != _sha256_file(selection_path):
        raise ValueError("pocket selection hash does not match the validation receipt")
    try:
        rank = int(chosen["rank"])
        score = float(chosen["score"])
        probability = float(chosen["probability"])
    except (KeyError, TypeError, ValueError):
        raise ValueError("pocket selection rank or probability is invalid") from None
    if rank != 1 or not math.isfinite(score) or not math.isfinite(probability) or probability < 0 or probability > 1:
        raise ValueError("pocket selection is not the validated rank-1 prediction")
    center = _axis_values(chosen.get("center_angstrom"), "center_angstrom")
    size = _axis_values(chosen.get("size_angstrom"), "size_angstrom")
    if any(value < 8 or value > 100 for value in size):
        raise ValueError("predicted docking-box size is outside the accepted range")
    return {
        "method": method,
        "rank": rank,
        "score": score,
        "probability": probability,
        "center": center,
        "size": size,
        "selection_sha256": _sha256_file(selection_path),
        "validation_sha256": _sha256_file(validation_path),
        "box_definition": str(chosen.get("box_definition", "")),
    }
