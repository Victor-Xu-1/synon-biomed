# Product architecture

Synon Biomed is organized around one dependency direction:

```text
Web workbench
    -> typed IPC / HTTP boundary
    -> server composition
    -> session runner
    -> tool gateway
    -> kernel, providers, Skills, connectors, and persistence
```

The arrows describe ownership, not a second runtime. Compatibility endpoints
delegate to the same canonical authorities and do not carry independent
business logic.

## Product surfaces

### 1. Workbench

The frontend composes conversation, evidence, structures, runs, project files,
and settings from typed clients. It owns presentation state and interaction
controllers; it does not decide authorization or runtime readiness.

### 2. Server composition

The Go server binds authenticated requests to workspace repositories, tool
catalogs, provider sessions, artifact services, and durable task state. It is
the boundary where user identity, policy, and public API contracts meet.

### 3. Execution fabric

The session runner coordinates bounded turns. The tool gateway applies one
ordered pipeline to model, HTTP, approved-resume, and internal calls. Kernel and
compute modules execute only after the relevant environment, network, and
approval contracts pass.

### 4. Evidence and persistence

Persistence owns durable records; artifact and source-citation modules attach
lineage; outbox/realtime modules publish stable events after the business
transaction commits. This keeps the visible result and the audit trail on the
same state machine.

## Contracts that must stay singular

- Product identity: [`product-identity.json`](../../product-identity.json)
- Module placement: [`module-topology.json`](../governance/module-topology.json)
- Harness execution order: [`harness-architecture.json`](../governance/harness-architecture.json)
- Web API surface: [`web-api-contract.json`](../governance/web-api-contract.json)
- Frontend dependency authority: `frontend/package-lock.json`

When a change crosses one of these contracts, update the owning authority and
its executable checks together. Do not add a parallel README, manifest, lock
file, compatibility implementation, or product-version source to work around a
contract.

## Failure and recovery model

The product treats provider failure, runtime unavailability, cancellation,
lease loss, and process restart as observable states. A task may be waiting,
running, interrupted, recovering, completed, or failed; the UI should render
the state and its recovery path rather than silently turning it into success.
