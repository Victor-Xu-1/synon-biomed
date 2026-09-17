# Synon 2D Interaction Runtime Notice

The Synon 2D Interaction Engine is independently implemented by Synon Biomed.
It does not contain or invoke Schrödinger source code or executables.

The server declares the following exact Python distributions to the governed
`local-conda` software provider. They are installed into an immutable runtime
generation on demand and are not committed to this repository.

| Distribution | Version | Project | License |
| --- | ---: | --- | --- |
| ProLIF | 2.2.1 | <https://github.com/chemosim-lab/ProLIF> | Apache-2.0 |
| RDKit | 2024.3.5 | <https://www.rdkit.org/> | BSD-3-Clause |
| CairoSVG | 2.8.2 | <https://cairosvg.org/> | LGPL-3.0-or-later |
| MDAnalysis | 2.10.0 | <https://www.mdanalysis.org/> | LGPL-2.1-or-later and LGPL-3.0-or-later; bundled component notices remain authoritative |
| Pillow | 12.3.0 | <https://python-pillow.org/> | MIT-CMU |
| NumPy | 2.4.6 | <https://numpy.org/> | BSD-3-Clause and bundled component licenses |

The managed environment retains each installed distribution's metadata and
license files. Any distributed runtime must preserve those notices and provide
the corresponding LGPL materials and relinking/modification rights required by
the upstream licenses. Generated SVG, PNG, and JSON outputs are user data and
are not derived copies of the upstream library source.
