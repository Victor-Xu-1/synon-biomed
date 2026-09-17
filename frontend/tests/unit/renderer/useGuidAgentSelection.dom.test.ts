/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, renderHook, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Assistant } from '@/common/types/agent/assistantTypes';
import type { ManagedAgent } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';
import {
  buildAgentRuntimeModeState,
  buildAgentRuntimeModelInfo,
  buildAgentRuntimeSlashCommands,
  buildAssistantModelInfo,
  resolveInitialAssistantModel,
  useGuidAssistantSelection,
} from '@/renderer/pages/guid/hooks/useGuidAssistantSelection';

let mockAssistants: Assistant[] = [];
let mockManagedAgents: ManagedAgent[] = [];

const { configGetMock, configSetMock, loadSessionDefaultsMock } = vi.hoisted(() => ({
  configGetMock: vi.fn(),
  configSetMock: vi.fn(),
  loadSessionDefaultsMock: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedSessionDefaults', () => ({
  loadSynonBiomedSessionDefaults: loadSessionDefaultsMock,
}));

vi.mock('@/common/config/configService', () => ({
  configService: {
    get: configGetMock,
    set: configSetMock,
  },
}));

vi.mock('@/renderer/pages/guid/hooks/useSynonBiomedAssistantsLoader', () => ({
  useSynonBiomedAssistantsLoader: () => ({
    assistants: mockAssistants,
  }),
}));

vi.mock('@/renderer/hooks/synonBiomed/runtime/useManagedAgents', () => ({
  useManagedAgentRuntimeCatalog: () => mockManagedAgents,
}));

