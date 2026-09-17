---
name: instrument-data-to-allotrope
description: Convert laboratory instrument exports in PDF, CSV, Excel, or text formats into Allotrope Simple Model JSON and flattened CSV with traceable parsing and validation. Use when standardizing instrument data for LIMS, ELN, data lakes, assay handoff, or reusable parser development.
allowed-tools: search_skills, skill, manage_environments, manage_packages, python, bash, read_file, edit_file, save_artifacts
---

# Instrument Data to Allotrope

Convert instrument output while preserving raw values, metadata, provenance, and calculated-data lineage.

## Provenance

Adapted from an Apache-2.0 scientific workflow. Complete attribution and license text are retained in `LICENSE.txt` and `THIRD_PARTY_LICENSES.md`. The adaptation uses Synon's managed environment and package tools.

## Workflow

1. Preserve the original file and calculate its hash.
2. Inspect file type, instrument identifiers, software version, run metadata, units, sample identifiers, and table structure.
3. Read `references/supported_instruments.md` and select the native Allotropy parser when supported.
4. Call `manage_environments(mode="list", dependencies=["allotropy", "pandas", "openpyxl", "pdfplumber"])`. Reuse a compatible environment; otherwise create `allotrope` with Python 3.11 and install the exact pins from `requirements.txt` through `manage_packages(..., use_pip=true)`.
5. Run `${SYNON_SKILL_DIR}/scripts/convert_to_asm.py --help` once in that environment, then execute the documented conversion with task-relative input and output paths. Do not copy or modify the bundled script.
6. Validate ASM output with `${SYNON_SKILL_DIR}/scripts/validate_asm.py` in the same environment.
7. Produce flattened CSV with `${SYNON_SKILL_DIR}/scripts/flatten_asm.py` only when a tabular handoff is useful.
8. Compare record counts, identifiers, units, timestamps, raw/calculated classification, and representative values against the source.

## Ambiguity rules

Read `references/field_classification_guide.md` before mapping uncertain fields. Ask when raw versus calculated status, units, instrument settings, or sample identity cannot be established. Do not silently coerce units or invent ontology mappings.

Fallback parsing is exploratory only. Label it clearly and do not claim GxP, LIMS, or regulatory suitability without schema-owner review and validated source-system controls.

## Deliver

Save the ASM JSON, optional flattened CSV, validation report, field-mapping table, source hash, parser version, assumptions, unsupported fields, and reproducible execution receipt.
