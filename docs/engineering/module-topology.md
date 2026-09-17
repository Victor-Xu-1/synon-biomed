# Synon Biomed module topology

`docs/governance/module-topology.json` is the machine-checked authority for
repository placement. Production files are organized by stable responsibility:

| Responsibility | Canonical source | Derived or boundary consumers |
|---|---|---|
| Product identity | `product-identity.json` | build info, frontend version, release metadata |
| Biomedical Skills | `skills/synonbiomed/` | `assets/synonbiomed/skills.manifest.json`, `internal/skills/`, Skill APIs/UI |
| Core runtime Skill | `internal/skills.BuiltinRuntimeCatalog` | merged into every runtime catalog; no second `SKILL.md` copy |
| Agent identity and capability defaults | `assets/synonbiomed/agents/` | content-addressed Agent manifest, `internal/agentruntime/`, Agent APIs/UI |
| Native model-tool and service-operation schemas | `internal/tools/registry/catalog.go`, `catalog_boundaries.go` | disjoint model and service catalogs; runtime registration and execution in `registry.go`; implementations under `internal/tools/` |
| Root Harness tool exposure | `internal/harnesscontract/` | runner snapshots and architecture inventory; dynamic MCP methods still use the MCP pool |
| Ordered tool execution | `internal/toolgateway/` | server adapters bind model, HTTP, approved-resume and internal calls to one immutable stage order |
| Session runner phases | `internal/sessionrunner/` | `runner_cycle.go` composes one bounded cycle; interruption policy, recovery continuation, frame-resume dispatch, verification generation/evidence/persistence and settlement live in responsibility-specific adapters |
| Network policy and trust | `internal/networkpolicy/`, `internal/networktls/` | server HTTP, MCP subprocesses, and kernel CONNECT relay |
| Kernel execution and granted egress | `internal/kernel/` | persistent/detached workers, managed-environment modules and inherited-FD policy relay; server supervision is split into host dispatch, delegation, child-run, collection and messaging adapters |
| Agent provider engine | `internal/agentruntime/` | typed contracts, provider loop, protocol repair, tool batches, result materialization and provider transport |
| Completion validation | `internal/artifacts/`, `internal/sessionrunner/` | server adapters separate deliverables, references, evidence, syntax and lineage before review/decision |
| MCP connector metadata | `internal/mcpdirectory/bundled_metadata.json` | directory orchestration, stdio transport, persistence and API/UI boundaries |
| MCP App runtime | `internal/server/mcp_app_broker.go` | ticketed browser viewer, `host.app`, result relay, and artifact pinning |
| Bundled MCP implementations | `assets/optional/mcp-servers/` | content-addressed manifests and runtime probes |
| Frontend transport | `frontend/packages/desktop/src/common/adapter/ipcBridge.ts` | stable facade over conversation, runtime, file, provider, database, preview and operations clients |
| Frontend conversation controllers | `frontend/packages/desktop/src/renderer/pages/conversation/` | composer draft/mobile controllers consume the typed file/artifact/Skill/MCP composition model under `components/chat/SendBox/`; owner-scoped reload persistence lives in `hooks/chat/sendBoxDraftPersistence.ts`; shared SendBox catalog/selection/history/overlay presenters, message projection/navigation and runtime operation controller/presenters; tests under `frontend/tests/` |
| Web composer authority | `internal/server/web_composer_capabilities.go`, `web_composer_runtime_context.go` | conversation-authorized Skill/MCP identities, exact artifact versions, bounded input-file materialization and one message submission contract |
| Frontend compute settings | `frontend/packages/desktop/src/renderer/pages/settings/components/compute/` | page composition delegates jobs, providers, provider dialogs and shared presentation primitives to independent modules |
| Compatibility HTTP boundary | `internal/server/*_compat_*` | project/frame routing, read cursor, lifecycle, messages and projection remain thin consumed adapters over canonical Transcript and workspace authorities |
| Persistence | `internal/persistence/` | API and runner services consume repositories rather than raw databases |
| Message adapters | `internal/adapters/{common,feishu,wechat}` | server routes and delivery workers |
| Audit tooling | `scripts/audit/` | source, secret, migration, runtime, and non-web boundary gates |
| Development and release tooling | responsibility-specific `scripts/` subdirectories | Makefile and CI invoke canonical scripts |

The repository carries explicit growth ceilings for the remaining server
aggregate, both registry responsibilities, flat frontend service directory,
and root `scripts/` directory. These ceilings are guardrails with modest
development headroom, not exact-size targets: ordinary cohesive changes may use
the available headroom without a mandatory extraction. A change that exceeds a
ceiling, mixes responsibilities, or creates a competing implementation still
requires a reviewed topology change or a cohesive extraction. Future feature
work should reduce oversized authorities when a natural module boundary exists;
raising a ceiling requires a reviewed topology change, not an incidental edit.

Protocol test scenarios under `docs/compatibility/scenarios/` and imported
source provenance recorded in manifests are not runtime authorities. Historical
development reports and captured responses are kept outside the source tree.
User data, installed dependencies, generated outputs, caches and credentials do
not belong in this source topology.

## Harness architecture contract

`docs/governance/harness-architecture.json` defines module ownership and ordered
execution boundaries. Runtime phase and tool-pipeline tests compare the actual
implementation with this product contract. Development plans, progress reports
and external comparison records are not runtime or source-distribution inputs.

The target dependency direction is:

`server composition -> session runner -> tool gateway -> kernel/tools`

Persistence, artifacts, Skills, MCP and model providers are injected through
typed interfaces at the owning boundary. A compatibility route may temporarily
delegate to a replacement authority, but it may not retain a second business
implementation. Compatibility adapters delegate to the same execution
authority instead of duplicating its business logic.

The documented product contracts are the Harness design baseline. Every
applicable task, tool, state, persistence, recovery, security, process and
UI-control boundary must have independently observable behavior. Synon
Biomed uses its own product requirements and explicit module interfaces.
Architecture claims must be supported by the actual source and executable
contracts; an unverified required behavior blocks completion.
