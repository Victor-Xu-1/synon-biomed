#!/usr/bin/env python3
"""Stream strict managed-RDKit validation and return a v2 count summary.

Every molecule is checked. Memory scales with the current molecule/line, not
the complete library. Exit 0 is valid, 2 invalid data, and 3 parser failure.
"""

from __future__ import annotations

import argparse
import io
import json
import re
import sys
from typing import Any, BinaryIO


def _result(format_name: str, **values: Any) -> dict[str, Any]:
    result = {
        "schemaVersion": 2, "format": format_name, "ok": False,
        "code": "validator_failed", "delimiterCount": 0,
        "supplierRecordCount": 0, "parsedCount": 0,
        "invalidRecordCount": 0, "atomCountRecords": 0,
        "terminalDelimiter": format_name == "smi",
    }
    result.update(values)
    return result


class DelimiterReader:
    """Observe SDF delimiters without retaining property lines or file copies."""

    def __init__(self, source: BinaryIO):
        self.source = source
        self.byte_count = 0
        self.delimiter_count = 0
        self.terminal_delimiter = False
        self._line_length = 0
        self._is_delimiter = True
        self._finished = False

    def _part(self, value: bytes) -> None:
        if self._is_delimiter:
            self._is_delimiter = (
                self._line_length + len(value) <= 4 and value == b"$" * len(value)
            )
        self._line_length += len(value)

    def _end_line(self) -> None:
        if self._line_length:
            self.terminal_delimiter = self._is_delimiter and self._line_length == 4
            if self.terminal_delimiter:
                self.delimiter_count += 1
        self._line_length = 0
        self._is_delimiter = True

    def read(self, size: int = -1) -> bytes:
        value = self.source.read(size)
        self.byte_count += len(value)
        cursor = 0
        for match in re.finditer(b"[\r\n]", value):
            self._part(value[cursor:match.start()])
            self._end_line()
            cursor = match.end()
        self._part(value[cursor:])
        if not value and not self._finished:
            self._end_line()
            self._finished = True
        return value

    def drain(self) -> None:
        # A parser stopping early must not hide an unread invalid tail.
        while self.read(64 * 1024):
            pass


def validate_sdf(source: BinaryIO) -> dict[str, Any]:
    reader = DelimiterReader(source)
    result = _result("sdf")
    try:
        from rdkit import Chem, rdBase
    except Exception:
        reader.drain()
        result.update(code="parser_unavailable", error="RDKit parser is unavailable in the managed runtime")
    else:
        result["rdkitVersion"] = str(rdBase.rdkitVersion)
        try:
            # Every parse failure contributes to the returned counts instead
            # of producing an unbounded, repetitive stderr transcript.
            with rdBase.BlockLogs():
                supplier = Chem.ForwardSDMolSupplier(
                    reader, sanitize=True, removeHs=False, strictParsing=True
                )
                for molecule in supplier:
                    result["supplierRecordCount"] += 1
                    result["atomCountRecords"] += 1
                    if molecule is None or molecule.GetNumAtoms() <= 0:
                        result["invalidRecordCount"] += 1
                    else:
                        result["parsedCount"] += 1
            reader.drain()
        except Exception:
            result.update(code="parser_failed", error="RDKit failed while parsing the SDF artifact")
            reader.drain()
    result["delimiterCount"] = delimiter_count = reader.delimiter_count
    result["terminalDelimiter"] = reader.terminal_delimiter
    if not reader.byte_count:
        result["code"] = "empty_sdf"
    elif not delimiter_count:
        result["code"] = "missing_record_delimiter"
    elif result["code"] in {"parser_unavailable", "parser_failed"}:
        pass
    else:
        missing = max(0, delimiter_count - result["supplierRecordCount"])
        result["invalidRecordCount"] += missing
        result["atomCountRecords"] += missing
        parsed_count = result["parsedCount"]
        result["ok"] = (
            reader.terminal_delimiter
            and result["supplierRecordCount"] == delimiter_count
            and parsed_count == delimiter_count
            and result["invalidRecordCount"] == 0
        )
        result["code"] = "valid_sdf" if result["ok"] else "invalid_sdf_records"
        if not reader.terminal_delimiter:
            result["code"] = "missing_terminal_delimiter"
        elif result["supplierRecordCount"] != delimiter_count:
            result["code"] = "record_count_mismatch"
    return result


def validate_smiles(source: BinaryIO) -> dict[str, Any]:
    result = _result("smi")
    try:
        from rdkit import Chem, rdBase
    except Exception:
        Chem = rdBase = None
    if rdBase is not None:
        result["rdkitVersion"] = str(rdBase.rdkitVersion)
    header_seen = False
    saw_header = False
    smiles_index = 0
    aliases = {"smiles", "canonical_smiles", "isomeric_smiles"}
    text = io.TextIOWrapper(source, encoding="utf-8", errors="strict", newline=None)
    try:
        for raw in text:
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            values = re.split(r"[\t, ]+", line)
            if not header_seen:
                header_seen = True
                lowered = [value.lower() for value in values]
                if any(value in aliases for value in lowered):
                    saw_header = True
                    smiles_index = next(index for index, value in enumerate(lowered) if value in aliases)
                    continue
            result["delimiterCount"] += 1
            result["supplierRecordCount"] += 1
            if Chem is None:
                continue
            result["atomCountRecords"] += 1
            molecule = None
            if smiles_index < len(values) and values[smiles_index]:
                try:
                    with rdBase.BlockLogs():
                        molecule = Chem.MolFromSmiles(values[smiles_index], sanitize=True)
                except Exception:
                    molecule = None
            if molecule is None or molecule.GetNumAtoms() <= 0:
                result["invalidRecordCount"] += 1
            else:
                result["parsedCount"] += 1
    except UnicodeDecodeError:
        result["code"] = "invalid_utf8"
        return result
    finally:
        text.detach()
    if not result["delimiterCount"]:
        result["code"] = "empty_smiles_records" if saw_header else "empty_smiles"
    elif Chem is None:
        result.update(code="parser_unavailable", error="RDKit parser is unavailable in the managed runtime")
    else:
        result["ok"] = result["parsedCount"] == result["delimiterCount"] and result["invalidRecordCount"] == 0
        result["code"] = "valid_smiles" if result["ok"] else "invalid_smiles_records"
    return result


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--format", choices=("sdf", "smi"), default="sdf")
    args = parser.parse_args()
    try:
        payload = validate_sdf(sys.stdin.buffer) if args.format == "sdf" else validate_smiles(sys.stdin.buffer)
    except Exception:
        payload = _result(args.format, code="input_read_failed", error="could not read scientific artifact bytes from stdin")
    sys.stdout.write(json.dumps(payload, ensure_ascii=False, separators=(",", ":"), sort_keys=True) + "\n")
    sys.stdout.flush()
    if payload["ok"]:
        return 0
    if payload["code"] in {"parser_unavailable", "parser_failed", "input_read_failed"}:
        return 3
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
