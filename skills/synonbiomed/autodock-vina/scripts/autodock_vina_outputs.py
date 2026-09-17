from __future__ import annotations

import csv
import hashlib
import math
import re
from pathlib import Path


def _atomic_text(path: Path, text: str) -> None:
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(text, encoding="utf-8")
    temporary.replace(path)


def _sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _pose_atom_count(path: Path) -> int:
    count = 0
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        if line[:6].strip() not in {"ATOM", "HETATM"}:
            continue
        if len(line) < 54:
            raise ValueError(f"truncated atom record in {path.name}")
        coordinates = [float(line[30:38]), float(line[38:46]), float(line[46:54])]
        if not all(math.isfinite(value) for value in coordinates):
            raise ValueError(f"non-finite atom coordinate in {path.name}")
        count += 1
    return count


def safe_pose_filename(rank: int, candidate_id: str) -> str:
    slug = re.sub(r"[^A-Za-z0-9._-]+", "-", candidate_id.strip()).strip("-._")
    if not slug:
        slug = f"ligand-{rank:04d}"
    return f"rank-{rank:04d}-{slug[:80]}.pdbqt"


def write_primary_pose_artifacts(
    output: Path,
    rows: list[dict[str, object]],
    primary_poses: dict[str, dict[str, object]],
) -> tuple[list[Path], Path, list[dict[str, object]]]:
    """Write one atom-bearing primary PDBQT per ranked candidate and its identity manifest."""
    directory = output / "primary_poses"
    directory.mkdir(parents=True, exist_ok=True)
    paths: list[Path] = []
    manifest_rows: list[dict[str, object]] = []
    for row in rows:
        candidate_id = str(row["ligand_id"])
        rank = int(row["rank"])
        primary = primary_poses[candidate_id]
        path = directory / safe_pose_filename(rank, candidate_id)
        content = (
            f"REMARK SYNON LIGAND {candidate_id} RANK {rank} PRIMARY RUN {primary['run_index']} "
            f"SOURCE_MODE {primary['mode_index']}\n"
            + str(primary["pdbqt_text"])
        )
        _atomic_text(path, content if content.endswith("\n") else content + "\n")
        if _pose_atom_count(path) == 0:
            raise RuntimeError(f"primary pose artifact contains no atoms: {path.name}")
        paths.append(path)
        manifest_rows.append(
            {
                "candidate_id": candidate_id,
                "rank": rank,
                "affinity_kcal_mol": primary["affinity_kcal_mol"],
                "sample_run": primary["run_index"],
                "source_mode": primary["mode_index"],
                "path": path.relative_to(output).as_posix(),
                "sha256": _sha256_file(path),
            }
        )
    manifest_path = output / "primary_pose_manifest.csv"
    with manifest_path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(manifest_rows[0]))
        writer.writeheader()
        writer.writerows(manifest_rows)
    return paths, manifest_path, manifest_rows


def _markdown_cell(value: object) -> str:
    return " ".join(str(value).replace("|", "\\|").split())


