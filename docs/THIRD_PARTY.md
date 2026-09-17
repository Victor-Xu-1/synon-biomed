# Third-Party And Reused Runtime Inventory

Synon Go keeps reused and third-party runtime components visible and auditable.
The component inventory below records retained integration and notice surfaces.
Component-level Skill notices
are retained in `skills/synonbiomed/THIRD_PARTY_LICENSES.md`.

Version-pinned evidence for upstream module archives that omit a license file
is stored under `docs/licenses/<module>@<version>/`. Generated release records
mark these files with a `curated/` path. The tagged README for
`github.com/mattn/go-localereader@v0.0.1` declares MIT; its complete upstream
MIT license is retained here for Windows release auditing.

Version-pinned full notices for selected frontend dependencies are retained in
`docs/licenses/frontend-dependencies/`, with registry archive integrity and
explicit dual-license choices. The lockfile inventory remains separate from
these full texts; local workspace links are identified separately rather than
misreported as unlicensed third-party packages. Neither inventory nor notice
presence is a blanket ownership claim over imported source or runtime assets.

The full browser-bundle notices, including the copied RDKit runtime, are in
`docs/licenses/frontend-bundle/NOTICE.txt`. Its package/version index shares
identical texts without discarding upstream copyright or permission notices.
It is distributed with release packages; it does not cover separately
downloaded scientific software or datasets.

The shared NVIDIA BioNeMo Agent Toolkit license bundle and pinned attribution
for retained scientific Skills are in `docs/licenses/bionemo-agent-toolkit/`.
Per-file modification notices and original third-party declarations remain in
the corresponding Skill sources; external model and service terms are separate.

## Shipped Capability Assets

| Component | Target path | Source | Runtime | License pointer |
| --- | --- | --- | --- | --- |
| SynonBiomed Agents | `assets/synonbiomed/agents/` | v1.1 Agent metadata | Go loader | Root license for first-party content; component notices for third-party material |
| SynonBiomed Skills | `skills/synonbiomed/` | v1.1 Skill source | Go plus declared sidecars | `skills/synonbiomed/THIRD_PARTY_LICENSES.md` |
| Biomedical MCP tools | `assets/optional/mcp-servers/bio-tools/` | v1.1 Python source | Optional Python 3.11+ | Component notices; the inventory is not a blanket license grant |
| Ketcher Chemistry MCP App | `assets/optional/mcp-servers/ketcher-chemistry/` | v1.1 widget, Ketcher 3.12.0 | Native Go stdio MCP server plus browser JavaScript widget | `assets/optional/mcp-servers/ketcher-chemistry/NOTICE` and checksum manifest |
| Kernel and compute sidecars | `assets/optional/kernels/`, `assets/optional/compute/` | v1.1 Python and shell source | Optional Python/shell | Component notices and the unresolved-provenance scope below |
| Synon 2D Interaction Runtime | Governed immutable `local-conda` generation | ProLIF 2.2.1, RDKit 2024.3.5, CairoSVG 2.8.2, MDAnalysis 2.10.0, Pillow 12.3.0, NumPy 2.4.6 | Server-side Python analysis and publication rendering | `docs/licenses/synon-2d-interaction-runtime/NOTICE.md` |
| Biomolecular electrostatics runtime | Governed immutable `local-conda` generation | APBS 3.4.1, PDB2PQR 3.7.1, RDKit 2024.3.5, NumPy 2.4.6 | Server-side PQR preparation, aligned protein/ligand Poisson-Boltzmann OpenDX generation, and Mol* surface coloring | `docs/licenses/synon-scientific-runtime-warmups/NOTICE.md` |
| Scientific runtime warmups | Governed immutable `local-conda` generations | Common structure toolkit; optional AutoDock Vina stack | Post-install background preparation selected during first-run setup | `docs/licenses/synon-scientific-runtime-warmups/NOTICE.md` |
| P2Rank pocket prediction runtime | External governed download plus immutable `local-conda` generation | P2Rank 2.5.1 archive and OpenJDK 17 | Registered binding-pocket prediction execution pack | `docs/licenses/p2rank-runtime/NOTICE.md`; operations: `docs/engineering/p2rank-runtime.md` |
| Micromamba 2.9.0+synon.1 | `assets/optional/micromamba/` (Linux amd64 only) | Official source `2676ec2050f7dd5b8a524287526f50a8a4fb9652` with controlled link-script exit handling; `BUILD.md` and manifest record the build boundary | Single optional native environment manager | `assets/optional/micromamba/LICENSE` (BSD-3-Clause), `DEPENDENCY-NOTICES.txt` |
| Synon Link extension | `assets/synon-link/` | Pinned v0.6.10 package | Chromium extension | Local manifest and upstream package notices |
| AionCore provider/tool logos | `internal/logoassets/assets/` | iOfficeAI/AionCore commit `020a27a77aeb2b5be7a2b4380aab8ae2686311c4` | Embedded Go Web assets | Source: `internal/logoassets/{LICENSE,SOURCE.md}`; release: `docs/licenses/aioncore-logos/` (Apache-2.0; third-party trademarks remain with their owners) |
| UCSC chromosome-size metadata | `frontend/public/genomes/ucsc/` | UCSC hg38, hg19, mm39, and mm10 assembly metadata | Offline IGV coordinate references | `frontend/public/genomes/ucsc/{NOTICE.txt,PROVENANCE.json}` and UCSC Conditions of Use |
| Go dependencies | `go.mod`, `go.sum` | Versioned Go modules | Compiled into release | Generated `SBOM.spdx.json` and `THIRD_PARTY_LICENSES.json` |
| Frontend source | `frontend/` | Source import and migration manifests in that directory | Web renderer | `frontend/LICENSE` and `frontend/THIRD_PARTY_LICENSES.json`; release copies: `docs/licenses/frontend/LICENSE` and `docs/frontend-third-party-licenses.json` |

