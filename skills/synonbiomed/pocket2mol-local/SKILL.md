---
name: pocket2mol-local
description: Install, validate, and run the official Pocket2Mol pocket-conditioned 3D molecular generator on a compatible local GPU. Use only after Pocket2Mol is selected for a verified protein pocket; this Skill owns existing-environment discovery, machine inspection, official source and checkpoint acquisition, compatibility repair, representative generation, and normalized editable outputs.
implementation-identities: [Pocket2Mol]
license: MIT
tags: [molecular-generation, pocket-conditioned, gpu, pocket2mol]
keywords: [Pocket2Mol, pocket autoregressive generation, pocket-conditioned molecule generation, local GPU molecule design, 口袋自回归生成, 基于口袋的分子生成, 本地GPU分子设计]
allowed-tools: skill, web_search, web_fetch, request_network_access, download_public_scientific_file, manage_environments, manage_packages, ask_user, bash, python, read_file, save_artifacts
required-capabilities: [pocket-conditioned-molecule-generation, gpu]
setup-evidence-urls:
  - https://pytorch-geometric.readthedocs.io/en/stable/install/installation.html
preferred-execution-assets:
  - scripts/acquire_checkpoint.py
  - scripts/run_pocket2mol.py
critical-constraints:
  - Before any installation, call manage_environments in list mode with Pocket2Mol runtime dependencies so the current CPU, memory, GPU/VRAM, and reusable environments are observed in this task. Reuse a compatible verified environment instead of installing again.
  - Use the official pengxingang/Pocket2Mol source and its official pretrained checkpoint. Record immutable source identity and file hashes; do not substitute an unrelated fork or an invented Pocket2Mol+ product name.
  - Acquire an official Google Drive folder checkpoint only with the bundled `scripts/acquire_checkpoint.py` asset and the exact folder URL from the completed official README. Never rewrite a `/drive/folders/` URL into `uc?id=`, wget, curl, or single-file gdown syntax.
  - The upstream Python 3.8, PyTorch 1.10.1, CUDA 11.3, PyG 2.0 environment is historical evidence, not a command to install an incompatible stack on a newer GPU. Resolve a current PyTorch and PyG stack that supports the observed GPU before mutation.
  - 'Before the first GPU mutation, deeply read the declared current PyG matrix; snippets and memory are not setup evidence. Match the official PyTorch index and PyG wheel page to the observed GPU and exact framework build. For a Blackwell `sm_120` device, reject CUDA 12.1/12.4 builds: official support starts with PyTorch 2.7 and CUDA 12.8, and the chosen build must still prove `sm_120` in its compiled architecture list.'
  - GPU presence, `torch.cuda.is_available()`, or a successful tensor allocation is not an architecture witness. Compare the observed device capability with the installed framework's compiled architecture list and run a representative compiled operation; an unsupported-architecture warning requires a current official wheel or source-build repair before Pocket2Mol execution, never a silent CPU rewrite of the reviewed wrapper.
  - Environment creation, imports, CUDA visibility, checkpoint loading, or a successful help command do not prove Pocket2Mol works. Mark it ready only after the bundled wrapper produces and validates at least one unique three-dimensional molecule from a representative real pocket.
  - Before representative generation, load `binding-mode-analysis` and use its bundled chain-aware execution asset to create a passing `synon.binding-pocket-handoff.v1` receipt. Never derive a pocket center with an ad hoc residue loop or by averaging the whole structure; the selected non-polymer ligand identity, chain, residue number, heavy-atom count, contacts, and box must remain auditable.
  - Do not fall back to ligand enumeration or another generator after an ordinary install or compatibility failure. Inspect the exact failure, close only a repeated non-progressing tactic, preserve completed source and environment evidence, and keep repairing the selected Pocket2Mol route while a materially different viable recovery remains. Ask only if a genuinely user-owned compute or data decision remains.
---

# Pocket2Mol Local

Run the selected Pocket2Mol route through Synon Biomed's managed environment
and public-file authorities. Do not ask the user to execute setup commands.

## 1. Inspect before installing

Call `manage_environments` in `list` mode with these dependency identities:
`torch`, `torch_geometric`, `torch_scatter`, `torch_sparse`, `torch_cluster`,
`rdkit`, `biopython`, `easydict`, `lmdb`, and `pyyaml`. The result is the
authority for existing environments and the current CPU, memory, and GPU/VRAM.
Also inspect any still-valid answered engine choice. If a compatible environment
already exists, reuse it and proceed to a representative run.

The official repository documents one CPU core for sampling and a CUDA GPU, but
does not publish a universal RAM or VRAM minimum. Do not invent those numbers.
Use the observed machine snapshot, checkpoint size, a one-sample representative
run, and measured peak behavior to establish local feasibility.

## 2. Resolve the official implementation

Read the current official repository and checkpoint instructions before
downloading:

- source: `https://github.com/pengxingang/Pocket2Mol`
- checkpoint instructions:
  `https://raw.githubusercontent.com/pengxingang/Pocket2Mol/master/ckpt/README.md`

Resolve the current immutable commit and official archive URL from those
sources. Download the archive and checkpoint only through
`download_public_scientific_file`, after the exact URLs appear in completed
source results. Preserve the source revision, archive SHA-256, checkpoint
SHA-256, and upstream license. Extract into the task workspace; never install
or write into the Skill directory.

The official checkpoint is published as a Google Drive folder rather than one
direct file URL. A folder ID is not a file ID: never guess
`drive.google.com/uc?id=<folder-id>` and never declare all network access broken
from that response. First use the exact official folder URL from the completed
README source. Install the current `gdown` transport client as a small support
package in the selected managed environment when it is absent (leave scientific
`implementation` blank), then execute the bundled acquisition asset:

