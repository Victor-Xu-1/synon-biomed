import { describe, expect, it } from 'vitest';

import type { Assistant } from '@/common/types/agent/assistantTypes';
import {
  pickDefaultAssistantSelectionKey,
  resolveAssistantSelectionKey,
} from '@/renderer/pages/guid/hooks/useGuidAssistantSelection';

describe('guid assistant selection helpers', () => {
  const assistants: Assistant[] = [
    assistant({ id: 'builtin-writer', source: 'builtin', runtimeKey: 'claude', sort_order: 20 }),
    assistant({ id: 'bare-unsupportedRuntime', source: 'generated', runtimeKey: 'unsupportedRuntime', sort_order: 10 }),
    assistant({ id: 'user-research', source: 'user', runtimeKey: 'gemini', sort_order: 30 }),
    assistant({ id: 'synonbiomed:aidd-expert', source: 'builtin', runtimeKey: 'synonbiomed', sort_order: 40 }),
    assistant({
      id: 'synonbiomed:operon',
      source: 'builtin',
      runtimeKey: 'synonbiomed',
      agent_id: 'OPERON',
      sort_order: 50,
    }),
  ];

  it('prefers explicit Synon Biomed assistant keys when the assistant exists', () => {
    expect(resolveAssistantSelectionKey('custom:synonbiomed:aidd-expert', assistants)).toBe('synonbiomed:aidd-expert');
  });

  it('does not accept non-Synon Biomed assistant ids from stale selections', () => {
    expect(resolveAssistantSelectionKey('custom:user-research', assistants)).toBeUndefined();
    expect(resolveAssistantSelectionKey('bare-unsupportedRuntime', assistants)).toBeUndefined();
  });

  it('does not accept legacy backend keys as assistant selection ids', () => {
    expect(resolveAssistantSelectionKey('claude', assistants)).toBeUndefined();
    expect(resolveAssistantSelectionKey('unsupportedRuntime', assistants)).toBeUndefined();
  });

  it('defaults to the general OPERON expert used by the v1.1 new-task workflow', () => {
    expect(pickDefaultAssistantSelectionKey(assistants)).toBe('synonbiomed:operon');
  });

  it('returns null when no assistants are available', () => {
    expect(pickDefaultAssistantSelectionKey([])).toBeNull();
  });
});

function assistant(
  overrides: Partial<Assistant> & { id: string; source: Assistant['source']; runtimeKey: string }
): Assistant {
  const agentId = overrides.runtimeKey === 'synonbiomed' ? 'AIDD_EXPERT' : `agent-${overrides.runtimeKey}`;
  const isUnsupportedRuntime = overrides.runtimeKey === 'unsupportedRuntime';
  const isSynonBiomed = overrides.runtimeKey === 'synonbiomed';
  return {
    id: overrides.id,
    source: overrides.source,
    name: overrides.id,
    name_i18n: {},
    description_i18n: {},
    enabled: true,
    sort_order: overrides.sort_order ?? 0,
    agent_id: agentId,
    agent: isSynonBiomed
      ? { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' }
      : isUnsupportedRuntime
        ? { type: 'unsupportedRuntime', source: 'internal' }
        : { type: 'acp', source: 'builtin', acp_backend: overrides.runtimeKey },
    enabled_skills: [],
    custom_skill_names: [],
    disabled_builtin_skills: [],
    context_i18n: {},
    prompts: [],
    prompts_i18n: {},
    models: [],
    agent_status: 'online',
    deletable: overrides.source === 'user',
    ...overrides,
  };
}
