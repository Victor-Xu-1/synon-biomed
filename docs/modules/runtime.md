# Runtime module

## Purpose

Turn an approved research request into a bounded, observable, recoverable task
that can use models and tools without creating a competing execution path.

## Boundary

The runtime coordinates provider turns, tool stages, interruption, recovery,
verification, and settlement. Scientific execution remains owned by kernel and
compute modules; durable records remain owned by persistence.

## Source map

- `internal/agentruntime/` — provider protocol and result materialization.
- `internal/sessionrunner/` — task lifecycle and recovery state machine.
- `internal/toolgateway/` and `internal/tools/` — one ordered tool pipeline.

## Lifecycle

`claim -> context -> provider -> tool -> verify -> complete`, with explicit
waiting, interrupted, recovering, and failed states where applicable.

## Verification

Use the focused package tests and the Harness conformance contract before
claiming a runtime behavior change. Do not validate the runtime with a mock
provider alone.
