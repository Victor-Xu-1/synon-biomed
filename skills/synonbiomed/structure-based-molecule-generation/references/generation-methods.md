# Molecular generation methods and reviewed engines

Use this reference after the design route is known. Verify the current official
source, immutable release or commit, license, model weights, hardware support,
and actual input/output schema at execution time. The table is a shortlist of
professional method families, not an instruction to install or run every tool.

## Decision snapshot

Before asking the user, call the live compute inventory and inspect the exact
candidate implementation. Keep the card short, but name one implementation per
option and report only CPU, memory, and GPU/VRAM. Do not write only “needs
compute” or “no special requirements”; mark unchecked values “unverified” and
name the preflight that will establish them.

| Implementation | Conditioning | CPU, memory, and GPU facts to verify and show |
| --- | --- | --- |
| PocketXMol | pocket-native 3D or pocket plus fixed motif | local CUDA GPU; report checked CPU, memory, and VRAM, or leave the value unverified |
| DiffSBDD | pocket-native diffusion or substructure inpainting | local CUDA compatibility; report observed CPU, memory, and VRAM, not an estimate copied from another model |
| Pocket2Mol | autoregressive atom-by-atom pocket-native 3D generation | report live CPU, memory, GPU, and VRAM before calling it provisionable; do not describe it as a diffusion model |
| TargetDiff | target-conditioned equivariant diffusion | report live CPU, memory, GPU, and VRAM before calling it provisionable |
| GraphBP | older autoregressive 3D pocket-generation baseline | verify the publisher-linked AIRS implementation, included pretrained model and legacy PyTorch/PyG compatibility before offering it |
| DiffLinker | pocket-conditioned equivariant diffusion for fragment linking | requires supplied three-dimensional fragment geometry and linker-size/anchor decisions; it is not an empty-pocket de novo substitute |
| authenticated hosted generator | schema-proven pocket or ligand conditioning | local CPU and memory for preprocessing; GPU is remote |
| GenMol, REINVENT 4, or MolMIM | ligand/scaffold/latent conditioning | not pocket-native; report the exact local or hosted deployment and its observed resource profile |
| deterministic RDKit enumeration | declared medicinal-chemistry transformations | CPU-capable fallback; report current CPU and memory, and label the limited learned novelty |

Preserve distinct engine choices when their setup, conditioning, novelty, data
boundary, or cost differs.

## Pocket-native 3D generation

| Engine family | Appropriate work | Required proof before use | Important boundary |
| --- | --- | --- | --- |
| PocketXMol or a compatible pocket-interaction foundation model | pocket-conditioned de novo design, docking, fragment linking/growing, partial-structure generation | official code and weights, exact task config, receptor/pocket input, real SDF/PDB output and metadata | confirm the selected config is molecular design rather than pose prediction; confidence is not affinity |
| DiffSBDD or another validated equivariant pocket diffusion model | de novo pocket generation, substructure inpainting, pocket-conditioned optimization | official repository, immutable checkpoint, documented pocket preprocessing, representative generation receipt | older environments and CUDA stacks require a compatibility probe; repository setup alone is not evidence of generation |
| Pocket2Mol | autoregressive atom-by-atom pocket-native generation | official implementation, checkpoint/data provenance, exact pocket definition and sampled 3D molecules | the published stack is old; resolve current PyTorch/PyG compatibility rather than misclassifying it as diffusion |
| TargetDiff | target-conditioned equivariant diffusion | official implementation, checkpoint/data provenance, exact pocket definition and sampled 3D molecules | distinguish its diffusion confidence from docking or affinity |
| GraphBP | autoregressive 3D pocket-generation baseline | official AIRS source, included pretrained checkpoint, exact pocket input and a representative generation | the retired repository points to AIRS and both retain a legacy dependency stack; do not call it lightweight or ready without current GPU-architecture evidence |
| DiffLinker | fragment-linking diffusion optionally conditioned on a protein pocket | official source/checkpoint, input fragments, their 3D geometry, pocket/protein input, linker size and generated linked molecules | use only for a real fragment-linking brief; a receptor plus one bound ligand does not establish the required fragment inputs |

