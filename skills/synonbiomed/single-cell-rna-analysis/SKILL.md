---
name: single-cell-rna-analysis
description: Perform a complete, lightweight single-cell RNA-seq analysis with Scanpy and Harmony: dataset selection, quality control, normalization, batch-aware embedding, clustering, cell-population annotation, marker genes, treatment-associated population shifts, pseudobulk comparisons, response signatures, visualizations, and review-ready artifacts. Use this as the default route for ordinary scRNA-seq analysis. Do not select scvi-tools unless the task explicitly needs a probabilistic latent model, semi-supervised label transfer, or Bayesian differential expression from verified raw integer UMI counts.
allowed-tools: search_skills, skill, download_public_scientific_file, manage_environments, manage_packages, python, bash, read_file, edit_file, save_artifacts
required-environment-packages:
  - scanpy
  - anndata
  - harmonypy
  - leidenalg
  - igraph
  - matplotlib
  - seaborn
  - pandas
  - scipy
preferred-execution-assets:
  - scripts/analysis_pipeline.py
  - scripts/annotation_pipeline.py
  - scripts/qc_analysis.py
---

# Single-Cell RNA-seq Analysis

Use the smallest sufficient scverse workflow. Preserve source data, make every
filtering and modeling decision reviewable, and separate exploratory findings
from evidence-supported conclusions.

## Provenance

Adapted from an Apache-2.0 scientific workflow. Complete attribution and license text are retained in `LICENSE.txt` and `THIRD_PARTY_LICENSES.md`. The adaptation uses Synon's managed environment and package tools.

## Admission and method choice

1. Confirm that the selected study contains the biological comparison and
   metadata needed for the question before downloading large matrices.
2. Prefer processed count matrices with stable sample, treatment, timepoint,
   patient, and response identifiers. Transfer complete public files through
   `download_public_scientific_file`, not Python or Bash networking. Require its
   completed receipt before parsing, and record accession, file names,
   checksums, and the source publication.
3. Inspect whether values are raw integer counts, normalized expression, or an
   existing embedding. Never reconstruct or pretend raw counts exist.
4. Use Scanpy PCA plus Harmony for ordinary batch-aware exploration. A request
   for integration, clustering, marker genes, composition changes, or response
   signatures is not by itself a reason to install scvi-tools or PyTorch.
5. Use `scvi-tools` only when verified raw integer UMI counts are available and
   the scientific question specifically requires scVI/scANVI, probabilistic
   reference mapping, or Bayesian differential expression. Use `scgpt` only
   when a foundation-model representation is itself required.

## Canonical workflow

1. Record input hash, organism, assay chemistry, sample or batch labels, expected tissue biology, and whether counts are raw.
2. Read `references/scverse_qc_guidelines.md`.
3. Prefer a read-only `manage_environments(mode="preflight", packages=["scanpy", "anndata", "harmonypy", "leidenalg", "igraph", "matplotlib", "seaborn"], resource_requirements=...)` comparison before a substantial environment change. Use its reuse, lightweight-local, or remote-compute recommendation when appropriate; it is advisory and does not replace task judgment. Do not incrementally install a second analysis stack.
4. Prefer the tested Skill assets over rebuilding the same Scanpy/Harmony pipeline in an ad hoc Python cell:
   - for one existing `.h5ad` or 10X `.h5`, run `${SYNON_SKILL_DIR}/scripts/qc_analysis.py --help`;
   - for per-sample gene-by-cell CSV matrices, build a task-local manifest with `sample_id,path` plus available `patient_id,treatment,response,batch` columns;
   - for one wide gene-by-cell CSV/TSV matrix, including gzip-compressed files, declare its header and any per-cell metadata preamble rows explicitly with `--header-row` and repeatable `--obs-row NAME=ROW`; when authoritative annotations are in a separate table, join them by exact cell ID with `--obs-file`, `--obs-id-column`, and repeatable `--obs-column TARGET=SOURCE` mappings such as `sample_id`, `patient_id`, `treatment`, `response`, or `cell_type`; select `--matrix-kind` from evidence about the source values rather than guessing;
   - run `${SYNON_SKILL_DIR}/scripts/analysis_pipeline.py --help`, then run `--preflight-only` before the full pipeline.
   Treat a `metadata_mapping_required` preflight as an input-contract result, not a successful comparison. Add the authoritative metadata mapping before the full pipeline, or explicitly clear `--composition-columns` only for an aggregate exploratory run that makes no sample, patient, treatment, or response claim. For a paired comparison, pass the exact `--subject-key`, `--contrast-key`, `--contrast-a`, and `--contrast-b` values found in authoritative metadata. Read `comparison_audit.json` and `composition_comparison.csv` before writing any quantitative comparison; do not estimate changes from pooled cells or prose. Use `--population-key` when an authoritative annotation column should define abundance populations instead of Leiden clusters. The manifest, `analysis_summary.json`, and `comparison_audit.json` are working data, not user-facing deliverables. Do not infer metadata from filename characters when authoritative sample metadata is available.
