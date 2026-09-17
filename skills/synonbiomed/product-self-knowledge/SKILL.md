---
name: product-self-knowledge
description: "Use this skill only when the user asks about Synon Biomed product behavior, local deployment, third-party LLM configuration, project files, built-in capabilities, or runtime boundaries."
license: Apache-2.0
---

# Synon Biomed Product Knowledge

## Core Principles

1. Synon Biomed is a local-first biomedical agent workspace.
2. Model access is configured through third-party LLM providers in the local LLM settings.
3. Project data, generated artifacts, conversations, and runtime state should stay in the configured local data directory unless the user explicitly enables a network-backed feature.
4. Answers about project behavior should be grounded in the local runtime configuration and files, not in external product documentation.

## Response Workflow

1. Identify whether the question is about UI behavior, LLM configuration, compute resources, connectors, Skills, MCP, project files, or generated artifacts.
2. Inspect the local configuration or runtime files when exact behavior matters.
3. Explain whether the behavior is local-only, user-triggered network access, or provider/network dependent.
4. Avoid references to legacy hosted products, legacy accounts, external subscription pages, or removed documentation portals.

## Local Reference Points

- Source installation and startup: `README.md`, `scripts/dev/install-source-cli.sh`
- Configuration and deployed operations: `.env.example`, `docs/operations-runbook.md`
- Runtime and model-provider ownership: `docs/engineering/module-topology.md`
- Bundled agents, Skills and optional runtimes: `assets/synonbiomed/agents/`, `skills/synonbiomed/`, `assets/optional/`
- Built-in tools and execution: `internal/tools/`, `docs/engineering/managed-execution-runtime.md`
