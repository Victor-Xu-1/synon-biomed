# Synon Scientific Runtime Warmups Notice

Synon Biomed can prepare optional scientific software after the Web gateway is
available. These distributions are downloaded into immutable local runtime
generations under the operator's external state directory. They are not
committed to the source repository or copied into a release archive.

The default common-structure runtime declares the following pinned Python
distributions. RDKit reuses the release-owned managed Python baseline; the
remaining wheels add only the corresponding structure and configuration
utilities.

| Distribution | Version | License |
| --- | ---: | --- |
| RDKit | 2024.3.5 | BSD-3-Clause |
| BioPython | 1.88 | Biopython License Agreement / BSD-style |
| Gemmi | 0.7.5 | MPL-2.0 |
| PyYAML | 6.0.3 | MIT |

The AutoDock Vina runtime publishes a current planning estimate and is selected
by default during first-run setup. A user can clear that selection before
continuing. Estimates are not runtime validation ceilings. Its direct package
authority is pinned as follows; the managed environment retains the complete
resolved transitive inventory and license metadata.

| Distribution | Version | License |
| --- | ---: | --- |
| AutoDock Vina | 1.2.7 | Apache-2.0 |
| Meeko | 0.8.0 | LGPL-2.1-only |
| RDKit | 2026.03.1 | BSD-3-Clause |
| Gemmi | 0.7.5 | MPL-2.0 |
| ProDy | 2.6.1 | MIT |
| BioPython | 1.88 | Biopython License Agreement / BSD-style |

The biomolecular electrostatics runtime is selected by default and is prepared
as a governed local environment rather than committed as a binary archive.
Its APBS distribution is obtained from conda-forge for the active platform;
PDB2PQR and RDKit are installed through the same immutable environment request.

| Distribution | Version | License |
| --- | ---: | --- |
| APBS | 3.4.1 | BSD-3-Clause |
| PDB2PQR | 3.7.1 | BSD-3-Clause |
| RDKit | 2024.3.5 | BSD-3-Clause |
| NumPy | 2.4.6 | BSD-3-Clause |

The runtime retains the resolved PROPKA, mmcif-pdbx, Requests, and APBS native
dependency metadata and license texts in its generated inventory. Electrostatic
results record the solver and preparation versions, pH, ionic strength, force
field, mesh spacing, potential units, aligned protein/ligand grid bounds, and
input SHA-256.

Each generated environment preserves installed distribution metadata. Any
redistribution of a prepared environment must preserve the applicable upstream
license texts, notices, and modification or relinking rights.

## Additional pharmaceutical-domain groups

The first-run catalog also exposes the following pinned direct requirements.
They are initially selected in the first-run interface and can be cleared by
the user before continuing. The managed environment marker records the complete
resolved inventory; installed distribution metadata is the authority for
transitive licenses.

| Runtime group | Pinned direct requirements |
| --- | --- |
| Drug chemistry and process calculations | `thermo==0.6.1`, `chemicals==1.5.2`, `chempy==0.10.1`, `ase==3.29.0`, `tabulate==0.9.0`, `requests==2.32.5`, `beautifulsoup4==4.14.2` |
| Molecular conversion and rapid quantum tools | `openbabel=3.2.1`, `xtb=6.7.1`, `ase=3.29.0`, `dimorphite-dl==2.0.2` |
| Classical QSAR and ADMET | `scikit-learn==1.9.0`, `xgboost==3.2.0`, `lightgbm==4.7.0`, `shap==0.51.0`, `optuna==5.0.0`, `mordredcommunity==2.0.7` |
| Clinical statistics and pharmacometrics | `lifelines==0.30.3`, `scikit-learn==1.9.0`, `pingouin==0.6.1`, `lmfit==1.3.4`, `pint==0.25.3`, `openpyxl==3.1.5` |
| Single-cell and omics analysis | `scanpy==1.11.5`, `anndata==0.12.19`, `harmonypy==0.0.10`, `leidenalg==0.10.2`, `igraph==0.11.9` |
| Pharmacogenomics command line | `samtools=1.24`, `bcftools=1.24`, `bedtools=2.31.1`, `minimap2=2.31`, `seqkit=2.13.0`, `pysam=0.24.1` |
| Molecular dynamics and simulation | `MDAnalysis==2.10.0`, `mdtraj==1.11.1.post2`, `ParmEd==4.3.1`, `openmm==8.6.0` |
| Medical imaging analysis | `pydicom==3.0.2`, `nibabel==5.4.2`, `SimpleITK==2.5.6`, `scikit-image==0.26.0` |
| Instrument and analytical data | `allotropy==0.1.55`, `pandas==2.0.3`, `openpyxl==3.1.2`, `pdfplumber==0.9.0` |
