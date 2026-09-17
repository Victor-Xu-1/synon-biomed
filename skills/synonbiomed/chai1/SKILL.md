---
name: chai1
description: >
  Structure prediction for protein, nucleic-acid, and small-molecule complexes
  with the Chai-1 foundation model (Chai Discovery 2024,
  github.com/chaidiscovery/chai-lab). Reach for this skill to predict an
  antibody-antigen or protein-ligand complex from a single FASTA, to re-fold
  designed binders as an AlphaFold-multimer alternative, or to drive
  co-folding from Python for batched campaigns on a GPU.
license: Apache-2.0
category: biomodels
requirements: [gpu]
metadata:
  display-name: Chai-1
  # github.com/chaidiscovery/chai-lab/blob/main/LICENSE: Apache-2.0
  # (relicensed at v0.4.0). verified 2026-06-30
  third_party:
    - kind: weights
      name: Chai-1
      provider: Chai Discovery
      license: Apache-2.0
      terms_url: https://github.com/chaidiscovery/chai-lab/blob/main/LICENSE
    # SKILL.md — `--use-msa-server` / `use_msa_server=True` sends the sequence
    # to the public ColabFold MMseqs2 server. No published ToS — wiki is the
    # closest data-use reference.
    - kind: service
      name: ColabFold MSA server (api.colabfold.com)
      provider: Steinegger Lab
      info_url: https://github.com/sokrypton/ColabFold/wiki
---

# Chai-1

Chai-1 is an all-atom diffusion co-folder in the same family as Boltz-2 and
AlphaFold3: a multi-entity FASTA in, mmCIF plus pTM/ipTM/pLDDT out, with
protein, RNA, DNA, and SMILES-ligand chains all first-class. It and `boltz`
cover the same surface; running both and keeping designs that pass either is a
common consensus filter, and Chai's Python entry point makes it the easier of
the two to embed in a loop. Code and weights are Apache-2.0 — commercial use
including drug discovery is explicitly permitted
(github.com/chaidiscovery/chai-lab).

## Running it

```python
from pathlib import Path
from chai_lab.chai1 import run_inference

Path("complex.fasta").write_text("""
>protein|name=target
MVTPEGNVSLVDESLLVGVTDEDRAVRS...
>protein|name=binder
AIQRTPKIQVYSRHPAENG...
>ligand|name=cofactor
CCCCCCCCCCCCCC(=O)O
""".strip())

candidates = run_inference(
    fasta_file=Path("complex.fasta"),
    output_dir=Path("out/"),
    num_trunk_recycles=3,
    num_diffn_timesteps=200,
    seed=42,
    device="cuda:0",
    use_esm_embeddings=True,
)
print([rd.aggregate_score.item() for rd in candidates.ranking_data])
```

## Running Chai-1 on Modal without losing outputs

For GPU work through `host.compute.create('modal')`, submit a self-contained
job that writes all durable outputs under `out/`. The compute wrapper harvests
`out/`; files written only to `/tmp/results`, `/work/results`, or an arbitrary
temporary directory may be lost when the sandbox exits.

Use this pattern for one complex or a small batch:

```python
from pathlib import Path
from chai_lab.chai1 import run_inference
import json
import shutil

work = Path.cwd()
out_dir = work / "out"
out_dir.mkdir(parents=True, exist_ok=True)

fasta = work / "complex.fasta"
assert fasta.exists(), fasta

run_dir = out_dir / fasta.stem
if run_dir.exists():
    shutil.rmtree(run_dir)
run_dir.mkdir(parents=True, exist_ok=True)

candidates = run_inference(
    fasta_file=fasta,
    output_dir=run_dir,
    num_trunk_recycles=3,
    num_diffn_timesteps=200,
    seed=42,
    device="cuda:0",
    use_esm_embeddings=True,
)

scores = [
    {"model": i, "aggregate_score": float(rd.aggregate_score.item())}
    for i, rd in enumerate(candidates.ranking_data)
]
(out_dir / "scores.json").write_text(json.dumps(scores, indent=2), encoding="utf-8")
```

When submitting through BYOC Modal:

- Pass the FASTA via `inputs=[{"src": "complex.fasta", "dst": "complex.fasta"}]`;
  do not reference `/tmp/input.fasta` unless you explicitly create that file
  inside the remote command.
- Write a script file and upload it as an input; avoid building a long
  `python -c "..."` command by string concatenation.
- One BYOC handle is sequential: submit job N+1 only after job N has reached a
  terminal `compute_done` state. For parallel Chai runs, create one handle per
  concurrent job or put all FASTA files into a single batch script.
- Set the Modal container timeout longer than the expected inference plus
  harvest margin. Always call `result()` or wait for the compute notification
  before the sandbox idle watchdog can remove unrecovered outputs.

The FASTA header is `>{entity_type}|name={id}` with `entity_type` ∈
{`protein`, `rna`, `dna`, `ligand`}; ligand records carry a SMILES string as
the sequence body, and modified residues are written inline as
`...AAK(SEP)AAG...`. From the shell the same job is `chai-lab fold
complex.fasta out/ --use-msa-server`. Without `--use-msa-server` (or
`use_msa_server=True` in Python) the model runs on ESM embeddings alone, which
is faster but typically a few ipTM points behind the MSA-backed run.

`output_dir` receives `pred.model_idx_{0..4}.cif` plus a matching
`scores.model_idx_{N}.npz` per sample with `aggregate_score`, `ptm`, `iptm`,
`per_chain_ptm`, and clash flags. Rank by `aggregate_score`; treat `iptm` >
0.5 as a soft pass for an interface. The function refuses a non-empty `output_dir`, so
clear or rotate it between calls.

## Unset `CHAI_DOWNLOADS_DIR` fails mid-run with PermissionError on a read-only image

Chai downloads ~5 GB on the first inference call (not at install time),
including its own traced ESM2-3B for the embedding path. If
`CHAI_DOWNLOADS_DIR` is unset, the default is inside `site-packages`: on a
read-only image that fails with a confusing `PermissionError` mid-run, and on
a writable one it silently re-downloads ~5 GB into the container on every cold
start. Export the variable to a persisted volume so the download happens once.

## No-MSA mode still loads a 3 B-parameter ESM — same VRAM, not less

`use_esm_embeddings=True` without an MSA still loads a 3-billion-parameter
language model into GPU memory alongside the trunk; it removes the MSA-server
round-trip, not the VRAM cost. If you OOM, drop `num_diffn_timesteps` or fold
fewer chains per call rather than expecting the no-MSA mode to fit a smaller
card.

## Errors worth recognizing

| You see | It means / do this |
|---|---|
| `PermissionError` under `site-packages/chai_lab/...` | `CHAI_DOWNLOADS_DIR` not set on a read-only image — export it to a writable path or the pre-populated mount. |
| `RuntimeError: CUDA out of memory` during ESM embedding | The traced ESM2-3B is loading alongside the trunk — use an 80 GB tier or split chains across calls. |

---

**Next:** filter survivors on confidence/clash metrics or feed them back to
`proteinmpnn` for the next design round.
