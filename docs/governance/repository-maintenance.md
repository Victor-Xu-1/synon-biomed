# Repository maintenance

This document is the operational checklist for the public repository. It is
kept separate from product behavior and from external task evidence.

## Branches and pull requests

- `main` is the protected integration branch.
- Development happens on a topic branch and enters `main` through a pull
  request.
- Required checks include `Merge Gate / CI required`, `CodeQL`, and the
  language analysis jobs for Actions, Go, JavaScript/TypeScript and Python.
  Conversations must be resolved before merge.
- This is a single-maintainer repository. A separate code-review pass and its
  findings belong on the PR before merge, including maintainer-authored work.
  Do not manufacture approval using another identity. A second GitHub approval
  is required only after an independent maintainer is available; authors cannot
  approve their own PRs.
  CODEOWNERS is introduced only when it can represent real independent
  ownership and review, not as a substitute for that review.
- A pull request must describe its scope, compatibility impact, tests, and
  rollback or recovery path.
- Delete obsolete topic branches only after confirming their changes are
  merged or intentionally superseded and a recovery reference is retained.
  A closed PR alone does not establish that unmerged work is disposable.

## Version and release line

`product-identity.json` is the product-version authority. The active community
line is `v0.1.x`; changing the minor line requires a separately reviewed policy
change. Releases use an annotated `vMAJOR.MINOR.PATCH` tag, exact source and
artifact checksums, a release candidate manifest, and the repository's release
authorization contract. No release is complete merely because a build passed.

## Automation and permissions

Release automation must use a GitHub App installed only on this repository.
Store the App's Client ID in repository variable `RELEASE_APP_ID` and its
private key in the `release-automation` environment secret
`RELEASE_APP_PRIVATE_KEY`. The environment is restricted to `main`. Personal tokens,
private keys, and credentials must never appear in source, issues, pull
requests, logs, or release assets.

Workflow files are pinned to reviewed immutable action commits and checked by
the repository policy gate. Publishing jobs receive package-write permission
only when they are processing an already verified immutable release.

## Dependency and provenance maintenance

Every third-party or reused component needs a source, version, checksum,
license, notice, runtime purpose, and update procedure in [the inventory](../THIRD_PARTY.md).
The inventory's unresolved-provenance section is authoritative: it is a
disclosure of missing redistribution evidence, not an ownership or commercial
relicensing grant. Do not remove that warning or convert it into a positive
license claim without primary-source authorization.

## Security and community operations

- Security reports follow [SECURITY.md](../../SECURITY.md).
- Contributions follow [CONTRIBUTING.md](../../CONTRIBUTING.md).
- Community behavior follows [CODE_OF_CONDUCT.md](../../CODE_OF_CONDUCT.md).
- Source and secret audits run before release promotion.
- Code scanning, dependency alerts, and secret scanning are reviewed when
  their findings change; a missing scan baseline is not treated as a clean
  result.

## Release readiness

Before announcing a public release, verify all of the following on the exact
candidate revision:

1. required PR checks and review are complete;
2. the full release-quality workflow is successful;
3. the candidate manifest, source revision, checksums, notices, and SBOM agree;
4. the immutable Release is published from the authorized tag; and
5. package visibility and an anonymous digest pull are verified.

If any item is unavailable, report the release as blocked instead of implying
that the repository is fully governed.
