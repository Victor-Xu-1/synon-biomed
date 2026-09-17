#!/usr/bin/env python3
"""Validated, sparse loading of per-sample gene-by-cell count matrices."""

from __future__ import annotations

import csv
import gzip
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

import anndata as ad
import numpy as np
import pandas as pd
from scipy import sparse


REQUIRED_COLUMNS = ("sample_id", "path")


@dataclass(frozen=True)
class SampleSpec:
    sample_id: str
    path: Path
    metadata: dict[str, str]


@dataclass(frozen=True)
class WideMatrixSpec:
    path: Path
    delimiter: str
    header_row: int
    observation_rows: dict[str, int]
    cell_names: tuple[str, ...]
    observation_values: dict[str, tuple[str, ...]]
    gene_count: int


def load_manifest(path: str | Path) -> list[SampleSpec]:
    manifest_path = Path(path).expanduser().resolve()
    frame = pd.read_csv(manifest_path, dtype=str).fillna("")
    missing_columns = [name for name in REQUIRED_COLUMNS if name not in frame.columns]
    if missing_columns:
        raise ValueError(f"manifest missing required columns: {', '.join(missing_columns)}")
    if frame.empty:
        raise ValueError("manifest contains no samples")
    specs: list[SampleSpec] = []
    errors: list[str] = []
    seen: set[str] = set()
    for row_number, row in frame.iterrows():
        sample_id = row["sample_id"].strip()
        raw_path = row["path"].strip()
        if not sample_id:
            errors.append(f"row {row_number + 2}: sample_id is empty")
            continue
        if sample_id in seen:
            errors.append(f"row {row_number + 2}: duplicate sample_id {sample_id!r}")
            continue
        seen.add(sample_id)
        sample_path = Path(raw_path).expanduser()
        if not sample_path.is_absolute():
            sample_path = manifest_path.parent / sample_path
        sample_path = sample_path.resolve()
        if not sample_path.is_file():
            errors.append(f"row {row_number + 2}: file does not exist: {sample_path}")
            continue
        metadata = {
            str(column): str(row[column]).strip()
            for column in frame.columns
            if column not in REQUIRED_COLUMNS and str(row[column]).strip()
        }
        specs.append(SampleSpec(sample_id=sample_id, path=sample_path, metadata=metadata))
    if errors:
        raise ValueError("manifest validation failed:\n- " + "\n- ".join(errors))
    return specs


def validate_compatible_gene_order(specs: Iterable[SampleSpec]) -> list[str]:
    specs = list(specs)
    if not specs:
        raise ValueError("no samples were provided")
    reference = _read_gene_index(specs[0].path)
    errors: list[str] = []
    for spec in specs[1:]:
        genes = _read_gene_index(spec.path)
        if genes != reference:
            errors.append(
                f"{spec.sample_id}: gene order differs from {specs[0].sample_id}; "
                "normalize matrices to one explicit gene index before concatenation"
            )
    if errors:
        raise ValueError("count-matrix compatibility check failed:\n- " + "\n- ".join(errors))
    if len(reference) != len(set(reference)):
        raise ValueError("count matrices contain duplicate gene identifiers")
    return reference


def load_sparse_count_matrices(
    specs: Iterable[SampleSpec],
    *,
    chunk_rows: int = 512,
) -> ad.AnnData:
    specs = list(specs)
    genes = validate_compatible_gene_order(specs)
    datasets: list[ad.AnnData] = []
    for spec in specs:
        cell_names = list(pd.read_csv(spec.path, nrows=0).columns[1:])
        if not cell_names:
            raise ValueError(f"{spec.sample_id}: matrix contains no cell columns")
        blocks: list[sparse.csr_matrix] = []
        observed_genes: list[str] = []
        for chunk in pd.read_csv(spec.path, index_col=0, chunksize=chunk_rows):
            observed_genes.extend(map(str, chunk.index))
            values = chunk.to_numpy(dtype=np.float32, copy=False)
            if not np.isfinite(values).all() or (values < 0).any():
                raise ValueError(f"{spec.sample_id}: counts must be finite and non-negative")
            blocks.append(sparse.csr_matrix(values.T))
        if observed_genes != genes:
            raise ValueError(f"{spec.sample_id}: matrix changed after compatibility preflight")
        matrix = sparse.hstack(blocks, format="csr")
        sample = ad.AnnData(
            X=matrix,
            obs=pd.DataFrame(index=pd.Index(cell_names, name="cell_id")),
            var=pd.DataFrame(index=pd.Index(genes, name="gene_id")),
        )
        sample.obs["sample_id"] = spec.sample_id
        for name, value in spec.metadata.items():
            sample.obs[name] = value
        datasets.append(sample)
    return ad.concat(datasets, axis=0, join="inner", merge="same", index_unique="-")


