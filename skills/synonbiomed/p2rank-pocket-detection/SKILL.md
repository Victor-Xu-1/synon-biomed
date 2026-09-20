---
name: p2rank-pocket-detection
description: Predict and rank ligand-binding pockets on apo or otherwise unannotated protein PDB structures with the pinned P2Rank engine, then emit a validated pocket center and docking-box handoff. Use when docking or structure-guided work lacks a bound reference ligand, an annotated site, or user-supplied pocket coordinates.
license: MIT
implementation-identities: [P2Rank]
keywords: [P2Rank, binding pocket prediction, pocket detection, apo protein, docking box, binding site prediction, 口袋预测, 口袋识别, 结合位点预测, 对接盒子]
allowed-tools: download_public_scientific_file, manage_environments, skill, bash, read_file, save_artifacts, ask_user
required-capabilities: [binding-pocket-prediction]
required-environment-packages: [openjdk=17]
preferred-execution-assets:
  - scripts/p2rank_binding_pockets.py
critical-constraints:
  - Execute P2Rank only through scripts/p2rank_binding_pockets.py with the pinned official 2.5.1 release archive; never replace it with a protein centroid, an ad hoc residue average, or a model-authored coordinate.
  - Do not treat a predicted pocket as an experimentally established site. Preserve P2Rank rank, score, calibrated probability, profile, source hash, and derived-box method in every downstream docking handoff.
---

# P2Rank pocket detection

Use this Skill only when a protein structure lacks an authoritative bound
ligand, `SITE` record, validated pocket artifact, or explicit user-supplied
center. P2Rank is a ligand-agnostic prediction method: it ranks likely
small-molecule binding sites on the solvent-accessible protein surface. Its
probability is a calibrated prediction, not proof of biological relevance.

## User decision boundary

Before acquiring or running P2Rank, preserve one explicit user decision when
the task did not already request automatic pocket prediction. Present concrete
routes such as:

- `使用 P2Rank 自动预测最高评分口袋`;
- provide a known center or validated pocket artifact;
- provide a receptor containing a reference ligand or annotated site.

If the user chooses `让 Synon Biomed 决定`, that delegates the method choice;
select P2Rank by its public implementation name and use rank 1. Do not insert
numeric coordinates in the option. The execution pack computes them only after
the user decision and emits the exact receipt consumed downstream.

## Fixed source and environment

Use the latest verified stable package pinned by this Skill, not the 2.6 alpha:

- release: `P2Rank 2.5.1`;
- official archive: `https://github.com/rdk/p2rank/releases/download/2.5.1/p2rank_2.5.1.tar.gz`;
- SHA-256: `d243f2d9036ac053fefb9407b5fe1c85f4fe077c519fd975ac585e995feab274`;
- license: MIT;
- runtime: Java 17 or newer.

Transfer that archive only with `download_public_scientific_file` so the task
retains its source URL, artifact version, bytes, and hash. Never use Bash,
Python, curl, wget, or a guessed mirror to download it.

Call `manage_environments(mode="list", dependencies=["openjdk"])` and reuse a
compatible environment. If none exists, create one immutable environment with
Python 3.11 and `openjdk=17` from `conda-forge`. Do not mutate the docking
environment or install Java from Bash.

## Canonical execution

Copy or resolve the receptor and pinned release archive into the task
workspace, then run exactly the materialized pack:

```bash
python "${SYNON_SKILL_DIR}/scripts/p2rank_binding_pockets.py" \
  --structure "inputs/receptor.pdb" \
  --p2rank-archive "inputs/p2rank_2.5.1.tar.gz" \
  --method P2Rank \
  --profile auto \
  --top-k 5 \
  --threads 4 \
  --minimum-box-size 20 \
  --box-padding 6
```

Do not create, remove, or mark an output directory before running the pack.
If the default `pocket_detection` path already exists, the pack preserves it
and deterministically selects the first absent `pocket_detection-2`,
`pocket_detection-3`, and so on. A workspace marker never authorizes reuse.
Use the validated output path printed by the pack for downstream docking. An
explicit non-default output path must be absent and fails closed if it exists.

`--profile auto` uses verified PDB provenance/method records, never B-factor
variance. Explicit `EXPDTA` is authoritative: crystallography selects `default`,
while predicted/theoretical, NMR, and cryo-EM methods select the
B-factor-independent `alphafold` profile recommended by P2Rank. Without
`EXPDTA`, only an AlphaFold-specific prediction title/disclaimer or an explicit
predicted-model title is accepted; an incidental AlphaFold comparison or
reference is not provenance. Conflicting, unknown, or missing method evidence
requires the user to confirm the source and pass an explicit profile.
The execution pack rejects unsafe archives and internal-state symlinks, wrong
hashes, Java versions below 17, missing surface atom identities, non-finite
scores, invalid probabilities, and boxes outside the downstream 8–100 Angstrom
range.

The authoritative outputs are:

- `pocket_candidates.csv`: rank, score, probability, center, derived size,
  surface-atom count, and lining residues for the retained candidates;
- `pocket_selection.json`: selected rank-1 pocket and exact source/method
  provenance for downstream docking;
- `pocket_validation.json`: execution-pack checks and hashes;
- `selected_pocket_atoms.pdb`: selected pocket-lining surface atoms;
- `p2rank_predictions.csv` and `p2rank.log`: raw engine evidence.

For AutoDock Vina, pass `pocket_selection.json` through the docking pack's
`--pocket-selection` argument. Never copy its center into a newly authored
command or text file; the docking pack verifies the receptor hash and consumes
the receipt directly.

## Acceptance

Do not call the route ready until a real PDB run produces `overall_pass=true`,
at least one candidate, rank 1 with probability in `[0,1]`, a non-empty selected
surface-atom PDB, and a docking box whose source hash exactly matches the
receptor. Report P2Rank as a prediction and retain the option for a user to
replace it with a known experimental site.
