# Science module

## Purpose

Provide controlled local, managed, and remote scientific execution with clear
environment, network, approval, and evidence boundaries.

## Source map

- `internal/kernel/` — persistent/detached kernel execution and egress policy.
- `internal/compute/` — provider, job, session, and usage lifecycle.
- `internal/sciencecapability/` and `internal/runtimecontrol/` — capability
  catalog and required/optional runtime readiness.

## Readiness language

Cataloged capabilities can be discovered. Installed runtimes exist on disk.
Ready runtimes pass the live health contract. Verified execution has a real
protocol or integration result on the exact revision under review.

## Verification

Read [managed execution runtime](../engineering/managed-execution-runtime.md)
and the operations runbook before changing runtime preparation, recovery, or
provider selection.
