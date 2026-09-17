---
name: medicinal-chemistry-optimization
description: Design and prioritize medicinal chemistry analogs using structure-activity relationships, physicochemical properties, DMPK liabilities, selectivity, safety alerts, synthetic feasibility, and intellectual-property constraints. Includes deterministic local RDKit analog enumeration and diversity selection. Use for an explicitly selected analog-enumeration fallback, ligand diversification, hit-to-lead, lead optimization, scaffold hopping, multiparameter optimization, or design-make-test-analyze cycles. This is not a model-backed de novo or protein-pocket-conditioned molecular generator.
implementation-identities: [medicinal-chemistry-optimization, RDKit analog enumeration]
required-capabilities: [ligand-analog-enumeration]
keywords: [RDKit, analog, analogue, ligand, analog-enumeration, diversity, fingerprint, MaxMin, scaffold, medicinal-chemistry, 配体, 类似物, 类似物枚举, 多样性]
allowed-tools: search_skills, skill, manage_environments, bash, read_file, save_artifacts
preferred-execution-assets:
  - scripts/rdkit_analog_generator.py
---

# Medicinal Chemistry Optimization

Treat compound design as a multiobjective evidence problem.

## Inputs

Collect structures, stereochemistry, assay definitions, raw and normalized potency, selectivity, physicochemical data, ADME/DMPK results, safety flags, synthesis information, structural biology evidence, uncertainty, and project constraints. Reject comparisons that mix incompatible assays or units.

## Workflow

1. Define the optimization profile and hard gates.
2. Build a SAR table that preserves assay provenance and uncertainty.
3. Identify potency, selectivity, solubility, permeability, clearance, exposure, safety, and synthetic liabilities.
4. Generate chemically diverse hypotheses: substituent scans, matched molecular pairs, scaffold or linker changes, stereochemical alternatives, conformational control, and property-balancing changes.
5. Use structure-based design, docking, quantum chemistry, and property calculations only where their assumptions are appropriate.
6. Rank proposals by expected information gain, not a single score. Include a negative-control or falsifying design when practical.
7. Check novelty, known chemistry, route feasibility, reactive groups, and likely metabolite risks before nomination.

## Canonical local RDKit analog generation

This route enumerates analogs around a supplied parent ligand; it does not
condition generation on protein-pocket geometry and must not be described as
AI, de novo, target-aware, or pocket-conditioned generation. Select it when the
request actually asks for local analog diversification, or after a professional
generation route is unavailable or unsuitable and the user accepts the
scientific trade-off.

For a user-selected local RDKit route, do not write a bespoke mutation loop in
`python`. Use the bundled deterministic generator. It reads any verified parent
SMILES from the source handoff, applies defined dummy-atom fragment attachment
at a declared SMARTS position, preserves the complete parent heavy-atom graph
sanitizes and canonicalizes a pool, selects the exact requested count with a
Morgan-fingerprint MaxMin picker, generates 3D conformers, and writes validation
evidence.

1. Call `manage_environments(mode="list", dependencies=["rdkit"])` and reuse a
   compatible environment.
2. Run the script's `--help` once through `bash` in that environment.
3. Materialize one canonical `ligand_handoff.json` from the verified source
   result. Its exact schema is:

```json
{
  "organic_ligands": [
    {
      "comp_id": "verified component ID",
      "description": "verified description",
      "smiles": "verified canonical or isomeric SMILES"
    }
  ]
}
```

   The generator reads `organic_ligands[--ligand-index]`; `ligands`,
   `parent_ligands`, and nested handoff variants are not accepted. Validate
   that the selected row is an object with non-empty `smiles` before starting
   Bash. Do not discover this schema by failing the generator.
4. Run one task-relative command for that validated handoff:

