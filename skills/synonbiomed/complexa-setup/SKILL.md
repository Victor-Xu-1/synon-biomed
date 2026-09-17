---
name: complexa-setup
description: Prepare a verified Proteina-Complexa execution route with the installed Complexa CLI or a governed equivalent. Use for runtime selection, environment initialization, checkpoint acquisition, readiness inspection, and pipeline validation before protein-design generation or evaluation.
tags: [protein-design, complexa, capability-readiness]
keywords: [Complexa setup, Complexa install, Complexa checkpoints, Complexa preflight, complexa init, complexa download, complexa validate, 蛋白设计环境, Complexa环境, 模型权重, 运行预检]
allowed-tools: search_skills, skill, ask_user, repl, list_compute, manage_environments, manage_packages, bash, read_file, edit_file, save_artifacts
references:
  - reference/env_keys.md
  - reference/downloads.md
critical-constraints:
  - Use only the checked-out Complexa CLI, files that exist in the selected immutable source, or the governed capability-acquisition workflow; never call a runtime/assets helper or preflight file that is absent from this Skill directory.
  - Missing preconfiguration is not proof of infeasibility. Inspect the live machine, connected compute or service route, credentials, license, immutable source, weights, and one task-class validation before rejecting the standard workflow.
  - Do not install OS packages, clone repositories, download weights, or mutate an environment through ad hoc Bash; use capability-acquisition and the admitted environment or package tools with the required approval.
  - Never print, copy into artifacts, or expose credential values. Preserve only boolean presence, provider identity, and the approved data boundary.
  - User-visible questions, progress, and setup summaries follow the conversation language while exact CLI names, paths required by the scientific program, versions, units, and identifiers remain unchanged.
---

<!-- Modified for Synon Biomed. Upstream attribution and terms: docs/licenses/bionemo-agent-toolkit/NOTICE.md. -->

# Complexa Setup

This Skill owns readiness for a Proteina-Complexa route. The installed
complexa CLI and the files in its immutable source checkout are the execution
authority. There is no bundled shared preflight or manifest helper in this
Skill; do not invent one.

## 1. Start from the selected scientific route

Return to protein-design-strategy when the requested design class is not yet
known. Setup depends on whether the selected route is:

- a protein-surface binder;
- a ligand-binding protein;
- motif-plus-ligand scaffolding;
- evaluation or refolding only.

Record the selected pipeline config, target artifact, expected stages, required
backends, output contract, and whether proprietary inputs may leave the host.
Do not download every model family merely because it exists.

## 2. Read-only readiness

Inspect connected compute and the local machine before mutation. Use
list_compute for admitted providers and a bounded local probe for exact host
facts. Do not print environment values.

~~~bash
command -v complexa
complexa --help
complexa download --status
complexa validate env
nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader
df -Pk .
~~~

Treat each result precisely:

- a missing CLI means the local route is not ready, not that Complexa is
  scientifically impossible;
- complexa validate env is a shallow environment check, not generation proof;
- complexa download --status identifies installed model families but does not
  prove the selected config can load them;
- GPU presence alone does not prove VRAM, driver, weights, source version, or
  task-size fit.

If the CLI/source is absent or incompatible, load capability-acquisition.
Verify the official source, license, immutable commit or release, documented
installation, weights, expected disk/VRAM, and one representative validation.
Use managed environment/package operations for mutation. Do not run ad hoc
package, OS, repository, remote-installer, or model-acquisition commands from
Bash; every such mutation stays behind the governed capability path.

## 3. Choose local, remote, or service execution

Prefer a verified local route when the selected pipeline fits the measured
machine and data should remain local. A connected MCP/API or remote-compute
route is equivalent only if its live schema accepts the same target,
conditioning, and model stage and returns editable structures, sequences,
metrics, and provenance.

If two to four viable routes remain and data boundary, cost, latency, setup,
capacity, or deliverables materially differ, call ask_user once after
preflight. Recommend one route and state each option's advantage, limitation,
resource or service requirement, and expected outputs in the conversation
language. If only one route is valid, continue without asking. Never ask the
user to diagnose a failed installation.

## 4. Initialize the checked source

When the checked source already contains a functioning environment, reuse it.
For an uninitialized approved source, use its own CLI:

~~~bash
complexa init
complexa init --runtime docker
~~~

Select UV or Docker from actual OS/runtime compatibility and measured
readiness. Do not ask a blind binary question. complexa init --force can
replace existing configuration and therefore requires an explicit request plus
a recoverable copy; never use it as routine repair.

Read reference/env_keys.md only for keys used by the selected route. Edit
machine-specific non-secret paths through edit_file. Keep credentials in the
governed secret boundary and verify only their presence. After initialization:

~~~bash
complexa validate env
~~~

## 5. Acquire only the selected model set

Use reference/downloads.md and complexa download --help from the checked
version to map the selected pipeline to exact flags. Typical maintained CLI
families include:

~~~bash
complexa download --complexa --all
complexa download --complexa-ligand --all
complexa download --complexa-ame --all
complexa download --status
~~~

These examples are version-sensitive. Confirm them against --help before
execution. Acquisition is a governed external mutation: preserve approval,
source, version, destination, expected size, transfer receipt, and final status.
Partial or merely present files are not ready weights.

If multiple model sets are scientifically viable and their download size,
compute needs, cost, or output scope differ materially, present the informed
choice through ask_user. Otherwise acquire the smallest set that completes the
selected route.

## 6. Validate the actual pipeline

Use the exact config and target that the parent workflow will execute:

~~~bash
complexa target show <target>
complexa validate target <pipeline-config> --target <target>
complexa validate design <pipeline-config>
~~~

Validation must prove:

- the target exists in the selected config's target dictionary;
- target chains, residues, ligand or motif mapping resolve to real input data;
- selected checkpoints and reward/refolding backends exist;
- the checked config is compatible with the immutable code and weights;
- the machine or remote route has capacity for a bounded task-class pilot.

complexa validate design reads the config as written. Validate any planned
overrides separately and do not claim they were checked by the base config.
Before a large campaign, run one bounded representative design through the
parent complexa-design workflow and verify parseable structures, sequences,
metrics, stable IDs, and receipts. A clone, environment build, checkpoint
listing, --help, or zero-exit validation alone is not a design result.

## 7. Evidence and failure boundary

Keep setup evidence internal by default: immutable source and weight identity,
selected runtime/provider, boolean credential readiness, measured resources,
exact commands, environment generation, validation receipts, and unresolved
gaps. Publish only the user-relevant setup decision or blocker unless the user
asks for an audit artifact.

On failure, preserve the exact receipt and change only the evidence-proven
boundary. One materially corrected attempt is allowed. A repeated equivalent
failure closes that local route; evaluate an equivalent MCP/API or remote route
of the same scientific class. A weaker scientific method is a separate
user-owned choice, not an automatic fallback.

## Resource framing

The full maintained Complexa campaign is generally a high-memory CUDA
workflow; the checked bundle documents 40 GB-class GPU requirements for its
full staged local route, while sequence-only or evaluation variants differ.
Report the selected version's official requirements and the measured host or
provider facts. Do not promise a runtime, throughput, or success rate from a
generic GPU label.

Use reference/env_keys.md for current configuration semantics and
reference/downloads.md for the checked source's model-family layout. Verify
both against the selected source because flags and weight names are
version-sensitive.
