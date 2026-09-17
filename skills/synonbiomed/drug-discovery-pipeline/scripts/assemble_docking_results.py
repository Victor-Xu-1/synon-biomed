from __future__ import annotations

import argparse
import csv
import hashlib
import json
import math
import re
from collections import Counter
from pathlib import Path


REQUIRED_PROPERTY_COLUMNS = (
    "candidate_id",
    "canonical_smiles",
    "molecular_weight",
    "clogp",
    "tpsa",
    "qed",
    "hbd",
    "hba",
    "rotatable_bonds",
    "lipinski_violations",
)
DOCKING_COLUMNS = (
    "ligand_id",
    "best_affinity_kcal_mol",
    "mode_count",
    "repeat_count",
    "poses_per_candidate",
    "primary_pose_run",
    "primary_pose_mode",
    "reference_centroid_distance_angstrom",
    "reference_axis_cosine",
    "primary_pose_selection",
    "receptor_sha256",
    "center_x",
    "center_y",
    "center_z",
    "size_x",
    "size_y",
    "size_z",
    "seed",
    "exhaustiveness",
    "num_modes",
    "rank",
)
PRIMARY_SELECTION_CONTRACT = (
    "minimum_affinity_then_reference_geometry_on_exact_ties_else_stable_run_mode"
)
PRIMARY_SELECTION_BASES = {
    "best_affinity",
    "best_affinity_then_reference_geometry_tiebreak",
    "best_affinity_then_stable_run_mode_tiebreak",
}


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def atomic_text(path: Path, text: str) -> None:
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(text, encoding="utf-8")
    temporary.replace(path)


def read_csv(path: Path, required: tuple[str, ...]) -> list[dict[str, str]]:
    with path.open(newline="", encoding="utf-8") as handle:
        reader = csv.DictReader(handle)
        missing = [column for column in required if column not in (reader.fieldnames or [])]
        if missing:
            raise ValueError(f"{path.name} is missing required columns: {missing}")
        rows = list(reader)
    if not rows:
        raise ValueError(f"{path.name} contains no data rows")
    return rows


def read_smiles_records(path: Path) -> dict[str, str]:
    lines = [
        line.strip()
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip() and not line.lstrip().startswith("#")
    ]
    if not lines:
        raise ValueError(f"{path.name} contains no molecule rows")
    first = re.split(r"[\t, ]+", lines[0])
    lowered = [value.strip().lower() for value in first]
    smiles_aliases = {"smiles", "canonical_smiles", "isomeric_smiles"}
    id_aliases = {"candidate_id", "ligand_id", "molecule_id", "id", "name"}
    header = any(value in smiles_aliases | id_aliases for value in lowered)
    if header:
        try:
            smiles_index = next(index for index, value in enumerate(lowered) if value in smiles_aliases)
            id_index = next(index for index, value in enumerate(lowered) if value in id_aliases)
        except StopIteration as error:
            raise ValueError(f"{path.name} header must identify both molecule ID and SMILES columns") from error
        data_lines = lines[1:]
    else:
        smiles_index, id_index, data_lines = 0, 1, lines
    if not data_lines:
        raise ValueError(f"{path.name} contains a header but no molecule rows")
    records: dict[str, str] = {}
    for line_number, line in enumerate(data_lines, start=2 if header else 1):
        values = re.split(r"[\t, ]+", line)
        if max(smiles_index, id_index) >= len(values):
            raise ValueError(f"{path.name} row {line_number} does not contain both molecule ID and SMILES")
        candidate_id = values[id_index].strip()
        smiles = values[smiles_index].strip()
        if not candidate_id or not smiles or candidate_id in records:
            raise ValueError(f"{path.name} row {line_number} has an empty or duplicate molecule identity")
        records[candidate_id] = smiles
    return records


