---
name: complexa-design
description: >
  Run an end-to-end Proteina-Complexa campaign after the protein-design strategy
  has selected a supported protein-surface binder, ligand-binding protein, or
  motif-plus-ligand scaffolding route. It drives generation, inverse folding,
  refolding, filtering, and diversity analysis while preserving exact pipeline,
  model, target, and per-design provenance.
compatibility: "complexa CLI in a governed immutable environment; selected config and weights validated; compatible local or remote CUDA compute"
allowed-tools: search_skills, skill, ask_user, repl, list_compute, manage_environments, manage_packages, bash, read_file, edit_file, save_artifacts
---

<!-- Modified for Synon Biomed. Upstream attribution and terms: docs/licenses/bionemo-agent-toolkit/NOTICE.md. -->

# Complexa Design Skill

Drive the full four-stage `complexa design` pipeline: generate (flow matching +
search) -> filter (top-N by reward) -> evaluate (refold with AF2/RF3/ESMFold) ->
analyze (success rate, FoldSeek/MMseqs diversity). Pick the right pipeline
config for the design intent, validate the run upfront so the user does not
discover a missing ckpt mid-folding, run it, and emit a replayable manifest +
per-design success CSV.

## What this skill enables

- Protein binder design for protein targets (AF2 reward + ColabDesign refold).
- Ligand binder design for small-molecule targets (RF3 reward + RF3 refold).
- AME motif scaffolding with ligand context (motif + ligand features, RF3).
- Search-based optimization: single-pass, best-of-n, beam-search, fk-steering, mcts.
- Refold backends: ColabDesign (AF2), RF3, Boltz2, ESMFold (fast iteration).
- Pass-rate + diversity analysis with per-`result_type` thresholds.

## Step 1: Pre-flight

Always run the installed Complexa CLI's own status and validation commands
before launching a design — generation needs the GPU and the right checkpoint,
evaluation needs AF2/RF3 weights and tool binaries. If the CLI or environment is
absent, use the governed capability-acquisition path instead of calling an
unbundled helper.

```bash
complexa download --status
complexa validate design <selected-pipeline-config>
```

Read the exact status and validation output and stop this route if any of these
are missing for the chosen pipeline:

- `gpu.available: false` -> all pipelines fail.
- `gpu.vram_gb < 40` -> generation OOMs at default `batch_size: 16`; lower to 8.
- `ckpts.complexa[.ckpt]` -> required for protein binder.
- `ckpts.complexa_ligand[.ckpt]` -> required for ligand binder.
- `ckpts.complexa_ame[.ckpt]` -> required for AME.
- `env.AF2_DIR` missing -> protein binder **generation AND eval both fail**. AF2
  is the `af2folding` search reward (loaded at generation time), not just the
  `colabdesign` refold backend — so a missing `AF2_DIR` crashes at sample 0,
  before any structure is produced. This is why `complexa download --complexa`
  alone is insufficient; you also need `--all` (which brings AF2).
- `env.RF3_CKPT_PATH` or `env.RF3_EXEC_PATH` missing -> ligand binder / AME
  generation reward and default eval (`rf3_latest`) fail.

If a checkpoint is missing, load `complexa-setup` or `capability-acquisition`
and follow its governed provisioning and approval path. Do not ask the user to
diagnose the environment or manually reproduce an installation command.

## Step 2: Pick the pipeline

Complexa has **one default pipeline (protein binder) and two extensions**
(ligand binder, AME / enzyme). The pipeline is selected entirely by the
`configs/search_*_pipeline.yaml` you pass to `complexa design` — each YAML
pins its own model checkpoint, autoencoder, targets dict, default reward, and
default refold backend. Switching pipelines is just "swap the config path and
the target name comes from a different dict".

### Protein-surface binder

```bash
complexa design configs/search_binder_local_pipeline.yaml \
    ++run_name=pdl1_v1 ++generation.task_name=02_PDL1
```

Use this only when the verified design class is a binder against a protein
surface. The config pins `complexa.ckpt` + `complexa_ae.ckpt`, reads
targets from `configs/targets/targets_dict.yaml`, rewards with AF2
(`af2folding`), inverse-folds with SolubleMPNN, and evaluates with
ColabDesign / AF2. Use this pipeline only when the verified scientific design
class is a protein-surface binder; do not treat missing task detail as evidence
for that class.