## Retained Material With Unresolved Provenance

The Shell grammar parser used by `internal/executionprep` is `mvdan.cc/sh/v3`
v3.14.1 (BSD-3-Clause). It parses syntax only and does not introduce a second
execution runtime. Its license is retained in the upstream module, generated
release inventory, and embedded third-party license page.

The original redistribution license has not been established for the complete
retained contents of these legacy components:

- `assets/optional/kernels/kernel_worker.R`;
- `assets/optional/kernels/synon_host_bridge.py`;
- `assets/optional/compute/operon_compute_provider/` and
  `assets/optional/compute/provider_kernel_bootstrap.py`;
- `assets/synonbiomed/agents/bookmarker/metadata.yaml`.

Publication does not establish a redistribution grant for this material.
Neither the first-party AGPL license nor a first-party commercial agreement
purports to grant rights owned by another party. Public copies, similar code,
and import records alone do not establish such permission. Existing component
notices remain intact; this statement does not revoke any valid upstream grant.
This is a disclosure of unresolved provenance, not a finding of infringement.

The Agent, Skill and MCP inventories also do not establish that every local
file has been traced to an original rights holder. Apply each component's
actual license to its covered content; do not treat inventory inclusion or
notice presence as blanket permission to redistribute or commercially
relicense all bundled material.

## Not Implicitly Redistributable

The historical reuse inventory records the license observed at import time;
it is not a replacement for current first-party licensing or upstream notices.
The root license and commercial licensing notice describe the first-party grant,
subject to the separate component terms and unresolved scope above.

Conda lock metadata records each actual upstream package URL, version, build,
content digest, and license identifier. Schema version 2 omits comparison-only
metadata. Package and license records are unchanged by this metadata migration;
the inventory does not grant rights to redistribute upstream package binaries.
Historical environment IDs are accepted only at the configuration compatibility
boundary, and immutable database migration identities remain unchanged.

The memory classifier, memory rules, extraction instructions and operation
schema under `internal/memoryclassifier/assets`, `internal/memoryprompt/assets`
and `internal/memoryextract/assets` are maintained as first-party assets under
the root license. Their contract is the typed memory API and behavioral tests;
they do not require a separately installed application or recovered prompt
bundle. Historical assets are not part of the current source distribution.

The Python worker entrypoint and the `worker_*` modules under
`assets/optional/kernels/synon_biomed_runtime/`, the Linux syscall-policy builder
in `internal/kernel/confinement_filter_linux_amd64.go`, and the research/reviewer
definitions in `assets/synonbiomed/agents/{operon,reviewer}/metadata.yaml` are
maintained as first-party implementations under the root license. Their current
behavioral contract is documented in `docs/engineering/kernel-execution-contract.md`.
They do not load a frozen interpreter implementation. This scope does not
relicense adjacent R, host-bridge, compute, Agent or Skill assets; those retain
their own provenance and applicable component notices. Import-time inventories
remain historical records, not proof of permission for a new distribution.

Model weights, downloaded datasets, credentials, user files, caches, local
databases, and generated runtime output are not source or release assets.
Seccomp helpers, BioNeMo vendor snapshots, image runtimes, fonts, and seed
archives are included only after their exact
non-Web runtime need, license, checksum, and managed installation path pass the
active Goal's reuse decision gate.

## Update Rules

1. Preserve upstream notices and exact protocol directory names.
2. Update the local SHA-256 asset manifest whenever a shipped asset changes.
3. Do not commit dependency installation trees or Python bytecode caches.
4. Record source, version, license, checksum, runtime wiring, and release need
   for every new third-party component.
5. Final releases include generated SPDX 2.3 `SBOM.spdx.json`, consolidated
   `THIRD_PARTY_LICENSES.json`, in-toto/SLSA `PROVENANCE.intoto.json`, and this
   inventory. Generation reads real binary build info and fails when a linked
   module lacks a bounded upstream license file.
