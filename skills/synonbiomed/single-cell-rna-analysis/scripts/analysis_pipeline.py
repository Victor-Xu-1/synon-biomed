#!/usr/bin/env python3
"""Run a sparse, checkpointed scRNA-seq core analysis from supported matrices."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--manifest", help="CSV with sample_id,path and optional metadata columns")
    source.add_argument("--matrix", help="One gene-by-cell CSV/TSV, optionally gzip-compressed")
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--delimiter", choices=("tab", "comma"), default="tab")
    parser.add_argument("--header-row", type=int, default=1)
    parser.add_argument(
        "--obs-row",
        action="append",
        default=[],
        metavar="NAME=ROW",
        help="One-based preamble row carrying per-cell metadata; repeat for multiple fields",
    )
    parser.add_argument("--obs-file", help="Optional CSV/TSV with one row per matrix cell")
    parser.add_argument("--obs-id-column", default="cell_id", help="Cell-ID column in --obs-file")
    parser.add_argument("--obs-delimiter", choices=("tab", "comma"), default="comma")
    parser.add_argument(
        "--obs-column",
        action="append",
        default=[],
        metavar="TARGET=SOURCE",
        help="Map an annotation column to a canonical analysis name; repeat as needed",
    )
    parser.add_argument(
        "--matrix-kind",
        choices=("counts", "normalized-linear", "log-normalized"),
        default="counts",
    )
    parser.add_argument("--batch-key", default="sample_id")
    parser.add_argument("--composition-columns", default="sample_id,patient_id,treatment,response")
    parser.add_argument("--population-key", default="cluster")
    parser.add_argument("--subject-key", help="Biological replicate key for an explicit paired comparison")
    parser.add_argument("--contrast-key", help="Metadata column containing the two comparison levels")
    parser.add_argument("--contrast-a", help="Reference contrast level")
    parser.add_argument("--contrast-b", help="Comparison contrast level; reported as B minus A")
    parser.add_argument("--chunk-rows", type=int, default=512)
    parser.add_argument("--n-top-genes", type=int, default=2000)
    parser.add_argument("--n-pcs", type=int, default=30)
    parser.add_argument("--leiden-resolution", type=float, default=0.5)
    parser.add_argument("--max-mt-percent", type=float, default=15.0)
    parser.add_argument("--preflight-only", action="store_true")
    return parser


def main() -> None:
    args = build_parser().parse_args()
    from matrix_manifest import inspect_wide_matrix, load_manifest, load_observation_table, validate_compatible_gene_order

    wide_spec = None
    specs = None
    external_observations = None
    if args.manifest:
        if args.obs_file or args.obs_column:
            raise ValueError("--obs-file and --obs-column are available only with --matrix")
        specs = load_manifest(args.manifest)
        genes = validate_compatible_gene_order(specs)
        preflight = {
            "status": "passed",
            "input_mode": "sample-manifest",
            "sample_count": len(specs),
            "gene_count": len(genes),
            "sample_ids": [spec.sample_id for spec in specs],
            "metadata_columns": ["sample_id", *sorted({key for spec in specs for key in spec.metadata})],
        }
    else:
        observation_rows = _parse_observation_rows(args.obs_row)
        delimiter = "\t" if args.delimiter == "tab" else ","
        wide_spec = inspect_wide_matrix(
            args.matrix,
            delimiter=delimiter,
            header_row=args.header_row,
            observation_rows=observation_rows,
        )
        if args.obs_file:
            external_observations = load_observation_table(
                args.obs_file,
                id_column=args.obs_id_column,
                column_map=_parse_column_map(args.obs_column),
                expected_cell_ids=wide_spec.cell_names,
                delimiter="\t" if args.obs_delimiter == "tab" else ",",
            )
        metadata_columns = set(wide_spec.observation_values)
        if external_observations is not None:
            metadata_columns.update(map(str, external_observations.columns))
        sample_values = wide_spec.observation_values.get("sample_id", ())
        if external_observations is not None and "sample_id" in external_observations:
            sample_values = tuple(external_observations["sample_id"].astype(str))
        sample_ids = sorted({value for value in sample_values if str(value).strip()})
        preflight = {
            "status": "passed",
            "input_mode": "wide-matrix",
            "sample_count": len(sample_ids),
            "gene_count": wide_spec.gene_count,
            "cell_count": len(wide_spec.cell_names),
            "sample_ids": sample_ids,
            "metadata_columns": sorted(metadata_columns),
        }
    requested_composition_columns = [value.strip() for value in args.composition_columns.split(",") if value.strip()]
    available_composition_columns = [
        value for value in requested_composition_columns if value in preflight["metadata_columns"]
    ]
    preflight["composition_columns_requested"] = requested_composition_columns
    preflight["composition_columns_available"] = available_composition_columns
    population_ready = args.population_key == "cluster" or args.population_key in preflight["metadata_columns"]
    preflight["population_key"] = args.population_key
    preflight["population_key_available"] = population_ready
    preflight["population_semantics"] = (
        "generated_cluster_ids_unannotated"
        if args.population_key == "cluster"
        else "provided_population_labels"
    )
    preflight["cell_type_claims_supported"] = args.population_key != "cluster"
    preflight["descriptive_composition_ready"] = bool(available_composition_columns) and population_ready
    comparison_values = (args.subject_key, args.contrast_key, args.contrast_a, args.contrast_b)
    comparison_requested = any(comparison_values)
    if comparison_requested and not all(comparison_values):
        raise ValueError(
            "--subject-key, --contrast-key, --contrast-a, and --contrast-b must be provided together"
        )
    comparison_metadata_ready = bool(
        comparison_requested
        and args.subject_key in preflight["metadata_columns"]
        and args.contrast_key in preflight["metadata_columns"]
    )
    preflight["comparison_requested"] = comparison_requested
    preflight["comparison_ready"] = comparison_metadata_ready and population_ready
    if requested_composition_columns and not available_composition_columns:
        preflight["status"] = "metadata_mapping_required"
        preflight["message"] = (
            "No requested grouping metadata is available. Add embedded --obs-row mappings, "
            "join an external --obs-file, or explicitly clear --composition-columns for an "
            "aggregate exploratory run that makes no treatment, response, sample, or patient claims."
        )
    elif not population_ready:
        preflight["status"] = "metadata_mapping_required"
        preflight["message"] = (
            f"Requested population column {args.population_key!r} is unavailable. "
            "Map it from the external observation table or use the generated 'cluster' population."
        )
    elif comparison_requested and not comparison_metadata_ready:
        preflight["status"] = "metadata_mapping_required"
        preflight["message"] = (
            "The explicit comparison requires both the requested subject and contrast metadata. "
            "Map authoritative metadata before running the comparison."
        )
    print(json.dumps(preflight, ensure_ascii=False, indent=2))
    if args.preflight_only:
        return
    if preflight["status"] != "passed":
        raise ValueError(preflight["message"])

    from analysis_core import (
        composition_table,
        embedding_and_clustering,
        marker_table,
        quality_control,
        save_qc_metrics,
        save_umap,
    )
    from comparison_core import compare_paired_composition
    from matrix_manifest import attach_observation_table, load_sparse_count_matrices, load_sparse_wide_matrix

    output = Path(args.output_dir).expanduser().resolve()
    output.mkdir(parents=True, exist_ok=True)
    adata = (
        load_sparse_count_matrices(specs, chunk_rows=args.chunk_rows)
        if specs is not None
        else load_sparse_wide_matrix(wide_spec, chunk_rows=args.chunk_rows)
    )
    if external_observations is not None:
        adata = attach_observation_table(adata, external_observations)
    filtered = quality_control(
        adata,
        max_mt_percent=args.max_mt_percent,
        source_is_counts=args.matrix_kind == "counts",
    )
    filtered.write_h5ad(output / "qc_filtered.h5ad", compression="gzip")
    _write_qc_retention(
        output / "qc_retention.csv",
        cells_before=int(adata.n_obs),
        cells_after=int(filtered.n_obs),
        genes_before=int(adata.n_vars),
        genes_after=int(filtered.n_vars),
        max_mt_percent=args.max_mt_percent,
    )
    save_qc_metrics(adata, filtered, output / "qc_metrics.pdf", batch_key=args.batch_key or None)
    analyzed = embedding_and_clustering(
        filtered,
        batch_key=args.batch_key or None,
        n_top_genes=args.n_top_genes,
        n_pcs=args.n_pcs,
        leiden_resolution=args.leiden_resolution,
        matrix_kind=args.matrix_kind,
    )
    analyzed.write_h5ad(output / "analysis_core.h5ad", compression="gzip")
    marker_table(analyzed).to_csv(output / "cluster_markers.csv", index=False)
    population_key = args.population_key
    composition = composition_table(analyzed, available_composition_columns, population_key=population_key)
    composition.to_csv(output / "cell_composition.csv", index=False)
    comparison_audit = None
    if comparison_requested:
        comparison = compare_paired_composition(
            composition,
            subject_key=args.subject_key,
            contrast_key=args.contrast_key,
            contrast_a=args.contrast_a,
            contrast_b=args.contrast_b,
            population_key=population_key,
        )
        comparison.statistics.to_csv(output / "composition_comparison.csv", index=False)
        comparison_audit = comparison.audit
        (output / "comparison_audit.json").write_text(
            json.dumps(comparison.audit, ensure_ascii=False, indent=2) + "\n"
        )
    save_umap(analyzed, output / "umap.pdf", list(dict.fromkeys(["cluster", population_key, args.batch_key])))
    outputs = [
        "qc_filtered.h5ad",
        "analysis_core.h5ad",
        "qc_retention.csv",
        "qc_metrics.pdf",
        "cluster_markers.csv",
        "cell_composition.csv",
        "umap.pdf",
    ]
    if comparison_requested:
        outputs.extend(["composition_comparison.csv", "comparison_audit.json"])
    summary = {
        **preflight,
        "cells_before_qc": int(adata.n_obs),
        "cells_after_qc": int(filtered.n_obs),
        "genes_after_qc": int(filtered.n_vars),
        "clusters": int(analyzed.obs["cluster"].nunique()),
        "population_key": population_key,
        "comparison_audit": comparison_audit,
        "outputs": outputs,
    }
    (output / "analysis_summary.json").write_text(json.dumps(summary, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(summary, ensure_ascii=False, indent=2))


def _parse_observation_rows(values: list[str]) -> dict[str, int]:
    rows: dict[str, int] = {}
    for value in values:
        name, separator, raw_row = value.partition("=")
        name = name.strip()
        if not separator or not name or name in rows:
            raise ValueError(f"invalid --obs-row {value!r}; expected unique NAME=ROW")
        try:
            row = int(raw_row)
        except ValueError as error:
            raise ValueError(f"invalid --obs-row {value!r}; ROW must be an integer") from error
        if row < 1:
            raise ValueError(f"invalid --obs-row {value!r}; ROW must be positive")
        rows[name] = row
    return rows


def _parse_column_map(values: list[str]) -> dict[str, str]:
    mapping: dict[str, str] = {}
    sources: set[str] = set()
    for value in values:
        target, separator, source = value.partition("=")
        target, source = target.strip(), source.strip()
        if not separator or not target or not source or target in mapping or source in sources:
            raise ValueError(f"invalid --obs-column {value!r}; expected unique TARGET=SOURCE")
        mapping[target] = source
        sources.add(source)
    return mapping


def _write_qc_retention(
    path: Path,
    *,
    cells_before: int,
    cells_after: int,
    genes_before: int,
    genes_after: int,
    max_mt_percent: float,
) -> None:
    import csv

    with path.open("w", encoding="utf-8", newline="") as handle:
        writer = csv.DictWriter(
            handle,
            fieldnames=(
                "cells_before",
                "cells_after",
                "cells_removed",
                "cell_retention_fraction",
                "genes_before",
                "genes_after",
                "max_mt_percent",
            ),
        )
        writer.writeheader()
        writer.writerow(
            {
                "cells_before": cells_before,
                "cells_after": cells_after,
                "cells_removed": cells_before - cells_after,
                "cell_retention_fraction": cells_after / cells_before if cells_before else 0.0,
                "genes_before": genes_before,
                "genes_after": genes_after,
                "max_mt_percent": max_mt_percent,
            }
        )


if __name__ == "__main__":
    main()
