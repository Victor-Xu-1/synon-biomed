---
name: sbdd-ppi-workflow
description: Route evidence-based structure-guided design at protein-protein interfaces through the canonical Synon Biomed structure, medicinal-chemistry, docking, interaction-analysis, and reporting Skills. Use for PPI interface characterization, pocket or hotspot assessment, ligand design, docking, and auditable PPI design reports.
license: Apache-2.0
allowed-tools: search_skills, skill, ask_user, repl, manage_environments, python, read_file, download_public_scientific_file, save_artifacts
---

# PPI Structure-Guided Design Router

This Skill owns only PPI-specific routing and scientific boundaries. It does
not download structures, generate molecules, run docking, calculate chemical
properties, parse scores, or render figures itself. Load the canonical Skill
that owns each requested operation and retain its validated outputs.

## Establish the biological and structural question

Resolve the target proteins, species, accessions, interaction evidence, and the
interface or complex of interest through `drug-discovery-pipeline` and the
connected evidence tools. Distinguish:

- an experimentally observed biological assembly;
- a predicted complex with explicit model confidence;
- an inferred interface that still requires experimental validation.

Do not create a dimer by an arbitrary rotation or translation. Do not infer a
biological assembly from a monomeric AlphaFold model. If the requested partner,
stoichiometry, construct, assembly, or interface is materially ambiguous, ask
one focused question before computation.

## Select one structure route

1. Prefer a verified experimental PDB biological assembly when it contains the
   relevant partners and interface. Preserve PDB ID, assembly ID, chain/entity
   mapping, experimental method, resolution, release date, and coordinate hash.
2. When no suitable experimental complex exists and prediction is requested,
   search for and load the applicable complex-prediction Skill (for example a
   Boltz, Chai, OpenFold, or Complexa capability that is actually available).
   Preserve model version, inputs, confidence fields, seeds, and limitations.
3. A predicted monomer, template-aligned guess, or geometric symmetry copy is
   not evidence of a PPI interface and must not be presented as one.

Download a verified public coordinate exactly once with
`download_public_scientific_file`. Reuse its immutable artifact version in all
downstream steps.

## Interface evidence

In one verified managed environment, calculate a reproducible interface table
from the chosen complex. At minimum retain:

`partner_a`, `partner_b`, `chain_a`, `chain_b`, `residue_a`, `residue_b`,
`atom_a`, `atom_b`, `minimum_distance_angstrom`, `contact_category`, and
`source_structure_id`.

Also produce a residue-level summary with contact counts, minimum distance,
buried-surface or solvent-accessibility values when a validated implementation
is available, and the method/version used. Label distance-based hydrogen bonds,
salt bridges, hydrophobic contacts, and hotspots as geometric hypotheses unless
the required chemistry, protonation, and energetic evidence was evaluated.
Never infer secondary structure or energetic hotspots from a single
CA-distance heuristic.

## Delegate design and docking

Load only the modules required by the request:

| Need | Canonical Skill |
| --- | --- |
| entity, literature, assay, and structure evidence | `drug-discovery-pipeline` |
| protein-pocket-conditioned or target-aware model generation | `structure-based-molecule-generation` |
| explicitly selected deterministic analog-enumeration fallback | `medicinal-chemistry-optimization` |
| descriptor and physicochemical analysis | `chem-physical-properties` |
| governed local Vina docking | `autodock-vina` |
| verified ligand-contact analysis | `binding-mode-analysis` |
| 2D structures and self-contained 3D viewer | `cheminfo-render` |

For another predictor or generator, discover its exact Skill first. Do not copy
its API client, package installation, model invocation, or parser into this
router. A local analog enumerator is not equivalent to a pocket-conditioned
generator; use the latter's dynamic preflight and explicit fallback decision.
Keep measured evidence, geometric contacts, docking affinity, pose confidence,
and model-derived affinity in separate columns.

For PPI pockets, define the search region only from verified interface
coordinates, a co-complex ligand, or an explicitly documented pocket method.
Record the atoms/residues used and the resulting center and box. A generic
interface center is not automatically a ligandable pocket.

## Professional deliverables

Publish only after each delegated validation passes:

1. an editable target/structure provenance table;
2. an editable long-form PPI interface table and residue summary;
3. the requested candidate property/ranking CSV and source SDF/SMILES;
4. for Vina work, `docking_complex_ensemble.pdb` with the fixed receptor,
   original ligand when available, and every candidate best pose, plus
   `docking_components.csv`;
5. a self-contained 3D viewer when it materially aids review;
6. a professional Markdown report separating observations, calculations,
   predictions, limitations, and proposed experimental validation.

Counts, ranks, ranges, thresholds, and repeated values must come from the
validated machine-readable outputs, not from visual transcription or a second
model-authored calculation. Keep raw payloads, temporary scripts, validation
JSON, logs, and environment inventories as internal evidence unless requested.

Completion requires exact identifier continuity, readable/editable artifacts,
source and computation provenance, and agreement between the report and tables.
A prose answer, an unvalidated coordinate, or a docking score without its pose
and component mapping is not complete.
