from __future__ import annotations

import json
import math
import os
import re
from pathlib import Path
from typing import Sequence


def _safe_label(label: object) -> str:
    text = str(label).strip() or "molecule"
    safe = "".join(ch if ch.isalnum() or ch in ("-", "_", ".") else "_" for ch in text)
    return safe[:96] or "molecule"


def _ensure_parent(path: str | os.PathLike[str]) -> Path:
    p = Path(path)
    p.parent.mkdir(parents=True, exist_ok=True)
    return p


def _save_rdkit_image(image, path: str | os.PathLike[str]) -> str:
    """Save an RDKit drawing result across PIL/SVG/bytes return variants."""
    p = _ensure_parent(path)
    if hasattr(image, "save"):
        image.save(str(p))
    elif isinstance(image, bytes):
        p.write_bytes(image)
    elif isinstance(image, str):
        p.write_text(image, encoding="utf-8")
    else:
        raise TypeError(f"unsupported RDKit image object: {type(image).__name__}")
    if not p.exists() or p.stat().st_size <= 0:
        raise RuntimeError(f"rendered image was not written: {p}")
    return str(p)


def _write_json(path: str | os.PathLike[str], payload: dict) -> str:
    p = _ensure_parent(path)
    p.write_text(json.dumps(payload, indent=2, ensure_ascii=False), encoding="utf-8")
    if p.stat().st_size <= 0:
        raise RuntimeError(f"metadata JSON was not written: {p}")
    return str(p)


def _json_for_script(value: object) -> str:
    return json.dumps(value, ensure_ascii=False).replace("</", "<\\/")


def _css_color(value: str) -> str:
    return re.sub(r"[^#a-zA-Z0-9(),.% -]", "", value).strip() or "white"


def _find_vendored_3dmol_js() -> Path | None:
    """Locate the 3Dmol.js asset shipped with this Synon Biomed runtime."""
    roots = [Path(__file__).resolve(), Path.cwd().resolve()]
    seen: set[Path] = set()
    for root in roots:
        for base in [root, *root.parents]:
            if base in seen:
                continue
            seen.add(base)
            for asset_dir in (
                base / "runtime" / "assets" / "web-dist" / "assets",
                base / "assets" / "web-dist" / "assets",
                base / "web-dist" / "assets",
                base / "web" / "assets",
            ):
                if not asset_dir.exists():
                    continue
                candidates = sorted(asset_dir.glob("3Dmol*.js"), key=lambda p: p.stat().st_size, reverse=True)
                if candidates:
                    return candidates[0]
    return None


def _load_vendored_3dmol_js() -> str:
    asset = _find_vendored_3dmol_js()
    if asset is None:
        raise FileNotFoundError("could not locate vendored 3Dmol.js in the Synon Biomed runtime assets")
    js = asset.read_text(encoding="utf-8")
    if "$3Dmol" not in js and "3Dmol" not in js:
        raise RuntimeError(f"vendored 3Dmol.js asset looks invalid: {asset}")
    return js.replace("</script", "<\\/script")


def _inline_vendored_3dmol(html: str) -> str:
    """Replace py3Dmol CDN script references with the vendored local 3Dmol.js."""
    js = _load_vendored_3dmol_js()
    inline = f"<script>\n{js}\n</script>"
    script_re = re.compile(
        r"<script\b[^>]*\bsrc=[\"'][^\"']*(?:3Dmol|3dmol)[^\"']*[\"'][^>]*>\s*</script>",
        flags=re.IGNORECASE,
    )
    patched, count = script_re.subn(inline, html, count=1)
    if count:
        return patched
    if "<head>" in html:
        return html.replace("<head>", f"<head>\n{inline}", 1)
    return f"<!doctype html><html><head>{inline}</head><body>{html}</body></html>"


