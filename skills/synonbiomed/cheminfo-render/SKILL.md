---
name: cheminfo-render
description: "Stable chemistry rendering helpers for RDKit molecule PNGs, molecule grid images, SMILES tables, and py3Dmol HTML viewers. Load before drawing small molecules, CRBN/PROTAC structures, binding-pocket views, or 2D/3D chemical figures. Provides render_molecule_images(), save_py3dmol_html(), and make_py3dmol_view_html(). Avoids common RDKit/PIL/py3Dmol API mistakes, CDN-dependent 3Dmol viewers, shell process substitution, and fragile f-string HTML generation."
license: Apache-2.0
---

# Cheminfo Render

Use this skill whenever a task needs molecule structure images, molecule grids,
or browser-viewable 3D protein/ligand scenes.

The helpers are shipped as manifest-verified Python kernel runtime assets.
Loading this skill supplies the usage instructions; import the stable helper
module explicitly before rendering. `kernel.py` is only a compatibility
re-export for runtimes that support skill kernel modules, not a second copy of
the implementation. Prefer these helpers over hand-written low-level RDKit or
py3Dmol drawing code.
The 3D viewer helpers are self-contained by default: they inline the vendored
3Dmol.js runtime shipped with Synon Biomed, so the generated HTML does not need
CDN access after it is saved.

In a compatible Synon Biomed managed Python runtime, import the helpers as:

```python
from cheminfo_render_helpers import (
    make_py3dmol_view_html,
    render_molecule_images,
    save_py3dmol_html,
)
```

Do not import `rdMolDraw2D` from `rdkit.Chem`; use
`from rdkit.Chem.Draw import rdMolDraw2D` when low-level drawing is unavoidable.
Do not set RDKit drawing options before checking they exist with `hasattr`.
Do not call `rdMolDraw2D.PrepareAndDrawInPNG`; that API is not present in the
managed RDKit runtime. Use the first-party helper instead.

## Correct Patterns

1. For a list of SMILES strings, call `render_molecule_images(...)`.
   Its complete stable keyword contract is `out_dir`, `grid_filename`,
   `mol_prefix`, `use_svg`, `mol_size`, `grid_sub_img_size`,
   `mols_per_row`, `max_grid_molecules`, and the opt-in `metadata_filename`.
   `use_svg=True` is the default and writes one SVG per valid molecule;
   the default call does not write a JSON metadata sidecar. Structured
   metadata is returned in memory; supply an explicit `metadata_filename`
   only when a separate export is required.
   `use_svg=False` intentionally writes PNGs only. Do not invent other
   rendering flags.

2. For a py3Dmol scene, create the `py3Dmol.view(...)`, style it, then call
   `save_py3dmol_html(view, "pocket_3d.html")`.

3. If you already have PDB text and only need an HTML viewer, call
   `make_py3dmol_view_html(pdb_text, "viewer.html")`.

   For `docking_complex_ensemble.pdb`, keep
   `docking_components.csv` beside the viewer and use its residue mapping for
   labels, legends, and selections. Render the fixed protein as a restrained
   cartoon and show one selected ligand pose at a time without refitting or
   moving the receptor camera. Provide previous/next pose navigation, the
   candidate ID, pose rank, and affinity beside the scene. Also provide an
   optional two-ligand comparison in the same fixed pocket: reference versus
   candidate or any two retained poses, each with an independently selectable
   contrasting color. A screenshot or HTML view never replaces the editable
   PDB, SDF, component CSV, pose-score CSV, and ranking CSV.

4. For long HTML viewers, build and save the whole file in Python with these
   helpers. Do not generate viewer HTML with shell heredocs, process
   substitution, or f-strings containing JavaScript/PDB backslashes.

## Avoid

- Do not set `MolDrawOptions.fontSize`; it is not a stable RDKit option.
- Do not write `img.data` from `Draw.MolsToGridImage(...)`; RDKit returns a PIL
  image in this runtime, so use `img.save(...)`.
- Do not call `view.png("file.png")`; py3Dmol's static PNG capture is browser
  dependent and this runtime path saves reliable HTML viewers instead.
- Do not download 3Dmol.js from `3dmol.org`, jsDelivr, unpkg, or another CDN
  inside a task. The shipped helper already embeds the local vendored 3Dmol.js.

## Example

```python
smiles = ["O=C1CCC(=O)NC1=O", "CC(=O)Nc1ccc(O)cc1"]
ids = ["glutarimide", "acetaminophen"]

result = render_molecule_images(
    smiles,
    ids=ids,
    grid_filename="molecules_2d_grid.png",
    mol_prefix="mol_",
    use_svg=True,
)

save_artifacts([result["grid"], *result["molecule_pngs"]], language="python")
```

For a design set, label every molecule with the same stable `candidate_id` used
in the SDF, ranking CSV, docking PDB REMARK records, and component registry.
Publish the overview grid and a self-contained 3D viewer when they materially
help review; keep per-molecule images optional to avoid flooding the file panel.
