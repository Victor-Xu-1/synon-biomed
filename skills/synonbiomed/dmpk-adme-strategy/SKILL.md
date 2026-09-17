---
name: dmpk-adme-strategy
description: Plan and interpret drug metabolism, pharmacokinetics, ADME, bioanalysis, exposure-response, therapeutic drug monitoring, metabolite, transporter, and drug-interaction evidence. Use for compound progression, human PK projection, first-in-human support, dose selection or optimization, trough-concentration and exposure-toxicity analysis, TDM strategy, DDI strategy, or DMPK gap assessments.
keywords:
  - DMPK
  - ADME
  - pharmacokinetics
  - exposure response
  - exposure efficacy
  - exposure toxicity
  - AUC24/MIC
  - therapeutic drug monitoring
  - TDM
  - trough concentration
  - dose optimization
  - 临床药理
  - 药代动力学
  - 暴露疗效
  - 暴露毒性
  - 治疗药物监测
  - 治疗药物监测采样
  - 暴露疗效关系
  - 暴露毒性关系
  - 谷浓度
  - 剂量优化
  - 采样策略
  - 剂量调整策略
critical-constraints:
  - Treat search snippets as discovery evidence. Quantitative clinical claims require the exact retrieved source passage, structured authoritative record, label, or guideline that supports the nearby population, endpoint, unit, and estimate. 检索摘要只能用于发现来源，不能单独支撑临床定量结论。
  - Keep observed values, literature thresholds, fitted estimates, simulations, and illustrative assumptions separate. Never present a hand-shaped curve or an unfitted equation as a data-derived exposure-response model. 必须区分观察值、文献阈值、拟合结果、模拟和示意假设，不能把手工曲线写成数据模型。
  - Do not invent dose-adjustment percentages, therapeutic windows, sampling schedules, or special-population rules. Every numeric recommendation must map to one evidence-ledger row with an exact source locator and supporting excerpt; otherwise omit the number, explain how to establish it, and mark it unresolved. 不得凭记忆补出剂量百分比、治疗窗或采样频率。
  - Do not generalize a threshold across populations, indications, matrices, assays, formulations, or sampling definitions without evidence and an explicit uncertainty statement. 不得把某一人群或检测方法的阈值直接外推到其他场景。
  - Reconcile repeated numeric values, units, populations, and conclusions across the report, tables, figures, and calculation outputs before publication. 保存前逐项核对报告、数据表、图和计算结果中的数字、单位、人群与结论。
---

# DMPK and ADME Strategy

Build a question-driven evidence plan tied to clinical use.

## Frame

Record modality, route, intended dose and frequency, population, efficacy exposure target, therapeutic window, formulation, development stage, and decision to be supported. Confirm analytes, matrices, species, units, and sampling design.

## Evidence plan

1. Define absorption, distribution, clearance, metabolism, excretion, and bioavailability questions.
2. Select phase-appropriate in vitro and in vivo studies, including protein binding, permeability, metabolic stability, enzymes, transporters, metabolites, mass balance, and tissue distribution where relevant.
3. Define validated or qualified bioanalytical methods and sample integrity controls.
4. Integrate exposure, pharmacology, toxicology, and formulation data.
5. Evaluate DDI victim and perpetrator risks, intrinsic and extrinsic factors, special populations, and food effects as applicable.
6. Build transparent human PK projections with assumptions, uncertainty ranges, and model qualification.
7. Set progression criteria and specify what new evidence would change the decision.

## Exposure-response and therapeutic drug monitoring

For exposure-response, dose optimization, or TDM work, build a row-level evidence table before modelling. Preserve population, indication, regimen, formulation, analyte, matrix, sampling definition, assay, sample size, endpoint definition, estimate, uncertainty, source locator, and the exact supporting excerpt. A search-result snippet can identify a study and may support only the facts it explicitly contains; it is not a substitute for the full methods and result needed by a stronger claim.

Build the report from that evidence ledger, not from remembered clinical conventions. Before saving, perform one focused reconciliation pass: locate every numeric therapeutic window, sampling interval, dose change, toxicity boundary, and effect estimate in the ledger. If the exact number lacks a source locator and supporting excerpt, remove it from the recommendation, describe the decision method, and leave the value unresolved. This reasoning pass improves the answer but must not stop the task from completing with an honest evidence limitation.

Choose the analysis depth from the available data:

- With patient- or arm-level observations, fit an appropriate model, show data coverage, uncertainty, diagnostics, and sensitivity analyses.
- With only published thresholds or heterogeneous study summaries, synthesize them as ranges and evidence strata. Do not fabricate intermediate observations or fit a smooth efficacy or toxicity curve through invented points.
- A schematic may explain a concept, but label it as illustrative and keep it separate from empirical figures and quantitative recommendations.

Any numeric dose-adjustment rule, target window, sampling schedule, or special-population modification must identify its source and applicability. When evidence is insufficient, provide a decision algorithm and the local data or prospective study needed to set the number instead of supplying false precision.

## Deliver

Produce a DMPK matrix, data-quality findings, PK parameter table, model assumptions, translational risks, study sequence, claim-to-source table, and decision-ready recommendations. Keep observed, calculated, modelled, and illustrative values separate. Preserve substantial source and analysis tables as editable structured files. Figures must be generated from the saved analysis data or be explicitly labelled schematic; the report, tables, figures, and code must use the same values and units.

Do not extrapolate across species, formulations, matrices, or assays without justification. Do not present a model-derived dose as clinically acceptable without multidisciplinary review.

## Source authorities

Verify against current ICH multidisciplinary guidance, especially [M10 bioanalytical validation and M12 drug interactions](https://admin.ich.org/page/multidisciplinary-guidelines), [ICH Safety guidance](https://admin.ich.org/page/safety-guidelines), and current FDA exposure-response or clinical pharmacology guidance.
