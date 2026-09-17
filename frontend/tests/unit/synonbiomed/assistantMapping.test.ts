/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';
import {
  buildSynonBiomedAssistants,
  buildSynonBiomedSkills,
  mergeSynonBiomedAssistants,
  mergeSynonBiomedSkills,
  type SynonBiomedCatalog,
} from '@/renderer/services/synonBiomedCatalog';
import type { Assistant } from '@/common/types/agent/assistantTypes';

const baseCatalog: SynonBiomedCatalog = {
  product: 'Synon Biomed',
  runtime: {
    runtimeAssetsDir: '/home/victor_1/synonbiomed-workbench/synonbiomed/runtime/assets',
    agents: {
      count: 2,
      names: ['aidd-expert', 'genomics-bioinfo-expert'],
    },
    skills: { count: 2, names: ['alphafold2', 'literature-review'] },
    mcpServers: { count: 1, names: ['bio-tools'] },
    thirdPartyAssets: { count: 1, names: ['bionemo-agent-toolkit'] },
  },
  backend: {
    baseUrl: 'http://127.0.0.1:8892',
    health: {
      status: 'healthy',
      service: 'gateway',
      agentsRegistered: 2,
    },
    agents: {
      count: 2,
      names: ['AIDD_EXPERT', 'GENOMICS_BIOINFO_EXPERT'],
    },
  },
};

describe('Synon Biomed assistant mapping', () => {
  it('converts backend expert agents into SynonAI builtin assistants', () => {
    const assistants = buildSynonBiomedAssistants(baseCatalog);

    expect(assistants.map((assistant) => assistant.id)).toEqual([
      'synonbiomed:aidd-expert',
      'synonbiomed:genomics-bioinfo-expert',
    ]);
    expect(assistants[0]).toMatchObject({
      source: 'builtin',
      name: 'AIDD Expert',
      agent_id: 'AIDD_EXPERT',
      agent_status: 'online',
      deletable: false,
      enabled: true,
      agent: {
        type: 'synonbiomed',
        source: 'builtin',
        acp_backend: 'synonbiomed',
      },
    });
    expect(assistants[0].description_i18n['zh-CN']).toContain('Synon Biomed');
    expect(assistants[1].name_i18n['zh-CN']).toBe('Genomics Bioinfo Expert');
  });

  it('marks expert assistants offline when the Synon Biomed backend is unhealthy', () => {
    const assistants = buildSynonBiomedAssistants({
      ...baseCatalog,
      backend: {
        ...baseCatalog.backend,
        health: {
          ...baseCatalog.backend.health,
          status: 'unavailable',
        },
      },
    });

    expect(assistants).toHaveLength(2);
    expect(assistants.every((assistant) => assistant.agent_status === 'offline')).toBe(true);
    expect(assistants[0].agent_status_message).toContain('unavailable');
  });

  it('preserves existing Synon Biomed assistant state without keeping non-Synon agents', () => {
    const existing: Assistant[] = [
      {
        ...buildSynonBiomedAssistants(baseCatalog)[0],
        name: 'Existing AIDD Expert',
      },
      {
        ...buildSynonBiomedAssistants(baseCatalog)[1],
        id: 'writer',
        name: 'Writer',
        source: 'user',
      },
    ];

    const merged = mergeSynonBiomedAssistants(existing, buildSynonBiomedAssistants(baseCatalog));

    expect(merged.map((assistant) => assistant.id)).toEqual([
      'synonbiomed:aidd-expert',
      'synonbiomed:genomics-bioinfo-expert',
    ]);
    expect(merged[0].name).toBe('Existing AIDD Expert');
  });
  it('makes Synon Biomed experts the only native assistant catalog and removes every other agent source', () => {
    const nonSynonBiomedAssistants: Assistant[] = [
      {
        id: 'unsupported:runtime',
        name: 'Unsupported runtime',
        source: 'generated',
        sort_order: -3,
        enabled: true,
        agent: { type: 'unsupportedRuntime', source: 'internal' },
      },
      {
        id: 'bare:claude-code',
        name: 'Claude Code',
        source: 'generated',
        sort_order: -2,
        enabled: true,
        agent: { type: 'acp', source: 'builtin', acp_backend: 'claude' },
      },
      {
        id: 'bare:codex-cli',
        name: 'Codex CLI',
        source: 'generated',
        sort_order: -1,
        enabled: true,
        agent: { type: 'acp', source: 'builtin', acp_backend: 'codex' },
      },
      {
        id: 'my-lab-note-assistant',
        name: 'My Lab Note Assistant',
        source: 'user',
        sort_order: 100,
        enabled: true,
      },
    ];

    const merged = mergeSynonBiomedAssistants(nonSynonBiomedAssistants, buildSynonBiomedAssistants(baseCatalog));

    expect(merged.map((assistant) => assistant.id)).toEqual([
      'synonbiomed:aidd-expert',
      'synonbiomed:genomics-bioinfo-expert',
    ]);
  });

  it('returns an empty catalog when Synon Biomed experts are unavailable', () => {
    const baseAssistants: Assistant[] = [
      { id: 'bare:codex-cli', name: 'Codex CLI', source: 'generated', sort_order: -1, enabled: true },
      { id: 'my-lab-note-assistant', name: 'My Lab Note Assistant', source: 'user', sort_order: 100, enabled: true },
    ];

    expect(mergeSynonBiomedAssistants(baseAssistants, [])).toEqual([]);
  });

  it('uses Synon Biomed runtime skills as the only native skills catalog', () => {
    const baseSkills = [
      { name: 'cron', source: 'builtin', is_custom: false },
      { name: 'xiaohongshu-post', source: 'builtin', is_custom: false },
      { name: 'lab-note-template', source: 'custom', is_custom: true },
    ];
    const synonBiomedSkills = buildSynonBiomedSkills(baseCatalog);

    expect(mergeSynonBiomedSkills(baseSkills, synonBiomedSkills).map((skill) => skill.name)).toEqual([
      'alphafold2',
      'literature-review',
    ]);
    expect(mergeSynonBiomedSkills(baseSkills, [])).toEqual([]);
  });
});
