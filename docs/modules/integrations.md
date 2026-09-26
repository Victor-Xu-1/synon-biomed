# Integrations module

## Purpose

Expose biomedical Skills, MCP connectors, model providers, and network policy
through explicit catalog, permission, transport, and provenance contracts.

## Source map

- `skills/synonbiomed/` — user-facing Skill bundles.
- `internal/mcpdirectory/` — connector metadata, lifecycle, and transport.
- `internal/providers/` — model/provider boundaries.
- `internal/networkpolicy/` and `internal/networktls/` — trust and egress rules.

## Boundary

Discovery does not grant execution. A connector or Skill must pass the relevant
catalog, permission, provider, and runtime readiness checks before a task can
use it.

## Verification

Start with the Harness conformance contract and the compatibility scenarios for
the connector or capability being changed. Keep third-party provenance in
`docs/THIRD_PARTY.md` and `docs/licenses/`.
