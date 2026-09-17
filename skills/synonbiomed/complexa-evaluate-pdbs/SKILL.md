---
name: complexa-evaluate-pdbs
description: >
  Standalone evaluation of an existing PDB directory with Proteina-Complexa.
  Use this skill whenever the user wants to "evaluate PDB files", "re-fold these
  designs", "compute interface pAE", "compute i_pLDDT for a folder",
  "run AF2 / RF3 / ESMFold on my designs", "score binder candidates",
  "designability of this folder", "scRMSD for designs", "motif RMSD for these
  PDBs", "complexa analysis", "complexa evaluate from a PDB directory",
  "evaluate from pdb dir", or score third-party outputs (BindCraft, AlphaProteo,
  RFdiffusion, hand-curated decoys). It picks the correct `evaluate_*.yaml`
  config, wires `++dataset.pdb_dir` and the folding backend, runs
  `complexa analysis` (the evaluate → analyze chain), parses the result CSV,
  reports pass-rates against the right `result_type` thresholds, and emits a
  replayable `eval_manifest.json`. Reach for this skill before hand-rolling
  refolding scripts.
compatibility: "complexa CLI in a governed immutable environment; selected evaluation config, backend weights, and compatible local or remote compute validated"
allowed-tools: search_skills, skill, ask_user, repl, list_compute, manage_environments, manage_packages, bash, read_file, edit_file, save_artifacts
---

<!-- Modified for Synon Biomed. Upstream attribution and terms: docs/licenses/bionemo-agent-toolkit/NOTICE.md. -->

# Complexa Evaluate-PDBs Skill

Score a directory of pre-existing PDB files against the same metrics Proteina-Complexa uses internally. Wraps `complexa analysis <evaluate_config> ++sample_storage_path=<dir>`: the CLI runs the `evaluate` step (refold + interface metrics + monomer metrics) and then the `analyze` step (success thresholds, diversity, pass-rate CSVs). Do **not** run `complexa generate` here — the inputs already exist.

## What this skill enables

- Re-fold a directory of designed PDBs with AF2 (`colabdesign`), RF3 (`rf3_latest`), ESMFold (`esmfold`), or Boltz2 (`boltz2_default`).
- Compute binder interface metrics: `i_pAE`, `min_ipAE`, `i_pTM`, `pLDDT`, binder/complex scRMSD.
- Compute monomer **designability** (ProteinMPNN-redesigned scRMSD) and **codesignability** (original sequence refold scRMSD) as part of binder analysis.
- For AME inputs: joint binder + motif RMSD scoring (motif overlay on refolded structure).
- Aggregate into per-PDB CSVs plus pass-rate summaries using the default thresholds for the `result_type`.

## Step 1: Pre-flight

Always check the exact backend, weights, GPU/provider capacity, input count, and
disk before launching a refold job. Load `complexa-setup` and use its real
CLI/compute checks; no shared preflight helper or preflight JSON is bundled in
this Skill.

```bash
complexa download --status
complexa validate env
complexa validate design <evaluate-config>
nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader
command -v foldseek
command -v mmseqs
```

Resolve from those checks:

- `gpu.available` and `gpu.vram_gb` — colabdesign/RF3 need ≥40 GB; ESMFold tolerates ≥24 GB.
- `env.missing_required` — must include the keys for the chosen folding backend:
  - `colabdesign` → `AF2_DIR`
  - `rf3_latest` → `RF3_CKPT_PATH`, `RF3_EXEC_PATH`
  - `esmfold` → ESMFold weights resolvable
- `tools.{foldseek,mmseqs}` — required by `aggregation.compute_diversity` / `compute_mmseqs_diversity` (both default `true`).

If a required backend, weight, or tool is missing, load `complexa-setup` or
`capability-acquisition` and repair the governed capability boundary. Do not ask
the user to diagnose or manually install it.

## Step 2: Identify the design type

Like `complexa design`, evaluation has one default flow (protein binder) and two extensions (ligand binder, AME). The evaluate config you pass to `complexa analysis` decides everything else (which metrics, which refolder defaults, which thresholds the analyze step applies).

### Default — protein binder

```bash
complexa analysis configs/evaluate_from_pdb_dir.yaml \
    ++sample_storage_path=/abs/path/to/pdbs \
    ++dataset.task_name=02_PDL1 \
    ++result_type=protein_binder \
    ++metric.binder_folding_method=colabdesign \
    ++metric.inverse_folding_model=soluble_mpnn \
    ++run_name=eval_pdl1_af2
```

Use this when the user's PDBs are protein-binder designs (multi-chain, binder is the last chain) or third-party outputs from BindCraft / AlphaProteo / RFdiffusion. Pulls thresholds for `protein_binder` (`i_pAE * 31 ≤ 7.0`, `pLDDT ≥ 0.9`, `scRMSD_ca < 1.5 Å`).

### Extensions — pick the matching config

