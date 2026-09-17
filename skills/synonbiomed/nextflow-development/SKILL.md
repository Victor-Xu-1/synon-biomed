---
name: nextflow-development
description: Prepare and run pinned nf-core RNA-seq, Sarek, or ATAC-seq pipelines on local FASTQ files or public GEO/SRA data. Use for Nextflow workflow setup, samplesheets, genome references, reproducible sequencing analysis, variant calling, expression analysis, or chromatin accessibility analysis.
allowed-tools: search_skills, skill, manage_environments, manage_packages, python, bash, read_file, edit_file, save_artifacts
---

# nf-core Pipeline Development

Run version-pinned workflows with explicit data, genome, container, and provenance controls.

## Provenance

Adapted from an Apache-2.0 scientific workflow. Complete attribution and license text are retained in `LICENSE.txt` and `THIRD_PARTY_LICENSES.md`. The adaptation removes remote shell installation and routes setup through governed runtime controls.

## Workflow

1. Identify assay, organism, reference build, read layout, strandedness, sample groups, and requested outputs.
2. For GEO/SRA inputs, read `references/geo-sra-acquisition.md`, inspect study metadata, and confirm the sample subset before download.
3. Run `scripts/check_environment.py`. Require a supported Java and Nextflow version plus an available container profile such as Docker, Singularity, or Apptainer.
4. Call `manage_environments(mode="list", dependencies=["nextflow", "openjdk"])`. Reuse a compatible environment or create `nextflow` with pinned Nextflow and Java packages. Never use a remote shell installer.
5. Select `rnaseq`, `sarek`, or `atacseq` and read the corresponding file under `references/pipelines/`.
6. Pin the nf-core pipeline release, container profile, genome assets, configuration, and parameters.
7. Run the pinned nf-core test profile in the selected environment before real data. If the required container runtime is unavailable, preserve that exact blocker and wait instead of changing profiles silently.
8. Generate and validate the samplesheet with the bundled scripts.
9. Execute with bounded resources, persistent work directory, and `-resume` support.
10. Verify exit status, trace, report, timeline, software versions, sample completion, MultiQC, and expected scientific outputs.

## Failure rules

Stop on sample-sheet ambiguity, genome mismatch, missing container runtime, failed test profile, incomplete samples, or reference incompatibility. Preserve logs and work hashes; do not silently switch genome builds or pipeline versions.

## Deliver

Save the validated samplesheet, pinned command and configuration, version inventory, execution receipt, MultiQC/report artifacts, output manifest, failed-sample table, and limitations.