```bash
python "${SYNON_SKILL_DIR}/scripts/rdkit_analog_generator.py" \
  --input-json ligand_handoff.json \
  --ligand-index 0 \
  --count 30 \
  --pool-size 500 \
  --seed 42 \
  --attachment-smarts "[cH]" \
  --id-prefix ANALOG \
  --output-dir rdkit_analogs
```

Replace the verified handoff path, ligand index, requested count, evidence-backed
attachment SMARTS, stable ID prefix, and task-relative output directory. The
complete verified parent heavy-atom graph is always preserved; do not pass or
invent a second required-core SMARTS. Do not hardcode a remembered ligand
SMILES, use manual `RWMol`/`CombineMols` mutation code, decrement explicit
hydrogens, call undocumented atom valence setters, or pad a short output with
duplicates.

The successful output contract is:

- `rdkit_analogs/designed_ligands.sdf` — exactly the requested unique 3D molecules;
- `rdkit_analogs/designed_ligands.smi` — one header-free record per candidate in the exact standard order `canonical isomeric SMILES<TAB>candidate_id`;
- `rdkit_analogs/designed_ligands.csv` — auditable structures and calculated properties;
- `rdkit_analogs/generation_validation.json` — parent identity, RDKit version,
  pool/invalid/selected counts, seed, fingerprint settings, scaffold count,
  pairwise Tanimoto summary, QED range, Lipinski-violation distribution, hashes,
  and passing exact-count/uniqueness/required-core/3D/property-table checks.

The CSV columns are exactly, in order:
`candidate_id`, `canonical_smiles`, `molecular_weight`, `clogp`, `tpsa`,
`qed`, `hbd`, `hba`, `rotatable_bonds`, `lipinski_violations`,
`parent_tanimoto`, and `murcko_scaffold`.
Read these exact names or inspect the header before selecting columns; never
guess aliases such as `compound_id`, `smiles`, `mw`, `logp`, or `qed_score`.
The SDF preserves the same candidate ID and calculated values as molecule
properties so downstream docking and visualization can retain identity without
guessing from temporary filenames.

If a smaller subset is selected downstream, regenerate all three companion
files from the selected stable IDs and verify identical ID and canonical-SMILES
sets before docking. Never infer the `.smi` column order, write a header-only
placeholder, or publish a subset when any companion count differs.

If the script fails, preserve the receipt and fix only its evidence-proven
input. Do not fall back to model-authored molecule editing.

## Quantitative result integrity

Treat the saved machine-readable calculation outputs as the authority for every
count, threshold comparison, range, rank, and aggregate reported to the user.
Before writing the user-facing report, use the selected execution environment
to calculate those summaries directly from the exact CSV or validation output;
do not estimate them by visually scanning rows or by recalling a rule. Preserve
the canonical definition of each named rule: for example, the Lipinski rule of five
uses molecular weight, lipophilicity, hydrogen-bond donors, and
hydrogen-bond acceptors, while rotatable-bond count is a separate developability
descriptor and cannot replace a failed Lipinski criterion.

This is a pre-publication step in the normal task, not an automatic review after
the final answer. Draft the report from the computed summary, re-read the draft
once before saving it, and repair any disagreement with the source table before
publishing the final answer. Never append a second result-reading or review turn
after the task has already completed.

## Deliver

Return an editable design table with canonical structure identifier, hypothesis,
predicted effect, evidence, uncertainty, calculated properties, synthetic route
concept, required assays, diversity cluster, and priority. Publish the complete
CSV, SDF, and SMILES set; use the validation JSON only as working evidence unless
the user requests it. Separate measured, calculated, literature-derived, and
speculative values.

Do not invent assay values, docking scores, yields, or selectivity. A design is a testable hypothesis until experimental evidence exists.

## Source authorities

Use current primary literature and project assay data. Use the [FDA discovery and development overview](https://www.fda.gov/patients/drug-development-process/step-1-discovery-and-development) for the development context and current ICH guidance when an optimization choice affects impurities, safety, DMPK, or product quality.