### Extension A — ligand binder (small-molecule pocket)

```bash
complexa design configs/search_ligand_binder_local_pipeline.yaml \
    ++run_name=v11_v1 ++generation.task_name=39_7V11_LIGAND \
    ++metric.binder_folding_method=rf3_latest
```

Use this only when the verified design class is a protein designed around a
small-molecule ligand or cofactor and the selected target entry includes the
required ligand context. Do not infer the design class from a target-name
suffix. The config
**also activates LoRA** (`r=32`, `lora_alpha=64`) which is required for the
released ligand checkpoint — leave the `lora:` block alone.

### Extension B — AME / motif + ligand (enzyme scaffolding)

```bash
complexa design configs/search_ame_local_pipeline.yaml \
    ++run_name=ame_chm ++generation.task_name=M0096_1chm
```

Use this only when the verified design class is motif scaffolding with ligand
context and the target entry provides the fixed motif mapping and ligand
geometry. The config sets
`env_vars.USE_V2_COMPLEXA_ARCH=True` (the CLI runner injects it into the
subprocess) and uses both `MotifFeatures` and `LigandFeatures`. Default search
is `single-pass`; switch to `best-of-n` only if you also enable the
`CompositeRewardModel` (commented out in `ame_generate.yaml`).

### Pipeline cheat sheet — what changes when you switch

| Knob | Protein binder (default) | Ligand binder | AME (enzyme) |
|---|---|---|---|
| **Pipeline YAML** | `configs/search_binder_local_pipeline.yaml` | `configs/search_ligand_binder_local_pipeline.yaml` | `configs/search_ame_local_pipeline.yaml` |
| **Model ckpt** | `complexa.ckpt` | `complexa_ligand.ckpt` | `complexa_ame.ckpt` |
| **Autoencoder ckpt** | `complexa_ae.ckpt` | `complexa_ligand_ae.ckpt` | `complexa_ame_ae.ckpt` |
| **Targets dict** | `configs/targets/targets_dict.yaml` | `configs/targets/ligand_targets_dict.yaml` | `configs/design_tasks/ame_dict_v2.yaml` |
| **Task-name pattern** | `<NN>_<NAME>` (e.g. `02_PDL1`, `22_DerF21`) | `<NN>_<PDB>_LIGAND` (e.g. `39_7V11_LIGAND`) | `M####_<pdb>` (e.g. `M0096_1chm`) |
| **`USE_V2_COMPLEXA_ARCH`** | (unset → v1) | (unset → v1) | `"True"` (set in YAML) |
| **LoRA** | (none) | required (`r=32, alpha=64`) | required |
| **Default search algo** | `best-of-n` | `best-of-n` | `single-pass` |
| **Reward model** | AF2 (`af2folding`) | RF3 (`rf3folding`) | `null` (no reward at default) |
| **Inverse folder** | `soluble_mpnn` | `ligand_mpnn` | `ligand_mpnn` |
| **Default refold backend** | `colabdesign` (AF2) | `rf3_latest` | `rf3_latest` |
| **Analysis `result_type`** | `protein_binder` | `ligand_binder` | `motif_ligand_binder` |
| **Required ckpts (`complexa download`)** | `--complexa --all` (AF2 in community) | `--complexa-ligand --all` (RF3 in community) | `--complexa-ame --all` (RF3 in community) |

For the full per-pipeline breakdown (reward weights, success thresholds,
analysis modes), see [reference/pipelines.md](reference/pipelines.md).

## Step 3: Resolve parameters

Derive routine run names, validated config paths, and implementation defaults
from the selected pipeline and current evidence. Use `ask_user` only after
preflight when two or more viable choices materially change the scientific
objective, search breadth, expected quality, compute demand, cost, or
deliverables. In that case recommend one route and explain each option's
principal advantage, limitation, resource requirement, and expected output in
the conversation language. Do not interrupt for a reversible routine setting.

