# Module guide

This is the human navigation layer over the machine-checked topology in
[`docs/governance/module-topology.json`](../governance/module-topology.json).
Each entry points to the module's source of truth and keeps product language
separate from implementation detail.

| Area | Source entry | Responsibility | Start with |
| --- | --- | --- | --- |
| Product identity | `product-identity.json` | Product name, slug, and version authority | [Versioning](../governance/versioning.md) |
| Web workbench | `frontend/packages/desktop/` | UI, typed clients, renderer controllers, and browser tests | [Frontend README](../../frontend/README.md) |
| Server boundary | `internal/server/` | HTTP/API composition, auth, transcript and lifecycle adapters | [Module topology](../engineering/module-topology.md) |
| Agent engine | `internal/agentruntime/` | Provider protocol, tool batches, repair, and result materialization | [Harness conformance](../engineering/synon-harness-conformance.md) |
| Session runner | `internal/sessionrunner/` | Durable turn lifecycle, interruption, recovery, and settlement | [Architecture](architecture.md) |
| Tool gateway | `internal/toolgateway/` and `internal/tools/` | Ordered tool stages and catalog/registry boundaries | [Harness architecture](../governance/harness-architecture.json) |
| Kernel and compute | `internal/kernel/` and `internal/compute/` | Isolated scientific execution and provider jobs | [Managed runtime](../engineering/managed-execution-runtime.md) |
| Persistence and events | `internal/persistence/` and `internal/outbox/` | Transactions, durable state, realtime materialization | [Outbox README](../../internal/outbox/README.md) |
| Skills | `skills/synonbiomed/` | User-facing biomedical capability bundles | [Skills README](../../skills/README.md) |
| Scripts and release | `scripts/` | Development, audit, packaging, smoke, and release automation | [Scripts README](../../scripts/README.md) |

## Ownership rule

When a document disagrees with code or a machine-readable contract, fix the
owner's source and then update the navigation page. This guide is not a second
architecture authority.