5. Calculate count depth, detected genes, mitochondrial, ribosomal, hemoglobin, and available assay-specific metrics. Examine them globally and by sample or batch.
6. Use robust outlier rules as starting points, assess biological plausibility, and record exact thresholds and affected counts. Check empty droplets, ambient RNA, doublets, stress signatures, and batch-specific failures when relevant.
7. Preserve raw counts in a layer when they exist. Normalize and log-transform a working representation, select highly variable genes without leaking treatment labels, scale, and run PCA.
8. Inspect batch and biological covariates in PCA space. When correction is justified, run Harmony on the PCA representation and retain both corrected and uncorrected embeddings for comparison.
9. Build neighbors, Leiden clusters, and UMAP from the selected representation. Test multiple defensible resolutions and reject resolutions driven mainly by low quality or batch.
10. Annotate populations from marker evidence and reference biology. Generated Leiden
    IDs are unannotated clusters, not cell types. When `population_key=cluster`,
    `cell_type_claims_supported=false` is deliberate: inspect the marker table,
    create a reviewable CSV with `cluster,cell_type,evidence_markers`, then run
    `${SYNON_SKILL_DIR}/scripts/annotation_pipeline.py --help` to validate every
    cited marker against that cluster's ranked marker evidence and recompute
    annotated composition, comparison, and UMAP outputs before making cell-type
    claims.
    Keep ambiguous populations explicitly unresolved; do not force every cluster
    into a named type.
11. Compute marker genes with effect sizes and detection fractions. Avoid treating per-cell tests as patient-level evidence.
12. Quantify expansion or contraction per patient/sample using population proportions and paired or stratified summaries.
    The subject count, complete pair IDs, direction, magnitude, p-value, and multiplicity adjustment must
    come from the saved comparison audit and table. If only one complete pair is
    available, describe it as a single-pair observation; if the two subjects move
    in opposite directions, do not call the change consistent. Use pseudobulk expression
    for treatment or response comparisons when biological replicates permit it.
13. Build response signatures only from leakage-safe contrasts. Report cohort size, class balance, validation method, uncertainty, and whether the signature is exploratory.
14. Compare before/after QC and before/after integration. Confirm that expected populations and biological contrasts were not erased.

The core pipeline uses sparse, memory-bounded chunked matrix loading for both
sample manifests and wide matrices, validates the declared structure before
compute, checkpoints the QC result, uses a dependency-light HVG method, and
validates Harmony orientation against the number of cells. Extend its saved `analysis_core.h5ad` for task-specific cell
type annotation or statistically supported contrasts; do not copy the entire
pipeline into a new cell to change one downstream step.

## Deliver

Save the annotated `.h5ad`, original data with QC annotations, threshold and
retention tables, cell-population abundance table, paired/stratified comparison
table and audit when requested, marker table, pseudobulk or response-signature
table when supported, before/after QC and embedding plots, software versions,
runtime receipt, source-evidence table, and a concise report that distinguishes
measured results, interpretation, and limitations. Before publishing the report,
read back every quantitative claim against the machine-readable tables, visually
inspect every figure for clipped legends or unreadable labels, and remove claims
whose supporting output was not actually produced. Do not promise analysis code
or reproducibility files unless those exact files are included in the saved
deliverables.

Do not overwrite the source file. Do not use one universal mitochondrial or gene-count threshold across tissues or technologies without justification.
Do not claim treatment-associated change from pooled cell counts when patient-
level replication is missing. Do not fabricate missing metadata or validations.