- **Target name** — must be a key in the relevant dict (`targets_dict.yaml` for
  protein binder, `ligand_targets_dict.yaml` for ligand, `ame_dict_v2.yaml` for
  AME). Confirm it exists *before* running — do not let the pipeline fail with
  "Unknown target" after generation starts. Check membership directly:

  ```bash
  complexa target show <name>                       # prints the entry, or errors if absent
  # or grep the dict:  rg '^\s*<name>:' configs/targets/targets_dict.yaml
  ```

  **If the target is not in the dict, hand off to `complexa-target` first, then
  come back here** — do not stall on "Unknown target". The clean chain is:

  1. Detect the requested target's absence with `complexa target show <name>`.
  2. Switch to the `complexa-target` skill: gather the PDB source, chain/residue
     spec (or ligand SMILES), hotspots, and binder length, then register the entry.
  3. Verify with `complexa target show <name>` and `complexa validate target <config> --target <name>`.
  4. Return to this skill at Step 4 with the now-valid target name.
- **Run name** — a short identifier appended to the output dir (e.g. `pdl1_v1`).
- **Search algorithm** — default to `beam-search` with `beam_width=8` and
  `n_branch=4` for production. Use `single-pass` for a quick smoke test.
- **Evaluation refold backend** — protein binder defaults to `colabdesign`
  (AF2); ligand/AME default to `rf3_latest`. Use `esmfold` for fast iteration
  (worse but seconds per sample). ESMFold weights ship inside `complexa download
  --all`, but that sub-download needs `HF_TOKEN` set in `.env` or it silently
  rate-limits and leaves no ESMFold dir — confirm `community_models/ckpts/ESMFold`
  exists (`complexa download --status`) before relying on the `esmfold` backend.

## Step 4: Validate

Validate before running. This is cheap (seconds) and catches missing ckpts,
missing reward/eval weights, and a missing target entry — any of which would
otherwise abort the pipeline mid-evaluation after hours of generation.

**`complexa validate design` does NOT accept `++` Hydra overrides.** The
`validate` subcommand only takes `<type> <config> [--target NAME]`; passing
`++generation.task_name=…` makes it exit with "unrecognized arguments". It also
reads the config YAML *as written* — overrides you plan to pass to `complexa
design` are not applied during validation. So validate the bare config, and use
`--target` (only available on `validate target`) to check a target other than
the config's own `generation.task_name`:

```bash
# Validates ckpts + reward/eval weights, and the target named *in the config*.
complexa validate design configs/search_binder_local_pipeline.yaml

# To check a specific target (e.g. one you will pass via ++generation.task_name),
# validate it explicitly — this path DOES check dict membership:
complexa validate target configs/search_binder_local_pipeline.yaml --target 02_PDL1
```

The validator returns non-zero on errors and prints a pass/fail report. Re-run
until it returns clean.

What `validate design` does **not** cover (verify these yourself):

- Override-dependent settings — e.g. if you will pass
  `++metric.binder_folding_method=esmfold`, validation still checks whatever the
  config's default backend is. Confirm your overrides match the weights you
  downloaded.
- The target you pass at run time via `++generation.task_name=` — validate it
  separately with `validate target … --target <name>` (above), or confirm it is
  in the dict directly (`complexa target show <name>`).

## Step 5: Run the pipeline

`complexa design` is the right tool for the full 4-stage run — it orchestrates
`generate → filter → evaluate → analyze` as sequential subprocesses with a
shared run name, log dir, and multi-GPU split (see `run_design_pipeline` in
`src/proteinfoundation/cli/cli_runner.py`). Re-implementing that manually
loses the per-stage log routing and progress prints.

Use `++` (forced) Hydra overrides; they apply to all stages. The minimal
production protein-binder invocation:

```bash
complexa design configs/search_binder_local_pipeline.yaml \
    ++run_name=pdl1_v1 \
    ++generation.task_name=02_PDL1 \
    ++generation.search.algorithm=beam-search \
    ++generation.search.beam_search.beam_width=8 \
    ++metric.binder_folding_method=colabdesign
```

For ligand binder / AME, swap the pipeline YAML and target name per Step 2's
cheat sheet — every other override above is pipeline-agnostic and can be
reused as-is.

