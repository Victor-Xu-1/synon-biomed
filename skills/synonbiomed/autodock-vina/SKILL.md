---
name: autodock-vina
description: Managed AutoDock Vina workflow for real structure-guided docking, virtual screening, pose ranking, and reproducible affinity results. Use when a conclusion requires calculated poses or Vina affinity scores.
implementation-identities: [AutoDock Vina]
keywords: [docking, dock, ligand, receptor, structure, pdb, pose, affinity, design, ranking, screening, 分子对接, 对接, 配体, 结构, 共晶, 设计, 排序, 虚拟筛选]
allowed-tools: search_skills, skill, repl, download_public_scientific_file, manage_environments, manage_packages, python, bash, read_file, edit_file, save_artifacts
required-capabilities:
  - molecular-docking
required-environment-packages:
  - vina
  - meeko
  - rdkit
  - gemmi
  - prody
  - biopython
  - openbabel
preferred-execution-assets:
  - scripts/autodock_vina.py
critical-constraints:
  - For a generated-candidate workflow, run the drug-discovery-pipeline assembler after both validations pass and publish its ranking, report, component registry, and pose-score table; never hand-edit the quantitative report.
  - Execute docking only through scripts/autodock_vina.py; never call or wrap vina, mk_prepare_receptor.py, or mk_prepare_ligand.py directly, never split ranked_poses.pdbqt, and publish only the pack-generated validated pose files and report.
  - For a raw apo receptor without a bound reference ligand, require an explicit binding-site route decision and offer the registered P2Rank resolver; never substitute the whole-protein centroid or model-estimated coordinates.
---

# AutoDock Vina managed workflow

## Scope

Use this Skill only when the requested conclusion depends on a calculated pose,
Vina affinity, ranked candidates, or a structure-guided screen. Binding-site
inspection, literature review, and contact analysis alone do not require
docking.

Use one managed environment and the bundled workflow script. Do not install
Vina, Meeko, or RDKit from Bash/Python and do not write a second docking
driver. The bundled script owns receptor/ligand preparation, documented CLI
witnesses, Vina argv construction, ranking, output validation, and cleanup.
The Harness supplies the canonical task response language to the pack, so its
validated report is generated in the same language as the request. Never
translate or rewrite a promoted pack report with `edit_file`; regenerate it
through the pack when the task language changes.

`autodock_vina.py` is the sole execution facade. `autodock_vina_inputs.py`
owns CDX normalization, the core owns Vina execution, and
`autodock_vina_outputs.py` owns poses and the deterministic report.
`autodock_vina_pockets.py` owns validated pocket-receipt consumption. Bound
ligand evidence stays with `binding-mode-analysis`; apo-pocket prediction stays
with `p2rank-pocket-detection`. Extend those owners, never add a second command
path.

## Evidence before execution

1. Resolve a latest liganded structure in `repl` through the single selector
   `host.mcp("structures-interactions", "pdb_select_latest_liganded_structure", ...)`,
   supplying the task's evidence-backed UniProt accession and organism. It
   owns release-date-descending search, ordered entry/ligand hydration,
   substantive organic-ligand classification, and the audit trail of newer
   rejected candidates. Do not replace it with hand-picked PDB IDs or a second
   client-side sort. Preserve the selected RCSB entry ID, release date,
   `organic_ligands`, `rejected_newer_candidates`, `selection_contract`,
   canonical `coordinate_files.mmcif_url`, retrieval time, and selected
   artifact versions. Download only that returned HTTPS coordinate URL through
   `download_public_scientific_file` and retain its SHA-256; never use
   `web_fetch`, Python, Bash, or an invented MCP download method. Use the lower
   level search, structure, and ligand methods only for exploratory questions
   that do not ask for the latest qualifying entry.
2. Supply a chemically prepared receptor or an evidence-backed receptor source,
   plus one ligand file containing the exact requested ligand set. PDBQT inputs
   bypass preparation; PDB/mmCIF and CDX/SDF/MOL/MOL2 inputs are prepared by the
   pack's reviewed conversion and Meeko path. Pass CDX directly to the pack so
   its Open Babel conversion, molecule-count validation, and stable source-order
   identifiers stay in the same execution receipt.