describe('useGuidAssistantSelection', () => {
  beforeEach(() => {
    loadSessionDefaultsMock.mockReset();
    loadSessionDefaultsMock.mockResolvedValue({});
    configGetMock.mockReturnValue(undefined);
    configSetMock.mockResolvedValue(undefined);
    mockManagedAgents = [];
    mockAssistants = [
      {
        id: 'synonbiomed:aidd-expert',
        source: 'builtin',
        name: 'AIDD Expert',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 1,
        agent_id: 'AIDD_EXPERT',
        agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
        enabled_skills: [],
        custom_skill_names: [],
        disabled_builtin_skills: [],
        context_i18n: {},
        prompts: [],
        prompts_i18n: {},
        models: ['synonbiomed-default', 'synonbiomed-deep-research'],
        agent_status: 'online',
        deletable: false,
      } satisfies Assistant,
    ];
  });

  it('derives availability and model info from assistant catalog data', async () => {
    const { result } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBe('synonbiomed:aidd-expert');
    });

    expect(result.current.selectedAssistantAvailable).toBe(true);
    expect(result.current.selectedAcpModel).toBe('synonbiomed-default');
    expect(result.current.currentAcpCachedModelInfo).toEqual({
      current_model_id: 'synonbiomed-default',
      current_model_label: 'synonbiomed-default',
      available_models: [
        { id: 'synonbiomed-default', label: 'synonbiomed-default' },
        { id: 'synonbiomed-deep-research', label: 'synonbiomed-deep-research' },
      ],
    });
  });

  it('restores the last selected guid assistant before falling back to the Synon Biomed default', async () => {
    mockAssistants = [
      assistantFixture({ id: 'synonbiomed:aidd-expert', runtimeKey: 'synonbiomed', source: 'generated', sortOrder: 1 }),
      assistantFixture({
        id: 'synonbiomed:oncology-expert',
        runtimeKey: 'synonbiomed',
        source: 'builtin',
        sortOrder: 2,
      }),
    ];
    configGetMock.mockImplementation((key: string) =>
      key === 'guid.lastAssistantId' ? 'synonbiomed:oncology-expert' : undefined
    );

    const { result } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBe('synonbiomed:oncology-expert');
    });
  });

  it('restores the last selected guid assistant when the guid page resets for a new chat', async () => {
    mockAssistants = [
      assistantFixture({ id: 'synonbiomed:aidd-expert', runtimeKey: 'synonbiomed', source: 'generated', sortOrder: 1 }),
      assistantFixture({
        id: 'synonbiomed:oncology-expert',
        runtimeKey: 'synonbiomed',
        source: 'builtin',
        sortOrder: 2,
      }),
    ];
    configGetMock.mockImplementation((key: string) =>
      key === 'guid.lastAssistantId' ? 'synonbiomed:oncology-expert' : undefined
    );

    const { result } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: true,
        locationKey: 'new-chat',
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBe('synonbiomed:oncology-expert');
    });
  });

  it('persists manual guid assistant selections for the next visit', async () => {
    mockAssistants = [
      assistantFixture({ id: 'synonbiomed:aidd-expert', runtimeKey: 'synonbiomed', source: 'generated', sortOrder: 1 }),
      assistantFixture({
        id: 'synonbiomed:oncology-expert',
        runtimeKey: 'synonbiomed',
        source: 'builtin',
        sortOrder: 2,
      }),
    ];

    const { result } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBe('synonbiomed:aidd-expert');
    });

    act(() => {
      result.current.setSelectedAssistantId('synonbiomed:oncology-expert');
    });

    expect(configSetMock).toHaveBeenCalledWith('guid.lastAssistantId', 'synonbiomed:oncology-expert');
  });

  it('falls back to the default assistant when the persisted guid assistant no longer exists', async () => {
    mockAssistants = [
      assistantFixture({ id: 'synonbiomed:aidd-expert', runtimeKey: 'synonbiomed', source: 'generated', sortOrder: 1 }),
      assistantFixture({
        id: 'synonbiomed:oncology-expert',
        runtimeKey: 'synonbiomed',
        source: 'builtin',
        sortOrder: 2,
      }),
    ];
    configGetMock.mockImplementation((key: string) =>
      key === 'guid.lastAssistantId' ? 'removed-assistant' : undefined
    );

    const { result } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBe('synonbiomed:aidd-expert');
    });
  });

  it('does not synthesize a backend slug when no assistants exist', async () => {
    vi.resetModules();
    vi.doMock('@/renderer/pages/guid/hooks/useSynonBiomedAssistantsLoader', () => ({
      useSynonBiomedAssistantsLoader: () => ({
        assistants: [],
      }),
    }));

    const { useGuidAssistantSelection: useSelectionWithoutAssistants } =
      await import('@/renderer/pages/guid/hooks/useGuidAssistantSelection');

    const { result } = renderHook(() =>
      useSelectionWithoutAssistants({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBeNull();
    });

    expect(result.current.defaultAssistantId).toBeNull();
    expect(result.current.selectedAssistantBackend).toBe('');
    expect(result.current.selectedAssistantAvailable).toBe(false);

    vi.doUnmock('@/renderer/pages/guid/hooks/useSynonBiomedAssistantsLoader');
    vi.resetModules();
  });

  it('uses the selected assistant agent_id to read runtime catalogs from managed agents', async () => {
    mockAssistants = [
      {
        id: 'custom-1781258588874-26ad',
        source: 'user',
        name: '文件规划助手 (Copy)',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 1,
        agent_id: '2d23ff1c',
        agent: {
          type: 'synonbiomed',
          source: 'builtin',
          acp_backend: 'synonbiomed',
        },
        enabled_skills: [],
        custom_skill_names: [],
        disabled_builtin_skills: [],
        context_i18n: {},
        prompts: [],
        prompts_i18n: {},
        models: [],
        agent_status: 'online',
        deletable: true,
      } satisfies Assistant,
    ];
    mockManagedAgents = [
      {
        id: '2d23ff1c',
        backend: 'synonbiomed',
        available_models: {
          current_model_id: 'synonbiomed-deep-research',
          current_model_label: 'Deep Research',
          available_models: [
            { id: 'synonbiomed-default', label: 'Synon Biomed Default' },
            { id: 'synonbiomed-deep-research', label: 'Deep Research' },
          ],
        },
        available_modes: {
          current_mode_id: 'bypassPermissions',
          available_modes: [
            { id: 'default', name: 'Default' },
            { id: 'bypassPermissions', name: 'Bypass Permissions' },
          ],
        },
      } as unknown as ManagedAgent,
    ];

    const { result } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBe('custom-1781258588874-26ad');
    });

    expect(result.current.selectedAcpModel).toBe('synonbiomed-deep-research');
    expect(result.current.currentAcpCachedModelInfo).toEqual({
      current_model_id: 'synonbiomed-deep-research',
      current_model_label: 'Deep Research',
      available_models: [
        { id: 'synonbiomed-default', label: 'Synon Biomed Default' },
        { id: 'synonbiomed-deep-research', label: 'Deep Research' },
      ],
    });
    expect(result.current.selectedMode).toBe('bypassPermissions');
    expect(result.current.currentAgentModeOptions).toEqual([
      { value: 'default', label: 'Default', description: undefined },
      { value: 'bypassPermissions', label: 'Bypass Permissions', description: undefined },
    ]);
  });

  it('keeps the full runtime model list when assistant models only contain a default', async () => {
    mockAssistants = [
      {
        id: 'assistant-with-default-model',
        source: 'user',
        name: 'Assistant With Default Model',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 1,
        agent_id: 'AIDD_EXPERT',
        agent: {
          type: 'synonbiomed',
          source: 'builtin',
          acp_backend: 'synonbiomed',
        },
        enabled_skills: [],
        custom_skill_names: [],
        disabled_builtin_skills: [],
        context_i18n: {},
        prompts: [],
        prompts_i18n: {},
        models: ['default'],
        agent_status: 'online',
        deletable: true,
      } satisfies Assistant,
    ];
    mockManagedAgents = [
      {
        id: 'AIDD_EXPERT',
        backend: 'synonbiomed',
        available_models: {
          current_model_id: 'default',
          current_model_label: 'Default',
          available_models: [
            { id: 'default', label: 'Default' },
            { id: 'synonbiomed-deep-research', label: 'Deep Research' },
            { id: 'synonbiomed-literature-review', label: 'Literature Review' },
          ],
        },
      } as unknown as ManagedAgent,
    ];

    const { result } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBe('assistant-with-default-model');
    });

    expect(result.current.currentAcpCachedModelInfo?.available_models.map((model) => model.id)).toEqual([
      'default',
      'synonbiomed-deep-research',
      'synonbiomed-literature-review',
    ]);
  });

  it('keeps a guid-page model selection in memory across same-assistant runtime catalog refreshes', async () => {
    mockAssistants = [
      {
        id: 'assistant-with-runtime-models',
        source: 'user',
        name: 'Runtime Model Assistant',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 1,
        agent_id: 'AIDD_EXPERT',
        agent: {
          type: 'synonbiomed',
          source: 'builtin',
          acp_backend: 'synonbiomed',
        },
        enabled_skills: [],
        custom_skill_names: [],
        disabled_builtin_skills: [],
        context_i18n: {},
        prompts: [],
        prompts_i18n: {},
        models: [],
        agent_status: 'online',
        deletable: true,
      } satisfies Assistant,
    ];
    const buildManagedAgent = () =>
      ({
        id: 'AIDD_EXPERT',
        backend: 'synonbiomed',
        available_models: {
          current_model_id: 'default',
          current_model_label: 'Default',
          available_models: [
            { id: 'default', label: 'Default' },
            { id: 'synonbiomed-deep-research', label: 'Deep Research' },
          ],
        },
      }) as unknown as ManagedAgent;
    mockManagedAgents = [buildManagedAgent()];

    const { result, rerender } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAcpModel).toBe('default');
    });

    act(() => {
      result.current.setSelectedAcpModel('synonbiomed-deep-research');
    });

    expect(result.current.selectedAcpModel).toBe('synonbiomed-deep-research');

    mockManagedAgents = [buildManagedAgent()];
    rerender();

    expect(result.current.selectedAcpModel).toBe('synonbiomed-deep-research');
  });

  it('does not fall back to historical static modes when managed agent catalog has no modes', async () => {
    mockAssistants = [
      {
        id: 'assistant-synonbiomed-empty',
        source: 'generated',
        name: 'Synon Biomed',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 1,
        agent_id: 'AIDD_EXPERT_EMPTY',
        agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
        enabled_skills: [],
        custom_skill_names: [],
        disabled_builtin_skills: [],
        context_i18n: {},
        prompts: [],
        prompts_i18n: {},
        models: [],
        agent_status: 'online',
        deletable: false,
      } satisfies Assistant,
    ];
    mockManagedAgents = [{ id: 'AIDD_EXPERT_EMPTY', backend: 'synonbiomed' } as unknown as ManagedAgent];

    const { result } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBe('assistant-synonbiomed-empty');
    });

    expect(result.current.currentAgentModeOptions).toEqual([]);
    expect(result.current.selectedMode).toBe('default');
  });

  it('reads Synon Biomed mode options from the managed agent catalog', async () => {
    mockAssistants = [
      {
        id: 'synonbiomed:aidd-expert',
        source: 'generated',
        name: 'AIDD Expert',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 1,
        agent_id: 'AIDD_EXPERT',
        agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
        enabled_skills: [],
        custom_skill_names: [],
        disabled_builtin_skills: [],
        context_i18n: {},
        prompts: [],
        prompts_i18n: {},
        models: [],
        agent_status: 'online',
        deletable: false,
      } satisfies Assistant,
    ];
    mockManagedAgents = [
      {
        id: 'AIDD_EXPERT',
        agent_type: 'synonbiomed',
        available_modes: {
          current_mode_id: 'default',
          available_modes: [
            { id: 'default', name: 'Default' },
            { id: 'auto_edit', name: 'Auto Edit' },
            { id: 'yolo', name: 'YOLO' },
          ],
        },
      } as unknown as ManagedAgent,
    ];

    const { result } = renderHook(() =>
      useGuidAssistantSelection({
        resetAssistant: false,
      })
    );

    await waitFor(() => {
      expect(result.current.selectedAssistantId).toBe('synonbiomed:aidd-expert');
    });

    expect(result.current.selectedAssistantBackend).toBe('synonbiomed');
    expect(result.current.selectedMode).toBe('default');
    expect(result.current.currentAgentModeOptions.map((mode) => mode.value)).toEqual(['default', 'auto_edit', 'yolo']);
  });
});

