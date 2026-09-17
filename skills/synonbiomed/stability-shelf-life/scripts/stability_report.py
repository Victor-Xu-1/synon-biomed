"""Render a decision-safe stability report from validated analysis outputs."""

from __future__ import annotations

from pathlib import Path
from typing import Protocol


class ObservationLike(Protocol):
    batch_id: str
    condition_role: str
    time_month: float
    attribute: str


def _number(value: object) -> str:
    if value == "" or value is None:
        return "not reached"
    return f"{float(value):.2f}"


def write_decision_report(
    path: Path,
    observations: list[ObservationLike],
    results: list[dict[str, object]],
    language: str,
) -> None:
    long_term = [row for row in observations if row.condition_role == "long_term"]
    batches = sorted({row.batch_id for row in long_term})
    attributes = sorted({row.attribute for row in long_term})
    observed_through = min(
        (
            max(
                row.time_month
                for row in long_term
                if row.attribute == attribute and row.batch_id == batch
            )
            for attribute in attributes
            for batch in batches
        ),
        default=0.0,
    )
    formal_supported = len(batches) >= 3
    if language == "zh":
        title = "# 稳定性数据决策报告"
        scope = [
            "## 证据范围",
            "",
            f"- 长期条件批次数：{len(batches)}",
            f"- 已分析属性：{', '.join(attributes) if attributes else '无'}",
            f"- 各批次/属性共同完成的长期观察时长：{observed_through:g} 个月",
            "",
            "## 当前可支持的结论",
            "",
        ]
        if formal_supported:
            conclusion = (
                "批次数达到形式上的最低门槛，但仍须结合全部稳定性指示属性、"
                "批间一致性及适用的外推分支，才能确定有效期。"
            )
        else:
            conclusion = (
                f"现有数据仅证明所测批次在已观察的 {observed_through:g} 个月内、"
                "就所提供属性而言符合限度。它不能用于给未来批次指定正式或暂定有效期；"
                "任何超过已观察时长的具体有效期均缺乏当前证据支持。"
            )
        headers = ["批次", "条件", "属性", "观察至(月)", "斜率/月", "R²", "单侧95%置信限与限度相交(月)"]
        diagnostics_heading = "## 统计诊断"
        caveat = (
            "相交时间仅为该属性和该批次的统计诊断，不是有效期。尚未提供的溶出度、"
            "单个杂质、水分、物理性质、微生物和包装相容性等属性不能视为已通过。"
        )
        next_steps = [
            "## 后续工作",
            "",
            "1. 补充至少两个具有代表性的主要批次，并持续长期稳定性考察。",
            "2. 纳入所有适用的稳定性指示属性，并按产品特定定义判断加速条件下是否发生显著变化。",
            "3. 加速考察通常以既定6个月方案完成；若达到产品适用的显著变化标准，应按适用指南增加中间条件考察，而不是把延长加速考察当作默认外推依据。",
            "4. 在批次和属性完整后，评估批间一致性、适用的 ICH Q1E 分支及最短限制性结果。",
        ]
    else:
        title = "# Stability Data Decision Report"
        scope = [
            "## Evidence scope",
            "",
            f"- Long-term batches: {len(batches)}",
            f"- Attributes analyzed: {', '.join(attributes) if attributes else 'none'}",
            f"- Common long-term observation through: {observed_through:g} months",
            "",
            "## Supported conclusion",
            "",
        ]
        if formal_supported:
            conclusion = (
                "The batch count meets the formal minimum, but shelf life still requires review of all "
                "stability-indicating attributes, batch consistency, and the applicable extrapolation branch."
            )
        else:
            conclusion = (
                f"The data establish only that the tested batch met the supplied limits through "
                f"{observed_through:g} observed months for the supplied attributes. They do not support "
                "assigning a formal or provisional expiry to future batches; any specific expiry beyond "
                "the observed duration is unsupported by the current evidence."
            )
        headers = ["Batch", "Condition", "Attribute", "Observed through (mo)", "Slope/mo", "R²", "One-sided 95% bound intersection (mo)"]
        diagnostics_heading = "## Statistical diagnostics"
        caveat = (
            "An intersection is a batch-and-attribute statistical diagnostic, not a shelf life. Missing "
            "attributes such as dissolution, individual impurities, water, physical properties, microbiology, "
            "and container performance cannot be treated as passing."
        )
        next_steps = [
            "## Next work",
            "",
            "1. Add at least two representative primary batches and continue long-term monitoring.",
            "2. Include every applicable stability-indicating attribute and apply the product-specific accelerated significant-change definition.",
            "3. Complete the planned accelerated study, conventionally through 6 months; if the applicable significant-change definition is met, add intermediate-condition data as required rather than treating a longer accelerated study as the default basis for extrapolation.",
            "4. Once batches and attributes are complete, assess batch consistency, the applicable ICH Q1E branch, and the shortest limiting result.",
        ]

    lines = [title, "", *scope, conclusion, "", diagnostics_heading, ""]
    lines.extend(["| " + " | ".join(headers) + " |", "|" + "|".join(["---"] * len(headers)) + "|"])
    for row in results:
        lines.append(
            "| "
            + " | ".join(
                [
                    str(row["batch_id"]),
                    str(row["condition"]),
                    str(row["attribute"]),
                    _number(row["observed_through_month"]),
                    f"{float(row['slope_per_month']):.6g}",
                    f"{float(row['r_squared']):.4f}",
                    _number(row["confidence_limit_intersection_month"]),
                ]
            )
            + " |"
        )
    lines.extend(["", caveat, "", *next_steps, ""])
    path.write_text("\n".join(lines), encoding="utf-8")