3. For a raw PDB/mmCIF co-crystal, pass `--reference-ligand` and let the pack
   derive the docking center from that component's verified coordinates. When
   it is omitted, the pack may select one unambiguous bound organic component
   from authoritative PDB SITE annotations or a single plausible non-polymer;
   ambiguous or apo coordinates require one short binding-site decision instead
   of an invented local center or protein centroid. Offer P2Rank by its
   public implementation name for automatic apo-pocket prediction, a manual
   center, or a receptor/reference-ligand input. If the user selects P2Rank or
   delegates the decision to Synon Biomed, load `p2rank-pocket-detection`, run
   its reviewed pack, and pass both `pocket_selection.json` and
   `pocket_validation.json` directly to this pack. When current-task user input explicitly supplies the
   three center coordinates, pass them together with
   `--center-authority resolved-user-input`; the Harness verifies the numeric
   tuple against user evidence before process start. Prepared PDBQT receptors
   use the same evidence-bound explicit-center route. Do not write ad-hoc
   Gemmi/BioPython center-parsing code. Record the coordinate system and
   rationale. Routine seed, exhaustiveness, and mode-count defaults are owned
   by the pack unless the task explicitly constrains them.
4. Use `manage_environments(mode="list", dependencies=["vina", "meeko", "rdkit", "gemmi", "prody", "biopython", "openbabel"])`
   as the only readiness check. Do not probe retired runtime tools or guessed
   Python object methods.

## Environment and execution

1. Call `manage_environments(mode="list", dependencies=["vina", "meeko", "rdkit", "gemmi", "prody", "biopython", "openbabel"])`.
2. Reuse a compatible environment. If none exists, call
   `manage_environments(mode="create", name="autodock-vina", language="python", python_version="3.11", packages=["vina=1.2.7", "meeko=0.8.0", "rdkit=2026.03.1", "gemmi=0.7.5", "prody=2.6.1", "biopython=1.88", "openbabel=3.2.1"], channels=["conda-forge"], background=true)` and wait for its notification. These pins are the same authority used by the bundled execution pack and the accepted CDX conversion path. If that name already exists but is incompatible, create one new task-appropriate name with the same complete package set; do not mutate the incompatible generation in place.
3. Run the bundled script's `--help` once through `bash` with that environment.
4. Run one task-relative command, normally in the background for a screen:

~~~bash
python "${SYNON_SKILL_DIR}/scripts/autodock_vina.py" \
  --receptor inputs/receptor.pdb \
  --ligand inputs/designed_ligands.cdx \
  --reference-ligand LIG \
  --size-x 20.0 --size-y 20.0 --size-z 20.0 \
  --seed 42 --repeat-count 3 --exhaustiveness 8 --num-modes 9
~~~

For a validated P2Rank route, do not transcribe its coordinates into the
command. Consume the two hashed receipts and let the pack own both center and
box size:

~~~bash
python "${SYNON_SKILL_DIR}/scripts/autodock_vina.py" \
  --receptor inputs/receptor.pdb \
  --ligand inputs/designed_ligands.cdx \
  --pocket-selection pocket_detection/pocket_selection.json \
  --pocket-validation pocket_detection/pocket_validation.json \
  --seed 42 --repeat-count 3 --exhaustiveness 8 --num-modes 9
~~~

Replace only task-relative input paths, the verified co-crystal component ID,
and evidence-backed box dimensions. For a raw PDB/mmCIF receptor, the pack
selects one deterministic instance of `--reference-ligand`, selects the polymer
chains contacting that instance, removes ligands/waters, writes a normalized
PDB, derives and records that ligand centroid, and records the selected chains
before Meeko preparation. A raw apo receptor cannot use naked `--center-*`
values; it may instead consume the paired passing P2Rank receipts. PDBQT receptors require all three explicit center values and the same
Harness-verified `--center-authority` because they do not retain a reference
ligand.
Use repeated `--receptor-chain` only when the authoritative structure evidence
already identifies the intended receptor chain. The
script executes `vina --help`, `mk_prepare_ligand.py --help`, and
`mk_prepare_receptor.py --help` before docking, and also executes the reviewed
`obabel` CDX conversion when the ligand input is CDX. Do not preconvert CDX or
call any of those CLIs directly. Vina 1.2.x output is captured
by the script; do not add an unsupported `--log` flag. Do not create, remove,
or mark an output directory before execution. The default is `out`; when it
already exists, the pack preserves it and selects the first absent `out-2`,
`out-3`, and so on. A workspace marker never authorizes reuse. An explicit
`--output-dir` must be task-relative, stay inside the authorized workspace, and
be absent before execution. Use the validated output path printed by the pack.

