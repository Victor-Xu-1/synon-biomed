# Contributing to Synon Biomed

Help improve the research workflow, correct documentation, report reproducible
bugs, or contribute code. Changes enter through reviewed pull requests, not
direct pushes to `main`. Please follow our [Code of conduct](CODE_OF_CONDUCT.md).

欢迎用中文或英文描述问题与改进建议。请附可复现步骤，并在分享日志、截图或数据前脱敏；
安全漏洞不要发到公开 Issue。

## Choose a starting point

| Contribution | What makes it useful |
| --- | --- |
| Bug report | Version or commit, OS, minimal steps, expected and actual behavior, sanitized error output |
| Feature proposal | Research workflow, current limitation, intended outcome, and constraints |
| Documentation | The confusing or incorrect passage, its source of truth, and a clearer replacement |
| Code or tests | A focused change, a regression test, and evidence from the affected user path |

Search [existing issues](https://github.com/Victor-Xu-1/synon-biomed/issues) and
[pull requests](https://github.com/Victor-Xu-1/synon-biomed/pulls) first. Discuss
large changes to architecture, dependencies, storage, public contracts, or
licensing before investing in an implementation. Report vulnerabilities through
[Security](SECURITY.md), not a public bug report.

## Set up a development checkout

1. Fork the repository if you do not have write access, and create a topic
   branch from the current `main`. Keep unrelated work on separate branches.
2. Follow the [source startup instructions](docs/operations-runbook.md#source-startup).
   The Ubuntu/WSL source workflow requires Git, Go `>=1.26`, Node.js
   `>=22.22 <25`, and npm. See [.env.example](.env.example) for configuration.
3. Use the [module topology](docs/engineering/module-topology.md) to locate
   the owning module before editing. Do not add a competing implementation or
   dependency authority.
4. Keep runtime state outside source. Do not share a running installation's
   state directory or ports with a development instance. See the runbook for
   startup, state, and recovery options.

For frontend work, install the locked dependencies from the repository root:

```bash
make frontend-install
```

This runs `npm ci --ignore-scripts` in `frontend/`. npm and
`frontend/package-lock.json` are the frontend dependency authority; do not add
another package-manager lockfile.

## Verify the change you made

Start with a test that reproduces the bug or specifies the new behavior, then
run the directly affected regression checks. The [Makefile](Makefile) and
[frontend scripts](frontend/package.json) define the commands below.

| Change | Local checks and review |
| --- | --- |
| Go code | Test the changed packages and their affected consumers; `go test ./internal/buildinfo` is an example for build-information changes, not an all-purpose test |
| Frontend | `make frontend-typecheck`, `make frontend-lint`, `npm --prefix frontend run format:check`, and `make frontend-test` |
| Packaged Web behavior | `make frontend-build`, then `make frontend-test-packaged` |
| Documentation | Check local links, commands, source claims, and the rendered Markdown |
| Any change | `git diff --check` and `make audit-source-clean` |

For a changed UI flow, also exercise the real browser path: loading, empty and
error states, the key interaction, and the final result. API and persistence
changes need real protocol or database checks. Model/tool changes need relevant
real integration evidence; mock results alone do not prove that a provider or
scientific tool works.

The [PR workflow](.github/workflows/quality-pr.yml) selects affected Go and
frontend checks. The [full quality workflow](.github/workflows/quality.yml)
serves broader scheduled and release verification. Do not substitute a small
local check for required CI, or run a complete release matrix for an unrelated
text edit. Record exactly what ran, what passed, and what could not run.

## Prepare a reviewable pull request

- Explain the problem, intended behavior, changed modules, and any API, data,
  compatibility, or recovery impact. Keep one coherent change per PR.
- Include exact test commands and results. Call out missing credentials,
  external services, or unverified platforms instead of implying success.
- Update user-facing instructions when behavior or configuration changes.
  A concept illustration must be labeled as such; only an actual capture of a
  running product may be presented as a runtime screenshot. Never fabricate
  task status or scientific results for a preview.
- Use a Conventional Commit-style title, such as `fix:`, `feat:`, `docs:`,
  `test:`, or `chore:`. Keep ordinary commit titles free of product versions or
  release slogans. Version changes follow the separate
  [versioning policy](docs/governance/versioning.md).
- Validate untrusted input, preserve actionable errors, and remove replaced
  paths after verifying their replacement. No silent fallback, empty shell,
  or test-only success branch.
- Do not commit secrets, user files, local databases, runtime caches, generated
  release archives, or test output. Use small, sanitized fixtures when a
  permanent regression needs data.

Required checks and review must complete before merging. A merged PR is not
automatically a packaged release, deployed service, or validated scientific
result.

## Licensing and research responsibility

Preserve upstream notices. Record third-party additions, their versions,
checksums, terms, and update procedure in the
[third-party inventory](docs/THIRD_PARTY.md).

First-party code is offered under [AGPL-3.0-only](LICENSE), except where separate
component terms apply. A contribution under AGPL alone does not grant permission
to relicense it commercially; no copyright assignment or contributor agreement
is implied. See [licensing and commercial options](COMMERCIAL-LICENSE.md).

Synon Biomed is research software. Passing engineering checks does not replace
scientific review or a user's assessment of a result.

---

[Project overview](README.md) · [Code of conduct](CODE_OF_CONDUCT.md) · [Security](SECURITY.md)
