# Synon Biomed

## A traceable workspace for biomedical discovery

Synon Biomed connects research questions, evidence, molecular context,
scientific tools, and recoverable execution in one workspace. It is designed
for teams that need to move quickly without losing the trail from question to
result.

Synon Biomed 将研究问题、证据、分子上下文、科研工具与可恢复执行汇聚到同一个工作台，
帮助团队在加速研发的同时保留从问题到结果的完整脉络。

![Synon Biomed product concept](docs/assets/synon-biomed-hero-v2.png)

> **Concept illustration / 概念图** — AI-generated artwork, not a screenshot
> of the running product or a scientific result.

## Start with the product guide

| You want to… | Read |
| --- | --- |
| Understand the product and its principles | [Product guide](docs/product/README.md) |
| Explore current capabilities | [Capability map](docs/product/capability-map.md) |
| Understand the architecture | [Product architecture](docs/product/architecture.md) |
| Find a module or owner | [Module guide](docs/product/module-guide.md) |
| Understand the visual direction and screenshot rules | [Visual guide](docs/product/interface-preview.md) |
| Install, run, recover, or upgrade | [Operations runbook](docs/operations-runbook.md) |
| Change code safely | [Contributing](CONTRIBUTING.md) and [module topology](docs/engineering/module-topology.md) |

## What the workspace helps you do

- **Frame a research brief** — turn an idea into a bounded, reviewable question.
- **Connect evidence** — keep publications, structures, files, and prior runs
  linked to the work that uses them.
- **Activate capabilities explicitly** — discover Skills, connectors, models,
  and scientific runtimes through declared contracts and permissions.
- **Run and recover tasks** — see progress, interruption, approval, failure, and
  resumption instead of treating every operation as a synchronous black box.
- **Review the result** — distinguish claims, artifacts, references, and
  execution evidence before sharing a conclusion.

## Architecture in one view

```text
Workbench -> typed API/IPC -> server composition -> session runner
          -> tool gateway -> kernels, providers, Skills, connectors, persistence
```

The product keeps one execution authority. Compatibility adapters delegate to
the same contracts instead of maintaining a competing business path.

## Run from source

Use Ubuntu or WSL with Git, Go `>=1.26`, Node.js `>=22.22 <25`, and npm.

```bash
git clone https://github.com/Victor-Xu-1/synon-biomed.git
cd synon-biomed
bash scripts/dev/install-source-cli.sh
synon start
```

Open the reported `READY_URL`, normally
`http://127.0.0.1:8765/#/login`. There is no default Web password; the first
run may open onboarding.

For runtime storage, environment policy, upgrades, rollback, and Windows/macOS
packaging, follow the [operations runbook](docs/operations-runbook.md) rather
than copying machine-local paths from an issue or screenshot.

## Engineering guarantees

- Product identity has one authority: [`product-identity.json`](product-identity.json).
- Frontend dependencies use npm and [`frontend/package-lock.json`](frontend/package-lock.json).
- Module placement is checked by [`docs/governance/module-topology.json`](docs/governance/module-topology.json).
- Long-running work exposes durable state, bounded execution, and recovery paths.
- Optional connectors and runtimes remain explicit and isolated from the core
  workspace.

The words **implemented**, **installed**, **ready**, and **verified** are not
interchangeable. The [capability map](docs/product/capability-map.md) defines
the distinction so documentation does not overstate machine-local readiness.

## Releases, licensing, and provenance

Versioned installers and SHA-256 checksums are published through
[GitHub Releases](https://github.com/Victor-Xu-1/synon-biomed/releases) when a
release exists. The OCI package at [GitHub Packages](https://github.com/Victor-Xu-1/synon-biomed/pkgs/container/synon-biomed)
is for automated ORAS retrieval, not `docker run`.

First-party code is licensed under **AGPL-3.0-only**, except where a separate
component term applies. See [LICENSE](LICENSE),
[commercial licensing boundaries](COMMERCIAL-LICENSE.md), and the
[third-party inventory](docs/THIRD_PARTY.md).
