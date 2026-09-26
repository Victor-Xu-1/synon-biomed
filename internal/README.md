# Internal runtime modules

The `internal/` tree contains the Go implementation behind Synon Biomed. It is
organized by stable responsibility, not by feature chronology or task name.

The intended dependency direction is:

`server composition -> session runner -> tool gateway -> kernel/tools`

Persistence, artifacts, Skills, MCP, providers, and observability are injected
at their owning boundaries. Start with the [product architecture](../docs/product/architecture.md)
for the user-facing picture and the [machine-checked topology](../docs/engineering/module-topology.md)
for exact placement rules.

## Common entry points

- `internal/server/` — authenticated HTTP and product composition.
- `internal/sessionrunner/` — durable task lifecycle and recovery.
- `internal/toolgateway/` — one ordered execution pipeline.
- `internal/tools/` — tool catalog, registry, and implementations.
- `internal/agentruntime/` — provider protocol and result materialization.
- `internal/kernel/` — isolated kernel execution and environment policy.
- `internal/persistence/` — repositories and transactional state.
- `internal/outbox/` — post-commit delivery and realtime materialization.

Module READMEs should explain contracts and ownership; they should not become
parallel implementation specifications.