def render_molecule_images(
    smiles: Sequence[str],
    ids: Sequence[str] | None = None,
    *,
    out_dir: str | os.PathLike[str] = ".",
    grid_filename: str = "molecules_2d_grid.png",
    mol_prefix: str = "mol_",
    use_svg: bool = True,
    mol_size: tuple[int, int] = (500, 350),
    grid_sub_img_size: tuple[int, int] = (400, 320),
    mols_per_row: int = 4,
    max_grid_molecules: int | None = None,
    metadata_filename: str | os.PathLike[str] | None = None,
) -> dict:
    """Render molecule images without a JSON sidecar by default.

    Structured metadata remains available in-memory under ``metadata``. A
    metadata file is written only when the caller explicitly supplies
    ``metadata_filename``.
    """
    from rdkit import Chem
    from rdkit.Chem import Draw

    if not smiles:
        raise ValueError("smiles must contain at least one entry")

    labels = list(ids) if ids is not None else [f"mol-{i + 1:02d}" for i in range(len(smiles))]
    if len(labels) != len(smiles):
        raise ValueError(f"ids length ({len(labels)}) does not match smiles length ({len(smiles)})")
    if mols_per_row <= 0:
        raise ValueError("mols_per_row must be positive")
    if max_grid_molecules is not None and max_grid_molecules <= 0:
        raise ValueError("max_grid_molecules must be positive when provided")
    if not isinstance(use_svg, bool):
        raise TypeError("use_svg must be a boolean")
    if metadata_filename is not None and not str(metadata_filename).strip():
        raise ValueError("metadata_filename must be non-empty when provided")

    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    valid_mols = []
    valid_labels = []
    invalid = []
    molecule_records = []
    for idx, (smi, label) in enumerate(zip(smiles, labels), start=1):
        mol = Chem.MolFromSmiles(str(smi))
        if mol is None:
            error = "RDKit could not parse SMILES"
            invalid_record = {
                "ok": False,
                "index": idx,
                "id": str(label),
                "input_smiles": str(smi),
                "smiles": str(smi),
                "error": error,
            }
            invalid.append({"index": idx, "id": str(label), "smiles": str(smi), "error": error})
            molecule_records.append(invalid_record)
            continue
        valid_mols.append(mol)
        valid_labels.append(str(label))
        molecule_records.append(
            {
                "ok": True,
                "index": idx,
                "id": str(label),
                "input_smiles": str(smi),
                "canonical_smiles": Chem.MolToSmiles(mol, canonical=True),
            }
        )

    if not valid_mols:
        raise ValueError(f"no valid SMILES were provided; invalid={invalid!r}")

    molecule_pngs = []
    molecule_svgs = []
    record_by_label = {record["id"]: record for record in molecule_records}
    for mol, label in zip(valid_mols, valid_labels):
        png_path = out / f"{mol_prefix}{_safe_label(label)}.png"
        Draw.MolToFile(mol, str(png_path), size=mol_size, legend=label)
        if not png_path.exists() or png_path.stat().st_size <= 0:
            raise RuntimeError(f"failed to render molecule PNG: {png_path}")
        if use_svg:
            svg_path = out / f"{mol_prefix}{_safe_label(label)}.svg"
            svg = Draw.MolsToGridImage([mol], molsPerRow=1, subImgSize=mol_size, legends=[label], useSVG=True)
            svg_path.write_text(str(svg), encoding="utf-8")
            if not svg_path.exists() or svg_path.stat().st_size <= 0:
                raise RuntimeError(f"failed to render molecule SVG: {svg_path}")
            molecule_svgs.append(str(svg_path))
        molecule_pngs.append(str(png_path))
        record_by_label[label]["png_path"] = str(png_path)
        if use_svg:
            record_by_label[label]["svg_path"] = str(svg_path)

    page_size = max_grid_molecules or len(valid_mols)
    grid_pages = []
    grid_base = Path(grid_filename)
    page_count = math.ceil(len(valid_mols) / page_size)
    for page_index in range(page_count):
        start = page_index * page_size
        end = start + page_size
        page_mols = valid_mols[start:end]
        page_labels = valid_labels[start:end]
        page_filename = grid_filename
        if page_count > 1:
            page_filename = f"{grid_base.stem}_page_{page_index + 1:02d}{grid_base.suffix or '.png'}"
        grid = Draw.MolsToGridImage(
            page_mols,
            molsPerRow=mols_per_row,
            subImgSize=grid_sub_img_size,
            legends=page_labels,
            useSVG=False,
        )
        grid_pages.append(_save_rdkit_image(grid, out / page_filename))

    metadata = {
        "input_count": len(smiles),
        "valid_count": len(valid_mols),
        "invalid_count": len(invalid),
        "grid_pages": grid_pages,
        "molecule_pngs": molecule_pngs,
        "molecule_svgs": molecule_svgs,
        "molecules": molecule_records,
    }
    metadata_json = None
    if metadata_filename is not None:
        metadata_json = _write_json(out / metadata_filename, metadata)
    return {
        "grid": grid_pages[0],
        "grid_pages": grid_pages,
        "molecule_pngs": molecule_pngs,
        "molecule_svgs": molecule_svgs,
        "valid_ids": valid_labels,
        "invalid_smiles": invalid,
        "molecules": molecule_records,
        "metadata": metadata,
        "metadata_json": metadata_json,
    }


