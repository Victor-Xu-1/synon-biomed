/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';

import { buildAssistantEditorBackends } from '@/renderer/pages/settings/SynonBiomedExpertsSettings/assistantUtils';
import type { ManagedAgent } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';

describe('buildAssistantEditorBackends', () => {
  it('derives editor backends only from Synon Biomed management agents', () => {
    const agents: ManagedAgent[] = [
      managedAgent({
        id: 'AIDD_EXPERT',
        name: 'AIDD Expert',
        status: 'online',
      }),
      managedAgent({
        id: 'GENOMICS_EXPERT',
        name: 'Genomics Expert',
        status: 'unchecked',
      }),
      managedAgent({
        id: 'OFFLINE_EXPERT',
        name: 'Offline Expert',
        status: 'offline',
      }),
    ];

    expect(buildAssistantEditorBackends(agents, 'en-US')).toEqual([
      {
        id: 'AIDD_EXPERT',
        name: 'AIDD Expert',
        runtimeKey: 'synonbiomed',
        modelOptions: [],
      },
      {
        id: 'GENOMICS_EXPERT',
        name: 'Genomics Expert',
        runtimeKey: 'synonbiomed',
        modelOptions: [],
      },
    ]);
  });

  it('uses localized Synon Biomed management names and falls back to agent_type when backend is empty', () => {
    const agents: ManagedAgent[] = [
      managedAgent({
        id: 'AIDD_EXPERT',
        backend: undefined,
        agent_type: 'synonbiomed',
        name: 'AIDD Expert',
        name_i18n: { 'zh-CN': 'AIDD 专家' },
        status: 'online',
      }),
    ];

    expect(buildAssistantEditorBackends(agents, 'zh-CN')).toEqual([
      {
        id: 'AIDD_EXPERT',
        name: 'AIDD 专家',
        runtimeKey: 'synonbiomed',
        modelOptions: [],
      },
    ]);
  });

  it('keeps the current Synon Biomed binding visible even when it is known unavailable', () => {
    const agents: ManagedAgent[] = [
      managedAgent({
        id: 'ONCOLOGY_EXPERT',
        name: 'Oncology Expert',
        status: 'offline',
      }),
    ];

    expect(buildAssistantEditorBackends(agents, 'en-US', 'ONCOLOGY_EXPERT')).toEqual([
      {
        id: 'ONCOLOGY_EXPERT',
        name: 'Oncology Expert',
        runtimeKey: 'synonbiomed',
        modelOptions: [],
      },
    ]);
  });
});

function managedAgent(overrides: Partial<ManagedAgent> & { id: string; name: string }): ManagedAgent {
  return {
    id: overrides.id,
    icon: undefined,
    name: overrides.name,
    name_i18n: overrides.name_i18n,
    description: undefined,
    description_i18n: undefined,
    backend: overrides.backend ?? 'synonbiomed',
    agent_type: 'synonbiomed',
    agent_source: overrides.agent_source ?? 'builtin',
    agent_source_info: {},
    enabled: overrides.enabled ?? true,
    installed: overrides.installed ?? true,
    command: overrides.command ?? overrides.backend ?? 'claude',
    args: [],
    env: [],
    behavior_policy: { supports_side_question: true },
    sort_order: overrides.sort_order ?? 0,
    status: overrides.status ?? 'online',
    ...overrides,
  } as ManagedAgent;
}
