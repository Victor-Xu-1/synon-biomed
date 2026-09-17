# P2Rank Pocket Prediction Runtime

## Purpose and authority

The `binding-pocket-prediction.p2rank` execution pack predicts ranked
small-molecule binding pockets when a receptor has no authoritative bound
ligand, annotated site, validated pocket artifact, or user-supplied docking
box. A predicted pocket is a computational hypothesis, not experimental site
evidence.

Runtime identity is owned by
`internal/sciencecapability/scientific-capabilities.v2.json`. The registry pins
the P2Rank source URL, byte count, SHA-256, Java package, canonical Skill, input
contract, parameters, outputs, and validation fields. Generic Harness code does
not choose coordinates or contain a P2Rank-specific fallback.

## Provisioning

1. Acquire the registered P2Rank 2.5.1 archive through
   `download_public_scientific_file`. Direct `curl`, `wget`, Python download, or
   an unregistered mirror is not authoritative.
2. Verify the exact size and SHA-256 recorded in
   [`../licenses/p2rank-runtime/NOTICE.md`](../licenses/p2rank-runtime/NOTICE.md).
3. Reuse or create an immutable `local-conda` environment containing
   `openjdk=17` from `conda-forge` through `manage_environments`.
4. Run only the materialized
   `p2rank-pocket-detection/scripts/p2rank_binding_pockets.py` entrypoint in the
   implementation-bound environment.

The 275,625,956-byte source archive remains external state. Operators must
allow enough space for the archive, bounded extraction (up to 2 GB by the pack
contract), the immutable Java environment, and task output. Capacity failures
are recoverable resource conditions; they must not be hidden as format or
scientific failures.

## Structure profile selection

P2Rank's default model uses B-factor as a feature. The runtime therefore never
uses B-factor variance to infer whether a structure is crystallographic or an
AlphaFold prediction:

- explicit X-ray or neutron-diffraction `EXPDTA` selects `default` and takes
  precedence over incidental AlphaFold text in titles or remarks;
- explicit predicted/theoretical, NMR, electron-microscopy, or cryo-EM
  `EXPDTA` selects the B-factor-independent `alphafold` profile;
- without `EXPDTA`, only an AlphaFold-specific prediction title/disclaimer or
  an explicit predicted-model title is accepted as predicted provenance;
- conflicting methods, unknown `EXPDTA`, incidental AlphaFold references, and
  otherwise ambiguous structures stop before execution and require an explicit
  profile based on the verified source.

The chosen profile and reason are written to `pocket_selection.json` and the
execution log.

## Output and recovery contract

The pack stages output in a unique task-local directory and promotes it only
after validation. History and failure directories reject symbolic links,
non-directory parents, path escape, and replacement during validation. The
promoted directory contains the common `.synon-execution-pack.json` ownership
marker.

Host-owned execution logs bind the canonical pack command to exact file
digests. On later Python, R, Bash, or REPL sessions, verified promoted output
directories are mounted read-only over the writable task workspace. A changed
scientific result requires a new canonical pack execution; translations and
presentation-only edits belong in separate files.

Expected authoritative outputs are:

- `pocket_candidates.csv`;
- `pocket_selection.json`;
- `pocket_validation.json`;
- `selected_pocket_atoms.pdb`;
- `p2rank_predictions.csv` and `p2rank.log` as retained engine evidence.

If execution fails, inspect the task-local `.p2rank-failures/<generation>`
record, repair only the reported dependency/input/resource condition, and run a
new generation. Never reuse partial output as a docking box.

## Operational acceptance

Before release, run the exact pack against both:

1. a representative crystallographic PDB with verified `EXPDTA`; and
2. a representative AlphaFold/predicted PDB with verified provenance.

For each run require `overall_pass=true`, rank 1, probability in `[0,1]`, a
non-empty selected-pocket atom PDB, a finite 8–100 Angstrom docking box, matching
source/archive hashes, the intended profile, and a successful host file-write
receipt. Preserve commands, input identifiers, output digests, engine version,
Java witness, and elapsed time in the PR evidence.

## Upgrade and rollback

Changing the P2Rank version, archive digest, Java major, profile mapping, or
output schema requires a registry change, focused pack/security tests, both
real-structure acceptance runs, and an updated third-party notice. Rollback
deactivates the new runtime generation and reverts the registry/pack together;
do not reinterpret outputs produced by a different engine version.
