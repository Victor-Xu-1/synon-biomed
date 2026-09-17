#!/usr/bin/env python3
"""Deterministic stability trend analysis with ICH Q1E-style confidence bounds."""

from __future__ import annotations

import argparse
import csv
import json
import math
from collections import defaultdict
from dataclasses import dataclass
from pathlib import Path
from statistics import NormalDist

from stability_report import write_decision_report


REQUIRED_COLUMNS = {
    "batch_id",
    "condition_role",
    "condition",
    "time_month",
    "attribute",
    "value",
    "direction",
    "lower_limit",
    "upper_limit",
}

# One-sided 95% Student-t critical values, indexed by residual degrees of freedom.
T95_ONE_SIDED = {
    1: 6.3138, 2: 2.9200, 3: 2.3534, 4: 2.1318, 5: 2.0150,
    6: 1.9432, 7: 1.8946, 8: 1.8595, 9: 1.8331, 10: 1.8125,
    11: 1.7959, 12: 1.7823, 13: 1.7709, 14: 1.7613, 15: 1.7531,
    16: 1.7459, 17: 1.7396, 18: 1.7341, 19: 1.7291, 20: 1.7247,
    21: 1.7207, 22: 1.7171, 23: 1.7139, 24: 1.7109, 25: 1.7081,
    26: 1.7056, 27: 1.7033, 28: 1.7011, 29: 1.6991, 30: 1.6973,
}


@dataclass(frozen=True)
class Observation:
    batch_id: str
    condition_role: str
    condition: str
    time_month: float
    attribute: str
    value: float
    direction: str
    lower_limit: float | None
    upper_limit: float | None


@dataclass(frozen=True)
class Regression:
    slope: float
    intercept: float
    r_squared: float
    residual_se: float
    x_mean: float
    sxx: float
    n: int


def optional_float(value: str) -> float | None:
    value = value.strip()
    return None if value == "" else float(value)


def load_observations(path: Path) -> list[Observation]:
    with path.open(newline="", encoding="utf-8-sig") as handle:
        reader = csv.DictReader(handle)
        fields = set(reader.fieldnames or [])
        missing = sorted(REQUIRED_COLUMNS - fields)
        if missing:
            raise ValueError("missing required columns: " + ", ".join(missing))
        rows: list[Observation] = []
        for line_number, row in enumerate(reader, start=2):
            direction = row["direction"].strip().lower()
            role = row["condition_role"].strip().lower()
            if direction not in {"increase", "decrease", "either"}:
                raise ValueError(f"line {line_number}: invalid direction {direction!r}")
            if role not in {"long_term", "accelerated", "intermediate"}:
                raise ValueError(f"line {line_number}: invalid condition_role {role!r}")
            observation = Observation(
                batch_id=row["batch_id"].strip(),
                condition_role=role,
                condition=row["condition"].strip(),
                time_month=float(row["time_month"]),
                attribute=row["attribute"].strip(),
                value=float(row["value"]),
                direction=direction,
                lower_limit=optional_float(row["lower_limit"]),
                upper_limit=optional_float(row["upper_limit"]),
            )
            if not observation.batch_id or not observation.condition or not observation.attribute:
                raise ValueError(f"line {line_number}: identity fields must be non-empty")
            if observation.time_month < 0 or not math.isfinite(observation.value):
                raise ValueError(f"line {line_number}: time and value must be finite and time non-negative")
            if direction == "increase" and observation.upper_limit is None:
                raise ValueError(f"line {line_number}: increasing attributes require upper_limit")
            if direction == "decrease" and observation.lower_limit is None:
                raise ValueError(f"line {line_number}: decreasing attributes require lower_limit")
            if direction == "either" and (observation.lower_limit is None or observation.upper_limit is None):
                raise ValueError(f"line {line_number}: either-direction attributes require both limits")
            rows.append(observation)
    if not rows:
        raise ValueError("input contains no observations")
    return rows


def fit_regression(rows: list[Observation]) -> Regression:
    if len(rows) < 3:
        raise ValueError("at least three time points are required for regression")
    xs = [row.time_month for row in rows]
    ys = [row.value for row in rows]
    x_mean = sum(xs) / len(xs)
    y_mean = sum(ys) / len(ys)
    sxx = sum((x - x_mean) ** 2 for x in xs)
    if sxx <= 0:
        raise ValueError("regression requires at least two distinct time points")
    slope = sum((x - x_mean) * (y - y_mean) for x, y in zip(xs, ys)) / sxx
    intercept = y_mean - slope * x_mean
    residuals = [y - (intercept + slope * x) for x, y in zip(xs, ys)]
    sse = sum(value * value for value in residuals)
    sst = sum((y - y_mean) ** 2 for y in ys)
    residual_se = math.sqrt(sse / (len(rows) - 2))
    r_squared = 1.0 if sst == 0 and sse == 0 else (1.0 - sse / sst if sst > 0 else float("nan"))
    return Regression(slope, intercept, r_squared, residual_se, x_mean, sxx, len(rows))


def t95_one_sided(df: int) -> float:
    if df <= 0:
        raise ValueError("positive residual degrees of freedom required")
    return T95_ONE_SIDED.get(df, NormalDist().inv_cdf(0.95))