def inspect_wide_matrix(
    path: str | Path,
    *,
    delimiter: str = "\t",
    header_row: int = 1,
    observation_rows: dict[str, int] | None = None,
) -> WideMatrixSpec:
    matrix_path = Path(path).expanduser().resolve()
    if not matrix_path.is_file():
        raise ValueError(f"wide matrix does not exist: {matrix_path}")
    if len(delimiter) != 1:
        raise ValueError("wide-matrix delimiter must be one character")
    observation_rows = dict(observation_rows or {})
    if header_row < 1 or any(row < 1 for row in observation_rows.values()):
        raise ValueError("wide-matrix row numbers are one-based positive integers")
    if header_row in observation_rows.values() or len(set(observation_rows.values())) != len(observation_rows):
        raise ValueError("header and observation metadata rows must be distinct")
    preamble_end = max([header_row, *observation_rows.values()])
    preamble: dict[int, list[str]] = {}
    genes: list[str] = []
    with _open_matrix_text(matrix_path) as source:
        reader = csv.reader(source, delimiter=delimiter)
        for row_number, fields in enumerate(reader, start=1):
            if row_number <= preamble_end:
                preamble[row_number] = fields
                continue
            if not fields or not fields[0].strip():
                raise ValueError(f"wide matrix row {row_number} has no gene identifier")
            genes.append(fields[0].strip())
    header = preamble.get(header_row, [])
    cell_names = tuple(value.strip() for value in header[1:])
    if not cell_names or any(not value for value in cell_names) or len(cell_names) != len(set(cell_names)):
        raise ValueError("wide matrix cell identifiers are empty or duplicated")
    if not genes or len(genes) != len(set(genes)):
        raise ValueError("wide matrix gene identifiers are empty or duplicated")
    observation_values: dict[str, tuple[str, ...]] = {}
    for name, row_number in observation_rows.items():
        name = str(name).strip()
        values = tuple(value.strip() for value in preamble.get(row_number, [])[1:])
        if not name or not values or len(values) != len(cell_names):
            raise ValueError(f"observation row {row_number} for {name!r} does not match the cell header")
        observation_values[name] = values
    return WideMatrixSpec(
        path=matrix_path,
        delimiter=delimiter,
        header_row=header_row,
        observation_rows=observation_rows,
        cell_names=cell_names,
        observation_values=observation_values,
        gene_count=len(genes),
    )