def read_sdf_records(path: Path) -> dict[str, str]:
    raw = path.read_text(encoding="utf-8")
    blocks = [block for block in raw.split("$$$$") if block.strip()]
    if not blocks:
        raise ValueError(f"{path.name} contains no molecule records")
    records: dict[str, str] = {}
    property_pattern = re.compile(r"^>\s*<([^>]+)>")
    for record_index, block in enumerate(blocks, start=1):
        lines = block.lstrip("\r\n").splitlines()
        title = lines[0].strip() if lines else ""
        properties: dict[str, str] = {}
        for index, line in enumerate(lines):
            match = property_pattern.match(line.strip())
            if match is None:
                continue
            value = lines[index + 1].strip() if index + 1 < len(lines) else ""
            properties[match.group(1).strip().lower()] = value
        candidate_id = properties.get("candidate_id") or properties.get("ligand_id") or title
        smiles = properties.get("canonical_smiles") or properties.get("smiles") or ""
        if not candidate_id or not smiles or candidate_id in records:
            raise ValueError(f"{path.name} record {record_index} has an empty or duplicate molecule identity")
        records[candidate_id] = smiles
    return records


def validate_structure_set(
    properties: dict[str, dict[str, str]],
    sdf_records: dict[str, str],
    smiles_records: dict[str, str],
) -> None:
    expected_ids = set(properties)
    if set(sdf_records) != expected_ids or set(smiles_records) != expected_ids:
        raise ValueError(
            "candidate identity mismatch across property, SDF, and SMILES files: "
            f"properties={len(expected_ids)} sdf={len(sdf_records)} smiles={len(smiles_records)}"
        )
    for candidate_id, property_row in properties.items():
        canonical = property_row["canonical_smiles"].strip()
        if sdf_records[candidate_id] != canonical or smiles_records[candidate_id] != canonical:
            raise ValueError(f"canonical SMILES mismatch across companion files for {candidate_id}")


def finite_float(row: dict[str, str], column: str) -> float:
    value = float(row[column])
    if not math.isfinite(value):
        raise ValueError(f"non-finite {column} for {row.get('candidate_id') or row.get('ligand_id')}")
    return value


def optional_finite_float(row: dict[str, str], column: str) -> float | None:
    raw = str(row.get(column) or "").strip()
    if raw == "":
        return None
    value = float(raw)
    if not math.isfinite(value):
        raise ValueError(f"non-finite optional {column} for {row.get('candidate_id')}")
    return value


def load_passing_validation(path: Path) -> dict[str, object]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict) or value.get("overall_pass") is not True:
        raise ValueError(f"validation did not pass: {path.name}")
    return value


def docking_selection_evidence(validation: dict[str, object]) -> dict[str, object]:
    sampling = validation.get("sampling")
    complex_ensemble = validation.get("complex_ensemble")
    if not isinstance(sampling, dict) or not isinstance(complex_ensemble, dict):
        raise ValueError("docking validation is missing primary-pose selection evidence")
    sampling_contract = str(sampling.get("primary_selection") or "")
    complex_contract = str(complex_ensemble.get("selection_contract") or "")
    sampling_bases = sampling.get("observed_selection_bases")
    complex_bases = complex_ensemble.get("observed_selection_bases")
    if (
        sampling_contract != PRIMARY_SELECTION_CONTRACT
        or complex_contract != PRIMARY_SELECTION_CONTRACT
        or not isinstance(sampling_bases, list)
        or not isinstance(complex_bases, list)
    ):
        raise ValueError("docking validation primary-pose selection contract is inconsistent")
    observed = sorted({str(value) for value in sampling_bases})
    if (
        observed != sorted({str(value) for value in complex_bases})
        or not observed
        or any(value not in PRIMARY_SELECTION_BASES for value in observed)
    ):
        raise ValueError("docking validation primary-pose selection bases are inconsistent")
    return {
        "selection_contract": PRIMARY_SELECTION_CONTRACT,
        "observed_selection_bases": observed,
    }


def bounded_text(value: object, field: str) -> str:
    text = str(value or "").strip()
    if not text or len(text) > 256 or "\x00" in text:
        raise ValueError(f"generation provenance field is invalid: {field}")
    return text