```bash
python "${SYNON_SKILL_DIR}/scripts/acquire_checkpoint.py" \
  --folder-url "exact official /drive/folders/ URL" \
  --output-dir "task-relative checkpoint directory" \
  --expected-filename pretrained.pt
```

The asset preserves partial bytes, always uses folder mode, rejects file-ID
rewrites, and emits a bounded JSON receipt only after the expected nonempty
model file is not HTML/XML and has been hashed. Request access to the official
domain and any exact redirect host reported by the failed attempt, then rerun
the same asset so it resumes. A user-provided local path is a last user-owned
recovery choice after the folder-aware asset and an officially linked mirror
have each produced exact blocking evidence—not the default response to one
failed URL.

## 3. Build one compatible managed environment

Use the historical upstream matrix only as compatibility evidence:
Python 3.8, PyTorch 1.10.1, CUDA 11.3, PyG 2.0, RDKit, BioPython, EasyDict,
LMDB, PyYAML, NumPy, SciPy, scikit-learn, and tqdm. First determine the current
GPU architecture and the currently supported official PyTorch/PyG wheel set.
Then use `manage_environments` and `manage_packages`; never run pip or conda in
Python/Bash.

Keep compiled PyG packages (`torch_scatter`, `torch_sparse`, `torch_cluster`)
matched to the exact PyTorch and CUDA family. Use only reviewed public HTTPS
package sources accepted by the managed package tool. Preserve an official
package-scoped conda channel when one is required; for PyG binary wheels use
the structured `pip_find_links` field rather than embedding `-f` or
`--find-links` in a package string. Install the framework first and compiled
extensions in a later `pip_phases` entry with `--no-build-isolation` only when
the official build contract requires it. Bind the expected imports as
`import_names`. After installation,
verify imports, `torch.cuda.is_available()`, the observed GPU name and
capability, `torch.cuda.get_arch_list()`, a representative compiled CUDA
operation, checkpoint readability, and Pocket2Mol model construction. Treat an
architecture warning or a device capability absent from the compiled
architecture contract as incompatible even when allocation succeeds. Recheck
the current official framework selector and install a matching wheel or build;
do not rewrite the reviewed wrapper to use CPU. If an old serialization default
blocks the official checkpoint, use PyTorch's documented trusted-checkpoint
compatibility control; do not edit or reserialize the official checkpoint.

For Blackwell devices reporting compute capability `12.0`, an environment
proposal using `pytorch-cuda=12.1`, CUDA 12.4, or a framework whose compiled
architecture list ends at `sm_90` is known-incompatible and must not be
mutated. Resolve a current official PyTorch wheel family at or above the
Blackwell baseline (PyTorch 2.7 with CUDA 12.8), then select the matching PyG
wheel page shown by the current stable PyG matrix. This is a compatibility
floor, not a fixed version: prefer a newer supported stable pair when the
current driver and PyG matrix support it. Use the exact official wheel index
and wheel page as structured package-source fields, and pin the coherent
framework/CUDA pair so a generic package solver cannot silently select a CPU or
older CUDA build.

## 4. Prove the real pocket path

Load `binding-mode-analysis`, pass its execution asset the exact component ID,
chain, and residue identity returned by the structure source, and obtain a
passing `pocket_validation.json`. That receipt—not a newly authored atom loop—is
the pocket authority: it proves that the center came from one unambiguous
non-polymer ligand and records the ligand heavy-atom count, receptor contacts,
and box dimensions. Convert the same verified receptor to PDB without changing
its coordinates. Start with one sample and one CPU core on the selected GPU.
Run the bundled wrapper:

```bash
python "${SYNON_SKILL_DIR}/scripts/run_pocket2mol.py" \
  --repository "task-relative extracted official repository" \
  --checkpoint-acquisition "checkpoint directory/checkpoint_acquisition.json" \
  --source-revision "verified immutable commit" \
  --protein "task-relative receptor PDB" \
  --pocket-validation "binding_mode/pocket_validation.json" \
  --required-count 1 \
  --output-dir "pocket2mol_preflight"
```

The wrapper rejects an absent, ambiguous, or non-passing checkpoint acquisition
receipt or pocket receipt and
uses its validated center plus the largest recorded box dimension as the cubic
sampling region. A representative pass requires a parseable nonempty SDF,
finite 3D coordinates, one unique canonical SMILES, CUDA execution, and a
passing validation record. Only then call the route verified ready and scale
`--required-count` high enough to retain the user's requested number after
validity and diversity filtering.

## 5. Recover without changing the method

On failure, inspect the first causal error and make one materially different
repair at a time: Python/PyTorch/PyG compatibility, missing official runtime
dependency, trusted checkpoint loading, GPU architecture, pocket conversion,
folder-versus-file download semantics, redirect/network transport, or sampling
configuration. Resume from downloaded source, checkpoint, and the last valid
environment generation. Do not restart from scratch, ask the user to perform
ordinary setup, or switch to a weaker generator merely because setup is old or
the first attempt failed.

## 6. Handoff

The wrapper writes a normalized multi-record SDF, canonical-SMILES CSV, and a
machine-readable validation record. Preserve the raw upstream output directory
as working evidence. Hand the normalized candidate set, exact pocket input,
engine/source/checkpoint identity, hashes, requested/returned counts, and
validation to `structure-based-molecule-generation`. Pocket2Mol proposes
chemistry; docking and affinity remain separate evidence lanes.
