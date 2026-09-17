---
name: protein-design-strategy
description: Route de novo protein, protein binder, motif scaffold, enzyme scaffold, oligomer, ligand-binding protein, and fixed-backbone sequence design through professional backbone-generation, inverse-folding, structure-prediction, and filtering workflows. Select local managed compute or an equivalent MCP/API from live readiness evidence.
tags: [protein-design, binder-design, inverse-folding, motif-scaffolding]
keywords: [protein design, de novo protein, protein binder, motif scaffolding, enzyme design, symmetric protein, backbone generation, inverse folding, ProteinMPNN, RFdiffusion, Complexa, 蛋白设计, 蛋白质设计, 结合蛋白, 蛋白骨架生成, 序列设计, 反向折叠, 基序支架, 酶设计]
allowed-tools: search_skills, skill, ask_user, repl, web_search, list_compute, manage_environments, manage_packages, bash, read_file, download_public_scientific_file, save_artifacts
references:
  - references/methods.md
critical-constraints:
  - Classify backbone generation, sequence design, complex prediction, and design filtering as separate stages; success or confidence in one stage never proves another stage succeeded.
  - Generic protein-binder design is not antibody design; route antibodies and nanobodies to antibody-design-strategy.
  - A generated backbone is not a viable protein until at least one designed sequence refolds to the intended monomer or complex with acceptable geometry and the exact validation method is recorded.
  - Do not call pLDDT, pTM, ipTM, PAE, sequence likelihood, or an internal reward experimental affinity, activity, expression, stability, or success probability.
  - Use a local engine only after hardware, immutable weights, environment, input contract, and one representative run pass; otherwise use an equivalent live MCP/API or remote compute rather than silently weakening the method.
  - When multiple live protein-design routes would materially change fold novelty, motif control, interface hypothesis, compute demand, service cost, or deliverables, present one informed ask_user choice after readiness preflight with a recommended route and explicit advantages, limitations, resource needs, and expected outputs.
  - Write user-visible plans, questions, progress, artifact descriptions, and reports in the conversation language while preserving exact model names, identifiers, schemas, residue notation, units, and scientific symbols.
---

# Protein Design Strategy

This Skill owns general protein design routing. It does not own antibodies,
RNA, or small molecules.

## Choose the scientific design class

| Route | Required input | Primary method family | Typical delegated Skills |
| --- | --- | --- | --- |
| `unconditional_backbone` | length/topology/symmetry constraints | generative backbone diffusion or flow matching | `rfdiffusion-nim`, `complexa-design` |
| `motif_scaffold` | fixed catalytic/functional motif and residue mapping | motif-conditioned backbone generation | `rfdiffusion-nim`, `complexa-design` AME |
| `protein_binder` | target structure, intended interface, accessible hotspots | target-conditioned binder backbone generation | `complexa-design`, `rfdiffusion-nim` |
| `ligand_binding_protein` | ligand identity/coordinates and pocket geometry | ligand-aware backbone/sequence design | `complexa-design`, `ligandmpnn` |
| `fixed_backbone_sequence` | validated backbone and fixed/designable positions | inverse folding | `proteinmpnn-nim`, `proteinmpnn`, `solublempnn`, `ligandmpnn` |
| `design_diversification` | an existing design and allowed structural movement | partial diffusion plus inverse folding | reviewed RFdiffusion/Complexa route |

Do not run every method. Choose one primary backbone route that matches the
scientific conditioning, then one compatible sequence-design route and at
least one independent structure/complex evaluation route. Use a second
backbone generator only when it adds a scientifically distinct hypothesis or
the first route fails readiness.

## Ask at a material route choice

First inspect the design brief, target artifacts, live engines, machine
CPU/GPU/RAM/disk, connected services, credentials, and remote compute. If two
to four scientifically valid routes remain and they imply different fold
novelty, motif fidelity, interface control, experimental risk, compute demand,
cost, or outputs, call `ask_user` once and recommend one on current evidence.
For every option explain the design class, principal advantage, important
limitation, local or remote resource requirement, and the backbone, sequence,
refolded-structure, metrics, and provenance deliverables. Questions and
trade-offs follow the conversation language; exact engine names, residue
notation, versions, and units remain unchanged. Proceed autonomously when one
route clearly dominates or the remaining differences are reversible
implementation details. Never ask the user to troubleshoot a failed engine;
repair or replace it within the same scientific class first.

## Freeze the design brief

Record target and species, source structure and chains, biological assembly,
motif or hotspot residues, fixed and designable residues, binder/fold length,
oligomeric state or symmetry, termini, disulfides or cofactors, excluded
chemistry, requested candidate count, and desired experimental function. Do not
guess chain mappings, motif numbering, ligand identity, or user-owned functional
trade-offs.

## Select local, MCP/API, or remote execution

1. Inspect live Skills, MCP schemas, compute providers, and machine
   CPU/GPU/RAM/disk. A provider is eligible only when its exact input schema
   supports the selected route and it returns editable structures or sequences
   with model provenance.
2. Prefer a verified managed local engine when the documented environment and
   weights fit the machine. Local readiness requires a real representative
   design, not only a clone, image pull, `--help`, or environment creation.
3. If local resources are insufficient, use a connected professional MCP/API or
   remote compute route of the same scientific class. Load
   `capability-acquisition` only when no existing route is ready; verify official
   source, license, release/commit, weights, environment lock, and a bounded
   smoke design before the full run.
4. Preserve failed readiness evidence and switch method once. Do not create an
   unbounded install/edit/retry loop or replace a failed structure generator
   with sequence-only mutation.

## Canonical execution chain

1. Validate and normalize the target, motif, ligand, symmetry, and residue
   mapping. Define a task-relative candidate ID namespace.
2. Generate multiple backbone hypotheses with an immutable model/config/seed.
   Reject malformed chains, broken motifs, impossible geometry, or missing
   required components before inverse folding.
3. Design multiple sequences per retained backbone. Preserve fixed positions,
   chain roles, ligand-contact residues, disulfides, and amino-acid exclusions.
4. Predict each designed monomer or complex with an independent structure model
   such as `boltz2-nim` or `openfold3-nim`. Where practical, avoid using the same
   model family as both generator reward and sole evaluator.
5. Evaluate backbone refolding, motif RMSD, interface PAE/contact quality,
   clashes, buried surface, unsatisfied polar groups, sequence recovery,
   diversity, aggregation/solubility liabilities, and task-specific geometry.
   Keep raw metrics separate and apply named thresholds only when sourced or
   explicitly defined for the campaign.
6. Rank a transparent Pareto-style set rather than hiding incompatible metrics
   inside an unexplained composite score. Retain rejected designs and reasons.

## Acceptance and deliverables

Return editable PDB/mmCIF backbones and refolded structures, FASTA sequences,
a candidate-by-stage table, model/config/seed/weight provenance, per-design
metrics, diversity analysis, rejection reasons, and a readable report. Every
final candidate must join backbone, sequence, predicted structure, and metrics
by stable ID. Empty diversity values, missing evaluators, or incomplete stages
are unavailable evidence, not zero or success.

Designs are computational hypotheses. Do not claim binding, catalysis,
expression, stability, immunogenicity, or manufacturability without appropriate
experimental evidence.
