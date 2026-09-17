---
name: structure-based-molecule-generation
description: Route protein-pocket-conditioned, target-aware, or structure-based molecular generation to a professional model, connected MCP, or reviewed installable scientific engine. Use when ligand creation must depend on receptor or binding-pocket geometry. This workflow dynamically discovers, provisions, and verifies the best-fit professional capability against live machine resources.
tags: [structure-based-drug-design, pocket-conditioned-generation, professional-molecular-generation]
keywords: [structure-based molecule generation, pocket-conditioned generation, binding-pocket-conditioned generation, target-aware generation, 3D molecular generation, 3D small molecules, editable 3D small molecules, protein pocket, binding pocket, generator provenance, de novo design, SBDD, AI molecule design, 基于口袋的分子生成, 结构导向分子生成, 口袋驱动分子生成, 三维小分子生成, 结合口袋, 结合口袋条件, 受体口袋, 靶点条件生成, 可编辑三维结构文件, AI分子设计]
allowed-tools: search_skills, skill, ask_user, repl, web_search, list_compute, manage_environments, manage_packages, bash, read_file, download_public_scientific_file, save_artifacts
required-capabilities: [pocket-conditioned-molecule-generation]
references:
  - references/generation-methods.md
critical-constraints:
  - For a request whose scientific requirement is protein-pocket-conditioned or target-aware molecular generation, establish a live professional generation route and its exact input contract before starting generic compound enumeration or a scientifically weaker substitute.
  - Target identity, structure retrieval, pocket definition, and generator readiness are distinct evidence lanes; success in one lane never proves that another is available or complete.
  - When a bound ligand defines the pocket, require an explicit chain-aware non-polymer ligand receipt before generation containing component ID, chain, residue number, heavy-atom count, contact evidence, center, dimensions, and source hash. Load `binding-mode-analysis` and use its reviewed execution asset; never accept a center obtained by averaging every atom in the receptor or by an unverified ad hoc residue loop.
  - Consider a weaker generation class only after concrete readiness evidence excludes every viable professional route for the current connected services, approved installation boundary, compute providers, credentials, and machine resources; never downgrade silently.
  - A ligand-conditioned model, property optimizer, docking engine, or RDKit enumerator is not pocket-conditioned generation unless the generator itself consumed verified receptor or pocket geometry.
  - Keep generator scores, docking confidence, docking affinity, predicted properties, measured activity, and synthetic-feasibility evidence in separate named fields; never collapse them into an unexplained score.
  - For a material generation choice, evaluate connected hosted models, provisionable local professional engines, ligand/scaffold-conditioned models, and the deterministic fallback before narrowing the slate. Present three to four concrete routes whenever at least three are scientifically credible or provisionable; never collapse several engines into one vague "or another tool" option or pad the slate with a route that cannot be made executable.
  - A user selects an implementation, not an unresolved method category. Every substantial-compute option must bind its public implementation field to one concrete engine or service discovered in the current task. Do not interpret a broad choice such as pocket-conditioned generation as permission to choose Pocket2Mol, DiffSBDD, PocketXMol, or another engine afterward.
  - A language runtime, machine-learning framework, cheminformatics toolkit, validator, or generic dependency bundle is not a pocket-conditioned molecular generator. Never preflight or create a generation environment until one actual scientific engine or service and its documented invocation have been identified.
  - Never infer a package name, repository URL, model-weight location, or entry point from an implementation's display name. Establish its canonical publisher source and documented invocation first; a failed source guess is repaired within the same selected implementation and never authorizes a different generator.
  - Before presenting a substantial generation choice, inspect the live compute snapshot and the exact candidate implementation. Each option names that implementation and gives only concise observed or official CPU, memory, and GPU/VRAM facts; unknown values remain unresolved and phrases such as "some resources" or "no special requirements" are not decision evidence. Put a material fee or data-transfer boundary in the limitation only when it changes the choice.
  - Before ask_user can recommend a substantial local engine, complete manage_environments list for current environments and machine resources, then complete an exact non-mutating preflight for the recommended implementation using sourced requirements. If either authority is missing, keep the route pending and do not recommend it; never invent resource numbers to satisfy the decision schema.
  - Write user-visible plans, questions, progress, artifact descriptions, and reports in the conversation language while preserving exact engine names, identifiers, schemas, units, and scientific notation.
  - This routing Skill has no executable kernel.py or get_available_generators helper; never import an undocumented module or probe the Skill directory with shell/read_file. Use loaded reference content, live schemas, and the delegated execution Skill.
---

# Structure-Based Molecule Generation

Route a structure-conditioned generation request by capability, not by a fixed
tool name or prompt phrase.

The standard professional workflow is authoritative. A missing preconfigured
endpoint, environment, or credential is a capability gap to resolve, not proof
that the task requires a scientifically weaker substitute.

## Professional method portfolio

Classify the scientific conditioning before choosing an engine. These routes
are complementary, not interchangeable:

