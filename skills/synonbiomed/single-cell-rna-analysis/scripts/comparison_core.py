#!/usr/bin/env python3
"""Evidence tables for replicated single-cell composition comparisons."""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
import pandas as pd
from scipy.stats import wilcoxon


@dataclass(frozen=True)
class CompositionComparison:
    audit: dict[str, object]
    statistics: pd.DataFrame


def compare_paired_composition(
    composition: pd.DataFrame,
    *,
    subject_key: str,
    contrast_key: str,
    contrast_a: str,
    contrast_b: str,
    population_key: str,
) -> CompositionComparison:
    """Compare population proportions across an explicit within-subject contrast.

    Multiple samples for the same subject and contrast level are averaged before
    testing. Missing populations are represented as zero, while subjects lacking
    either contrast level are excluded and reported in the audit.
    """

    required = {subject_key, contrast_key, population_key, "proportion"}
    missing = sorted(required.difference(composition.columns))
    if missing:
        raise ValueError(f"composition table is missing required columns: {', '.join(missing)}")
    if not contrast_a or not contrast_b or contrast_a == contrast_b:
        raise ValueError("contrast values must be distinct non-empty strings")

    working = composition.loc[
        composition[contrast_key].astype(str).isin((contrast_a, contrast_b)),
        [subject_key, contrast_key, population_key, "proportion"],
    ].copy()
    working[subject_key] = working[subject_key].astype(str)
    working[contrast_key] = working[contrast_key].astype(str)
    working[population_key] = working[population_key].astype(str)
    if working.empty:
        raise ValueError("composition table contains no rows for the requested contrast")

    subject_levels = (
        working[[subject_key, contrast_key]]
        .drop_duplicates()
        .groupby(subject_key, observed=True)[contrast_key]
        .agg(lambda values: sorted(set(values)))
    )
    complete_subjects = sorted(
        subject for subject, levels in subject_levels.items() if set(levels) == {contrast_a, contrast_b}
    )
    incomplete_subjects = sorted(set(subject_levels.index.astype(str)).difference(complete_subjects))

    populations = sorted(working[population_key].unique(), key=_natural_key)
    columns = [
        "population",
        "contrast_a",
        "contrast_b",
        "paired_subjects",
        "mean_a",
        "mean_b",
        "mean_delta_b_minus_a",
        "median_delta_b_minus_a",
        "p_value",
        "p_adjusted_bh",
    ]
    if not complete_subjects:
        return CompositionComparison(
            audit=_comparison_audit(
                working,
                subject_key=subject_key,
                contrast_key=contrast_key,
                contrast_a=contrast_a,
                contrast_b=contrast_b,
                population_key=population_key,
                complete_subjects=complete_subjects,
                incomplete_subjects=incomplete_subjects,
                status="insufficient_paired_subjects",
            ),
            statistics=pd.DataFrame(columns=columns),
        )

    aggregated = (
        working.groupby([subject_key, contrast_key, population_key], observed=True)["proportion"]
        .mean()
        .reset_index()
    )
    rows: list[dict[str, object]] = []
    raw_p_values: list[float] = []
    for population in populations:
        subset = aggregated[
            aggregated[subject_key].isin(complete_subjects)
            & (aggregated[population_key] == population)
        ]
        pivot = subset.pivot(index=subject_key, columns=contrast_key, values="proportion").reindex(complete_subjects)
        a_values = pivot.get(contrast_a, pd.Series(index=complete_subjects, dtype=float)).fillna(0.0).to_numpy()
        b_values = pivot.get(contrast_b, pd.Series(index=complete_subjects, dtype=float)).fillna(0.0).to_numpy()
        deltas = b_values - a_values
        p_value = _paired_p_value(a_values, b_values)
        raw_p_values.append(p_value)
        rows.append(
            {
                "population": population,
                "contrast_a": contrast_a,
                "contrast_b": contrast_b,
                "paired_subjects": len(complete_subjects),
                "mean_a": float(np.mean(a_values)),
                "mean_b": float(np.mean(b_values)),
                "mean_delta_b_minus_a": float(np.mean(deltas)),
                "median_delta_b_minus_a": float(np.median(deltas)),
                "p_value": p_value,
            }
        )
    adjusted = _benjamini_hochberg(raw_p_values)
    for row, p_adjusted in zip(rows, adjusted, strict=True):
        row["p_adjusted_bh"] = p_adjusted

    status = "passed" if len(complete_subjects) >= 2 else "descriptive_only_single_pair"
    return CompositionComparison(
        audit=_comparison_audit(
            working,
            subject_key=subject_key,
            contrast_key=contrast_key,
            contrast_a=contrast_a,
            contrast_b=contrast_b,
            population_key=population_key,
            complete_subjects=complete_subjects,
            incomplete_subjects=incomplete_subjects,
            status=status,
        ),
        statistics=pd.DataFrame(rows, columns=columns),
    )


def _comparison_audit(
    working: pd.DataFrame,
    *,
    subject_key: str,
    contrast_key: str,
    contrast_a: str,
    contrast_b: str,
    population_key: str,
    complete_subjects: list[str],
    incomplete_subjects: list[str],
    status: str,
) -> dict[str, object]:
    return {
        "status": status,
        "subject_key": subject_key,
        "contrast_key": contrast_key,
        "contrast_a": contrast_a,
        "contrast_b": contrast_b,
        "population_key": population_key,
        "population_semantics": (
            "generated_cluster_ids_unannotated" if population_key == "cluster" else "provided_population_labels"
        ),
        "subjects_observed": int(working[subject_key].nunique()),
        "paired_subjects": len(complete_subjects),
        "complete_subject_ids": complete_subjects,
        "incomplete_subject_ids": incomplete_subjects,
        "method": "subject-level mean proportions; paired Wilcoxon signed-rank; BH correction",
    }


def _paired_p_value(a_values: np.ndarray, b_values: np.ndarray) -> float:
    if len(a_values) < 2 or np.allclose(a_values, b_values):
        return float("nan")
    return float(wilcoxon(b_values, a_values, zero_method="wilcox", alternative="two-sided").pvalue)


def _benjamini_hochberg(p_values: list[float]) -> list[float]:
    adjusted = [float("nan")] * len(p_values)
    finite = [(index, value) for index, value in enumerate(p_values) if np.isfinite(value)]
    if not finite:
        return adjusted
    ordered = sorted(finite, key=lambda item: item[1])
    running = 1.0
    count = len(ordered)
    for rank_from_end, (index, value) in enumerate(reversed(ordered), start=1):
        rank = count - rank_from_end + 1
        running = min(running, value * count / rank)
        adjusted[index] = float(min(1.0, running))
    return adjusted


def _natural_key(value: str) -> tuple[object, ...]:
    return (0, int(value)) if value.isdigit() else (1, value)
