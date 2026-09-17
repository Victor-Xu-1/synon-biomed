---
# Modified for Synon Biomed. Upstream terms: docs/licenses/bionemo-agent-toolkit/NOTICE.md.
name: drug-discovery-pipeline
description: >
  Evidence-first medicinal-chemistry and structure-guided drug-discovery router.
  Reuse Synon Biomed's existing ChEMBL, PubChem, BindingDB, UniProt, PDB,
  AlphaFold, and PubMed MCP tools, normalize compound and target identifiers,
  and synthesize measured evidence with provenance. When generation, docking,
  virtual screening, or affinity prediction is requested, orchestrate the
  existing GenMol, DiffDock, AutoDock Vina, and Boltz2 Skills without
  duplicating their clients or runtimes. Use for compound-target evidence, hit
  discovery, lead optimization, structure selection, virtual screening,
  molecule generation, docking, and binding-affinity work.
license: Apache-2.0 AND CC-BY-4.0
keywords: [ligand, design, docking, molecule, structure, protein, pdb, structure-search, pdb-search, binding, drug-discovery, lead-optimization, 配体, 对接, 结构查询, 共晶结构, 结合模式, 结构多样的新候选, 药物发现]
allowed-tools: search_skills, skill, ask_user, repl, web_search, list_compute, manage_environments, manage_packages, bash, read_file, download_public_scientific_file, save_artifacts
critical-constraints:
  - When any validated molecular-generation route and AutoDock Vina both succeed, run scripts/assemble_docking_results.py against their passing validation files before the final answer; never calculate or edit the numerical report by hand.
  - Publish the assembler ranking and report together with the docking complex, component registry, and pose-score table before claiming the combined workflow complete.
  - The UniProt get_uniprot_entries call accepts only its advertised plural accessions and documented fields; never add a features field or another unadvertised parameter.
  - Derive binding-site geometry through the loaded binding-mode-analysis execution asset; do not replace it with model-authored Biopython residue parsing.
  - A generated-candidate deliverable is incomplete unless its property CSV, SDF, and SMILES companions contain identical candidate IDs, canonical SMILES, and counts.
preferred-execution-assets:
  - scripts/assemble_docking_results.py
---

# Drug Discovery Pipeline

This Skill routes evidence; delegated Skills own execution.

## Route only the work the question requires

Classify the request before calling anything:

- **Evidence only**: use the minimum MCP lanes and stop after synthesis.
- **Compute only**: verify supplied inputs and provenance, then load only the
  requested compute Skill.
- **Evidence plus compute**: resolve identities and structure, freeze the
  handoff table, then execute requested branches.

Use only required lanes. Obtain approval before public egress of proprietary data.

## Existing MCP evidence contracts

1. Resolve a compound in `repl` with `host.mcp("chembl", "compound_search", name=...)`,
   or `host.mcp("chemistry", "pubchem_search_compounds", query=...)`,
   `namespace`, and a bounded `max_cids`. Keep ChEMBL ID, PubChem CID,
   canonical SMILES, and InChIKey. Never send `None`, empty structures, or
   optional fields that have no value.
2. Resolve a target with `host.mcp("chembl", "target_search", gene_symbol=...,
   organism=..., limit=20)`. Read `targets`, not `records`, using
   `components[].accession` and canonical `components[].gene_symbol` (`gene`
   is its portable alias). Match organism and ask unless one accession is
   unambiguous. Then call `host.mcp("genes-ontologies",
   "get_uniprot_entries", accessions=[verified_accession], fields=["accession",
   "id", "protein_name", "gene_names", "organism_name", "length"])`.
   Fields mode returns `records` (not `entries`).
   It accepts plural accessions only: never pass names, organism, query, or
   `accession=`, and never invent a UniProt search method.
3. Retrieve measured evidence with `host.mcp("chembl", "get_bioactivity", ...)`
   and mechanism evidence with `host.mcp("chembl", "get_mechanism", ...)`. These take
   `molecule_chembl_id` and/or `target_chembl_id`, not names. Omit unset filters
   and start with `limit: 20`.
4. Cross-check binding with `host.mcp("chemistry", "bindingdb_ligands_by_target", ...)`
   using a UniProt accession, or
   `host.mcp("chemistry", "bindingdb_targets_by_compound", ...)` using verified canonical
   SMILES. Start with `max_rows: 25` and expand only when necessary.
