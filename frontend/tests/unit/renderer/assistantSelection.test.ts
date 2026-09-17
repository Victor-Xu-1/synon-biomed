/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';
import type { Assistant } from '@/common/types/agent/assistantTypes';
import { selectableAssistants } from '@/renderer/utils/model/assistantSelection';

const mk = (id: string, source: Assistant['source'], sort_order: number, enabled = true): Assistant =>
  ({
    id,
    source,
    name: id,
    name_i18n: {},
    description_i18n: {},
    enabled,
    sort_order,
    enabled_skills: [],
    custom_skill_names: [],
    disabled_builtin_skills: [],
    context_i18n: {},
    prompts: [],
    prompts_i18n: {},
    models: [],
    agent_id: id,
    agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
    agent_status: 'online',
    deletable: source === 'user',
  }) as Assistant;

describe('selectableAssistants', () => {
  it('orders Synon Biomed experts by group and then sort_order', () => {
    const result = selectableAssistants([
      mk('builtin-a', 'builtin', 5),
      mk('user-b', 'user', 20),
      mk('generated-a', 'generated', 30),
      mk('user-a', 'user', 10),
      mk('generated-b', 'generated', 40),
    ]);
    expect(result.map((a) => a.id)).toEqual(['generated-a', 'generated-b', 'user-a', 'user-b', 'builtin-a']);
  });

  it('drops disabled assistants', () => {
    const result = selectableAssistants([
      mk('expert-on', 'generated', 10, true),
      mk('expert-off', 'generated', 20, false),
      mk('user-off', 'user', 30, false),
    ]);
    expect(result.map((a) => a.id)).toEqual(['expert-on']);
  });

  it('keeps generated experts ahead of builtin experts even when builtin has a lower sort_order', () => {
    const result = selectableAssistants([mk('builtin', 'builtin', 1), mk('generated', 'generated', 999)]);
    expect(result[0].id).toBe('generated');
  });
});
