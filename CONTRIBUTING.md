# Contributing to Synon Biomed

Thank you for improving Synon Biomed. Contributions are accepted through
reviewable pull requests; changes must not be pushed directly to `main`.

## Before you start

- Read the [operations runbook](docs/operations-runbook.md),
  [versioning policy](docs/governance/versioning.md), and
  [third-party inventory](docs/THIRD_PARTY.md).
- Check existing issues and pull requests before opening new work.
- Keep unrelated changes out of the branch. Do not commit credentials, local
  databases, runtime caches, generated release archives, or test output.
- Preserve upstream notices and license files when changing reused components.

## Development workflow

1. Create a topic branch from the latest `main`.
2. Keep one coherent change per pull request and describe the affected layers.
3. Use Conventional Commit-style pull request titles (`fix:`, `feat:`,
   `docs:`, `test:`, or `chore:`). Product releases currently remain on the
   `v0.1.x` patch line; version policy changes require a separate reviewed PR.
   Keep ordinary commit titles neutral: do not include a product version,
   "community source" label, or release slogan unless the change is the
   dedicated version-proposal/release commit.
4. Add a behavior-level regression test for every new behavior or bug fix.
5. Run the smallest relevant checks locally before pushing. Typical commands:

   ```bash
   go test ./internal/buildinfo
   make frontend-install
   (cd frontend && npx --no-install playwright install chromium)
   make frontend-test
   make frontend-typecheck
   make audit-source-clean
   ```

   Select the Go packages affected by the change (the command above is an
   example) and run frontend checks when the frontend is affected. The complete
   release-quality suite is for release candidates and scheduled verification,
   not every small change. Report checks that cannot run.
   Browser-backed HTML isolation tests require the Chromium revision selected by
   the locked Playwright dependency. The explicit installation step above also
   runs in frontend CI; it does not depend on a developer's browser profile.
6. Open a pull request with the problem, design, tests, compatibility impact,
   and rollback considerations. Do not merge until required checks and review
   are complete.

## Pull request expectations

- Keep public behavior, API contracts, migrations, and release metadata
  backwards compatible unless the PR explicitly documents the change.
- Validate external input at boundaries and retain actionable error context.
- Do not add a second implementation path, silent fallback, or test-only branch.
- For third-party additions, record source, version, checksum, license, notice,
  and update procedure in the authoritative inventory.
- Update user-facing documentation when commands, configuration, or behavior
  changes.

## Scope and support

This repository contains research software. A passing test suite does not
replace domain review or the user's final acceptance of scientific results.
For security issues, follow [SECURITY.md](SECURITY.md) rather than opening a
public issue.
