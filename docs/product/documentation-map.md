# Documentation system

Synon Biomed documentation is organized by the decision a reader needs to
make. New pages should extend an existing category instead of adding another
top-level narrative.

| Category | Audience | Canonical location |
| --- | --- | --- |
| Product | Researchers, evaluators, product contributors | `docs/product/` |
| Engineering | Maintainers changing runtime or UI contracts | `docs/engineering/` |
| Governance | Controllers and release owners | `docs/governance/` |
| Operations | Installers and operators | `docs/operations-runbook.md` |
| Compatibility | Contract and scenario authors | `docs/compatibility/` |
| Legal and provenance | Maintainers shipping third-party or generated assets | `docs/licenses/`, `docs/THIRD_PARTY.md` |

## Page contract

Every durable page should state its audience, scope, source authority, and
verification status. Product pages may explain intent and user value, but they
must link to the engineering or governance authority for exact behavior.

Generated evidence, screenshots from one-off runs, and machine-local output
belong outside the source tree unless a reproducible, stable asset is part of
the product or documentation experience.
