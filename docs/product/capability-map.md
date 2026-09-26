# Capability map

This map translates repository modules into product capabilities. A capability
is only considered available when its owning contract, runtime readiness, and
user-facing state agree; source presence alone is not proof of readiness.

| Product capability | What a researcher experiences | Primary authority | Evidence / contract |
| --- | --- | --- | --- |
| Research workspace | Projects, conversations, files, notes, and task history stay together. | `internal/server/`, `internal/persistence/` | [Web API contract](../governance/web-api-contract.json) |
| Evidence and provenance | Sources, artifacts, citations, and execution lineage remain inspectable. | `internal/artifacts/`, `internal/sourcecitation/`, `internal/sessionrunner/` | [Completion evidence](../engineering/synon-harness-conformance.md) |
| Skills and connectors | Biomedical Skills and MCP connectors can be discovered, granted, and used explicitly. | `skills/synonbiomed/`, `internal/skills/`, `internal/mcpdirectory/` | [Skill catalog](../engineering/synon-harness-conformance.md) |
| Agent engine | Provider turns, tool batches, protocol repair, and result materialization use typed boundaries. | `internal/agentruntime/` | [Harness architecture](../governance/harness-architecture.json) |
| Tool execution | Model, HTTP, approved-resume, and internal calls share ordered gateway stages. | `internal/toolgateway/`, `internal/tools/` | [Module topology](../engineering/module-topology.md) |
| Scientific execution | Persistent and detached kernels run through granted environments and network policy. | `internal/kernel/`, `internal/compute/` | [Kernel contract](../engineering/kernel-execution-contract.md) |
| Runtime readiness | Required Python/R environments and optional scientific runtimes report explicit states. | `internal/runtimecontrol/`, `internal/sciencecapability/` | [Managed runtime](../engineering/managed-execution-runtime.md) |
| Durable task lifecycle | Long tasks expose progress, interruption, recovery, and settlement state. | `internal/sessionrunner/`, `internal/server/` | [Verification scope](../engineering/verification-scope.md) |
| Frontend workbench | A typed Web client presents conversation, evidence, structures, runs, and settings. | `frontend/packages/desktop/` | [Frontend module guide](../../frontend/README.md) |

## Readiness language

- **Implemented** means the source and tests contain the capability contract.
- **Installed** means the required package or runtime exists on the current
  machine.
- **Ready** means the product's live health/readiness state confirms that the
  capability can execute under its current policy.
- **Verified** means the applicable API, integration, or browser check ran on
  the exact source revision being discussed.

Do not collapse these terms into a single marketing status. The distinction is
especially important for native Python/R environments and optional compute.
