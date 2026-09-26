# Synon Biomed module index

The module pages below are product-facing orientation pages. Exact placement,
growth ceilings, and dependency rules remain owned by
[`docs/governance/module-topology.json`](../governance/module-topology.json).

| Module | Product responsibility | Source |
| --- | --- | --- |
| [Workbench](workbench.md) | Research questions, evidence, structures, runs, and settings | `frontend/packages/desktop/` |
| [Runtime](runtime.md) | Agent turns, task lifecycle, tools, and recovery | `internal/agentruntime/`, `internal/sessionrunner/`, `internal/toolgateway/` |
| [Science](science.md) | Kernels, managed environments, compute providers, and readiness | `internal/kernel/`, `internal/compute/`, `internal/sciencecapability/` |
| [Evidence](evidence.md) | Artifacts, citations, lineage, persistence, and delivery | `internal/artifacts/`, `internal/sourcecitation/`, `internal/persistence/`, `internal/outbox/` |
| [Integrations](integrations.md) | Skills, MCP connectors, providers, and network policy | `skills/synonbiomed/`, `internal/mcpdirectory/`, `internal/providers/` |

Each module page states its purpose, boundary, entry points, lifecycle states,
and verification starting points. It does not duplicate the implementation
contract or promise optional runtime availability.