| Design route | Required conditioning | Professional method family | Use when |
| --- | --- | --- | --- |
| `pocket_native_3d` | receptor structure plus a verified pocket or bound-ligand site | pocket-interaction foundation model, equivariant diffusion, or pocket autoregressive generation | new chemistry must be created directly in the three-dimensional pocket |
| `pocket_substructure` | pocket plus a fixed ligand fragment, motif, anchor, or attachment points | pocket-conditioned inpainting, fragment growth, or fragment linking | the binding hypothesis requires retaining a warhead, hinge binder, pharmacophore, or fragment geometry |
| `ligand_scaffold_conditioned` | one or more verified ligands, scaffolds, motifs, or matched pairs | SAFE fragment generation, scaffold decoration, R-group replacement, linker design, or Mol2Mol transformation | the design must stay near known chemistry but need not consume the receptor during generation |
| `ligand_latent_optimization` | a verified seed molecule and explicit property/similarity objectives | latent-space sampling or multi-objective generative optimization | exploring a controlled neighborhood around a lead |
| `deterministic_analog_enumeration` | a verified parent structure plus declared transformations | rule-based medicinal-chemistry enumeration and diversity selection | an explicitly accepted non-model route or the final evidence-backed fallback |

For a broad design campaign with both a trustworthy pocket and a reference
ligand, prefer a complementary portfolio when two professional routes are
actually ready: one pocket-native 3D route for novel chemotypes and one
ligand/scaffold route for hypothesis-preserving analogs. Do not run every
available generator. Choose the smallest complementary set that addresses the
requested novelty, fixed features, property objectives, compute budget, and
deliverables. If only one professional route passes readiness, use it and state
the resulting chemical-space limitation instead of pretending a multi-method
portfolio ran.

The runtime-rendered engine map attached to this Skill provides reviewed
candidates and official-source checks, not permanent preferences. It is already
present in the loaded guidance; do not try to open a Skill-directory path. A
connected MCP or managed endpoint may replace a listed local engine only when
its live schema proves the same scientific conditioning and output contract.

## Ask at a material route choice

Inspect the task, exact input artifacts, current machine resources, connected
services, credentials, and provisionable routes before asking. Do not present a
route that cannot consume the required conditioning or has already failed a
bounded readiness check.

First use `manage_environments` in `list` mode to inventory reusable
environments and the current machine. For each local candidate that may be
recommended, then use `manage_environments` in `preflight` mode with the
checked implementation's sourced requirements. Both calls are read-only. Use
`list_compute` for configured remote providers. A provider list alone is not a
local hardware inventory. If the implementation's requirements are not yet
known, verify its current official source and leave the route pending rather
than inventing a number.

When multiple routes remain and their scientific trade-offs matter to the user,
call `ask_user` once. A professional design task should normally offer three to
four concrete routes after examining the reviewed portfolio; do not stop at two
until the remaining method families have been checked and found scientifically
inapplicable or not provisionable. A route that requires a feasible setup or an
authenticated service may be offered as such, but it must not be called ready.
Recommend one route on current evidence and make each option decision-ready:

- identify the generation class and whether it consumes pocket geometry,
  ligand/scaffold chemistry, or both;
- state the main benefit and the most important scientific limitation;
- state only the observed machine CPU, memory, and GPU/VRAM plus the checked
  implementation's corresponding requirements; for a remote route say that
  the GPU is remote. Leave an unchecked number unresolved rather than
  describing it vaguely. Put a material fee or data-transfer boundary in the
  limitation instead of expanding the resource line;
- state the expected editable structures, provenance, candidate scale, and
  downstream validation that the option will deliver.

Keep each option specific. Finish implementation discovery before asking and
name one exact checked or provisionable engine/service in each option. A method
family is context, not a selectable execution identity. Do not combine PocketXMol, DiffSBDD,
Pocket2Mol/TargetDiff, hosted services, or ligand-conditioned generators under
one catch-all label. Preserve these as separate choices when their compute,
conditioning, novelty, or data-boundary trade-offs differ.

When the user selects Pocket2Mol, load `pocket2mol-local` and follow its
managed installation, compatibility-repair, representative-generation, and
normalized-output contract. Do not recreate that setup from memory.

For any other selected implementation, search for and load its dedicated Skill
when one exists. Otherwise load `capability-acquisition`, establish the official
source, immutable version, weight provenance, installation phases, and real
entry point, and only then create its managed environment. Keep the exact
selected implementation identity unchanged through preflight, installation,
representative execution, and downstream provenance.

Use one short lead-in followed by the `ask_user` card. Do not duplicate the
same options in a prose table before or after the card.

Use the conversation language for the question and trade-offs. Preserve exact
engine names, versions, units, and identifiers. Do not ask the user to diagnose
installation or runtime failures. If only one scientifically valid route
remains, or the user delegated method choice and one route clearly dominates,
continue without interruption. Ask again only when new evidence invalidates the
selected route or creates a genuinely new user-owned trade-off.

