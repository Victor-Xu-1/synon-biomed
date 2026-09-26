# Workbench module

## Purpose

Give researchers one place to frame a question, inspect evidence and
structures, follow a run, and review project artifacts.

## Boundary

The frontend owns rendering, interaction state, loading/empty/error states, and
accessibility. It does not authorize a request, decide runtime readiness, or
write persistence directly.

## Entry points

- Routes: `/login`, `/onboarding`, `/guid`, `/conversation/:id`, `/project/:id`.
- Source: `frontend/packages/desktop/src/renderer/`.
- Typed transport: `frontend/packages/desktop/src/common/adapter/`.

## Verification

Start with `npm run typecheck`, `npm run lint`, `npm run test:unit`, and the
applicable Playwright suite. A concept image in the product guide is not a
browser acceptance result.
