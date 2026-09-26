# Evidence module

## Purpose

Keep artifacts, citations, execution lineage, durable state, and post-commit
events connected so a result can be inspected rather than merely displayed.

## Source map

- `internal/artifacts/` — versions, content, validation, and lineage.
- `internal/sourcecitation/` — source references and citation records.
- `internal/persistence/` — repository and transaction authorities.
- `internal/outbox/` — durable delivery and realtime materialization.

## Contract

Business mutation and its typed event commit together where the outbox contract
applies. Delivery is at least once; receivers must deduplicate stable event IDs.

## Verification

Use the outbox README, persistence integration tests, artifact lifecycle tests,
and the release acceptance contract. A screenshot of a result is not evidence
of durable lineage.
