# Development and release scripts

Scripts are grouped by responsibility and invoked by the Makefile, CI, or the
documented source/release workflows. They are not a second application layer.

| Area | Location | Purpose |
| --- | --- | --- |
| Audit and quality | `scripts/audit/`, `scripts/quality/` | Source hygiene, topology, provenance, and policy gates |
| Development | `scripts/dev/` | Local install, startup, and developer helpers |
| Packaging | `scripts/packaging/` and release scripts | Reproducible archives and identity checks |
| Compatibility | `scripts/compat/` | Contract fixtures and compatibility probes |
| Smoke and acceptance | `scripts/smoke-*`, `scripts/*acceptance*` | Real protocol and live-path verification |

Prefer an existing canonical script over a one-off copy. A new script should
declare its inputs, output location, failure behavior, and whether it changes
the source tree. Runtime data, logs, caches, screenshots, and release output
belong outside tracked source or under ignored state directories.