`--repeat-count` controls independent Vina runs per candidate; each run uses a
deterministic adjacent seed. `--num-modes` controls the sampled modes per run.
The execution pack retains all sampled scores as working evidence but projects
exactly one primary pose per candidate into the user-facing PDB and pose table.
The lowest Vina affinity always wins. When affinities tie, reference-ligand
centroid proximity and long-axis agreement provide a secondary geometric
tie-break. When no reference ligand is available, exact score ties use stable
run and mode ordering. Validation records which basis was actually observed;
neither tie-break can override a better docking score or force unrelated
chemotypes into an artificial atom mapping.

## Acceptance

A successful run must retain the exact managed environment generation, Bash
receipt, input hashes, stdout/stderr, output files, and validation record. The
workflow publishes and validates:

- `<validated-output>/docking_scores.csv` with exactly these columns:
  `ligand_id`, `best_affinity_kcal_mol`, `mode_count`, `receptor_sha256`,
  `center_x`, `center_y`, `center_z`, `size_x`, `size_y`, `size_z`, `seed`,
  `repeat_count`, `exhaustiveness`, `num_modes`, `poses_per_candidate`,
  `primary_pose_run`, `primary_pose_mode`, reference-geometry diagnostics, and
  `rank`. `poses_per_candidate` is always 1. Read this canonical table directly;
  do not guess or rename an affinity field before inspecting its header;
- `<validated-output>/ranked_poses.pdbqt`;
- `<validated-output>/primary_poses/*.pdbqt`, one validated file per candidate, plus
  `<validated-output>/primary_pose_manifest.csv` with ID, rank, score, source mode, and hash.
  Never split `ranked_poses.pdbqt`; its pre-`MODEL` bytes are metadata;
- `<validated-output>/docking_pose_scores.csv`, one primary-pose row per candidate with its
  affinity, source run/mode, and reference-geometry diagnostics;
- `<validated-output>/docking_pose_samples.csv`, the complete internal score ledger for every
  sampled run and mode, including the selected-primary flag;
- `<validated-output>/docking_complex_ensemble.pdb`, a standard editable PDB containing the
  fixed selected protein exactly once, the selected original co-crystal ligand
  as residue `REF` when available, and exactly one primary pose for every
  docked candidate as a separate residue component. Candidate residue names
  are stable three-character component codes and every REMARK maps that code
  back to the original candidate ID, candidate rank, source run/mode, and
  affinity;
- `<validated-output>/docking_components.csv`, the editable component registry joining protein
  chains, the optional reference ligand, candidate IDs, PDB residue groups,
  ranks, affinities, and source pose files;
- `<validated-output>/vina.log` with bounded CLI receipts;
- `<validated-output>/validation.json` with passing input fidelity, source integrity,
  candidate-ID fidelity, retained-pose count fidelity, complex-component
  integrity, output integrity, score summary, and process cleanup;
- `<validated-output>/docking_report.md`, generated from the same ranking and pose manifest,
  with the box basis and score limitations stated explicitly.

Candidate IDs come from the input molecule titles and are preserved exactly
through `docking_scores.csv`, `ranked_poses.pdbqt`, the PDB REMARK records, and
`docking_components.csv`. Never infer a candidate by adding one to a temporary
`ligand-N` filename or by relying on lexicographic filename order.

After all declared checks pass, publish the pack-generated readable report, the
complete candidate/ranking table, the source ligand file,
`docking_complex_ensemble.pdb`, `docking_components.csv`, and the validated
files under `primary_poses/` when individual conformations were requested. Keep
the combined PDBQT, the full sample ledger, validation JSON, and the raw log as
working evidence unless the user explicitly asks for them. Do not hand-author,
transcribe, or numerically restate a competing report with `edit_file`.
Never treat a zero exit code, prose, an ordinary CSV, or a pose-confidence
score as Vina affinity evidence.

When this docking run is one stage of a generated-candidate workflow that also
promises an integrated ranking or professional report, do not join tables with
ad-hoc pandas/Python and do not author quantitative Markdown with `edit_file`.
After docking validation passes, load `drug-discovery-pipeline` again so its
task-scoped directory is current, then run its documented
`scripts/assemble_docking_results.py` exactly once. That canonical assembler is
the only owner of the merged ranking, numeric report, and results summary; the
model may explain those outputs but must not transcribe or recalculate them.

## Failure boundary

A failed run ends the current execution unit. Preserve its exit code,
stdout/stderr, the pack-reported failure artifact, and any completed outputs. Before one new
execution, reload this Skill or inspect the documented interface named by the
receipt and change only the evidence-proven input or parameter. A second
semantically equivalent failure closes this path. Do not edit-run loop, guess
method names or flags, switch environments/providers, or substitute a
heuristic score. If the receipt requires user-owned input or approval, wait in
the corresponding user-input state.