def write_docking_report(
    path: Path,
    receptor_source: Path,
    ligand_source: Path,
    rows: list[dict[str, object]],
    center_source: str,
    reference_component: dict[str, object] | None,
    pocket_evidence: dict[str, object] | None,
    pose_manifest_rows: list[dict[str, object]],
    args: object,
) -> None:
    """Render a deterministic report from the exact ranking and pose manifest."""
    pose_paths = {str(row["candidate_id"]): str(row["path"]) for row in pose_manifest_rows}
    report_language = str(getattr(args, "report_language", "en")).strip().lower()
    if center_source == "reference_ligand_centroid" and reference_component is not None:
        if report_language == "zh":
            site_basis = (
                f"参考配体 {reference_component['component_id']}（链 {reference_component['source_chain']}，"
                f"残基 {reference_component['source_residue_number']}）的质心。"
            )
            site_limitation = "该排序仅适用于参考配体定义的结合位点。"
        else:
            site_basis = (
                "Reference-ligand centroid from "
                f"{reference_component['component_id']} chain {reference_component['source_chain']} "
                f"residue {reference_component['source_residue_number']}."
            )
            site_limitation = "The ranking is specific to the reference-defined binding site."
    elif center_source == "p2rank_predicted_pocket" and pocket_evidence is not None:
        method = pocket_evidence["method"]
        if report_language == "zh":
            site_basis = (
                f"{method['name']} {method['version']} 预测口袋第 {pocket_evidence['rank']} 名"
                f"（评分 {float(pocket_evidence['score']):.3f}；校准概率 "
                f"{float(pocket_evidence['probability']):.3f}）。"
            )
            site_limitation = (
                "所选位点是不依赖配体的口袋预测，并非实验确证的结合位点；"
                "应结合独立的结构或生化证据确认。"
            )
        else:
            site_basis = (
                f"{method['name']} {method['version']} predicted pocket rank {pocket_evidence['rank']} "
                f"(score {float(pocket_evidence['score']):.3f}; calibrated probability "
                f"{float(pocket_evidence['probability']):.3f})."
            )
            site_limitation = (
                "The selected site is a ligand-agnostic pocket prediction, not an experimentally established "
                "binding site; confirm it against orthogonal structural or biochemical evidence."
            )
    else:
        if report_language == "zh":
            site_basis = "由用户确认并传入执行包的显式对接盒坐标。"
            site_limitation = (
                "执行结果本身不能证明该对接盒属于生物学结合口袋；"
                "仅应针对所述坐标解释本次排序。"
            )
        else:
            site_basis = "Explicit docking-box coordinates supplied to the execution pack."
            site_limitation = (
                "The execution output does not by itself prove that this box is a biological binding pocket; "
                "interpret the ranking only for the stated coordinates."
            )
    center = (rows[0]["center_x"], rows[0]["center_y"], rows[0]["center_z"])
    if report_language == "zh":
        lines = [
            "# 分子对接报告", "", "## 方法", "", "- 计算引擎：AutoDock Vina",
            f"- 受体输入：`{receptor_source.name}`", f"- 配体输入：`{ligand_source.name}`",
            f"- 结合位点依据：{site_basis}",
            (
                f"- 对接盒：中心 ({center[0]}, {center[1]}, {center[2]}) Å；"
                f"尺寸 ({getattr(args, 'size_x')}, {getattr(args, 'size_y')}, {getattr(args, 'size_z')}) Å"
            ),
            (
                f"- 采样参数：随机种子 {getattr(args, 'seed')}；重复次数 {getattr(args, 'repeat_count')}；"
                f"穷尽性 {getattr(args, 'exhaustiveness')}；每次运行构象数 {getattr(args, 'num_modes')}"
            ),
            "", "## 排序结果", "",
            "| 排名 | 候选化合物 | 最佳亲和能 (kcal/mol) | 主构象 |",
            "| ---: | --- | ---: | --- |",
        ]
    else:
        lines = [
            "# Molecular docking report", "", "## Method", "", "- Engine: AutoDock Vina",
            f"- Receptor input: `{receptor_source.name}`", f"- Ligand input: `{ligand_source.name}`",
            f"- Binding-site basis: {site_basis}",
            (
                f"- Docking box: center ({center[0]}, {center[1]}, {center[2]}) Angstrom; "
                f"size ({getattr(args, 'size_x')}, {getattr(args, 'size_y')}, {getattr(args, 'size_z')}) Angstrom"
            ),
            (
                f"- Sampling: seed {getattr(args, 'seed')}; repeat count {getattr(args, 'repeat_count')}; "
                f"exhaustiveness {getattr(args, 'exhaustiveness')}; modes per run {getattr(args, 'num_modes')}"
            ),
            "", "## Ranked results", "",
            "| Rank | Candidate | Best affinity (kcal/mol) | Primary pose |",
            "| ---: | --- | ---: | --- |",
        ]
    for row in rows:
        candidate_id = str(row["ligand_id"])
        lines.append(
            f"| {row['rank']} | {_markdown_cell(candidate_id)} | "
            f"{float(row['best_affinity_kcal_mol']):.3f} | `{pose_paths[candidate_id]}` |"
        )
    if report_language == "zh":
        lines.extend([
            "", "## 解释与局限", "",
            f"- 本次运行中，Vina 分数越低，排序越靠前。{site_limitation}",
            "- Vina 分数是计算评分估计值，不是实验测得的结合亲和力或生物学活性。",
            "- `docking_scores.csv`、`primary_pose_manifest.csv`、`docking_components.csv` 和 `validation.json` 是本报告的机器可读权威数据。",
            "",
        ])
    else:
        lines.extend([
            "", "## Interpretation and limitations", "",
            f"- Lower Vina scores rank ahead of higher scores within this run. {site_limitation}",
            "- Vina scores are computational scoring estimates, not measured binding affinities or biological activity.",
            "- `docking_scores.csv`, `primary_pose_manifest.csv`, `docking_components.csv`, and `validation.json` are the machine-readable authorities for this report.",
            "",
        ])
    _atomic_text(path, "\n".join(lines))