def load_sparse_wide_matrix(spec: WideMatrixSpec, *, chunk_rows: int = 512) -> ad.AnnData:
    if chunk_rows <= 0:
        raise ValueError("chunk_rows must be positive")
    preamble_end = max([spec.header_row, *spec.observation_rows.values()])
    blocks: list[sparse.csr_matrix] = []
    genes: list[str] = []
    buffered: list[list[float]] = []
    # Bound each conversion block by value count as well as row count. A public
    # matrix can contain tens of thousands of cells, where 512 Python-float rows
    # would otherwise create a large transient allocation before sparse conversion.
    effective_chunk_rows = max(1, min(chunk_rows, 1_000_000 // len(spec.cell_names)))

    def flush() -> None:
        if not buffered:
            return
        values = np.asarray(buffered, dtype=np.float32)
        if values.ndim != 2 or values.shape[1] != len(spec.cell_names):
            raise ValueError("wide matrix data row width changed after preflight")
        if not np.isfinite(values).all() or (values < 0).any():
            raise ValueError("wide matrix values must be finite and non-negative")
        blocks.append(sparse.csr_matrix(values.T))
        buffered.clear()

    with _open_matrix_text(spec.path) as source:
        reader = csv.reader(source, delimiter=spec.delimiter)
        for row_number, fields in enumerate(reader, start=1):
            if row_number <= preamble_end:
                continue
            # Public expression tables commonly terminate data rows with one
            # delimiter even though the header has no corresponding column.
            # Treat exactly one final empty field as serialization whitespace;
            # never truncate a named or non-empty extra column.
            if len(fields) == len(spec.cell_names) + 2 and not fields[-1].strip():
                fields = fields[:-1]
            if len(fields) != len(spec.cell_names) + 1:
                raise ValueError(f"wide matrix row {row_number} has {len(fields) - 1} values; expected {len(spec.cell_names)}")
            genes.append(fields[0].strip())
            try:
                buffered.append([float(value) for value in fields[1:]])
            except ValueError as error:
                raise ValueError(f"wide matrix row {row_number} contains a non-numeric value") from error
            if len(buffered) >= effective_chunk_rows:
                flush()
    flush()
    if len(genes) != spec.gene_count or len(genes) != len(set(genes)):
        raise ValueError("wide matrix gene index changed after preflight")
    matrix = sparse.hstack(blocks, format="csr")
    obs = pd.DataFrame(index=pd.Index(spec.cell_names, name="cell_id"))
    for name, values in spec.observation_values.items():
        obs[name] = list(values)
    return ad.AnnData(X=matrix, obs=obs, var=pd.DataFrame(index=pd.Index(genes, name="gene_id")))


def load_observation_table(
    path: str | Path,
    *,
    id_column: str,
    column_map: dict[str, str] | None = None,
    expected_cell_ids: Iterable[str],
    delimiter: str = ",",
) -> pd.DataFrame:
    """Load and align external per-cell metadata to one wide matrix.

    ``column_map`` maps canonical analysis names to source-table columns, for
    example ``{"sample_id": "sample", "treatment": "timepoint"}``. When it
    is empty, every non-ID source column is retained under its original name.
    Missing matrix cells, duplicate IDs, ambiguous mappings, and conflicting
    target names fail before compute; unrelated extra annotation rows are
    ignored after their count is recorded by the caller.
    """

    table_path = Path(path).expanduser().resolve()
    if not table_path.is_file():
        raise ValueError(f"observation table does not exist: {table_path}")
    if len(delimiter) != 1:
        raise ValueError("observation-table delimiter must be one character")
    id_column = str(id_column).strip()
    if not id_column:
        raise ValueError("observation-table ID column is empty")
    frame = pd.read_csv(table_path, sep=delimiter, dtype=str).fillna("")
    if id_column not in frame.columns:
        raise ValueError(f"observation table is missing ID column {id_column!r}")
    identifiers = frame[id_column].astype(str).str.strip()
    if bool((identifiers == "").any()):
        raise ValueError("observation table contains an empty cell identifier")
    duplicated = identifiers[identifiers.duplicated()].unique().tolist()
    if duplicated:
        raise ValueError(f"observation table contains duplicate cell identifiers: {duplicated[:5]}")

    mapping = dict(column_map or {})
    if not mapping:
        mapping = {str(column): str(column) for column in frame.columns if str(column) != id_column}
    if any(not str(target).strip() or not str(source).strip() for target, source in mapping.items()):
        raise ValueError("observation column mappings must use non-empty TARGET=SOURCE names")
    if len(set(mapping.values())) != len(mapping):
        raise ValueError("observation column mappings must be one-to-one")
    missing_columns = sorted({source for source in mapping.values() if source not in frame.columns})
    if missing_columns:
        raise ValueError(f"observation table is missing mapped columns: {', '.join(missing_columns)}")

    selected = frame[[id_column, *mapping.values()]].copy()
    selected[id_column] = identifiers
    selected = selected.set_index(id_column)
    selected.index.name = "cell_id"
    selected = selected.rename(columns={source: target for target, source in mapping.items()})
    expected = pd.Index([str(value).strip() for value in expected_cell_ids], name="cell_id")
    if bool((expected == "").any()) or expected.has_duplicates:
        raise ValueError("wide matrix cell identifiers are empty or duplicated")
    missing_cells = expected.difference(selected.index)
    if len(missing_cells):
        raise ValueError(
            f"observation table is missing {len(missing_cells)} matrix cells; examples: "
            + ", ".join(map(str, missing_cells[:5]))
        )
    return selected.reindex(expected)


def attach_observation_table(adata: ad.AnnData, observations: pd.DataFrame) -> ad.AnnData:
    if not adata.obs_names.equals(observations.index):
        raise ValueError("observation table is not aligned to the matrix cell order")
    result = adata.copy()
    for name in observations.columns:
        incoming = observations[name].astype(str)
        if name in result.obs:
            existing = result.obs[name].astype(str)
            conflict = (existing.str.strip() != "") & (incoming.str.strip() != "") & (existing != incoming)
            if bool(conflict.any()):
                raise ValueError(f"observation table conflicts with embedded metadata column {name!r}")
            incoming = incoming.where(incoming.str.strip() != "", existing)
        result.obs[name] = incoming.to_numpy()
    return result


def _read_gene_index(path: Path) -> list[str]:
    return list(map(str, pd.read_csv(path, usecols=[0], dtype=str).iloc[:, 0]))


def _open_matrix_text(path: Path):
    if path.name.lower().endswith(".gz"):
        return gzip.open(path, "rt", encoding="utf-8", newline="")
    return path.open("r", encoding="utf-8", newline="")