5. For a latest experimental liganded structure, use the single deterministic
   selector `host.mcp("structures-interactions", "pdb_select_latest_liganded_structure", ...)`
   rather than manually sampling PDB IDs:

   ```python
   import host

   selection = host.mcp(
       "structures-interactions",
       "pdb_select_latest_liganded_structure",
       uniprot_accession="verified accession",
       organism="verified organism",
       max_candidates=25,
       max_ligands=25,
   )
   if not selection.get("selected"):
       raise RuntimeError(selection.get("reason") or "no qualifying structure")
   ```

   The selector owns release-date-descending search, ordered candidate
   hydration, substantive organic-ligand classification, and the audit list of
   newer rejected entries. Preserve `structure`, `organic_ligands`,
   `rejected_newer_candidates`, `search`, and `selection_contract` in the
   evidence handoff. Do not replace it with repeated `pdb_get_structures`
   sampling or claim that an older convenient entry is latest. Adjust its
   fragment-size chemistry bounds only when the scientific question requires
   it. Use `pdb_search_structures`, `pdb_get_structures`, and `pdb_get_ligands`
   independently only for exploratory questions that do not ask for the latest
   qualifying structure. Use
   `host.mcp("structures-interactions", "alphafold_get_prediction", ...)` only
   as a predicted fallback and label it as predicted.

Use only `coordinate_files.mmcif_url`; never guess keys or extensions. Download
it with `download_public_scientific_file`, never web or shell.
6. Add literature only when needed with `host.mcp("pubmed", "search_articles", ...)`, using
   a focused `query` and bounded `max_results`.

Preserve assay semantics, units, validity, citations, and source IDs. Never mix
incompatible measurements or present predictions as measured evidence.

## Freeze the compute handoff

Before computation, record:

- target/species/accession and chosen structure;
- receptor and ligand source IDs, artifact versions, formats, preparation,
  stereochemistry/protonation assumptions, and hashes;
- binding-site/box evidence, requested mode/count/objective/seed/budget, and
  hosted versus local egress decision.

If a required identity or artifact is ambiguous, ask a focused question rather
than guessing. For local NIMs that share a default port, run them sequentially
or configure distinct ports; never assume multiple services can all own
`localhost:8000`.

## Delegate compute; do not reimplement it

Invoke only the exact bundled Skill for each requested branch:

| Requested branch | Existing Skill | Required handoff | Acceptance evidence |
| --- | --- | --- | --- |
| protein-pocket-conditioned or target-aware molecular generation | `structure-based-molecule-generation` | receptor/pocket evidence, requested count and objective | generated 3D molecules, exact pocket input, engine provenance, validity/uniqueness checks |
| ligand- or scaffold-conditioned model generation | `genmol-nim` | SAFE input, count, QED or LogP objective, mode | generated molecules, validity/uniqueness checks, service provenance |
| local RDKit analog diversification | `medicinal-chemistry-optimization` | verified parent-SMILES handoff file, count, numeric seed | exact unique SDF/SMILES/CSV set plus generation-validation record |
| binding mode | `binding-mode-analysis` | verified mmCIF + ligand | contacts + source hash |
| DiffDock pose prediction | `diffdock-nim` | receptor artifact, canonical ligand input, pose count, mode | ranked pose artifacts, confidence values, request provenance |
| governed local Vina docking | `autodock-vina` | receptor and ligand artifact IDs/versions, box, numeric seed | durable compute receipt, parser-accepted Vina log, ranked poses |
| Boltz2 structure or affinity prediction | `boltz2-nim` | sequence/polymer definition, verified ligand identity, sampling settings, mode | mmCIF and confidence/affinity fields with prediction provenance |

Do not copy delegated clients or binary calls. Retain diagnostics when a branch
lacks its acceptance evidence.

## Cross-step joins and ranking

- Join by stable identifiers and artifact versions, never display names alone.
- Preserve every candidate from generation through docking and affinity with a
  stable candidate ID; record explicit rejection reasons.
- Keep QED/LogP, docking confidence, Vina affinity, Boltz2 affinity, measured
  assay values, and uncertainty in separate columns. They are not interchangeable
  scales and must not be collapsed into an unexplained composite score.
- Rank only against a user-approved objective. When multiple objectives remain,
  return a Pareto-style shortlist or a transparent sortable table.