def confidence_bound(model: Regression, month: float, side: str) -> float:
    fitted = model.intercept + model.slope * month
    leverage = math.sqrt(1.0 / model.n + ((month - model.x_mean) ** 2) / model.sxx)
    delta = t95_one_sided(model.n - 2) * model.residual_se * leverage
    return fitted + delta if side == "upper" else fitted - delta


def intersection_month(model: Regression, side: str, limit: float, observed_max: float) -> float | None:
    def crossed(month: float) -> bool:
        bound = confidence_bound(model, month, side)
        return bound >= limit if side == "upper" else bound <= limit

    if crossed(0.0):
        return 0.0
    high = max(observed_max, 1.0)
    ceiling = max(1200.0, observed_max * 10.0)
    while high < ceiling and not crossed(high):
        high *= 2.0
    if not crossed(high):
        return None
    low = 0.0
    for _ in range(80):
        mid = (low + high) / 2.0
        if crossed(mid):
            high = mid
        else:
            low = mid
    return high


def analyze(rows: list[Observation]) -> list[dict[str, object]]:
    groups: dict[tuple[str, str, str, str], list[Observation]] = defaultdict(list)
    for row in rows:
        groups[(row.batch_id, row.condition_role, row.condition, row.attribute)].append(row)
    output: list[dict[str, object]] = []
    for key in sorted(groups):
        group = sorted(groups[key], key=lambda row: row.time_month)
        identities = {(row.direction, row.lower_limit, row.upper_limit) for row in group}
        if len(identities) != 1:
            raise ValueError(f"inconsistent direction or limits for group {key}")
        direction, lower_limit, upper_limit = next(iter(identities))
        model = fit_regression(group)
        observed_max = max(row.time_month for row in group)
        candidates: list[float] = []
        if direction in {"increase", "either"} and upper_limit is not None:
            value = intersection_month(model, "upper", upper_limit, observed_max)
            if value is not None:
                candidates.append(value)
        if direction in {"decrease", "either"} and lower_limit is not None:
            value = intersection_month(model, "lower", lower_limit, observed_max)
            if value is not None:
                candidates.append(value)
        output.append({
            "batch_id": key[0], "condition_role": key[1], "condition": key[2], "attribute": key[3],
            "n": model.n, "observed_through_month": observed_max, "slope_per_month": model.slope,
            "intercept": model.intercept, "r_squared": model.r_squared, "residual_se": model.residual_se,
            "confidence_limit_intersection_month": min(candidates) if candidates else "",
            "direction": direction, "lower_limit": "" if lower_limit is None else lower_limit,
            "upper_limit": "" if upper_limit is None else upper_limit,
        })
    return output


def write_results(path: Path, rows: list[dict[str, object]]) -> None:
    fields = list(rows[0])
    with path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=fields)
        writer.writeheader()
        writer.writerows(rows)


def write_summary(path: Path, observations: list[Observation], results: list[dict[str, object]]) -> None:
    long_term = [row for row in observations if row.condition_role == "long_term"]
    batches = sorted({row.batch_id for row in long_term})
    attributes = sorted({row.attribute for row in long_term})
    observed_through = min(
        (max(row.time_month for row in long_term if row.attribute == attribute and row.batch_id == batch)
         for attribute in attributes for batch in batches),
        default=0.0,
    )
    formal_supported = len(batches) >= 3
    conclusion = "formal_shelf_life_requires_full_attribute_and_extrapolation_review" if formal_supported else "development_only_insufficient_batches"
    supported_conclusion = (
        "The batch count meets the formal minimum, but shelf life still requires all applicable "
        "stability-indicating attributes, batch-consistency review, and the applicable extrapolation branch."
        if formal_supported
        else
        "The supplied data do not establish a shelf life applicable to future batches. They describe "
        "the tested batch only through the common observed long-term time. Confidence-bound intersections "
        "are statistical diagnostics, not permission to assign an expiry."
    )
    payload = {
        "schema": "synon.stability.decision_summary.v1",
        "long_term_batch_count": len(batches),
        "long_term_attributes": attributes,
        "common_observed_through_month": observed_through,
        "formal_shelf_life_supported": formal_supported,
        "conclusion_code": conclusion,
        "accelerated_significant_change_assessment": "requires_product_specific_definition_and_all_applicable_attributes",
        "supported_conclusion": supported_conclusion,
        "results_file_role": "per_batch_condition_attribute_confidence_bound_diagnostics",
    }
    path.write_text(
        json.dumps(payload, ensure_ascii=False, allow_nan=False, indent=2) + "\n",
        encoding="utf-8",
    )


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True, type=Path)
    parser.add_argument("--results", required=True, type=Path)
    parser.add_argument("--summary", required=True, type=Path)
    parser.add_argument("--report", type=Path, help="Optional decision-safe Markdown report")
    parser.add_argument("--language", choices=("en", "zh"), default="en")
    args = parser.parse_args()
    if args.summary.suffix.lower() != ".json":
        parser.error("--summary must use a .json filename; save this machine-oriented output as working_data")
    observations = load_observations(args.input)
    results = analyze(observations)
    args.results.parent.mkdir(parents=True, exist_ok=True)
    args.summary.parent.mkdir(parents=True, exist_ok=True)
    write_results(args.results, results)
    write_summary(args.summary, observations, results)
    written = [args.results, args.summary]
    if args.report is not None:
        args.report.parent.mkdir(parents=True, exist_ok=True)
        write_decision_report(args.report, observations, results, args.language)
        written.append(args.report)
    print("wrote " + ", ".join(str(path) for path in written))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
