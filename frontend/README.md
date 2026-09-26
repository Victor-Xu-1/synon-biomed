# Web workbench

The `frontend/` workspace is the user-facing Synon Biomed workbench. It uses
npm and the committed `package-lock.json` as its single dependency authority.

## Responsibilities

- `packages/desktop/src/common/` — typed IPC and API client facades.
- `packages/desktop/src/renderer/` — pages, controllers, and presentation.
- `tests/` — node, DOM, integration, packaged, and browser coverage.

The renderer owns interaction state and accessibility behavior. Authorization,
capability readiness, persistence, and execution policy remain server-owned.

## Commands

```bash
npm ci
npm run typecheck
npm run lint
npm run format:check
npm run test:unit
npm run build
npm run test:packaged
npm run test:e2e:web
```

For the full source startup path, use the root README and
[`docs/operations-runbook.md`](../docs/operations-runbook.md). Do not introduce
another package manager or lockfile without a reviewed governance change.