## Define the required capability

Record the receptor or pocket artifact, the pocket definition and its source,
whether a reference ligand or fixed substructure must be retained, the desired
candidate count and diversity, and the optimization objective. Distinguish:

- protein-pocket-conditioned or target-aware model generation;
- ligand/scaffold-conditioned model generation;
- docking or rescoring of an already generated library.

These are different scientific capabilities. Do not substitute one for another
without making the trade-off explicit.

## Dynamic preflight and selection

1. Freeze a short design brief: route ID, receptor/pocket and source, reference
   ligands or fixed substructures, attachment points, requested count,
   novelty/diversity target, property objectives, stereochemistry, and excluded
   chemistry. Keep missing user-owned scientific choices unresolved or ask one
   focused question.
2. When the pocket is defined by a co-crystal ligand, load
   `binding-mode-analysis` and create its passing chain-aware pocket receipt
   from the exact component, chain, and residue identity established by the
   structure source. Treat any ad hoc whole-structure or ambiguous ligand center
   as invalid preparation evidence and repair it before generation.
3. Inspect the live connected MCP catalog and exact method schemas. A
   pocket-native method must accept coordinates, a pocket selection, or an
   equivalent target-structure tensor; a method accepting only SMILES or a
   target name is ligand- or target-aware, not pocket-conditioned.
4. Inspect installed Skills, managed environments, compute providers, and the
   machine CPU/GPU/memory/disk snapshot. Reuse a verified immutable engine
   before installing another copy. For a local research engine, verify its
   official repository, license, exact commit or release, weight identity,
   environment lock, documented inference entrypoint, and output schema.
5. If no suitable capability is ready, load `capability-acquisition`. Review
   current official sources, license, immutable version, model/weight source,
   download size, expected host/GPU memory, driver compatibility, runtime, and
   documented invocation before provisioning one professional engine.
6. Bring the best-fit route to a real readiness decision with one bounded
   representative call using the same conditioning class as the task. Verify
   that it returns parseable molecules with coordinates and provenance. A clone,
   environment creation, `--help`, or zero-exit setup command is not a generation
   result.
7. If a complementary professional route is scientifically useful, preflight it
   independently; do not let its failure invalidate a successful primary route.
   When the primary professional candidate is infeasible, select another route
   of the same conditioning class. Preserve every rejection receipt.
8. Only after no viable professional route remains for the current machine,
   connected services, remote compute, credentials, and approved installations
   may a weaker generation class be proposed through `ask_user`. If the user
   selects deterministic RDKit enumeration, load
   `medicinal-chemistry-optimization`, label it exactly as analog enumeration,
   and use pocket docking only as a later selection stage.

## Evidence contract

Before handing candidates to docking, preserve:

- route ID and whether the generator consumed pocket coordinates, ligand
  chemistry, a fixed substructure, a property oracle, or a combination;
- exact receptor and pocket input hashes and the pocket-definition method;
- generator provider/engine, immutable version, model or service identity, and
  execution receipt;
- requested and returned counts, stable candidate IDs, and raw 3D SDF output;
- validity, uniqueness, novelty against supplied/reference molecules,
  disconnected-fragment, coordinate, stereochemistry, scaffold-diversity, and
  basic property checks;
- fixed-fragment or attachment-point retention where applicable, plus
  pocket-clash and interaction-geometry checks for pocket-native output;
- an editable candidate-property CSV and a machine-readable validation record
  that state the actual generation class.

Generate enough raw candidates to meet the requested count after validation;
do not duplicate rows or lower the count silently. Diversity is a portfolio
property, not a single scalar: report unique structures, Murcko scaffolds,
pairwise similarity distribution, route contribution, and explicit rejection
reasons. A generator-provided confidence or reward is retained with its exact
meaning but never treated as measured activity, binding affinity, or synthetic
feasibility.

RDKit may validate, calculate descriptors, filter, or render molecules after a
professional generator runs. Those downstream operations do not make RDKit the
generator and must not erase the original engine provenance.

## Separate generation from evaluation

Generation proposes chemistry; it does not validate binding. Standardize the
validated molecules without erasing protonation, tautomer, isotope, or
stereochemical decisions, then use the task-appropriate downstream Skills:

- pocket pose generation or docking for geometry;
- an affinity or rescoring method only when its score semantics are explicit;
- physicochemical, liability, selectivity, ADME, and synthesis assessment as
  separate evidence lanes;
- medicinal-chemistry review of fixed motifs, warheads, strain, reactivity, and
  tractable analog series.

For a ranked pocket-design deliverable, docking should sample multiple runs or
modes but publish one best-scoring primary pose per molecule unless the user
asks for more. Reference-ligand geometry may resolve exact score ties; it must
not override a better docking score or force unrelated chemotypes into an atom
mapping.

Resume the parent drug-discovery workflow from the generated candidate set.
Do not finish at capability selection, installation, or a prose-only design.