Add `--verbose` to stream logs to the terminal instead of `./logs/`. The skill
does not poll progress — the user re-invokes if they want a status; point them
at `complexa status` and `./logs/design_pipeline_*/`.

### Direct module invocation (debug fallback)

To debug a single stage without pipeline orchestration, invoke the underlying
Hydra module directly. `complexa generate CONFIG` is just a logged subprocess
wrapper around this:

```bash
python -m proteinfoundation.generate \
    --config-path "$(realpath configs)" \
    --config-name search_binder_local_pipeline \
    ++run_name=debug_pdl1 \
    ++generation.task_name=02_PDL1
```

Same pattern for `proteinfoundation.{filter,evaluate,analyze}`. Use this when
you want to attach `ipdb`, run under `nsys`, or skip the pipeline log dir.
Prefer `complexa generate/filter/evaluate/analyze` for normal one-shot runs
(you get logging and parallel job splitting for free).

**AME-specific gotcha (when running AME with RF3 refold)**: RF3 will try to
complete missing atoms on the ligand based on its CCD code, which produces
shape errors in RMSD calculations and the wrong structure. Before RF3 sees the
PDB, rename the ligand residue to `L:0` so RF3 treats it as a generic ligand
and skips atom completion. If your AME generation outputs already encode the
ligand this way (the canonical Complexa pipeline does), no extra step is
needed; otherwise patch each PDB with `atomworks.io`:

```python
from atomworks.io import load_any, to_pdb_file
atom_array = load_any("my_design.pdb")[0]
ligand_mask = atom_array.chain_id == "A"
atom_array.res_name[ligand_mask] = "L:0"
to_pdb_file(atom_array, "my_design_rf3_ready.pdb")
```

Same applies if you're piping AME outputs into the `complexa-evaluate-pdbs`
skill with an RF3 backend. Skip the rename for AF2/colabdesign refold or for
non-AME pipelines.

Wall-clock at default (`nsteps=400`, `beam_width=8`, `batch_size=16`, 100
designs, colabdesign eval) is ~30–120 minutes on a single A100/H100.

## Step 6: Collect results

Outputs land in two directories. Surface both:

```bash
ls ./inference/${CONFIG_STEM}_${TASK}_*${RUN_NAME}/   # generated PDBs + filter
ls ./evaluation_results/${RUN_NAME}/                  # per-design CSV + analysis
```

Read the combined results CSV and summarize:

```bash
ls ./evaluation_results/*/binder_results_*_combined.csv
ls ./evaluation_results/*/motif_binder_results_*_combined.csv  # AME
ls ./evaluation_results/*/res_filter_*_pass_*.csv              # success rate
ls ./evaluation_results/*/res_div_foldseek_*.csv               # FoldSeek diversity
```

Pull the success rate from `res_filter_binder_pass_*.csv`, the per-design
metrics (interface pAE, pLDDT, scRMSD) from the combined CSV, and FoldSeek
TM-score diversity from `res_div_foldseek_*.csv`.

> **Empty diversity ≠ zero diversity.** FoldSeek diversity is only computed when
> the `foldseek` binary is installed (`FOLDSEEK_EXEC` in `.env`). If it is
> missing, the analysis step **silently** emits the diversity column with empty
> values — it looks like a computed result but is a tool-missing failure. Before
> reporting diversity, verify the configured `FOLDSEEK_EXEC` with a bounded
> version call and confirm the output table contains numeric values. If either
> check fails, report that diversity was not computed and use the governed
> capability-acquisition path before rerunning `complexa analyze`; never report
> empty values as zero diversity.

Report top-N designs by
i_pAE (protein binder) or min_ipAE (ligand binder).

## Step 7: Preserve reproducibility evidence

Preserve the exact immutable code/weight identity, selected pipeline config,
task-relative inputs, full executed command, seed and overrides, managed
environment generation, runtime receipts, output directories, and result CSVs.
Join every structure and metric by the stable design ID, then publish the
editable structures, sequence table, metrics table, and readable report with
`save_artifacts`. Keep raw logs and machine-readable validation as working
evidence unless the user asks for them. Do not call an unbundled manifest helper
or invent a manifest that was not produced by the executed route.

## Most-common overrides

