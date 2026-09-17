# Source Hygiene

## Directory Ownership

| Class | Location | Commit | Release |
| --- | --- | --- | --- |
| Production source | cmd, internal, skills, scripts | Yes | Selected by release manifest |
| Versioned capability assets | assets | Yes | Yes when manifest marks required |
| Protocol scenarios | docs/compatibility/scenarios | Sanitized regression inputs only | No |
| Historical engineering records | External private archives | No | No |
| Tests and fixtures | package testdata directories | Yes when sanitized and minimal | No unless explicitly required |
| Build output | dist, release | No | Produced by release pipeline |
| Runtime state | runtime, workspace, uploads, users, mcp-output | No | Created under configured data directory |
| Dependencies and caches | node_modules, vendor, language/build caches | No | Installed or built from lockfiles/manifests |

## Prohibited Source Residue

The source audit rejects credentials, local databases, logs, PID files,
dependency trees, runtime-state directories, test output, Python caches, editor
backups, patch rejects, operating-system metadata, `.superpowers`, and
`docs/superpowers` construction state. It also rejects a missing or symlinked
root `LICENSE`, retired engineering records, or any `*goal*.md` construction
document. Collaboration policies, task conversations and historical acceptance
reports live outside the source checkout; they are not clone/build inputs.
A top-level allowlist rejects otherwise innocuous but
unclassified files and directories. Historical captures, progress ledgers
and migration plans are forbidden from returning as clone or build inputs.

Capability names are never used as an exclusion rule. Whether a component
belongs in the project is decided by the current capability contracts,
applicable permissions and runtime evidence, not by a filename.

## Large And Binary Artifacts

There is no arbitrary package-size ceiling. Archives, native binaries, WebAssembly
modules, wheels, and files larger than 10 MiB require a JSON manifest in the
same directory. The manifest must record the exact filename, SHA-256 digest, and
byte count. A changed or unclassified artifact fails the source audit.

Component notices retain source and license information. A local artifact
manifest proves integrity; it does not replace provenance or licensing review.

## Commands

Run the focused fixture test:

    bash scripts/audit/audit-source-clean-test.sh

Audit the real source tree:

    bash scripts/audit/audit-source-clean.sh

Build both binaries from an audited copy without Git metadata:

    make clean-copy-build-test

Verify deterministic SBOM, license, provenance, and source digest generation:

    make supply-chain-test

Release construction must repeat the audit and then select files from the
current release inputs rather than copying the worktree wholesale. Formal
release-manifest generation requires verified supply-chain artifacts and a
provenance statement whose source revision is not marked dirty.
