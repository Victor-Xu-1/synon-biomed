---
name: rna-design-strategy
description: Route RNA secondary-structure inverse folding, multi-strand nucleic-acid system design, 3D RNA inverse design, mRNA coding-sequence optimization, and siRNA/ASO targeting through professional task-specific methods. Select verified local tools or equivalent MCP/API/remote compute from live readiness evidence.
tags: [rna-design, nucleic-acid-design, inverse-folding, mrna-design, oligonucleotide-design]
keywords: [RNA design, RNA inverse folding, RNA secondary structure design, RNA backbone inverse folding, 3D RNA backbone, multi strand RNA design, RNA 3D design, mRNA design, codon optimization, siRNA design, ASO design, aptamer design, NUPACK, ViennaRNA, gRNAde, LinearDesign, RNA设计, RNA反向折叠, RNA骨架反向折叠, 三维 RNA 骨架, 三维RNA骨架, RNA二级结构设计, 多链核酸设计, 三维RNA设计, mRNA设计, 密码子优化, siRNA设计, 反义寡核苷酸设计, 适配体设计]
allowed-tools: search_skills, skill, ask_user, repl, web_search, list_compute, manage_environments, manage_packages, python, bash, read_file, edit_file, save_artifacts
references:
  - references/methods.md
critical-constraints:
  - RNA secondary-structure inverse folding, multi-strand circuit design, 3D fixed-backbone design, mRNA coding optimization, siRNA/ASO targeting, CRISPR guide design, and aptamer design are different scientific tasks; never route them through one generic generator.
  - Preserve strand orientation, alphabet/material, modifications, temperature, ionic conditions, concentrations, target isoform/build, and numbering; do not silently convert RNA to DNA or ignore modified nucleotides.
  - An MFE structure alone is not a robust design; evaluate ensemble probabilities/defect, competing structures, off-target interactions, and task-specific constraints.
  - Evo2 is a DNA/genomic language model and is not a universal RNA design engine; use it only for an explicitly justified DNA/CDS representation stage and retain that boundary.
  - Use local computation only after the exact package/version/model and a representative task-class run pass; otherwise use an equivalent MCP/API or remote route without weakening the scientific task.
  - When multiple live RNA-design routes would materially change the molecular product, thermodynamic or geometric objective, modification assumptions, compute demand, service cost, or deliverables, present one informed ask_user choice after readiness preflight with a recommended route and explicit advantages, limitations, resource needs, and expected outputs.
  - Write user-visible plans, questions, progress, artifact descriptions, and reports in the conversation language while preserving exact sequence alphabets, identifiers, schemas, strand notation, units, and scientific symbols.
---

# RNA Design Strategy

First identify the RNA product and design objective. If the user says only
"design RNA", ask which route below is intended.

## Choose the RNA design class

| Route | Required input | Professional method family |
| --- | --- | --- |
| `secondary_inverse_fold` | target secondary structure, length, temperature/salt and sequence constraints | ViennaRNA RNAinverse/RNAfold-class inverse folding plus ensemble analysis |
| `multistrand_system` | target complexes/tubes, concentrations, on/off-target species, material | NUPACK multi-complex or multi-tube design with hard/soft constraints |
| `rna_3d_inverse` | one or more RNA backbone conformations and fixed positions | gRNAde-class single/multi-state geometric inverse folding plus independent refolding |
| `mrna_coding_design` | exact protein sequence, host/cell context, UTR/poly(A)/motif constraints | synonymous coding optimization such as LinearDesign plus structure, codon, motif, and manufacturability checks |
| `sirna_aso_targeting` | transcript accession/isoform/build, target region, chemistry and modality | accessibility/duplex thermodynamics such as RNAstructure OligoWalk plus transcriptome off-target analysis |
| `aptamer_or_rna_binder` | target, known motif/backbone/selection data and binding hypothesis | constrained secondary/3D design and structure/complex prediction; experimental selection remains essential |

CRISPR guide design is a separate genomic-target/off-target problem. Route it
to a dedicated CRISPR design Skill when present rather than treating it as
generic RNA inverse folding.