The 10 overrides that cover ~90% of runs. Full reference (every key, type,
default) is in [reference/overrides.md](reference/overrides.md).

| Override | Default | What it controls |
|----------|---------|------------------|
| `++generation.task_name=<name>` | (per config) | Which target / motif task to design for |
| `++run_name=<str>` | (config stem) | Output dir suffix and CSV tag |
| `++generation.search.algorithm=beam-search` | `best-of-n` (binder/ligand), `single-pass` (AME) | Search strategy |
| `++generation.search.beam_search.beam_width=8` | `4` | Beam-search width (more = better designs, slower) |
| `++generation.args.nsteps=200` | `400` | Diffusion steps (fewer = faster, lower quality) |
| `++generation.dataloader.batch_size=8` | `16` | Drop to 8 on a 40GB GPU |
| `++generation.filter.filter_samples_limit=500` | `1000` | Top-N samples to keep after filtering |
| `++metric.binder_folding_method=esmfold` | `colabdesign` (binder), `rf3_latest` (ligand/AME) | Evaluation refold backend |
| `++metric.num_redesign_seqs=8` | `2` | ProteinMPNN/LigandMPNN/SolubleMPNN sequences per design |
| `++aggregation.success_thresholds.i_pAE.threshold=10.0` | `7.0` (protein binder) | Loosen / tighten success criteria |

## Hardware requirements

| Resource | Minimum | Recommended |
|----------|---------|-------------|
| GPU | 1x CUDA GPU, 40 GB VRAM | A100 / H100 / L40S, 80 GB VRAM |
| CPUs | 16 | 24 (the `ncpus_` default in every pipeline config) |
| Disk | 50 GB at `./inference/` + `./evaluation_results/` | 200 GB for sweep runs |
| RAM | 32 GB | 64 GB+ |

Typical wall-clock for 100 designs, `beam_width=8`, default `nsteps=400`:

- Protein binder + colabdesign refold: ~60–120 min on 1x A100/H100.
- Ligand binder + RF3 refold: ~90–180 min (RF3 dominates).
- AME + RF3 refold: ~120–240 min.
- Any pipeline + ESMFold refold: ~30–60 min (fast iteration).

Bumping `gen_njobs=2` and `eval_njobs=2` may reduce wall-clock on a verified
two-GPU host; measure the selected pipeline rather than assuming linear speedup.

## Troubleshooting (common cases)

| Symptom | Cause | Fix |
|---------|-------|-----|
| `CUDA out of memory` in generate | `batch_size: 16` too big on 40GB GPU | `++generation.dataloader.batch_size=8` |
| `CUDA out of memory` in evaluate | AF2 / RF3 batched too aggressively | `++eval_njobs=1` and `++metric.num_redesign_seqs=2` |
| `InterpolationKeyError: AF2_DIR` | colabdesign eval but `.env` does not set `AF2_DIR` | Set `AF2_DIR` in `.env` or `++metric.binder_folding_method=esmfold` |
| `InterpolationKeyError: RF3_CKPT_PATH` | RF3 eval but RF3 not installed | `complexa download --all` or switch eval backend |
| `KeyError: 'task_name' not in target_dict_cfg` | Target not in `targets_dict.yaml` / `ligand_targets_dict.yaml` / `ame_dict_v2.yaml` | Use `complexa-target` skill to add it |
| 0 designs pass success thresholds | Defaults too strict for this target | Loosen via `++aggregation.success_thresholds.*` |

For the full list (chain-ID mismatches, hotspot residues, ligand residue
renaming for RF3, missing inverse-folding models, etc.) see
[reference/troubleshooting.md](reference/troubleshooting.md).

---

For per-pipeline details (which model, reward, inverse folding, evaluation
backend, `result_type`, LoRA settings, `USE_V2_COMPLEXA_ARCH` toggle), see
[reference/pipelines.md](reference/pipelines.md).

For the full override reference (every `generation.*`, `metric.*`,
`aggregation.*` key with type, default, example, and effect), see
[reference/overrides.md](reference/overrides.md).

For all troubleshooting cases (cause + fix + source), see
[reference/troubleshooting.md](reference/troubleshooting.md).
