---
name: antibody-design-strategy
description: Route structure-based de novo antibody or nanobody design, CDR co-design, affinity maturation, framework-constrained optimization, humanization, and developability review through antibody-specific methods. Select verified local compute or an equivalent MCP/API from live readiness evidence; never substitute a generic protein binder pipeline silently.
tags: [antibody-design, nanobody-design, cdr-design, antibody-optimization]
keywords: [antibody design, nanobody design, de novo antibody, CDR design, CDR optimization, antibody humanization, affinity maturation, epitope specific antibody, RFantibody, IgLM, IgFold, 抗体设计, 纳米抗体设计, CDR设计, CDR优化, 抗体人源化, 亲和力成熟, 表位特异性抗体]
allowed-tools: search_skills, skill, ask_user, repl, web_search, list_compute, manage_environments, manage_packages, bash, read_file, download_public_scientific_file, save_artifacts
references:
  - references/methods.md
critical-constraints:
  - Antibody and nanobody design require antibody-specific framework, CDR, chain-pairing, numbering, and antigen-interface semantics; generic protein-binder output is not an antibody.
  - Preserve heavy/light or VHH chain identity, species/germline context, numbering scheme, framework residues, CDR definitions, disulfides, and fixed residues through every stage.
  - Do not claim affinity, specificity, neutralization, humanization, low immunogenicity, expression, aggregation resistance, or developability from sequence likelihood or structure confidence alone.
  - Use antibody-specific generation and evaluation when available; use generic ProteinMPNN or structure predictors only as declared sub-stages, never as proof that an antibody pipeline ran.
  - Use local compute only after an antibody-class representative run passes; otherwise choose an equivalent live MCP/API or remote route and retain exact model, license, version, and data-boundary evidence.
  - When multiple live antibody-specific routes would materially change antibody format, epitope control, CDR or framework freedom, humanization strategy, compute demand, service cost, or deliverables, present one informed ask_user choice after readiness preflight with a recommendation and explicit advantages, limitations, resource needs, and expected outputs.
  - Write user-visible plans, questions, progress, artifact descriptions, and reports in the conversation language while preserving exact antibody numbering, chain IDs, model names, identifiers, units, and scientific symbols.
---

# Antibody Design Strategy

This Skill is separate from general protein design. It covers conventional
paired antibodies and single-domain antibodies/nanobodies.

## Choose the antibody task

| Route | Required input | Professional method family |
| --- | --- | --- |
| `de_novo_antibody` | antigen structure, accessible epitope/hotspots, antibody or nanobody format | antibody-finetuned backbone generation, antibody sequence design, antibody-complex filtering |
| `complex_cdr_codesign` | antibody-antigen complex, chain mapping, CDRs to redesign | complex-aware CDR sequence/structure co-design such as DiffAb/AbX-class models |
| `sequence_infilling` | paired heavy/light or VHH sequence, fixed framework/CDRs, region to vary | antibody language-model infilling such as IgLM-class methods |
| `affinity_maturation` | verified parent antibody, antigen/epitope and measured or modeled objective | constrained CDR/framework mutation, structure/interface evaluation, experimental-loop planning |
| `humanization` | non-human parent, desired human germline/framework and retained binding hypothesis | germline/framework selection, CDR grafting/backmutation analysis, structure and developability review |

When the request does not specify antibody format, antigen, epitope, parent
sequence, species, or whether de novo design versus optimization is intended,
ask one focused question. These choices change the scientific problem.

## Ask at a material route choice

Resolve what the existing evidence already establishes, then inspect
antibody-specific engine readiness, machine CPU/GPU/RAM/disk, connected
services, credentials, and remote compute. When two to four viable routes imply
meaningfully different antibody formats, epitope constraints, CDR/framework
freedom, humanization strategy, experimental risk, resource demand, service
cost, or outputs, call `ask_user` once and recommend one route on current
evidence. Each option must identify the antibody design class, its principal
advantage and important limitation, its material compute or remote-service
requirement, and its paired sequence, numbering, structure/complex, metrics, provenance, and
developability-review deliverables. Use the conversation language while
preserving exact framework/CDR notation, engine names, versions, and units.
Proceed without asking when only one antibody-valid route remains or the user
has delegated a clearly dominant choice. Do not make the user diagnose an
engine failure or offer a generic protein route as an equivalent option.

## Professional execution routes

1. For structure-based de novo antibodies or nanobodies, prefer an
   antibody-specific end-to-end route such as RFantibody when its official
   code, antibody-finetuned weights, environment, antigen input, and one pilot
   design pass readiness. Its backbone, ProteinMPNN sequence, and
   antibody-finetuned RF2 filtering stages must remain linked by stable IDs.
2. For an existing antibody-antigen complex, use a reviewed antibody CDR
   co-design/optimization engine when available. Preserve chain and CDR mapping
   and run enough independent samples to assess diversity; do not silently
   redesign framework regions.
3. For sequence-only ideation or infilling, use an antibody language model only
   within its license and supported format. Sequence likelihood is a prior, not
   binding or developability evidence.
4. Use IgFold- or equivalent antibody-aware structure prediction for fast
   paired-chain/VHH checks when appropriate, then an independent complex model
   such as Boltz2/Chai/OpenFold for antigen-complex hypotheses if its schema and
   size limits fit. No single predictor is the sole acceptance gate.

## Local versus MCP/API/remote

Inspect installed Skills, live MCP/API schemas, compute providers, and machine
resources. Prefer a managed local antibody engine only when the documented GPU,
CUDA, weights, license, and one antibody-class pilot run pass. Otherwise use an
equivalent authenticated MCP/API or remote compute route. If nothing is ready,
load `capability-acquisition`, verify official sources and license terms, and
perform one bounded pilot before a campaign. Never infer that a generic
RFdiffusion/ProteinMPNN run is equivalent to RFantibody.

## Canonical campaign

1. Verify antigen structure/assembly, glycans, accessible epitope, hotspot
   rationale, antibody format, chain mapping, numbering scheme, fixed
   framework/CDRs, disulfides, and length ranges.
2. Generate backbone or CDR hypotheses with immutable model/config/seed and
   stable design IDs. Use pilot-scale generation before thousands of designs;
   poor hotspots can create undocked antibodies.
3. Generate paired heavy/light or VHH sequences while preserving framework,
   chain pairing, canonical residues, disulfides, and fixed contacts.
4. Predict antibody structures and antibody-antigen complexes; calculate CDR
   geometry, interface contacts/PAE, clashes, buried surface, and epitope
   consistency. Retain model-specific uncertainty.
5. Assess sequence liabilities, unusual residues, glycosylation/deamidation/
   isomerization motifs, charge/hydrophobic patches, aggregation/solubility
   risk, germline identity, and portfolio diversity. These are risk indicators,
   not experimental developability claims.
6. Rank a transparent portfolio spanning sequence/CDR diversity and interface
   hypotheses. Do not select only the top internal model score.

## Acceptance and deliverables

Return paired heavy/light or VHH FASTA, IMGT/declared numbering and CDR table,
editable antibody and complex PDB/mmCIF files, epitope/hotspot evidence,
candidate metrics and liabilities, model/config/weight provenance, rejected
design reasons, and a professional report. Every final design must preserve the
same stable ID across sequence, numbering, structure, complex, and metrics.

Computational designs require experimental expression, binding/kinetics,
specificity, functional, developability, and immunogenicity assessment before
advancement.
