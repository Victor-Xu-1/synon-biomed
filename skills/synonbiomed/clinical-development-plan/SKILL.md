---
name: clinical-development-plan
description: Build and audit a clinical development plan linking target product profile, clinical pharmacology, phase strategy, populations, endpoints, evidence generation, safety, statistics, regulatory interactions, and decision gates. Use for asset development plans, indication expansion, first-in-human through confirmatory strategy, or clinical evidence gap assessment.
keywords: [clinical-pharmacology, translational-pharmacology, first-in-human, starting-dose, PK/PD, biomarker, dose-escalation, 临床药理, 转化药理, 首次人体, 起始剂量, 生物标志物, 剂量递增]
---

# Clinical Development Plan

Design a sequence of decisions, not a list of trials.

## Evidence and quantitative quality contract

1. Separate candidate-specific facts from class precedent and planning
   assumptions. Do not assign a numerical starting dose, exposure target,
   selectivity threshold, species ranking, or escalation ceiling when the
   required candidate potency, binding, PK, bioavailability, protein binding,
   metabolite, safety pharmacology, toxicokinetic, NOAEL/HNSTD, AUC, and Cmax
   inputs are absent. State the missing input and provide a calculation-ready
   decision procedure instead.
2. Use current regulator guidance and primary study records as authorities.
   Keep the exact source URL or identifier next to each externally sourced
   quantitative precedent. A search-result snippet is discovery evidence, not
   sufficient authority for a numeric recommendation.
3. Select toxicology and pharmacology species by demonstrated pharmacologic
   relevance: binding/functional activity, target distribution, metabolite
   coverage, and achievable exposure. Sequence homology alone cannot establish
   relevance, and unverified homology percentages must not be reported.
4. Preserve units through every dose calculation. For body-surface-area
   conversion, keep the FDA convention explicit, for example
   `HED (mg/kg) = animal dose (mg/kg) * animal Km / human Km`, then convert to a
   total dose only after choosing and stating body weight. Do not mix mg/kg,
   mg/m2, and mg/day in one equation. Calculate each independent route (for
   example NOAEL/HNSTD exposure margin and MABEL) from its own inputs and select
   the justified conservative route; there is no universal receptor-occupancy
   MABEL formula.
5. Match the design to population and risk. Healthy-volunteer SAD/MAD commonly
   uses sentinel dosing, placebo-controlled cohorts, staggered review, and
   protocol-specific stopping criteria; do not import oncology 3+3 or DLT
   language unless the actual indication and risk justify it.
6. Before publishing, read the report and every quantitative table back from
   disk. Independently recompute displayed dose conversions, exposure margins,
   percentages, and cohort totals; verify that assumptions, units, citations,
   decision gates, and repeated values agree. When a quantitative starting-dose
   example is requested or supported by real inputs, publish a calculation
   table alongside the narrative report.
7. When candidate-specific quantitative inputs are absent, do not create a
   hypothetical numerical example, a recommended dose or dose range, or a
   numbered escalation schedule. Deliver a calculation-ready worksheet that
   names each required input, unit, source, equation, uncertainty, and decision
   rule, and leave the result explicitly undetermined. Proposed assay thresholds
   must be labeled as target-product criteria requiring project approval, not as
   universal acceptance standards.
8. Execute every displayed dose, exposure, unit-conversion, percentage, cohort,
   and escalation calculation with an admitted calculation tool; do not perform
   arithmetic only in prose. Recompute it independently before delivery. A
   safety factor reduces an allowed dose or exposure; never multiply a proposed
   starting dose upward by a safety factor. If an equation cannot be dimensionally
   reconciled, omit the numerical result and record the unresolved input.
9. Do not recommend a specific toxicology species or report cross-species
   homology percentages until candidate-specific binding/functional activity,
   metabolite coverage, and achievable exposure have been measured or sourced.
   Present a staged species-selection matrix and conditional decision instead.
10. Say a strategy is "aligned with the cited elements" of guidance, not that it
    "complies" with guidance, unless every applicable requirement has actually
    been assessed. Keep class precedents separate from candidate decisions.

## Frame

Confirm indication, target population, mechanism, modality, route, dose concept, nonclinical and CMC readiness, competitive standard of care, unmet need, regions, and desired label. Create measurable target product profile claims.

## Workflow

1. Define the clinical questions that must be answered for dose, benefit, risk, population, and use.
2. Map clinical pharmacology, first-in-human, proof-of-concept, dose-ranging, confirmatory, special-population, interaction, and long-term evidence.
3. Justify populations, controls, endpoints, estimands, duration, biomarkers, and patient-reported outcomes.
4. Align safety monitoring and exposure with nonclinical findings and known class risks.
5. Define decision criteria, probability-changing evidence, adaptive or master-protocol options, and stopping rules.
6. Address diversity, regional applicability, pediatric or geriatric plans, operational feasibility, supply, and data standards.
7. Synchronize regulatory interactions, CMC changes, statistical plans, and submission timing.

## Deliver

Produce a clinical evidence map, study-sequence table, endpoint and estimand strategy, dose rationale, stage gates, risk register, authority interaction plan, and key assumptions.

Do not invent precedent, endpoint acceptability, effect size, or recruitment rates. Retrieve comparable trials and current guidance before recommending a pivotal design.

## Source authorities

Use current [ICH Efficacy guidance](https://admin.ich.org/page/efficacy-guidelines), especially E6, E8, E9, E10, E17, and E20 as applicable, plus the FDA [clinical research overview](https://www.fda.gov/patients/drug-development-process/step-3-clinical-research).
