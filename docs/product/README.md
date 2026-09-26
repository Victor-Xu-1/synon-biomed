# Synon Biomed product guide

Synon Biomed is a biomedical research workspace that keeps a question, its
evidence, scientific tools, execution state, and review trail in one place.
This guide is the product-facing entry point; implementation contracts remain
under [`docs/engineering/`](../engineering/) and release/operations rules stay
in [`docs/operations-runbook.md`](../operations-runbook.md).

![Synon Biomed workspace concept](../assets/synon-biomed-hero-v2.png)

> **Concept illustration / 概念图** — AI-generated artwork, not a screenshot
> of the running product or a scientific result.

## The product loop

1. **Frame the question** — capture the research brief and the intended scope.
2. **Connect evidence** — bring together literature, structures, files, and
   prior work without losing provenance.
3. **Choose a capability** — use a Skill, connector, model, or scientific
   runtime that is explicit about its authority and readiness.
4. **Run a bounded task** — execute through the shared tool and kernel
   boundaries, with progress, cancellation, and recovery visible.
5. **Review the result** — separate claims, artifacts, references, and evidence
   so another researcher can inspect what happened.

The loop is deliberately evidence-first: a polished answer is not a substitute
for a traceable source, an executable record, or a declared limitation.

## Explore by intent

| If you want to… | Start here |
| --- | --- |
| Understand the user-facing capabilities | [Capability map](capability-map.md) |
| See how product modules connect | [Architecture](architecture.md) |
| Understand visual direction and screenshot rules | [Visual guide](interface-preview.md) |
| Find the right source folder or owner | [Module guide](module-guide.md) |
| Install or run the product | [Operations runbook](../operations-runbook.md) |
| Verify a release or deployment | [Release acceptance contract](../release-acceptance-contract.md) |
| Contribute safely | [Contributing](../../CONTRIBUTING.md) |

## Product principles

- **Evidence before confidence.** Sources and limitations stay attached to the
  work that depends on them.
- **One execution fabric.** Model calls, tools, kernels, and compute providers
  cross one typed gateway instead of competing paths.
- **Recoverable by default.** Long-running tasks expose durable state and a
  path to resume, cancel, or inspect failure.
- **Human review stays in the loop.** The product makes provenance legible; it
  does not turn an unverified result into a scientific conclusion.
- **Optional means explicit.** Connectors and scientific environments are
  isolated and activated only through the documented capability contract.

## Scope and status

This is the source edition of Synon Biomed. The product is actively evolving;
the capability map describes the current architecture and links to executable
contracts rather than promising that every optional runtime is installed on
every machine.
