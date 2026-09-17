#!/usr/bin/env python3
"""Apply evidence-backed cluster annotations and recompute downstream outputs."""

from __future__ import annotations

import argparse
import json
from pathlib import Path

import pandas as pd
import scanpy as sc

from analysis_core import composition_table, save_composition_plot, save_marker_evidence_dotplot, save_umap
from annotation_core import apply_cluster_annotations, validate_cluster_annotations
from comparison_core import compare_paired_composition


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--analysis-core", required=True)
    parser.add_argument("--marker-table", required=True)
    parser.add_argument("--mapping", required=True)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--label-column", default="cell_type")
    parser.add_argument("--composition-columns", default="sample_id,patient_id,treatment,response")
    parser.add_argument("--batch-key", default="sample_id")
    parser.add_argument("--top-markers", type=int, default=250)
    parser.add_argument("--subject-key")
    parser.add_argument("--contrast-key")
    parser.add_argument("--contrast-a")
    parser.add_argument("--contrast-b")
    return parser


def main() -> None:
    args = build_parser().parse_args()
    comparison_values = (args.subject_key, args.contrast_key, args.contrast_a, args.contrast_b)
    comparison_requested = any(comparison_values)
    if comparison_requested and not all(comparison_values):
        raise ValueError(
            "--subject-key, --contrast-key, --contrast-a, and --contrast-b must be provided together"
        )

    output = Path(args.output_dir).expanduser().resolve()
    output.mkdir(parents=True, exist_ok=True)
    adata = sc.read_h5ad(args.analysis_core)
    marker_table = pd.read_csv(args.marker_table)
    mapping = validate_cluster_annotations(
        marker_table,
        pd.read_csv(args.mapping),
        observed_clusters=sorted(adata.obs["cluster"].astype(str).unique()),
        label_column=args.label_column,
        top_markers=args.top_markers,
    )
    analyzed = apply_cluster_annotations(adata, mapping, label_column=args.label_column)
    analyzed.write_h5ad(output / "analysis_annotated.h5ad", compression="gzip")
    mapping.to_csv(output / "cluster_annotations.csv", index=False)

    composition_columns = [
        value.strip()
        for value in args.composition_columns.split(",")
        if value.strip() and value.strip() in analyzed.obs
    ]
    composition = composition_table(
        analyzed,
        composition_columns,
        population_key=args.label_column,
    )
    composition.to_csv(output / "annotated_cell_composition.csv", index=False)
    save_composition_plot(
        composition,
        output / "annotated_cell_composition.pdf",
        population_key=args.label_column,
    )
    evidence_markers = [
        marker.strip()
        for values in mapping["evidence_markers"]
        for marker in values.split(";")
        if marker.strip()
    ]
    save_marker_evidence_dotplot(
        analyzed,
        output / "annotation_marker_evidence.pdf",
        groupby=args.label_column,
        markers=evidence_markers,
    )
    save_umap(
        analyzed,
        output / "umap_annotated.pdf",
        [args.label_column, args.batch_key],
    )

    outputs = [
        "analysis_annotated.h5ad",
        "cluster_annotations.csv",
        "annotated_cell_composition.csv",
        "annotated_cell_composition.pdf",
        "annotation_marker_evidence.pdf",
        "umap_annotated.pdf",
    ]
    comparison_audit = None
    if comparison_requested:
        comparison = compare_paired_composition(
            composition,
            subject_key=args.subject_key,
            contrast_key=args.contrast_key,
            contrast_a=args.contrast_a,
            contrast_b=args.contrast_b,
            population_key=args.label_column,
        )
        comparison.statistics.to_csv(output / "annotated_composition_comparison.csv", index=False)
        (output / "annotated_comparison_audit.json").write_text(
            json.dumps(comparison.audit, ensure_ascii=False, indent=2) + "\n",
            encoding="utf-8",
        )
        comparison_audit = comparison.audit
        outputs.extend(["annotated_composition_comparison.csv", "annotated_comparison_audit.json"])

    summary = {
        "status": "passed",
        "population_key": args.label_column,
        "population_semantics": "evidence_backed_cluster_annotations",
        "cell_type_claims_supported": True,
        "clusters": int(analyzed.obs["cluster"].nunique()),
        "population_labels": sorted(map(str, analyzed.obs[args.label_column].unique())),
        "comparison_audit": comparison_audit,
        "outputs": outputs,
    }
    (output / "annotation_summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
