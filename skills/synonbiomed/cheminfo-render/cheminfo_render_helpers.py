"""Compatibility import for the manifest-verified kernel runtime helper."""

from synon_biomed_runtime.cheminfo_render import (
    make_py3dmol_view_html,
    render_molecule_images,
    save_py3dmol_html,
)

__all__ = [
    "make_py3dmol_view_html",
    "render_molecule_images",
    "save_py3dmol_html",
]
