---
name: binding-mode-analysis
description: Analyze protein-ligand contacts from verified public PDB structures through the canonical structure MCP and managed Python runtime.
license: Apache-2.0
allowed-tools: repl, download_public_scientific_file, manage_environments, python, read_file, save_artifacts
preferred-execution-assets:
  - scripts/analyze_binding_pocket.py
---

# Binding Mode Analysis

Use this skill when the user asks how a ligand sits in a binding pocket, which
protein residues contact a ligand, whether interactions are polar or hydrophobic,
or how to compare binding poses from PDB structures.

For a completed AutoDock workflow, consume
`docking_complex_ensemble.pdb` together with `docking_components.csv`. The CSV
is the identity authority: select docked ligands by its `candidate_id`,
`chain_id`, `residue_name`, and `residue_number`, never by row order or a
temporary pose filename. The ensemble contains the fixed protein once, the
co-crystal ligand as `REF` when available, and at least the two highest-ranked
retained poses for every candidate as independent residue components. The
`pose_rank` and `affinity_kcal_mol` fields distinguish those poses, so an
analysis can compare the reference with a candidate or any two retained poses
in the same coordinate frame without moving the receptor.

First verify the entry in `repl` with
`host.mcp("structures-interactions", "pdb_get_structures", pdb_ids=[...])`
and resolve its ligands with
`host.mcp("structures-interactions", "pdb_get_ligands", pdb_id=..., max_ligands=25)`.
Use the returned `coordinate_files.mmcif_url`, ligand component ID, chemistry,
chain metadata, release date, and hashes as the immutable handoff. Never infer
the ligand from memory or from a ChEMBL cross-reference list.
`coordinate_files` guarantees `mmcif_url` only. Do not access `pdb_url` or any
other coordinate key unless that exact key is present in the returned record;
this workflow consumes the verified `mmcif_url` and no second coordinate route.

Call `manage_environments(mode="list", dependencies=["gemmi", "rdkit"])` and
reuse one compatible environment. Download the verified coordinate URL once
with `download_public_scientific_file`, retaining its artifact version and
SHA-256. Never fetch it through Python, Bash, a page reader, or file editing.
Parse the downloaded file through the bundled execution asset, not a newly
authored residue loop. Pass the exact component ID and chain returned by the MCP
evidence:

```bash
python "${SYNON_SKILL_DIR}/scripts/analyze_binding_pocket.py" \
  --structure "selected.cif" \
  --ligand-comp-id "verified component ID" \
  --ligand-chain "verified ligand chain" \
  --output-dir "binding_mode"
```

The asset uses `gemmi.read_structure` (not `read_cif`), requires an unambiguous
ligand, retains chain plus residue-number identity, computes bounded heavy-atom contacts, and
derives the docking box from the bound ligand heavy-atom extent with explicit
padding. Consume `pocket_contacts.csv`, `pocket_box.csv`, and the passing
`pocket_validation.json`; do not recompute the box by matching residue numbers
across chains. Keep hydrogen-bond candidates, hydrophobic contacts, metal
coordination, and van der Waals contacts as separate heuristic categories when
the task needs those classifications. Do not install packages from Python or
Bash and do not guess package methods after a failure.

This is a fast structural triage tool. It does not replace a full molecular
dynamics, docking, or quantum chemistry workflow. For final scientific claims,
inspect the returned ligand candidate, cutoff, top contacting residues, and the
original structure source before making mechanistic conclusions.

For multi-pose comparison, write one editable long-form CSV with these fields:
`candidate_id`, `rank`, `pose_rank`, `affinity_kcal_mol`, `ligand_chain`, `ligand_residue`, `protein_chain`,
`protein_residue_number`, `protein_residue_name`, `minimum_distance_angstrom`,
and `contact_category`. Also write a per-candidate summary CSV containing contact
counts by category and the nearest residues. Keep geometric contact categories
explicitly heuristic; atom distance alone does not establish hydrogen-bond
geometry, protonation, interaction energy, or binding affinity.

When writing a Markdown or CSV deliverable, materialize every contact row and
value in managed Python, write the final literal bytes once, validate the
complete file, and then call `save_artifacts`. An unresolved template is not a
report and must not be saved or published.

## Verify PDB IDs before citing or analyzing

Never invent or trust a PDB ID from memory. Before analyzing or citing a
structure, confirm the ID through the canonical structure MCP and a successful
coordinate download in this transcript. An unknown entry or missing coordinate
URL closes that candidate: remove it and re-verify from the search results
instead of keeping it.
