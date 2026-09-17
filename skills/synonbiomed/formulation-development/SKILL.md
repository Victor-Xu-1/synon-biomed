---
name: formulation-development
description: Plan phase-appropriate pharmaceutical formulation and dosage-form development from preformulation through clinical and commercial product design. Use for route and dosage-form selection, excipient compatibility, solubility or bioavailability problems, QTPP/CQA definition, prototype screening, or formulation risk assessment.
keywords:
  - formulation development
  - dosage form development
  - preformulation
  - solid form screening
  - salt screening
  - polymorph screening
  - first-in-human formulation
  - 制剂开发
  - 处方开发
  - 预处方
  - 固态筛选
  - 盐型筛选
  - 晶型筛选
  - 增溶
  - 增溶策略
  - 难溶性弱碱性口服小分子
  - 首个人体试验制剂
  - 难溶性口服小分子
tags:
  - pharmaceutics
  - formulation
  - solid-state
critical-constraints:
  - Treat water solubility reported without measurement pH, solid phase, ionic strength, and equilibrium conditions as an apparent value; do not use it as intrinsic solubility or construct a Henderson-Hasselbalch pH-solubility curve.
  - For a monoprotic weak base, the ideal free-form relationship is S = S0 × (1 + 10^(pKa - pH)); for a monoprotic weak acid it is S = S0 × (1 + 10^(pH - pKa)). Confirm the expected direction before using either relationship.
  - Do not infer permeability or BCS class from logP alone; keep BCS class unresolved until suitable permeability, mass-balance, or human absorption evidence exists.
  - Do not invent numeric acceptance criteria, risk probabilities, excipient levels, process ranges, shelf life, dissolution limits, storage conditions, or clinical bridging thresholds; state the method for setting an unknown value.
  - Use pKa only to design a salt-feasibility screen; do not rank or select a counterion before comparative form, solubility, stability, safety, and manufacturability data exist.
  - Keep salt, cocrystal, amorphous dispersion, lipid, particle-size reduction, complexation, and conventional crystalline routes provisional until named discriminating experiments support advancement or elimination.
  - Keep first-in-human strengths, unit counts, packaging, storage, and dosing conditions undecided until nonclinical, stability, manufacturability, and clinical-use evidence supports them.
  - Before publishing any calculation-derived decision, independently verify the governing equation, units, sign and direction, limiting or boundary behavior, and at least one representative value; reconcile machine-readable outputs with the narrative or leave the value unresolved.
---

# Formulation Development

Design the product around patient use, clinical exposure, quality, and manufacturability.

## Inputs

Establish the quality target product profile: indication, population, route, dose, strength, release profile, onset, shelf life, storage, device or container, administration setting, and commercial constraints. Gather drug-substance form, solubility, permeability, pKa, logP/logD, polymorphism, particle size, hygroscopicity, stability, and compatibility data.

Separate every input into measured, literature-supported, calculated, assumed, or missing. Never turn a missing salt screen, solid-state result, permeability result, dose-exposure relationship, or stability result into an apparent fact. If only sparse physicochemical descriptors are supplied, use them to prioritize experiments and alternatives, not to declare bioavailability, precipitation, metabolism, food effect, or clinical performance.

Do not infer BCS permeability or class from lipophilicity alone. A dose number or solubility calculation can identify a dissolution burden, but high permeability requires suitable permeability, mass-balance, or human absorption evidence. Until then, describe the BCS class as unresolved and carry both permeability cases through the decision.

Treat a supplied value described only as "water solubility" as an apparent value at an unspecified pH and solid form unless the input says otherwise. Do not use it as intrinsic free-form solubility in a Henderson-Hasselbalch pH-solubility curve. If intrinsic solubility, measurement pH, solid phase, ionic strength, and equilibrium conditions are missing, report the pH profile as not calculable and design the required experiment. Reject any idealized estimate that exceeds a physically plausible concentration or ignores solid-phase and activity limits.

For a monoprotic weak base, the ideal free-form relationship is S = S0 × (1 + 10^(pKa - pH)), so ideal ionization raises apparent solubility as pH decreases. For a monoprotic weak acid, the corresponding ideal relationship is S = S0 × (1 + 10^(pH - pKa)), so ideal ionization raises apparent solubility as pH increases. These equations are orientation checks, not substitutes for measured intrinsic solubility, solid-phase identity, activity corrections, salt disproportionation, precipitation, or nonideal solution behavior.

## Workflow

