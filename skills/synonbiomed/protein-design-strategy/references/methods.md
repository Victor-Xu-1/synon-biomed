# Reviewed protein design methods

Verify current releases, licenses, weights, hardware support, and exact schemas
at execution time.

- RFdiffusion: backbone generation, motif scaffolding, binder design, symmetry,
  and partial diffusion. Official source:
  <https://github.com/RosettaCommons/RFdiffusion>
- Proteina-Complexa: staged protein/ligand binder and motif-scaffolding campaign
  with generation, inverse folding, refolding, and analysis. Use the bundled
  `complexa-design` workflow only after its own preflight succeeds.
- ProteinMPNN: fixed-backbone protein sequence design. Official source:
  <https://github.com/dauparas/ProteinMPNN>
- LigandMPNN: ligand-, nucleic-acid-, and context-aware sequence design plus
  side-chain packing. Official source: <https://github.com/dauparas/LigandMPNN>
- Boltz2/OpenFold3: independent monomer or complex prediction and confidence
  evaluation. Their confidence and affinity fields retain model-specific
  semantics and are not experimental measurements.

The NIM Skills are valid provider routes only when their live endpoint version,
schema, authentication, readiness, and output are verified. A local academic
repository is valid only when its immutable code and weights can produce a real
task-class example in a managed environment.

## Resource facts for an informed choice

- RFdiffusion/RFdiffusion NIM needs model weights and a compatible CUDA route
  for practical backbone generation; use the checked release or NIM support
  matrix and live GPU result rather than a copied universal VRAM threshold.
- Proteina-Complexa's maintained local workflow documents a CUDA GPU with at
  least 40 GB VRAM plus substantial CPU, RAM, and disk for its full staged
  campaign. A hosted or remote equivalent must preserve the same backbone,
  sequence, refolding, and analysis outputs.
- ProteinMPNN-class inverse folding is materially lighter than backbone
  generation and may run on CPU or a modest GPU, but it cannot replace the
  missing backbone stage.
- Independent complex prediction can dominate memory and runtime. Compare the
  exact model, sequence/complex size, sampling count, local GPU fit, and remote
  service limits before offering a route.

Report measured or official requirements in each `ask_user` option. Unknown
capacity remains unresolved; do not promise runtime or success from model name
alone.