function assistantFixture({
  id,
  runtimeKey,
  source,
  sortOrder,
}: {
  id: string;
  runtimeKey: string;
  source: Assistant['source'];
  sortOrder: number;
}): Assistant {
  return {
    id,
    source,
    name: id,
    name_i18n: {},
    description_i18n: {},
    enabled: true,
    sort_order: sortOrder,
    agent_id: `agent-${runtimeKey}`,
    agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
    enabled_skills: [],
    custom_skill_names: [],
    disabled_builtin_skills: [],
    context_i18n: {},
    prompts: [],
    prompts_i18n: {},
    models: [],
    agent_status: 'online',
    deletable: source === 'user',
  };
}

describe('assistant model helpers', () => {
  it('builds model and mode info from agent runtime payloads', () => {
    const agent = {
      available_models: JSON.stringify({
        current_model_id: 'synonbiomed-deep-research',
        current_model_label: 'Deep Research',
        available_models: [
          {
            id: 'synonbiomed-default',
            label: 'Synon Biomed Default',
            description: 'Default biomedical workflow model',
          },
          {
            id: 'synonbiomed-deep-research',
            label: 'Deep Research',
            description: 'Most capable for complex work',
          },
        ],
      }),
      available_modes: JSON.stringify({
        current_mode_id: 'bypassPermissions',
        available_modes: [
          { id: 'default', name: 'Default' },
          { id: 'bypassPermissions', name: 'Bypass Permissions' },
        ],
      }),
    };

    expect(buildAgentRuntimeModelInfo(agent)).toEqual({
      current_model_id: 'synonbiomed-deep-research',
      current_model_label: 'Deep Research',
      available_models: [
        { id: 'synonbiomed-default', label: 'Synon Biomed Default', description: 'Default biomedical workflow model' },
        {
          id: 'synonbiomed-deep-research',
          label: 'Deep Research',
          description: 'Most capable for complex work',
        },
      ],
    });
    expect(buildAgentRuntimeModeState(agent)).toEqual({
      currentMode: 'bypassPermissions',
      options: [
        { value: 'default', label: 'Default', description: undefined },
        { value: 'bypassPermissions', label: 'Bypass Permissions', description: undefined },
      ],
    });
  });

  it('prefers model config_options before falling back to available_models', () => {
    const agent = {
      config_options: {
        config_options: [
          {
            id: 'model',
            category: 'model',
            type: 'select',
            currentValue: 'gpt-5.5',
            options: [
              { value: 'gpt-5.5', name: 'GPT-5.5' },
              { value: 'gpt-5.2', name: 'gpt-5.2' },
            ],
          },
        ],
      },
      available_models: {
        current_model_id: 'legacy-model',
        available_models: [{ id: 'legacy-model', label: 'Legacy Model' }],
      },
    };

    expect(buildAgentRuntimeModelInfo(agent)).toEqual({
      current_model_id: 'gpt-5.5',
      current_model_label: 'GPT-5.5',
      available_models: [
        { id: 'gpt-5.5', label: 'GPT-5.5' },
        { id: 'gpt-5.2', label: 'gpt-5.2' },
      ],
    });
  });

  it('prefers mode config_options before falling back to available_modes', () => {
    const agent = {
      config_options: {
        config_options: [
          {
            id: 'mode',
            category: 'mode',
            type: 'select',
            currentValue: 'full-access',
            options: [
              { value: 'read-only', name: 'Read Only' },
              { value: 'full-access', name: 'Full Access' },
            ],
          },
        ],
      },
      available_modes: {
        current_mode_id: 'legacy-mode',
        available_modes: [{ id: 'legacy-mode', name: 'Legacy Mode' }],
      },
    };

    expect(buildAgentRuntimeModeState(agent)).toEqual({
      currentMode: 'full-access',
      options: [
        { value: 'read-only', label: 'Read Only', description: undefined },
        { value: 'full-access', label: 'Full Access', description: undefined },
      ],
    });
  });

  it('builds slash commands from persisted agent available_commands metadata', () => {
    const agent = {
      available_commands: JSON.stringify({
        available_commands: [
          {
            name: 'review',
            description: 'Review the current diff',
            input: {
              hint: '⌘R',
            },
            _meta: {
              completion_behavior: 'neutral_tip_on_empty',
              empty_turn_tip_code: 'acp.empty_turn.choose_command',
              empty_turn_tip_params: { command_count: 1 },
            },
          },
        ],
      }),
    };

    expect(buildAgentRuntimeSlashCommands(agent)).toEqual([
      {
        name: 'review',
        description: 'Review the current diff',
        hint: '⌘R',
        kind: 'template',
        source: 'acp',
        selectionBehavior: 'insert',
        completionBehavior: 'neutral_tip_on_empty',
        emptyTurnTipCode: 'acp.empty_turn.choose_command',
        emptyTurnTipParams: { command_count: 1 },
      },
    ]);
  });

  it('ignores malformed available_commands metadata', () => {
    expect(
      buildAgentRuntimeSlashCommands({
        available_commands: JSON.stringify({
          available_commands: [{ name: 'missing-description' }, null, 'bad'],
        }),
      })
    ).toEqual([]);
  });

  it('builds ACP model info from assistant models', () => {
    expect(buildAssistantModelInfo(['synonbiomed-deep-research', 'synonbiomed-literature-review'])).toEqual({
      current_model_id: 'synonbiomed-deep-research',
      current_model_label: 'synonbiomed-deep-research',
      available_models: [
        { id: 'synonbiomed-deep-research', label: 'synonbiomed-deep-research' },
        { id: 'synonbiomed-literature-review', label: 'synonbiomed-literature-review' },
      ],
    });
  });

  it('builds Codex ACP model info from assistant models', () => {
    expect(buildAssistantModelInfo(['gpt-5.5', 'gpt-5.4'])).toEqual({
      current_model_id: 'gpt-5.5',
      current_model_label: 'gpt-5.5',
      available_models: [
        { id: 'gpt-5.5', label: 'gpt-5.5' },
        { id: 'gpt-5.4', label: 'gpt-5.4' },
      ],
    });
  });

  it('defaults to the first assistant model when no assistant preference has been applied yet', () => {
    expect(resolveInitialAssistantModel(['synonbiomed-deep-research', 'synonbiomed-literature-review'])).toBe(
      'synonbiomed-deep-research'
    );
  });

  it('does not synthesize Codex models when the assistant catalog has none', () => {
    expect(buildAssistantModelInfo([])).toBeNull();
    expect(resolveInitialAssistantModel([])).toBeNull();
  });

  it('applies a persisted global model default when the model is available', async () => {
    loadSessionDefaultsMock.mockResolvedValue({ defaultModelId: 'synonbiomed-deep-research' });
    const { result } = renderHook(() => useGuidAssistantSelection({}));

    await waitFor(() => expect(result.current.selectedAcpModel).toBe('synonbiomed-deep-research'));
    expect(result.current.sessionDefaults).toEqual({ defaultModelId: 'synonbiomed-deep-research' });
  });
});
