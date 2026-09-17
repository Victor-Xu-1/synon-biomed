---
name: quantum-chemistry-postprocessing
description: Post-process DFT/ab initio results with Multiwfn and molecular visualization. Analyze electrostatic potential, charge distribution, HOMO/LUMO orbitals, bond orders (Mayer/Wiberg), electron density topology (QTAIM). Render ESP surfaces and orbital images for publications. Handles .molden, .fchk, .wfn, .wfx, .cube, .pdb, .xyz inputs.
---
# Quantum Chemistry Post-Processing

Post-process DFT output: orbital analysis, charge distribution, ESP surfaces, publication-ready renders.

## Prerequisites

Python packages:
```bash
pip install numpy matplotlib pandas ase
```

Multiwfn (free): http://sobereva.com/multiwfn/
VMD (free for academics): https://www.ks.uiuc.edu/Research/vmd/

## Input types

| Format | Source | What it contains |
|--------|--------|-----------------|
| .molden | XTB, ORCA, Gaussian | Orbitals, wavefunction |
| .fchk | Gaussian | Full wavefunction data |
| .wfn/.wfx | Gaussian, ORCA | Wavefunction file |
| .cube | Gaussian, ORCA, VASP | Volumetric data |
| .xyz | Any | Atomic coordinates |
| .pdb | Any | Protein/small molecule structure |

## Analysis workflows

### 1. Orbital analysis (HOMO/LUMO energies & composition)

```bash
Multiwfn_noGUI input.molden << EOF
1
1
q
EOF
```

Parse output with Python:
```python
import re
text = open("multiwfn.out").read()
homo = re.search(r"HOMO\s+\(eV\)\s*:\s*(-?\d+\.\d+)", text)
lumo = re.search(r"LUMO\s+\(eV\)\s*:\s*(-?\d+\.\d+)", text)
E_HOMO = float(homo.group(1)) if homo else None
E_LUMO = float(lumo.group(1)) if lumo else None
print(f"HOMO: {E_HOMO:.3f} eV, LUMO: {E_LUMO:.3f} eV, Gap: {E_LUMO-E_HOMO:.3f} eV")
```

### 2. Electrostatic potential (ESP) on vdW surface

```bash
Multiwfn_noGUI input.molden << EOF
12
1
4
q
EOF
```

### 3. ESP mapped on electron density surface

```bash
Multiwfn_noGUI input.molden << EOF
12
3
6
q
EOF
```

### 4. Bond order analysis (Mayer)

```bash
Multiwfn_noGUI input.molden << EOF
7
1
q
EOF
```

### 5. Electron density topology (AIM/QTAIM)

```bash
Multiwfn_noGUI input.fchk << EOF
2
1
q
EOF
```

### 6. Excited state analysis (hole-electron)

```bash
Multiwfn_noGUI input.molden << EOF
18
1
q
EOF
```

## VMD rendering (publication-quality)

After Multiwfn generates cube files, render with VMD:

```tcl
mol new density.cube
mol addfile esp.cube
mol modstyle 0 0 Isosurface 0.01 1 0
mol modcolor 0 0 Volume 0 0
axes location Off
color Display Background white
render TachyonInternal esp.png
```

```bash
vmd -e render_esp.vmd -dispdev text
```

## Batch processing

```python
import os, glob, subprocess, json

inputs = glob.glob("inputs/*.molden")
results = []
for inp in inputs:
    name = os.path.splitext(os.path.basename(inp))[0]
    outdir = f"analysis_{name}"
    os.makedirs(outdir, exist_ok=True)
    # Run Multiwfn
    subprocess.run(["Multiwfn_noGUI", inp],
        stdin=open("multiwfn_script.txt"),
        cwd=outdir, capture_output=True)
    # Parse results
    results.append({"molecule": name, ...})
    
json.dump(results, open("batch_summary.json", "w"))
```

## Save artifacts

- `{molecule}_HOMO_LUMO.png` -- orbital visualization
- `{molecule}_ESP.png` -- ESP surface rendering (published figure)
- `{molecule}_properties.json` -- HOMO, LUMO, dipole, atomic charges
- `batch_summary.csv` -- aggregated properties across inputs

## References

- Multiwfn: Lu & Chen, J. Comput. Chem. 2012, 33, 580-592
- VMD: Humphrey et al., J. Mol. Graph. 1996, 14, 33-38
