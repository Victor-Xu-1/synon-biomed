#!/usr/bin/env python3
"""Validate SDF and SMILES files with managed RDKit.

The host sends bounded molecular-structure bytes on stdin and receives one compact JSON
object on stdout. Exit status 0 means the artifact is valid, 2 means RDKit ran
and rejected the artifact, and 3 means the trusted parser runtime is
unavailable or failed. The script deliberately has no host-Python fallback.
"""

from __future__ import annotations

import argparse
import io
import json
import re
import sys
from typing import Any


SCHEMA_VERSION = 1
MAX_INPUT_BYTES = 50 * 1024 * 1024


def _result(
    format_name: str = "sdf",
    *,
    ok: bool = False,
    code: str = "validator_failed",
    delimiter_count: int = 0,
    supplier_record_count: int = 0,
    parsed_count: int = 0,
    invalid_record_indexes: list[int] | None = None,
    atom_counts: list[int] | None = None,
    terminal_delimiter: bool = False,
    rdkit_version: str | None = None,
    error: str | None = None,
) -> dict[str, Any]:
    payload: dict[str, Any] = {
        "schemaVersion": SCHEMA_VERSION,
        "format": format_name,
        "ok": ok,
        "code": code,
        "delimiterCount": delimiter_count,
        "supplierRecordCount": supplier_record_count,
        "parsedCount": parsed_count,
        "invalidRecordIndexes": invalid_record_indexes or [],
        "atomCounts": atom_counts or [],
        "terminalDelimiter": terminal_delimiter,
    }
    if rdkit_version is not None:
        payload["rdkitVersion"] = rdkit_version
    if error is not None:
        payload["error"] = error
    return payload


def _write_result(payload: dict[str, Any]) -> None:
    sys.stdout.write(json.dumps(payload, ensure_ascii=False, separators=(",", ":"), sort_keys=True))
    sys.stdout.write("\n")
    sys.stdout.flush()


def _read_bounded_stdin() -> bytes:
    raw = sys.stdin.buffer.read(MAX_INPUT_BYTES + 1)
    if len(raw) > MAX_INPUT_BYTES:
        raise ValueError(f"scientific artifact input exceeds {MAX_INPUT_BYTES} bytes")
    return raw


def _delimiter_count(raw: bytes) -> int:
    return sum(1 for line in raw.splitlines() if line == b"$$$$")


def _has_terminal_delimiter(raw: bytes) -> bool:
    lines = raw.splitlines()
    while lines and not lines[-1]:
        lines.pop()
    return bool(lines) and lines[-1] == b"$$$$"


def validate_sdf(raw: bytes) -> dict[str, Any]:
    delimiter_count = _delimiter_count(raw)
    terminal_delimiter = _has_terminal_delimiter(raw)
    if not raw:
        return _result(
            ok=False,
            code="empty_sdf",
            delimiter_count=0,
            supplier_record_count=0,
            parsed_count=0,
            invalid_record_indexes=[],
            atom_counts=[],
            terminal_delimiter=False,
        )
    if delimiter_count == 0:
        return _result(
            ok=False,
            code="missing_record_delimiter",
            delimiter_count=0,
            supplier_record_count=0,
            parsed_count=0,
            invalid_record_indexes=[],
            atom_counts=[],
            terminal_delimiter=terminal_delimiter,
        )

    try:
        from rdkit import Chem, rdBase
    except Exception:
        return _result(
            ok=False,
            code="parser_unavailable",
            error="RDKit parser is unavailable in the managed runtime",
            delimiter_count=delimiter_count,
            supplier_record_count=0,
            parsed_count=0,
            invalid_record_indexes=[],
            atom_counts=[],
            terminal_delimiter=terminal_delimiter,
        )

    try:
        supplier = Chem.ForwardSDMolSupplier(
            io.BytesIO(raw),
            sanitize=True,
            removeHs=False,
            strictParsing=True,
        )
        invalid_indexes: list[int] = []
        atom_counts: list[int] = []
        parsed_count = 0
        supplier_record_count = 0
        for supplier_record_count, molecule in enumerate(supplier, start=1):
            if molecule is None:
                invalid_indexes.append(supplier_record_count)
                atom_counts.append(0)
                continue
            atom_count = int(molecule.GetNumAtoms())
            atom_counts.append(atom_count)
            if atom_count <= 0:
                invalid_indexes.append(supplier_record_count)
                continue
            parsed_count += 1

        if supplier_record_count < delimiter_count:
            invalid_indexes.extend(range(supplier_record_count + 1, delimiter_count + 1))
            atom_counts.extend([0] * (delimiter_count - supplier_record_count))

        invalid_indexes = sorted(set(invalid_indexes))
        ok = (
            terminal_delimiter
            and supplier_record_count == delimiter_count
            and parsed_count == delimiter_count
            and not invalid_indexes
        )
        code = "valid_sdf" if ok else "invalid_sdf_records"
        if not terminal_delimiter:
            code = "missing_terminal_delimiter"
        elif supplier_record_count != delimiter_count:
            code = "record_count_mismatch"
        return _result(
            ok=ok,
            code=code,
            delimiter_count=delimiter_count,
            supplier_record_count=supplier_record_count,
            parsed_count=parsed_count,
            invalid_record_indexes=invalid_indexes,
            atom_counts=atom_counts,
            terminal_delimiter=terminal_delimiter,
            rdkit_version=str(rdBase.rdkitVersion),
        )
    except Exception:
        return _result(
            ok=False,
            code="parser_failed",
            error="RDKit failed while parsing the SDF artifact",
            delimiter_count=delimiter_count,
            supplier_record_count=0,
            parsed_count=0,
            invalid_record_indexes=[],
            atom_counts=[],
            terminal_delimiter=terminal_delimiter,
            rdkit_version=str(rdBase.rdkitVersion),
        )