| Design type | Use the protein-binder default? | Evaluate config | Analyze config | `result_type` | Default backend |
|---|---|---|---|---|---|
| Protein binder | **Yes (default)** | `configs/evaluate_from_pdb_dir.yaml` | `configs/analyze.yaml` | `protein_binder` | `colabdesign` (AF2) |
| Ligand binder (binder + small-molecule) | Same evaluate config, swap 3 overrides | `configs/evaluate_from_pdb_dir.yaml` | `configs/analyze.yaml` | `ligand_binder` | `rf3_latest` |
| AME / motif + ligand (enzyme outputs) | No — needs motif-aware config | `configs/evaluate_ame_from_pdb_dir.yaml` | `configs/analyze_motif_binder.yaml` | `motif_ligand_binder` | `rf3_latest` |

**Extending to ligand binder** (same evaluate config as default, three override swaps):

```bash
complexa analysis configs/evaluate_from_pdb_dir.yaml \
    ++sample_storage_path=/abs/path/to/pdbs \
    ++dataset.task_name=39_7V11_LIGAND \
    ++result_type=ligand_binder \
    ++metric.binder_folding_method=rf3_latest \
    ++metric.inverse_folding_model=ligand_mpnn \
    ++run_name=eval_v11_rf3
```

**Extending to AME** (different config; ligand auto-completion gotcha — see Step 4):

```bash
complexa analysis configs/evaluate_ame_from_pdb_dir.yaml \
    ++sample_storage_path=/abs/path/to/pdbs \
    ++dataset.task_name=M0096_1chm \
    ++run_name=eval_ame_chm
```

See `reference/eval_configs.md` for the full matrix (every `result_type`, every threshold default, every supported folding backend).

## Step 3: Resolve inputs and material choices

Resolve the PDB directory, design type, target/task name, and compatible backend
from the request, files, selected target definition, and live readiness. Do not
turn every parameter into an interview. When two to four viable backends or
evaluation scopes materially differ in scientific coverage, resource demand,
cost, latency, or outputs, call `ask_user` once after preflight. Recommend one
and explain each option's advantage, limitation, compute/service requirement,
and expected metrics/structures in the conversation language.

For AME, inspect whether the ligand representation matches the checked RF3
input contract before execution. Repair an agent-owned preparation issue rather
than asking the user to certify an internal residue rename.

## Step 4: Run evaluate → analyze

Prefer `complexa analysis` (the evaluate→analyze chain) — it reuses the same config for both steps and writes a single log dir.

```bash
# Protein binder PDB dir, AF2 refold
complexa analysis configs/evaluate_from_pdb_dir.yaml \
  ++sample_storage_path=/abs/path/to/pdbs \
  ++dataset.task_name=02_PDL1 \
  ++metric.binder_folding_method=colabdesign \
  ++metric.inverse_folding_model=soluble_mpnn \
  ++result_type=protein_binder \
  ++run_name=eval_pdl1_af2
```

For ligand binders flip `binder_folding_method=rf3_latest`, `inverse_folding_model=ligand_mpnn`, `result_type=ligand_binder`. For AME use `configs/evaluate_ame_from_pdb_dir.yaml` — see `reference/eval_configs.md` for full worked examples.

If you need to inspect output between stages, run them separately. The configs above are shared between `evaluate` and `analyze`:

```bash
complexa evaluate configs/evaluate_from_pdb_dir.yaml ++sample_storage_path=/abs/path/to/pdbs ++run_name=eval_pdl1_af2
complexa analyze  configs/evaluate_from_pdb_dir.yaml ++run_name=eval_pdl1_af2
```

Dry-run first if the user is unsure (no GPU work happens; the planned file walk + invocation prints):

```bash
complexa analysis configs/evaluate_from_pdb_dir.yaml ++sample_storage_path=/abs/path/to/pdbs ++dryrun=true
```

### Direct module invocation (debug fallback)

`complexa evaluate` / `analyze` are subprocess wrappers around the Hydra
modules with logging + parallel job splitting bolted on. To attach a debugger
or run under a profiler, invoke the module directly:

```bash
python -m proteinfoundation.evaluate \
    --config-path "$(realpath configs)" \
    --config-name evaluate_from_pdb_dir \
    ++sample_storage_path=/abs/path/to/pdbs \
    ++dataset.task_name=02_PDL1 \
    ++metric.binder_folding_method=colabdesign \
    ++run_name=eval_debug
```

For normal one-shot runs prefer `complexa analysis` — you get the shared log
dir and a single replayable invocation, instead of having to thread the same
overrides through two `python -m` calls.

## Step 5: Parse results

Output lands under `./evaluation_results/${run_name}/`:

