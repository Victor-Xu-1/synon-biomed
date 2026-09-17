#!/usr/bin/env python3
"""Validation and application of evidence-backed cluster annotations."""

from __future__ import annotations

import re

import pandas as pd


def validate_cluster_annotations(
    marker_table: pd.DataFrame,
    mapping: pd.DataFrame,
    *,
    observed_clusters: list[str],
    label_column: str = "cell_type",
    top_markers: int = 250,
) -> pd.DataFrame:
    required_mapping = {"cluster", label_column, "evidence_markers"}
    missing_mapping = sorted(required_mapping.difference(mapping.columns))
    if missing_mapping:
        raise ValueError(f"annotation mapping is missing columns: {', '.join(missing_mapping)}")
    required_markers = {"group", "names"}
    missing_markers = sorted(required_markers.difference(marker_table.columns))
    if missing_markers:
        raise ValueError(f"marker table is missing columns: {', '.join(missing_markers)}")

    normalized = mapping.loc[:, ["cluster", label_column, "evidence_markers"]].copy()
    normalized["cluster"] = normalized["cluster"].astype(str)
    normalized[label_column] = normalized[label_column].fillna("").astype(str).str.strip()
    normalized["evidence_markers"] = normalized["evidence_markers"].fillna("").astype(str)
    if normalized["cluster"].duplicated().any():
        duplicates = sorted(normalized.loc[normalized["cluster"].duplicated(), "cluster"].unique())
        raise ValueError(f"annotation mapping contains duplicate clusters: {', '.join(duplicates)}")

    expected = set(map(str, observed_clusters))
    supplied = set(normalized["cluster"])
    if expected != supplied:
        missing = sorted(expected.difference(supplied), key=_natural_key)
        extra = sorted(supplied.difference(expected), key=_natural_key)
        raise ValueError(f"annotation mapping cluster mismatch: missing={missing}, extra={extra}")
    if (normalized[label_column] == "").any():
        raise ValueError("annotation mapping contains an empty population label")

    ranked = marker_table.copy()
    ranked["group"] = ranked["group"].astype(str)
    ranked["names"] = ranked["names"].astype(str)
    ranked = ranked.groupby("group", observed=True, sort=False).head(top_markers)
    markers_by_cluster = {
        cluster: set(group["names"])
        for cluster, group in ranked.groupby("group", observed=True)
    }
    normalized_markers: list[str] = []
    for row in normalized.itertuples(index=False, name=None):
        cluster, label, raw_evidence = row
        evidence = [value.strip() for value in re.split(r"[;,|]", raw_evidence) if value.strip()]
        unresolved = label.lower().startswith(("unresolved", "unassigned", "ambiguous"))
        if not evidence and not unresolved:
            raise ValueError(f"cluster {cluster} has no marker evidence for label {label!r}")
        unsupported = sorted(set(evidence).difference(markers_by_cluster.get(cluster, set())))
        if unsupported:
            raise ValueError(
                f"cluster {cluster} cites markers absent from its top {top_markers}: {', '.join(unsupported)}"
            )
        normalized_markers.append(";".join(evidence))
    normalized["evidence_markers"] = normalized_markers
    return normalized.sort_values("cluster", key=lambda values: values.map(_natural_key)).reset_index(drop=True)


def apply_cluster_annotations(adata, mapping: pd.DataFrame, *, label_column: str = "cell_type"):
    if "cluster" not in adata.obs:
        raise ValueError("analysis object has no generated cluster column")
    labels = mapping.set_index("cluster")[label_column].to_dict()
    result = adata.copy()
    result.obs[label_column] = result.obs["cluster"].astype(str).map(labels)
    if result.obs[label_column].isna().any():
        raise ValueError("annotation mapping did not cover every analyzed cell")
    result.obs[label_column] = result.obs[label_column].astype("category")
    return result


def _natural_key(value: str) -> tuple[object, ...]:
    value = str(value)
    return (0, int(value)) if value.isdigit() else (1, value)