def save_py3dmol_html(view, out_html: str | os.PathLike[str]) -> str:
    """Save a py3Dmol view as self-contained HTML."""
    p = _ensure_parent(out_html)
    if not hasattr(view, "_make_html"):
        raise TypeError("view must be a py3Dmol view object with _make_html()")
    html = view._make_html()
    if not isinstance(html, str) or "3Dmol" not in html:
        raise RuntimeError("py3Dmol did not return a usable HTML viewer")
    html = _inline_vendored_3dmol(html)
    p.write_text(html, encoding="utf-8")
    if p.stat().st_size <= 0:
        raise RuntimeError(f"py3Dmol HTML was not written: {p}")
    return str(p)


def make_py3dmol_view_html(
    pdb_text: str,
    out_html: str | os.PathLike[str],
    *,
    width: int = 800,
    height: int = 600,
    cartoon_color: str = "lightblue",
    background: str = "white",
    self_contained: bool = True,
) -> str:
    """Create a basic py3Dmol protein viewer from PDB text and save it as HTML."""
    if not pdb_text.strip():
        raise ValueError("pdb_text is empty")
    if self_contained:
        js = _load_vendored_3dmol_js()
        html = f"""<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Synon Biomed 3D Viewer</title>
  <style>
    html, body, #viewer {{
      width: 100%;
      height: 100%;
      min-height: {int(height)}px;
      margin: 0;
      background: {_css_color(background)};
      overflow: hidden;
    }}
  </style>
  <script>
{js}
  </script>
</head>
<body>
  <div id="viewer"></div>
  <script>
    const pdbText = {_json_for_script(pdb_text)};
    const threeDmol = window.$3Dmol || window["$3Dmol"] || window["3Dmol"];
    if (!threeDmol) {{
      document.body.textContent = "3Dmol.js failed to initialize.";
      throw new Error("3Dmol.js failed to initialize");
    }}
    window.$3Dmol = threeDmol;
    const viewer = threeDmol.createViewer("viewer", {{ backgroundColor: {_json_for_script(background)} }});
    viewer.addModel(pdbText, "pdb");
    viewer.setStyle({{}}, {{ cartoon: {{ color: {_json_for_script(cartoon_color)} }} }});
    viewer.setStyle({{ hetflag: true }}, {{ stick: {{ colorscheme: "Jmol" }} }});
    viewer.setStyle({{ resn: "HOH" }}, {{}});
    viewer.zoomTo();
    viewer.render();
  </script>
</body>
</html>
"""
        p = _ensure_parent(out_html)
        p.write_text(html, encoding="utf-8")
        if p.stat().st_size <= 0:
            raise RuntimeError(f"3Dmol HTML was not written: {p}")
        return str(p)

    import py3Dmol

    view = py3Dmol.view(width=width, height=height)
    view.addModel(pdb_text, "pdb")
    view.setStyle({"cartoon": {"color": cartoon_color}})
    view.setBackgroundColor(background)
    view.zoomTo()
    return save_py3dmol_html(view, out_html)