- Per-PDB metrics CSV — `*_results_*.csv` (one row per input PDB × `sequence_types`).
- Pass-rate summaries — written by the `analyze` step (e.g. `res_designability.csv`, `res_filter_ligand_pass_*.csv`, `success_criteria_*.json`).
- Diversity output — FoldSeek/MMseqs2 cluster files when the checked config
  enables them. Empty diversity values are unavailable evidence, not low or
  zero diversity. Before reporting diversity, verify the exact configured
  executable with a bounded version call and confirm the result column contains
  numeric values. Otherwise report it as not computed and repair through
  `complexa-setup` or `capability-acquisition` before rerunning.

Summarize to the user:

- Per-PDB row count and number of successful designs vs total.
- Default-threshold pass rate by `result_type` (e.g. for `protein_binder`: `i_pAE*31 <= 7.0 AND pLDDT >= 0.9 AND scRMSD_ca < 1.5`).
- Top 5 designs by primary metric (`i_pAE` for protein, `min_ipAE` for ligand, `motif_rmsd_pred_all` for AME).

## Step 6: Preserve and publish results

Preserve the exact input hashes, resolved config and result type, immutable code
and weight identity, backend, overrides, managed environment/provider receipts,
result CSV paths, refolded structures, tool availability, and rejected inputs.
Publish the editable per-design metrics table, requested refolded structures,
and readable report with `save_artifacts`. Keep raw logs and validation receipts
as working evidence unless the user asks for them. Do not call an unbundled
manifest helper.

## Most common overrides

| Override                                       | Effect                                                                |
|------------------------------------------------|-----------------------------------------------------------------------|
| `++sample_storage_path=<dir>`                  | The directory of PDBs to evaluate (required).                         |
| `++dataset.task_name=<name>`                   | Target / AME task name. Resolves target PDB + (for AME) motif contigs.|
| `++metric.binder_folding_method=<backend>`     | `colabdesign` / `rf3_latest` / `esmfold` / `boltz2_default`.          |
| `++metric.inverse_folding_model=<model>`       | `protein_mpnn` / `soluble_mpnn` / `ligand_mpnn`.                      |
| `++metric.sequence_types=[self,mpnn,mpnn_fixed]` | Which sequence flavors to refold.                                   |
| `++metric.num_redesign_seqs=N`                 | ProteinMPNN/LigandMPNN redesign count.                                |
| `++metric.compute_pre_refolding_metrics=true`  | Add bioinformatics/TMOL/HBPLUS metrics on the input structures.       |
| `++metric.keep_folding_outputs=true`           | Save the refolded PDBs (large, but useful for inspection).            |
| `++result_type=<type>`                         | Override default thresholds: `protein_binder` / `ligand_binder` / `motif_ligand_binder`. |
| `++aggregation.success_thresholds.<…>`         | Tighten or loosen specific thresholds (see `reference/eval_configs.md`). |
| `++eval_njobs=N`                               | Parallel GPUs for the evaluate step.                                  |
| `++dryrun=true`                                | Plan without running any folding.                                     |
| `++file_limit=N`                               | Cap input PDBs (handy for first-pass smoke tests).                    |

## Hardware

- **GPU**: ≥1 CUDA GPU. AF2 (`colabdesign`) and RF3 (`rf3_latest`) need ≥40 GB VRAM (A100/H100/L40S). ESMFold runs on ≥24 GB. Multi-GPU via `++eval_njobs=N`.
- **CPU/disk**: 24 CPUs default (`ncpus_: 24`). Each refolded PDB + intermediate output is ~1–5 MB; `keep_folding_outputs=true` can balloon to tens of GB for thousands of inputs.
- Use the checked backend documentation, live provider snapshot, and a bounded
  representative input to describe memory and wall-clock; no shared hardware
  table is bundled here.

## Troubleshooting

- **`Error: Config file not found`** — paths are relative to the repo root; `cd` to the repo before invoking `complexa analysis`.
- **`compute_motif_binder_metrics=True` but `result_type=protein_binder`** — `result_type` and the underlying `compute_*_metrics` must agree. Use `evaluate_ame_from_pdb_dir.yaml` for AME inputs rather than mutating `evaluate_from_pdb_dir.yaml`.
- **RF3 shape errors on AME PDBs** — RF3 tries to auto-complete the ligand atoms from CCD. Rename the ligand residue to `L:0` in every input PDB before evaluation; see the snippet in `README.md` (`atom_array.res_name[ligand_mask] = "L:0"`).
- **Diversity column is present but empty** — verify the configured
  `FOLDSEEK_EXEC`/`MMSEQS_EXEC` through bounded version calls. Repair via
  `complexa-setup`, or explicitly disable those metrics so their absence is
  intentional; never turn blanks into zero.
- **All pass-rates are 0%** — check the `binder_folding_method` matches the target type (RF3 for ligand, AF2 for protein) and that `++dataset.task_name` resolves to the correct reference PDB (`complexa target show <name>` to verify).

## Reference

Full evaluate/analyze config matrix, every supported `result_type`, per-threshold defaults, and worked examples (protein binder / ligand binder / AME): see `reference/eval_configs.md`.
