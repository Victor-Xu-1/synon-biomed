# Synon Biomed v0.1.1 Release Acceptance Contract

This document is the release-safe operator subset of the source repository's
authoritative Synon Biomed v0.1.1 Goal. It deliberately contains no workstation
paths, credentials, owner identifiers, raw model payloads, or external-service
secrets.

A package is not release-authorized merely because it builds, starts, passes a
historical compatibility ledger, or contains implemented feature pages. Release
authorization requires one exact source revision and package hash to pass all
applicable gates below:

The tracked `docs/governance/release-policy.json` is static policy only. Current
authorization is an external signed receipt bound to the source commit/tree,
full-quality workflow run, `RELEASE_CANDIDATE.json`, and every artifact digest.
Promotion must publish those verified bytes without rebuilding.
The source-tree SHA-256 is the digest of `git archive --format=tar` for the
clean exact commit. Git's tracked content and executable-bit authority make it
stable across NTFS/DrvFS and Linux permission projections; dirty or untracked
source and unresolved submodules are rejected.

1. product identity agrees across binary, health, CLI, Web metadata, manifest,
   archive name, and installer;
2. normal, concurrency/race, static-analysis, frontend type/lint/format, and
   production-build gates pass on the same source revision;
3. owner isolation, authentication, authorization, redaction, path/URL/archive
   validation, and dependency/license/SBOM checks pass;
4. schema upgrade, restart, backup, restore, rollback, and idempotent recovery
   pass against isolated real SQLite state;
5. Linux and Windows packages pass fresh install, startup, upgrade, restart,
   rollback, and uninstall without a source checkout or build-time runtime;
6. official Google Chrome desktop and narrow flows pass against the packaged Go
   service, including realtime recovery, AskUser, branch, artifact, preview,
   long-task, error, keyboard, and no-overflow behavior; and
7. the Synon Harness contract passes: pinned Python worker, sole
   `generate_plan`/`update_step_status` plan authority, explicit plan-mode
   completion gating, model-judged AskUser decisions, one managed
   environment/package flow, scoped REVIEWER and BOOKMARKER fixed jobs, closed
   failure kinds, and no retired scientific scorecard or validation retry loop;
8. authorized external model and credentialed connector checks pass, or the
   exact unavailable external prerequisites are recorded without a simulated
   success claim; and
9. the README and operator runbook installed in the package match the tested
   startup, safe-stop, health, backup, rollback, restart, observability, and
   uninstall lifecycle. Release-file rollback and user-state rollback remain
   separate authorities, and autostart is never enabled implicitly.

New `RELEASE_MANIFEST.json` files use schema version 4 and contain package
identity, runtime requirements and a complete SHA-256 file inventory, without
historical development scores. Schema version 3 packages remain verifiable for
installation and rollback; their historical `coverage` section is checked only
as part of the old format and never represents current release authorization.
The builder does not need historical engineering archives. Supply-chain,
source-provenance, license and exact-artifact release authorization remain
separate required checks.

Message-channel and Synon Link optimization are excluded from the current
Harness denominator. Their retained behavior must not regress.