- Predictions prioritize experiments; they do not prove biological outcomes.

## Deterministic result assembly

Result assembly is generator-agnostic. The selected generation route must
normalize its validated output into a candidate property table with the
canonical columns documented below while retaining its actual engine,
conditioning inputs, source or model identity, and validation record. RDKit
may calculate descriptors and validate generated structures, but descriptor
calculation does not change which engine generated them.

The candidate property CSV requires these generator-neutral columns:
`candidate_id`, `canonical_smiles`, `molecular_weight`, `clogp`, `tpsa`,
`qed`, `hbd`, `hba`, `rotatable_bonds`, and `lipinski_violations`. It may also
carry `parent_tanimoto`, `murcko_scaffold`, `generator_score`, and
`generator_score_kind`; unavailable optional fields remain empty rather than
being invented. The passing generation validation must include one
`provenance` object with `generation_class`, `engine`, `provider`,
`engine_version`, and `conditioning.kind` plus the exact lowercase SHA-256 in
`conditioning.input_sha256`. The assembler copies that provenance into every
ranked row, the deterministic summary, and the report.

After the selected generation route and `autodock-vina` pass, invoke
`skill("drug-discovery-pipeline")` again so this orchestrator's task-scoped
directory becomes current, then use the bundled joiner. Never write another
pandas/RDKit merge, create the numeric report with `edit_file`, or infer IDs
from `ligand-N` filenames:

```bash
python "${SYNON_SKILL_DIR}/scripts/assemble_docking_results.py" \
  --properties "validated generator candidate-property CSV" \
  --structures-sdf "validated generator SDF for the exact selected candidates" \
  --structures-smiles "validated generator SMILES file for the exact selected candidates" \
  --docking docking_out/docking_scores.csv \
  --generation-validation "validated generator record" \
  --docking-validation docking_out/validation.json \
  --target-label "verified target label" \
  --structure-id "verified structure ID" \
  --reference-ligand "verified component ID" \
  --report-language "the user's language: zh-CN or en-US" \
  --output-dir final_results
```

Run it in a verified environment. It requires passing validations plus identical
candidate IDs and canonical SMILES across CSV, SDF, SMILES, and docking inputs.
Header-only or mismatched companions fail before reporting. It writes:

- `final_results/candidate_ranking.csv`, the complete editable joined table;
- `final_results/project_report.md`, one editable, professional project report
  with a decision summary, evidence and method table, diversity and property
  profile, transparent review sets, top-candidate table, next experiments,
  deliverable guide, and limitations;
- `final_results/results_summary.json`, the machine authority for counts,
  ranges, ranks, hashes, and top rows.
- `docking_out/docking_complex_ensemble.pdb`, the fixed protein, original
  ligand when available, and one primary best-scoring pose for every ranked
  candidate in one editable model;
- `docking_out/docking_components.csv`, the exact component-to-candidate and
  pose map;
- `docking_out/docking_pose_scores.csv`, the editable long-form score table for
  every displayed primary candidate pose;
- `docking_out/docking_pose_samples.csv`, the internal multi-run/multi-mode
  sampling ledger with the selected-primary flag.

The JSON is working evidence, not a default user-facing artifact. The Markdown
report is generated from the validated tables in the user's language and is the
editable factual core. The model may add evidence-backed scientific context,
but must not recalculate or visually transcribe its numeric statements.

## Deliverable and completion gate

Return:

1. normalized compound/target/structure identifiers;
2. evidence table with source, tool, query scope, measurement semantics, and
   citations;
3. a complete editable candidate ranking table with separate measured and
   predicted fields;
4. the source SDF and SMILES set;
5. a component-grouped docking PDB with fixed protein, optional original
   ligand, and one primary best-scoring pose per candidate, plus primary-pose
   scores, component registry, and the internal multi-run sample ledger;
6. a professional Markdown report whose quantitative statements agree with the
   deterministic summary, with limitations and next experimental validation.

Preserve artifact IDs/versions, compute receipts, engine versions, settings,
seeds, parser status, validation JSON, logs, and failures as internal working
evidence. Do not crowd the user file panel with plans, handoff JSON, raw logs,
or validation payloads unless the user asks for them.

Do not call the pipeline complete merely because a prose answer was returned.
Every requested evidence lane must have a source result or an explicit
unavailable status, and every requested compute lane must meet the delegated
Skill's acceptance gate.
