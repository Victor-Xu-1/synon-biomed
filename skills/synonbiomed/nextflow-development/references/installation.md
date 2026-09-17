# Governed runtime setup

> Modified for Synon Biomed's managed scientific workflows. Upstream attribution
> and Apache-2.0 terms remain in the skill/catalog notices.

Provision workflow dependencies through `manage_environments` and
`manage_packages`, then run checks and workflows with that exact environment.
Do not download and execute remote installer scripts or modify the system
Python, Java, or container runtime from a skill.

## Runtime requirements

- Nextflow 23.04 or newer, pinned for the run.
- Java 11 or newer.
- Python packages used by the helper scripts: `requests` and `pyyaml`.
- One approved container profile: Docker, Singularity, or Apptainer.
- Sufficient workspace, cache, memory, and CPU for the selected nf-core test
  and production profiles.

Request Nextflow, Java, Python, and helper packages from the local governed
provider. A container daemon or HPC container runtime is an operator-managed
dependency; report it as a blocker when unavailable instead of installing it
with elevated privileges.

## Verification

Run all checks inside the immutable runtime generation:

```bash
nextflow -version
java -version
python scripts/check_environment.py
nextflow run nf-core/demo -r <pinned-release> -profile test,<container-profile> --outdir test_demo
```

Record versions, the container profile, the test run receipt, output hashes,
and the exact nf-core release before using scientific data.

## Common issues

- **Container unavailable:** ask the operator to enable an approved runtime.
- **Wrong Java/Nextflow version:** revise the governed provider plan and create
  a new immutable generation.
- **Cache or disk pressure:** relocate bounded caches to an authorized
  workspace and report required capacity.
- **Permission failure:** do not use `sudo`; request an approved runtime or
  escalate to the system administrator.