def validate_smiles(raw: bytes) -> dict[str, Any]:
    if not raw:
        return _result("smi", ok=False, code="empty_smiles", terminal_delimiter=True)
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError:
        return _result("smi", ok=False, code="invalid_utf8", terminal_delimiter=True)
    lines = [
        line.strip()
        for line in text.splitlines()
        if line.strip() and not line.lstrip().startswith("#")
    ]
    if not lines:
        return _result("smi", ok=False, code="empty_smiles", terminal_delimiter=True)
    first = re.split(r"[\t, ]+", lines[0])
    aliases = {"smiles", "canonical_smiles", "isomeric_smiles"}
    lowered = [value.strip().lower() for value in first]
    header = any(value in aliases for value in lowered)
    if header:
        smiles_index = next(index for index, value in enumerate(lowered) if value in aliases)
        data_lines = lines[1:]
    else:
        smiles_index = 0
        data_lines = lines
    if not data_lines:
        return _result("smi", ok=False, code="empty_smiles_records", terminal_delimiter=True)

    try:
        from rdkit import Chem, rdBase
    except Exception:
        return _result(
            "smi",
            ok=False,
            code="parser_unavailable",
            error="RDKit parser is unavailable in the managed runtime",
            delimiter_count=len(data_lines),
            supplier_record_count=len(data_lines),
            terminal_delimiter=True,
        )

    invalid_indexes: list[int] = []
    atom_counts: list[int] = []
    parsed_count = 0
    for record_index, line in enumerate(data_lines, start=1):
        values = re.split(r"[\t, ]+", line)
        if smiles_index >= len(values) or not values[smiles_index].strip():
            invalid_indexes.append(record_index)
            atom_counts.append(0)
            continue
        try:
            molecule = Chem.MolFromSmiles(values[smiles_index].strip(), sanitize=True)
        except Exception:
            molecule = None
        if molecule is None or molecule.GetNumAtoms() <= 0:
            invalid_indexes.append(record_index)
            atom_counts.append(0)
            continue
        parsed_count += 1
        atom_counts.append(int(molecule.GetNumAtoms()))
    ok = parsed_count == len(data_lines) and not invalid_indexes
    return _result(
        "smi",
        ok=ok,
        code="valid_smiles" if ok else "invalid_smiles_records",
        delimiter_count=len(data_lines),
        supplier_record_count=len(data_lines),
        parsed_count=parsed_count,
        invalid_record_indexes=invalid_indexes,
        atom_counts=atom_counts,
        terminal_delimiter=True,
        rdkit_version=str(rdBase.rdkitVersion),
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--format", choices=("sdf", "smi"), default="sdf")
    args = parser.parse_args()
    try:
        raw = _read_bounded_stdin()
    except ValueError as error:
        _write_result(_result(args.format, ok=False, code="input_too_large", error=str(error)))
        return 2
    except Exception:
        _write_result(_result(args.format, ok=False, code="input_read_failed", error="could not read scientific artifact bytes from stdin"))
        return 3

    payload = validate_sdf(raw) if args.format == "sdf" else validate_smiles(raw)
    _write_result(payload)
    if payload.get("ok") is True:
        return 0
    if payload.get("code") in {"parser_unavailable", "parser_failed", "input_read_failed"}:
        return 3
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