def sha256_text(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()


def generation_provenance(validation: dict[str, object]) -> dict[str, str]:
    provenance = validation.get("provenance")
    if isinstance(provenance, dict):
        conditioning = provenance.get("conditioning")
        if not isinstance(conditioning, dict):
            raise ValueError("generation provenance conditioning is missing")
        result = {
            "generation_class": bounded_text(provenance.get("generation_class"), "generation_class"),
            "generation_engine": bounded_text(provenance.get("engine"), "engine"),
            "generation_provider": bounded_text(provenance.get("provider"), "provider"),
            "generation_version": bounded_text(provenance.get("engine_version"), "engine_version"),
            "conditioning_kind": bounded_text(conditioning.get("kind"), "conditioning.kind"),
            "conditioning_sha256": bounded_text(conditioning.get("input_sha256"), "conditioning.input_sha256"),
        }
        if len(result["conditioning_sha256"]) != 64 or any(
            character not in "0123456789abcdef" for character in result["conditioning_sha256"]
        ):
            raise ValueError("generation provenance conditioning digest is invalid")
        return result

    # The deterministic RDKit v2 validator predates the common provenance
    # block. Normalize it in one place so interrupted historical tasks can
    # resume without pretending that analog enumeration was model generation.
    if validation.get("schema") == "synon.rdkit-analog-generation.validation.v2":
        parent = validation.get("parent")
        if not isinstance(parent, dict):
            raise ValueError("legacy RDKit validation is missing its parent")
        canonical_smiles = bounded_text(parent.get("canonical_smiles"), "parent.canonical_smiles")
        return {
            "generation_class": "analog-enumeration",
            "generation_engine": "rdkit-analog-generator",
            "generation_provider": "local-managed-environment",
            "generation_version": bounded_text(validation.get("rdkit_version"), "rdkit_version"),
            "conditioning_kind": "parent-ligand",
            "conditioning_sha256": sha256_text(canonical_smiles),
        }
    raise ValueError("generation validation is missing the common provenance contract")


def markdown_table(rows: list[dict[str, object]], limit: int = 10) -> str:
    lines = [
        "| Rank | Candidate | Vina affinity (kcal/mol) | QED | MW | cLogP | TPSA | Lipinski violations |",
        "| ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |",
    ]
    for row in rows[:limit]:
        lines.append(
            "| {rank} | {candidate_id} | {best_affinity_kcal_mol:.3f} | {qed:.3f} | "
            "{molecular_weight:.1f} | {clogp:.2f} | {tpsa:.1f} | {lipinski_violations} |".format(**row)
        )
    return "\n".join(lines)


def scalar(mapping: object, key: str) -> object | None:
    return mapping.get(key) if isinstance(mapping, dict) else None


def render_project_report(
    *,
    language: str,
    target_label: str,
    structure_id: str,
    reference_ligand: str,
    rows: list[dict[str, object]],
    affinities: list[float],
    qeds: list[float],
    violations: Counter[int],
    generation_validation: dict[str, object],
    provenance: dict[str, str],
    docking_validation: dict[str, object],
    docking_method: dict[str, object],
    structures_sdf_name: str,
    structures_smiles_name: str,
) -> str:
    diversity = generation_validation.get("diversity")
    pairwise = scalar(diversity, "pairwise_tanimoto")
    scaffold_count = scalar(diversity, "scaffold_count")
    tanimoto_min = scalar(pairwise, "minimum")
    tanimoto_mean = scalar(pairwise, "mean")
    tanimoto_max = scalar(pairwise, "maximum")
    top_five = ", ".join(str(row["candidate_id"]) for row in rows[:5])
    property_balanced = sorted(
        rows[: min(15, len(rows))],
        key=lambda row: (
            int(row["lipinski_violations"]),
            -float(row["qed"]),
            float(row["best_affinity_kcal_mol"]),
        ),
    )[:5]
    property_balanced_ids = ", ".join(str(row["candidate_id"]) for row in property_balanced)
    reference_text = reference_ligand or "N/A"
    generation_route = (
        f"{provenance['generation_class']} / {provenance['generation_engine']} "
        f"({provenance['generation_provider']}, {provenance['generation_version']})"
    )
    diversity_available = all(
        value is not None for value in (scaffold_count, tanimoto_min, tanimoto_mean, tanimoto_max)
    )
    selection_bases = {str(value) for value in docking_method["observed_selection_bases"]}

    if language == "zh-CN":
        diversity_line = (
            f"{scaffold_count} 个 Murcko 骨架；两两 Tanimoto 相似性 "
            f"{float(tanimoto_min):.3f}–{float(tanimoto_max):.3f}（均值 {float(tanimoto_mean):.3f}）"
            if diversity_available
            else "生成校验未提供多样性统计。"
        )
        if "best_affinity_then_reference_geometry_tiebreak" in selection_bases:
            tiebreak_text = "分数并列候选使用原始配体空间中心与长轴方向作次级判据"
        elif "best_affinity_then_stable_run_mode_tiebreak" in selection_bases:
            tiebreak_text = "无参考配体的同分候选按稳定的运行与构象序号确定主构象"
        else:
            tiebreak_text = "各候选最低分构象均唯一，未启用次级判据"
        method_line = (
            f"对接中心=({docking_method['center_x']:.3f}, {docking_method['center_y']:.3f}, "
            f"{docking_method['center_z']:.3f}) Å；盒子=({docking_method['size_x']:.1f}, "
            f"{docking_method['size_y']:.1f}, {docking_method['size_z']:.1f}) Å；"
            f"起始随机种子={docking_method['seed']}；每个候选独立运行="
            f"{docking_method['repeat_count']} 次；搜索强度={docking_method['exhaustiveness']}；"
            f"每次搜索构象数={docking_method['num_modes']}；每个候选展示 1 个最佳分数主构象；"
            f"{tiebreak_text}。"
        )
        return (
            f"# {target_label} 结构导向候选评估报告\n\n"
            "## 决策摘要\n\n"
            f"本次评估以 {structure_id} 为受体结构、{reference_text} 为原始共晶配体，"
            f"完成 {len(rows)} 个候选的三维对接与性质联表。候选编号在生成、对接和排名三个阶段一一对应。"
            f"预测亲和力范围为 {min(affinities):.3f} 至 {max(affinities):.3f} kcal/mol，"
            f"均值为 {sum(affinities) / len(affinities):.3f} kcal/mol。\n\n"
            f"- 按 Vina 分数领先的 5 个候选：{top_five}。\n"
            f"- 在排名前 15 中按 Lipinski 违反数、QED、Vina 分数依次筛选的性质平衡候选："
            f"{property_balanced_ids}。该分组仅用于安排复核顺序，不是新的综合评分。\n"
            "- 建议先进行构象与关键相互作用复核，再进入合成可行性、体外活性和 ADME 验证；"
            "不应仅凭单一对接分数推进化合物。\n\n"
            "## 结构与计算依据\n\n"
            f"| 项目 | 记录值 |\n| --- | --- |\n"
            f"| 靶点 | {target_label} |\n| 受体结构 | {structure_id} |\n"
            f"| 原始共晶配体 | {reference_text} |\n| 候选数 | {len(rows)} |\n"
            f"| 分子生成路径 | {generation_route} |\n"
            f"| 生成条件 | {provenance['conditioning_kind']}，输入摘要 {provenance['conditioning_sha256']} |\n"
            f"| 对接参数 | {method_line} |\n"
            f"| 结构多样性 | {diversity_line} |\n"
            f"| 生成校验 | {generation_validation.get('schema', 'N/A')}，通过 |\n"
            f"| 对接校验 | {docking_validation.get('schema', 'N/A')}，通过 |\n\n"
            "## 候选组合概况\n\n"
            f"- QED：{min(qeds):.3f}–{max(qeds):.3f}，均值 {sum(qeds) / len(qeds):.3f}。\n"
            "- Lipinski 违反数分布："
            + "，".join(f"{value} 条={violations.get(value, 0)} 个" for value in range(5))
            + "。\n- 完整结构、SMILES、性质、对接分数和构象映射均保留在可编辑数据文件中。\n\n"
            "## 排名前 10 的候选\n\n"
            + markdown_table(rows)
            + "\n\n## 结果解读\n\n"
            "Vina 分数用于同一受体、同一参数体系内的相对排序；QED 和 Lipinski 指标反映的是不同维度的"
            "早期成药性提示。报告不把这些指标合并为不透明的总分。排名靠前但分子量、脂溶性或极性不理想的"
            "候选，应在保留关键结合模式的前提下优先做性质优化。\n\n"
            "## 建议的项目复核顺序\n\n"
            "1. 在三维查看器中逐个检查前 10 名候选与原始共晶配体的姿态、关键残基和空间冲突。\n"
            "2. 对优先候选复核质子化、互变异构、手性、受体构象和水分子处理，并使用正交方法复算。\n"
            "3. 进行合成可行性、反应性基团、聚集体和干扰化合物风险评估。\n"
            "4. 以直接结合或功能实验验证活性，同时安排溶解度、渗透性、代谢稳定性和选择性实验。\n\n"
            "## 可编辑交付文件\n\n"
            "- `candidate_ranking.csv`：完整候选排名、结构和性质字段。\n"
            f"- `{structures_sdf_name}` / `{structures_smiles_name}`：可供建模与药物化学软件继续编辑的结构集。\n"
            "- `docking_complex_ensemble.pdb`：固定受体、原始配体及每个候选唯一的最佳分数主构象；预览时逐个切换或双配体比较。\n"
            "- `docking_components.csv` / `docking_scores.csv` / `docking_pose_scores.csv`：主构象映射、候选排名及主构象分数。\n\n"
            "## 局限性\n\n"
            "本结果是计算优先级建议，不证明结合、效力、选择性、暴露、安全性或临床获益。"
            "对接函数、受体构象、质子化状态和采样范围都会影响排序，必须以实验结果为最终依据。\n"
        )

    diversity_line = (
        f"{scaffold_count} Murcko scaffolds; pairwise Tanimoto {float(tanimoto_min):.3f}–"
        f"{float(tanimoto_max):.3f} (mean {float(tanimoto_mean):.3f})"
        if diversity_available
        else "Diversity statistics were not supplied by the generation validation."
    )
    if "best_affinity_then_reference_geometry_tiebreak" in selection_bases:
        tiebreak_text = "reference-ligand centroid and long-axis geometry resolved exact score ties"
    elif "best_affinity_then_stable_run_mode_tiebreak" in selection_bases:
        tiebreak_text = "exact score ties without a reference ligand used stable run and mode ordering"
    else:
        tiebreak_text = "every selected minimum-affinity pose was unique, so no secondary criterion was used"
    method_line = (
        f"center=({docking_method['center_x']:.3f}, {docking_method['center_y']:.3f}, "
        f"{docking_method['center_z']:.3f}) Å; box=({docking_method['size_x']:.1f}, "
        f"{docking_method['size_y']:.1f}, {docking_method['size_z']:.1f}) Å; "
        f"initial seed={docking_method['seed']}; independent runs per candidate="
        f"{docking_method['repeat_count']}; exhaustiveness={docking_method['exhaustiveness']}; "
        f"searched modes per run={docking_method['num_modes']}; one best-scoring primary pose is displayed; "
        f"{tiebreak_text}."
    )
    return (
        f"# {target_label} structure-guided candidate assessment\n\n"
        "## Decision summary\n\n"
        f"This assessment used {structure_id} with reference ligand {reference_text} and joined "
        f"{len(rows)} candidates across generation, docking, and ranking with exact identifiers. "
        f"Predicted affinities span {min(affinities):.3f} to {max(affinities):.3f} kcal/mol "
        f"(mean {sum(affinities) / len(affinities):.3f}).\n\n"
        f"- Top five by Vina score: {top_five}.\n"
        f"- Property-balanced review set from the top 15: {property_balanced_ids}. This transparent "
        "ordering uses Lipinski violations, QED, then Vina score and is not a new composite score.\n"
        "- Review binding modes before synthesis and experimental ADME or potency decisions.\n\n"
        "## Structure and computation basis\n\n"
        "| Item | Recorded value |\n| --- | --- |\n"
        f"| Target | {target_label} |\n| Receptor structure | {structure_id} |\n"
        f"| Reference ligand | {reference_text} |\n| Candidate count | {len(rows)} |\n"
        f"| Molecular generation route | {generation_route} |\n"
        f"| Generation conditioning | {provenance['conditioning_kind']}, input digest {provenance['conditioning_sha256']} |\n"
        f"| Docking parameters | {method_line} |\n| Structural diversity | {diversity_line} |\n"
        f"| Generation validation | {generation_validation.get('schema', 'N/A')}, passed |\n"
        f"| Docking validation | {docking_validation.get('schema', 'N/A')}, passed |\n\n"
        "## Portfolio profile\n\n"
        f"- QED range {min(qeds):.3f}–{max(qeds):.3f}; mean {sum(qeds) / len(qeds):.3f}.\n"
        "- Lipinski violation distribution: "
        + ", ".join(f"{value}={violations.get(value, 0)}" for value in range(5))
        + ".\n- Editable structures, descriptors, scores, and component mappings are retained in the data package.\n\n"
        "## Top 10 candidates\n\n"
        + markdown_table(rows)
        + "\n\n## Interpretation\n\n"
        "Vina scores provide relative prioritization within this receptor and parameter set. QED and Lipinski "
        "metrics describe different developability dimensions and are not collapsed into an opaque total score.\n\n"
        "## Recommended review sequence\n\n"
        "1. Inspect the top 10 poses against the reference ligand, key residues, and steric conflicts.\n"
        "2. Re-evaluate protonation, tautomerism, stereochemistry, receptor state, and structural waters.\n"
        "3. Assess synthetic accessibility, reactive groups, aggregation, and assay-interference risk.\n"
        "4. Confirm with orthogonal computation and direct binding or functional assays, then profile ADME and selectivity.\n\n"
        "## Editable deliverables\n\n"
        "- `candidate_ranking.csv`: complete ranking, structures, and descriptors.\n"
        f"- `{structures_sdf_name}` / `{structures_smiles_name}`: editable molecular structures.\n"
        "- `docking_complex_ensemble.pdb`: fixed receptor, reference ligand, and one best-scoring primary pose per candidate.\n"
        "- `docking_components.csv` / `docking_scores.csv` / `docking_pose_scores.csv`: primary-pose mapping, candidate ranking, and primary-pose scores.\n\n"
        "## Limitations\n\n"
        "These predictions do not establish binding, potency, selectivity, exposure, safety, or clinical efficacy. "
        "Scoring, receptor conformation, protonation, and sampling can change the rank order; experiments remain decisive.\n"
    )


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Join validated molecular-generation and docking outputs without guessing candidate identities."
    )
    parser.add_argument("--properties", type=Path, required=True)
    parser.add_argument("--structures-sdf", type=Path, required=True)
    parser.add_argument("--structures-smiles", type=Path, required=True)
    parser.add_argument("--docking", type=Path, required=True)
    parser.add_argument("--generation-validation", type=Path, required=True)
    parser.add_argument("--docking-validation", type=Path, required=True)
    parser.add_argument("--target-label", required=True)
    parser.add_argument("--structure-id", required=True)
    parser.add_argument("--reference-ligand", default="")
    parser.add_argument("--report-language", choices=("zh-CN", "en-US"), default="en-US")
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()

    properties = read_csv(args.properties, REQUIRED_PROPERTY_COLUMNS)
    docking = read_csv(args.docking, DOCKING_COLUMNS)
    generation_validation = load_passing_validation(args.generation_validation)
    provenance = generation_provenance(generation_validation)
    docking_validation = load_passing_validation(args.docking_validation)
    property_by_id = {row["candidate_id"].strip(): row for row in properties}
    if "" in property_by_id or len(property_by_id) != len(properties):
        raise ValueError("property table candidate IDs are empty or duplicated")
    sdf_by_id = read_sdf_records(args.structures_sdf)
    smiles_by_id = read_smiles_records(args.structures_smiles)
    validate_structure_set(property_by_id, sdf_by_id, smiles_by_id)
    docking_by_id = {row["ligand_id"].strip(): row for row in docking}
    if "" in docking_by_id or len(docking_by_id) != len(docking):
        raise ValueError("docking table ligand IDs are empty or duplicated")
    if property_by_id.keys() != docking_by_id.keys():
        missing_docking = sorted(property_by_id.keys() - docking_by_id.keys())
        missing_properties = sorted(docking_by_id.keys() - property_by_id.keys())
        raise ValueError(
            "candidate identity mismatch between generation and docking: "
            f"missing_docking={missing_docking[:10]} missing_properties={missing_properties[:10]}"
        )

    joined: list[dict[str, object]] = []
    for candidate_id, property_row in property_by_id.items():
        docking_row = docking_by_id[candidate_id]
        repeat_count = int(docking_row["repeat_count"])
        num_modes = int(docking_row["num_modes"])
        primary_pose_run = int(docking_row["primary_pose_run"])
        primary_pose_mode = int(docking_row["primary_pose_mode"])
        primary_pose_selection = str(docking_row["primary_pose_selection"]).strip()
        reference_centroid_distance = optional_finite_float(
            docking_row, "reference_centroid_distance_angstrom"
        )
        reference_axis_cosine = optional_finite_float(docking_row, "reference_axis_cosine")
        mode_count = int(docking_row["mode_count"])
        if repeat_count < 1 or not 1 <= primary_pose_run <= repeat_count:
            raise ValueError(f"primary pose run is outside the sampled range for {candidate_id}")
        if num_modes < 1 or not 1 <= primary_pose_mode <= num_modes:
            raise ValueError(f"primary pose mode is outside the sampled range for {candidate_id}")
        if mode_count < repeat_count:
            raise ValueError(f"sampled pose count is smaller than repeat count for {candidate_id}")
        if reference_centroid_distance is not None and reference_centroid_distance < 0:
            raise ValueError(f"reference centroid distance is negative for {candidate_id}")
        if reference_axis_cosine is not None and not 0 <= reference_axis_cosine <= 1:
            raise ValueError(f"reference axis cosine is outside [0,1] for {candidate_id}")
        if primary_pose_selection not in {
            "best_affinity",
            "best_affinity_then_reference_geometry_tiebreak",
            "best_affinity_then_stable_run_mode_tiebreak",
        }:
            raise ValueError(f"primary pose selection contract is invalid for {candidate_id}")
        joined.append(
            {
                "rank": int(docking_row["rank"]),
                "candidate_id": candidate_id,
                "canonical_smiles": property_row["canonical_smiles"],
                **provenance,
                "best_affinity_kcal_mol": finite_float(docking_row, "best_affinity_kcal_mol"),
                "mode_count": mode_count,
                "repeat_count": repeat_count,
                "poses_per_candidate": int(docking_row["poses_per_candidate"]),
                "primary_pose_run": primary_pose_run,
                "primary_pose_mode": primary_pose_mode,
                "reference_centroid_distance_angstrom": reference_centroid_distance,
                "reference_axis_cosine": reference_axis_cosine,
                "primary_pose_selection": primary_pose_selection,
                "molecular_weight": finite_float(property_row, "molecular_weight"),
                "clogp": finite_float(property_row, "clogp"),
                "tpsa": finite_float(property_row, "tpsa"),
                "qed": finite_float(property_row, "qed"),
                "hbd": int(property_row["hbd"]),
                "hba": int(property_row["hba"]),
                "rotatable_bonds": int(property_row["rotatable_bonds"]),
                "lipinski_violations": int(property_row["lipinski_violations"]),
                "parent_tanimoto": optional_finite_float(property_row, "parent_tanimoto"),
                "murcko_scaffold": str(property_row.get("murcko_scaffold") or "").strip(),
                "generator_score": optional_finite_float(property_row, "generator_score"),
                "generator_score_kind": str(property_row.get("generator_score_kind") or "").strip(),
            }
        )
    joined.sort(key=lambda row: (int(row["rank"]), str(row["candidate_id"])))
    if [int(row["rank"]) for row in joined] != list(range(1, len(joined) + 1)):
        raise ValueError("docking ranks are not a complete contiguous sequence")
    selection_evidence = docking_selection_evidence(docking_validation)
    if sorted({str(row["primary_pose_selection"]) for row in joined}) != selection_evidence[
        "observed_selection_bases"
    ]:
        raise ValueError("docking rows disagree with validation primary-pose selection evidence")

    args.output_dir.mkdir(parents=True, exist_ok=True)
    table_path = args.output_dir / "candidate_ranking.csv"
    summary_path = args.output_dir / "results_summary.json"
    report_path = args.output_dir / "project_report.md"
    with table_path.open("w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=list(joined[0]))
        writer.writeheader()
        writer.writerows(joined)

    affinities = [float(row["best_affinity_kcal_mol"]) for row in joined]
    qeds = [float(row["qed"]) for row in joined]
    violations = Counter(int(row["lipinski_violations"]) for row in joined)
    first_docking_row = docking[0]
    docking_method = {
        "receptor_sha256": first_docking_row["receptor_sha256"],
        "center_x": finite_float(first_docking_row, "center_x"),
        "center_y": finite_float(first_docking_row, "center_y"),
        "center_z": finite_float(first_docking_row, "center_z"),
        "size_x": finite_float(first_docking_row, "size_x"),
        "size_y": finite_float(first_docking_row, "size_y"),
        "size_z": finite_float(first_docking_row, "size_z"),
        "seed": int(first_docking_row["seed"]),
        "repeat_count": int(first_docking_row["repeat_count"]),
        "exhaustiveness": int(first_docking_row["exhaustiveness"]),
        "num_modes": int(first_docking_row["num_modes"]),
        "poses_per_candidate": int(first_docking_row["poses_per_candidate"]),
        **selection_evidence,
    }
    if docking_method["poses_per_candidate"] != 1:
        raise ValueError("exactly one primary pose is required for every docking candidate")
    if docking_method["repeat_count"] < 1:
        raise ValueError("repeat_count must be positive")
    if any(int(row["poses_per_candidate"]) != docking_method["poses_per_candidate"] for row in docking):
        raise ValueError("poses_per_candidate must be consistent across docking rows")
    if any(int(row["repeat_count"]) != docking_method["repeat_count"] for row in docking):
        raise ValueError("repeat_count must be consistent across docking rows")
    summary = {
        "schema": "synon.structure-guided-candidate-summary.v4",
        "overall_pass": True,
        "target_label": args.target_label,
        "structure_id": args.structure_id,
        "reference_ligand": args.reference_ligand or None,
        "candidate_count": len(joined),
        "candidate_id_integrity": True,
        "structure_set_integrity": {
            "sdf_count": len(sdf_by_id),
            "smiles_count": len(smiles_by_id),
            "canonical_smiles_match": True,
        },
        "generation_provenance": provenance,
        "docking": {
            "best_affinity_kcal_mol": min(affinities),
            "mean_affinity_kcal_mol": round(sum(affinities) / len(affinities), 6),
            "worst_affinity_kcal_mol": max(affinities),
            "method": docking_method,
        },
        "developability": {
            "qed_minimum": min(qeds),
            "qed_mean": round(sum(qeds) / len(qeds), 6),
            "qed_maximum": max(qeds),
            "lipinski_violations": {str(value): violations.get(value, 0) for value in range(5)},
        },
        "top_candidates": joined[:10],
        "source_validation_schemas": {
            "generation": generation_validation.get("schema"),
            "docking": docking_validation.get("schema"),
        },
        "source_sha256": {
            "properties": sha256_file(args.properties),
            "structures_sdf": sha256_file(args.structures_sdf),
            "structures_smiles": sha256_file(args.structures_smiles),
            "docking": sha256_file(args.docking),
            "generation_validation": sha256_file(args.generation_validation),
            "docking_validation": sha256_file(args.docking_validation),
        },
    }
    atomic_text(summary_path, json.dumps(summary, indent=2, ensure_ascii=False, sort_keys=True) + "\n")
    report = render_project_report(
        language=args.report_language,
        target_label=args.target_label,
        structure_id=args.structure_id,
        reference_ligand=args.reference_ligand,
        rows=joined,
        affinities=affinities,
        qeds=qeds,
        violations=violations,
        generation_validation=generation_validation,
        provenance=provenance,
        docking_validation=docking_validation,
        docking_method=docking_method,
        structures_sdf_name=args.structures_sdf.name,
        structures_smiles_name=args.structures_smiles.name,
    )
    atomic_text(report_path, report)
    print(json.dumps({"status": "passed", "candidate_count": len(joined), "output_dir": str(args.output_dir)}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
