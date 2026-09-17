"""OpenMM molecular dynamics with explicit or implicit solvent on CUDA.

The CUDA platform plugin is installed from conda-forge with the exact versions
validated by the published provider environment. The import witness runs while
the immutable image is built so an unusable environment never becomes active.
"""

import modal


META = {
    "packages": ["openmm", "openmmforcefields", "pdbfixer", "mdtraj", "openff-toolkit"],
    "gpu_default": "A100",
    "egress_domains": [],
}


def build(
    *, secrets: dict[str, str] | None = None
) -> tuple["modal.Image", dict[str, "modal.Volume"], dict[str, str]]:
    del secrets
    image = (
        modal.Image.micromamba(python_version="3.11")
        .micromamba_install(
            "openmm=8.2.0",
            "openmmforcefields=0.15.1",
            "pdbfixer=1.12",
            "mdtraj=1.11.1",
            "openff-toolkit=0.18.0",
            "cudatoolkit=11.8",
            "numpy=2.4.6",
            channels=["conda-forge"],
        )
        .run_commands(
            "ln -sf /opt/conda/bin/python /usr/local/bin/python && "
            "ln -sf /opt/conda/bin/python3 /usr/local/bin/python3 && "
            "python -c 'import openmm, pdbfixer, mdtraj, openmmforcefields, "
            "openff.toolkit; assert openmm.Platform.getNumPlatforms() >= 1'"
        )
    )
    return image, {}, {}