1. Identify formulation-critical material attributes and product CQAs.
2. Compare feasible dosage forms and enabling technologies using explicit decision criteria.
3. Assess excipient function, compatibility, safety, compendial status, supply, and concentration justification.
4. Design prototype and stress studies that isolate material, process, packaging, and storage effects.
5. Link dissolution or release performance to exposure and clinical use where possible.
6. Define phase-appropriate specifications, manufacturing process, container closure, microbial controls, and stability plan.
7. Plan formulation bridging after major changes in strength, site, process, or dosage form.

Keep the development path gated rather than prematurely selecting one technology:

- Salt and cocrystal options require experimental confirmation of form identity, stoichiometry, crystallinity, hygroscopicity, aqueous and biorelevant solubility, disproportionation, chemical and physical stability, manufacturability, counterion safety, and regulatory acceptability. A pKa difference is a screen-design input, not a selection result.
- Do not rank counterions by assumed safety, solubility improvement, cost, or pharmacopeial status unless those values are traced to suitable sources. Use pKa only to define a feasibility screen; present the experimental candidate set without calling a winner before comparative data exist.
- Amorphous dispersions, lipid systems, particle-size reduction, complexation, and conventional crystalline formulations remain competing options until discriminating data are available. For each option, state the minimum experiment that would advance or eliminate it.
- Thermal process ranges are hypotheses until melting point, glass transition, degradation, polymer miscibility, residence-time, torque, and residual-solvent data support them. Label proposed screening windows as such; do not present them as established controls.
- Unit size and container fit require composition mass, bulk or tapped density, manufacturability, and patient-use evidence. Do not claim that a capsule or tablet format fits from mass alone.
- Storage, packaging, food instructions, and shelf life require stability or clinical evidence. Until generated, provide the study that will establish them and keep the recommendation provisional.

For any comparison:

- Keep route, dosage form, formulation technology, and manufacturing process as distinct decisions.
- State the mechanism, required evidence, principal failure mode, development burden, and fallback trigger for each serious option.
- Use candidate-specific evidence where available. Class or platform evidence may motivate a test, but cannot establish candidate performance.
- Compute mass balance, dose/solubility ratios, concentrations, unit conversions, and ranked scores with an admitted calculation environment; independently check units and directionality before using them.
- Do not invent numeric acceptance criteria, excipient levels, process ranges, shelf life, dissolution limits, or clinical bridging thresholds. When inputs are absent, provide a justified method for setting them and mark the value undetermined.
- Treat regulatory guidance as a framework, not evidence that a proposed product is compliant. Cite the exact public source and distinguish a recommendation from a requirement.

## First-in-human readiness

Design the early clinical presentation for dose flexibility. Proposed strengths, unit counts, dose-escalation increments, and maximum administered mass must trace to the nonclinical starting-dose rationale, anticipated escalation, exposure uncertainty, swallowability, and manufacturing capability. Do not equate the projected therapeutic dose with the first administered dose or assume that one fixed-strength unit is sufficient.

Separate a practical clinical-trial prototype from the eventual commercial formulation. Define what must be bridged if formulation, strength, process, site, or release behavior changes. Any food condition, dosing instruction, or special population recommendation remains undecided until supported by appropriate data.

## Deliver

Produce a formulation decision matrix, prototype plan, risk register, QTPP-to-CQA traceability table, development study plan, evidence gaps, and a claim-to-source table. Preserve substantial decision tables as usable structured files alongside a readable report. Include a decision flow or other appropriate figure when the work contains sequential gates. Every recommended path must have a traceable rationale, discriminating experiment, success criterion-setting method, and fallback trigger.

In the report and tables, visibly distinguish:

- supplied or measured facts;
- literature or guidance statements with exact citations;
- calculated values with equations, units, and assumptions;
- proposed screening conditions;
- decisions that can be made now; and
- decisions deferred until named evidence is available.

Before the final answer, read back every saved output once. Confirm that tables agree with the narrative, units and calculations are internally consistent, cited sources support the nearby claims, filenames and links resolve, and no recommendation depends on an unstated candidate fact. Correct issues before presenting the final result; do not publish a result and then start a separate automatic review turn.

Do not select a formulation solely from predicted properties. Flag patent, excipient, device, biocompatibility, and patient-use questions for specialist review.

## Source authorities

Use [ICH Q8 Pharmaceutical Development and related quality guidance](https://admin.ich.org/page/quality-guidelines) and the current [M4Q quality dossier structure](https://www.fda.gov/media/190641/download) as the evidence frame.