## Ask at a material route choice

Use the request and existing sequence/structure evidence to narrow the product
class before asking, then inspect exact local package/model readiness, machine
CPU/GPU/RAM/disk, connected services, credentials, and remote compute. When two
to four viable routes differ materially in RNA product, thermodynamic versus
geometric objective, ensemble or off-target model, modified-nucleotide support,
resource demand, service cost, or outputs, call `ask_user` once and recommend
one route on current evidence. Each option must state its principal advantage,
important limitation, material local or remote requirement, and expected
sequence, structure, metrics, provenance, and validation deliverables. Use the
conversation language while retaining exact alphabets, strand notation,
package/model names, versions, and units. Proceed autonomously when the route is
scientifically determined by the requested product or one option clearly
dominates. Never ask the user to repair an environment or offer a DNA/genomic
model as an equivalent RNA route.

## Local versus MCP/API/remote

1. Inspect live Skills, MCP/API schemas, compute providers, and local
   CPU/GPU/RAM/disk. ViennaRNA, NUPACK, RNAstructure, and LinearDesign-class
   calculations are normally local-capable when their exact package/license and
   task limits pass. A managed environment and real smoke calculation are
   required.
2. Use GPU or remote/API routes for 3D inverse design or large foundation
   models when local resources do not fit. Verify immutable code/weights,
   input-backbone schema, output FASTA/structure contract, and one task-class
   representative run.
3. If no route is ready, load `capability-acquisition`; inspect official sources
   and licenses, then provision one professional engine. Do not substitute a
   language model's free-text sequence for thermodynamic or geometric design.

## Canonical workflow by route

### Secondary structure and multi-strand systems

Define RNA/DNA/modified material, temperature, salt, concentrations, target
structures/tubes, desired and off-target complexes, hard sequence motifs, and
allowed wobble/pseudoknot assumptions. Run several independent designs. Report
ensemble defect, target probability, base-pair probabilities, competing
structures, off-target complexes, and sensitivity to conditions. Prefer NUPACK
multi-tube formulations for interacting systems because they explicitly model
crosstalk and concentrations.

### 3D RNA inverse design

Validate backbone atoms, chains, residue numbering, missing coordinates,
multi-state conformations, fixed motifs, and sequence constraints. Generate
multiple sequences per backbone with an immutable gRNAde-class model. Refold or
predict each candidate with an independent RNA-capable structure/complex method
and compare backbone RMSD, base-pairing, clashes, motif preservation, and
multi-state consistency. A model confidence score is not function.

### mRNA coding design

Preserve the exact encoded protein. Optimize synonymous codons and RNA folding
with a declared host codon table and transparent objective trade-off; do not
optimize CAI or MFE alone. Evaluate translation-relevant 5' structure,
codon-pair/GC distribution, repeats/homopolymers, cryptic splice/polyadenylation
signals, restriction/manufacturing motifs, UTR/CDS junctions, and the impact of
modified nucleotides. LinearDesign output is one hypothesis, not proof of
expression, stability, innate-immune profile, or manufacturability.

### siRNA and ASO design

Resolve the exact transcript isoform and target coordinates. Enumerate candidate
windows, calculate target accessibility and duplex/oligomer thermodynamics,
apply modality-specific strand asymmetry and motif constraints, and search
transcriptome/genome off-targets including seed-like matches. Preserve chemical
modification and delivery assumptions separately. Do not claim knockdown,
allele selectivity, safety, or clinical suitability without experiments.

## Acceptance and deliverables

Return FASTA/CSV sequences with stable IDs, target/strand coordinates and
orientation, declared material and conditions, secondary structures and
dot-bracket/CT where applicable, ensemble/defect/off-target metrics, 3D PDB or
mmCIF for 3D routes, package/model/version/config/seed provenance, rejected
candidate reasons, and a readable report. Verify that every sequence uses the
intended alphabet, satisfies hard motifs and complementarity, and joins to its
metrics and structures by stable ID.

Computational RNA designs require appropriate biochemical, cellular, and
functional validation before use.
