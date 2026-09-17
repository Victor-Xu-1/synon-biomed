---
name: stability-shelf-life
description: Design, analyze, and audit drug-substance and drug-product stability programs, including storage conditions, time points, stability-indicating methods, trends, excursions, retest periods, and shelf-life proposals. Use for ICH stability protocols, expiry dating, comparability, packaging changes, or stability data review.
allowed-tools: search_skills, skill, repl, web_search, fetch_article_fulltext, bash, read_file, edit_file, save_artifacts
preferred-execution-assets:
  - scripts/stability_analysis.py
---

# Stability and Shelf Life

Base shelf-life conclusions on product-specific data and a justified statistical approach.

## Frame

Confirm product type, strength, formulation, manufacturing process, container closure, markets, climatic zones, storage and shipping conditions, development phase, and proposed retest period or shelf life.

## Workflow

1. Select applicable ICH stability guidance, including special rules for biologics, photostability, in-use, reconstituted, frozen, or accelerated conditions.
2. Define batches, scales, sites, packaging configurations, bracketing or matrixing rationale, conditions, time points, tests, specifications, and pulls.
3. Confirm methods are stability-indicating and suitable for intended decisions.
4. Review results for OOS/OOT signals, degradation pathways, mass balance, physical change, container interactions, and batch variability.
5. Trend quantitative attributes with transparent models and diagnostics; do not extrapolate beyond justified limits.
6. Assess excursions, transport, in-use periods, ongoing stability, commitments, and change impact.

## Quantitative decision rules

- A formal shelf life intended to apply to future batches is generally supported by at least three primary batches. Treat one-batch or development-batch results as descriptive and planning evidence; do not present them as a registration-supporting expiry for future batches.
- Evaluate every stability-indicating attribute separately and let the shortest justified estimate govern. Assay and total degradation products alone do not establish product shelf life when dissolution, individual impurities, water, physical attributes, microbiology, or container performance are missing.
- When change or variability makes regression useful, estimate the earliest intersection of the applicable confidence bound with the acceptance criterion: upper one-sided 95% for an increasing attribute and lower one-sided 95% for a decreasing attribute. A regression-line point estimate or R-squared value alone is not a shelf-life estimate.
- State and check model assumptions, residuals, uncertainty, censoring, analytical variability, and the small-sample limitation. Do not infer batch-to-batch variability or poolability from one batch.
- Apply the ICH Q1E decision tree before extrapolation. Record whether significant change occurred under accelerated or intermediate conditions, whether relevant supporting data exist, and which extrapolation branch applies. Never apply a numeric extrapolation allowance without satisfying that branch's prerequisites.
- Run regression, confidence-limit, unit, and intersection calculations in an admitted calculation environment and preserve measured data separately from derived results.

## Canonical analysis asset

For tabular stability data, use the tested `${SYNON_SKILL_DIR}/scripts/stability_analysis.py` path instead of authoring a second regression implementation:

1. Run `python "${SYNON_SKILL_DIR}/scripts/stability_analysis.py" --help` through Bash.
2. Create one task-local CSV with columns `batch_id,condition_role,condition,time_month,attribute,value,direction,lower_limit,upper_limit`. Use `long_term`, `accelerated`, or `intermediate` for `condition_role`; use `increase`, `decrease`, or `either` for `direction`; leave a non-applicable limit blank.
3. Run the script with `--input`, `--results`, `--summary`, `--report`, and `--language zh|en`. It uses only the Python standard library, preserves measured rows separately, calculates the applicable one-sided confidence-bound intersection, emits an explicit batch-sufficiency conclusion, and writes the decision-safe report core in the conversation language.
4. Read all three outputs. Publish the generated report core; additions may explain sources, product context, or missing work but must not replace or contradict its supported-conclusion section. The script's `formal_shelf_life_supported=false` or `development_only_insufficient_batches` result is authoritative for the supplied evidence. Regulatory or clinical context may require additional judgment, but cannot turn missing batches or attributes into evidence. Save the machine-readable summary JSON as internal working data, not as a user-facing snapshot; the readable report and useful CSV tables are the user deliverables.

## Deliver

Provide a protocol table, data-quality assessment, measured-data table, trend plots or model outputs with confidence limits, proposed retest or shelf-life rationale, unresolved risks, and required ongoing commitments. Preserve measured data and statistical results as usable structured files alongside the readable report.

Classify outputs by audience when saving them. Save the readable report and user-useful tabular inputs/results as `snapshot` artifacts. Save machine-oriented summaries, manifests, checkpoints, and control files as `working_data`; they support reproducibility but must not appear in the user-facing artifact tray or final delivery list. Classification follows the file's semantic role, not its extension. In particular, the bundled analysis script's `--summary` output must use a `.json` filename and must always be saved as `working_data`; never rename it to CSV or expose it as a snapshot. Do not expose internal scripts, runtime files, or execution metadata as deliverables.

Before delivery, read back every saved output and verify that plots, tables, confidence limits, limiting attributes, batch counts, time points, specifications, and narrative conclusions agree. Use the exact successful saved-file references in the final answer; never create empty or `#` links.

Do not assign expiry from accelerated data alone without an applicable scientific and regulatory basis. Preserve raw results, units, censoring, exclusions, and investigation status.

Complete the protocol-defined accelerated interval (conventionally 6 months for the standard drug-product study). Do not recommend extending accelerated storage beyond that interval merely because a fitted trend approaches a limit. If the product-specific significant-change definition is met, apply the appropriate ICH decision branch and add intermediate-condition data when required while continuing long-term monitoring.

## Source authorities

Use the current [ICH Q1 stability family](https://admin.ich.org/page/quality-guidelines), especially Q1E for statistical evaluation and extrapolation, Q5C for biotechnology products where applicable, and current regional authority guidance.