Current official references:

- PocketXMol: <https://github.com/pengxingang/PocketXMol>
- DiffSBDD: <https://github.com/arneschneuing/DiffSBDD>
- Pocket2Mol: <https://github.com/pengxingang/Pocket2Mol>
- TargetDiff: <https://github.com/guanjq/targetdiff>
- GraphBP current publisher location: <https://github.com/divelab/AIRS/tree/main/OpenMI/GraphBP>
- GraphBP retired repository and migration notice: <https://github.com/divelab/GraphBP>
- DiffLinker: <https://github.com/igashov/DiffLinker>

## Ligand-, scaffold-, and fragment-conditioned generation

| Engine family | Appropriate work | Required proof before use | Important boundary |
| --- | --- | --- | --- |
| GenMol NIM v2 or a compatible SAFE masked-fragment model | de novo templates, scaffold decoration, motif extension, superstructure generation, linker design | live `/v1/models`, exact SAFE/SMILES template, v2 `/generate` schema, generated SMILES and scores | ligand/fragment conditioned; QED or LogP ranking is not pocket conditioning or activity |
| REINVENT 4 | de novo generation, LibInvent scaffold decoration, LinkInvent fragment linking, Mol2Mol optimization, multi-component scoring and curriculum/RL | official version, prior identity, config, seed, scoring components and generated structures | scoring functions define the optimization objective; they do not become measured evidence |
| MolMIM NIM | seed-neighborhood sampling, latent interpolation, CMA-ES QED/plogP optimization | live endpoint/version, seed SMILES, similarity/property parameters and generated structures | latent/ligand conditioned; not pocket-native unless an external pocket score is explicitly integrated and reported |

Current official references:

- GenMol NIM: <https://docs.nvidia.com/nim/bionemo/genmol/latest/overview.html>
- REINVENT 4: <https://github.com/MolecularAI/REINVENT4>
- MolMIM NIM: <https://docs.nvidia.com/nim/bionemo/molmim/latest/overview.html>

## Route composition

- A pocket-native route supplies novel 3D chemistry conditioned on receptor
  geometry.
- A ligand/scaffold route preserves known medicinal-chemistry hypotheses and
  can explore R-groups, linkers, motifs, or the neighborhood of a lead.
- Pharmacophore, shape, docking, or property objectives may guide or rank a
  generator only when the integration is real and recorded. Post hoc filtering
  must not be described as generator conditioning.
- Deterministic RDKit enumeration remains useful for validation, descriptors,
  diversity selection, and an explicitly accepted analog-enumeration route. It
  is not an AI generator and is never evidence that pocket-conditioned
  generation ran.

For multi-route portfolios, assign stable candidate IDs at each generator,
retain route and engine provenance through deduplication, and select a balanced
set across methods and scaffolds before downstream docking. Do not let one
high-volume route crowd out all candidates from a complementary route without
an explicit evidence-based reason.

## Resource facts for an informed choice

- Pocket-native 3D research models normally require an exact checkpoint,
  receptor/pocket preprocessing, and a compatible CUDA GPU. Report the checked
  version's documented CPU, memory, and VRAM needs with the live machine result;
  keep disk and download checks inside preflight unless one is a material
  limitation. Do not copy a requirement from another model family.
- Hosted NIM/API routes replace local GPU and model-cache requirements with
  credential, service-limit, cost, latency, and data-boundary trade-offs. Verify
  the live endpoint and schema before presenting them as viable.
- Ligand/scaffold models may have lighter local requirements, but only the
  actual version and a representative run establish feasibility and throughput.
- Deterministic RDKit enumeration is normally CPU-capable and reproducible, but
  it explores a predefined transformation space rather than learning a
  pocket-conditioned chemical distribution.

Use these as comparison dimensions, not fixed thresholds. An `ask_user` option
must state observed or official resource facts and mark unknown capacity as
unresolved rather than inventing a GPU-memory or runtime estimate.
