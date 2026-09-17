/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, fireEvent, render, screen } from '@testing-library/react';
import type { ComposerContextItem } from '@/renderer/components/chat/SendBox/composerCompositionModel';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const {
  agentSelectionMock,
  locationMock,
  guidInputMock,
  capturedGuidActionRowProps,
  capturedAssistantSelectionAreaProps,
  capturedGuidInputCardProps,
  capturedGuidSendDeps,
  loadSynonBiomedCatalogSummaryMock,
  resolveGuidAssistantDefaultsMock,
  sendMock,
  navigateMock,
} = vi.hoisted(() => ({
  agentSelectionMock: {
    selectedAssistantId: 'synonbiomed:aidd-expert',
    selectedAssistant: {
      id: 'synonbiomed:aidd-expert',
      source: 'generated',
      name: 'AIDD Expert',
      name_i18n: {},
      description_i18n: {},
      enabled: true,
      sort_order: 10,
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
    },
    assistants: [
      {
        id: 'synonbiomed:aidd-expert',
        source: 'generated',
        name: 'AIDD Expert',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 10,
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
      },
    ],
    selectedAssistantBackend: 'synonbiomed',
    selectedAssistantAvailable: true,
    assistantCatalogStatus: 'ready',
    hasUsableAssistantCatalog: true,
    retryAssistantCatalog: vi.fn(),
    selectedMode: 'default',
    setSelectedMode: vi.fn(),
    selectedAcpModel: null,
    setSelectedAcpModel: vi.fn(),
    currentAcpCachedModelInfo: null,
    currentThoughtLevelOption: null,
    selectedThoughtLevelValue: '',
    setSelectedThoughtLevelValue: vi.fn(),
    sessionDefaults: {},
    defaultAssistantId: 'synonbiomed:aidd-expert',
    setSelectedAssistantId: vi.fn(),
  },
  guidInputMock: {
    input: '',
    setInput: vi.fn(),
    files: [],
    setFiles: vi.fn(),
    localFiles: [],
    setLocalFiles: vi.fn(),
    contextItems: [] as ComposerContextItem[],
    setContextItems: vi.fn(),
    dir: '',
    setDir: vi.fn(),
    loading: false,
    setLoading: vi.fn(),
    isInputFocused: false,
    isFileDragging: false,
    dragHandlers: {},
    onPaste: vi.fn(),
    handleTextareaFocus: vi.fn(),
    handleTextareaBlur: vi.fn(),
    handleFilesUploaded: vi.fn(),
    handleRemoveFile: vi.fn(),
    handleLocalFilesSelected: vi.fn(),
    handleRemoveLocalFile: vi.fn(),
  },
  locationMock: {
    state: null as unknown,
    key: 'guid-location',
    pathname: '/guid',
    search: '',
    hash: '',
  },
  capturedGuidActionRowProps: [] as Array<Record<string, unknown>>,
  capturedAssistantSelectionAreaProps: [] as Array<Record<string, unknown>>,
  capturedGuidInputCardProps: [] as Array<Record<string, unknown>>,
  capturedGuidSendDeps: [] as Array<Record<string, unknown>>,
  loadSynonBiomedCatalogSummaryMock: vi.fn(),
  resolveGuidAssistantDefaultsMock: vi.fn(() => ({
    disabledBuiltinSkillIds: [],
    skillMode: 'fixed' as const,
    skillIds: [],
    mcpIds: [],
  })),
  sendMock: {
    handleSend: vi.fn(),
    sendMessageHandler: vi.fn(),
    isButtonDisabled: false,
  },
  navigateMock: vi.fn(),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string; [key: string]: unknown }) => options?.defaultValue || key,
    i18n: { language: 'en-US' },
  }),
}));

vi.mock('react-router', () => ({
  useNavigate: () => navigateMock,
  useLocation: () => locationMock,
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    fs: {
      listAvailableSkills: { invoke: vi.fn().mockResolvedValue([]) },
    },
  },
}));

vi.mock('@/renderer/hooks/mcp/catalog', () => ({
  ensureBackendMcpCatalog: vi.fn().mockResolvedValue({ allServers: [] }),
}));

vi.mock('@/renderer/services/synonBiomedCatalog', async () => {
  const actual = await vi.importActual<typeof import('@/renderer/services/synonBiomedCatalog')>(
    '@/renderer/services/synonBiomedCatalog'
  );
  return {
    ...actual,
    loadSynonBiomedCatalogSummary: loadSynonBiomedCatalogSummaryMock,
  };
});

vi.mock('@/renderer/hooks/chat/useInputFocusRing', () => ({
  useInputFocusRing: () => ({
    activeBorderColor: '#000',
    inactiveBorderColor: '#ccc',
    activeShadow: 'active-shadow',
    inactiveShadow: 'inactive-shadow',
    surfaceBackgroundColor: 'surface-background',
  }),
}));

vi.mock('@/renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'owner-test' } }),
}));

const useGuidAssistantSelectionMock = vi.fn(() => agentSelectionMock);

vi.mock('@/renderer/pages/guid/hooks/useGuidAssistantSelection', () => ({
  useGuidAssistantSelection: (...args: unknown[]) => useGuidAssistantSelectionMock(...args),
  resolveAssistantSelectionKey: vi.fn(),
  pickDefaultAssistantSelectionKey: vi.fn(),
}));

vi.mock('@/renderer/pages/guid/hooks/useGuidInput', () => ({
  useGuidInput: () => guidInputMock,
}));

vi.mock('@/renderer/pages/guid/hooks/useGuidSend', async () => {
  const actual = await vi.importActual<typeof import('@/renderer/pages/guid/hooks/useGuidSend')>(
    '@/renderer/pages/guid/hooks/useGuidSend'
  );
  return {
    ...actual,
    useGuidSend: (deps: Record<string, unknown>) => {
      capturedGuidSendDeps.push(deps);
      return sendMock;
    },
  };
});

vi.mock('@/renderer/pages/guid/hooks/useTypewriterPlaceholder', () => ({
  useTypewriterPlaceholder: () => '',
}));

vi.mock('@/renderer/pages/guid/components/AssistantSelectionArea', () => ({
  default: (props: Record<string, unknown>) => {
    capturedAssistantSelectionAreaProps.push(props);
    return <div data-testid='assistant-selection-area' />;
  },
}));

vi.mock('@/renderer/pages/guid/components/GuidActionRow', () => ({
  default: (props: Record<string, unknown>) => {
    capturedGuidActionRowProps.push(props);
    return (
      <button type='button' data-testid='guid-action-row' onClick={props.onSend as React.MouseEventHandler}>
        Send
      </button>
    );
  },
}));

vi.mock('@/renderer/pages/guid/components/GuidInputCard', () => ({
  default: (props: Record<string, unknown>) => {
    capturedGuidInputCardProps.push(props);
    return <div data-testid='guid-input-card'>{props.actionRow as React.ReactNode}</div>;
  },
}));

vi.mock('@/renderer/pages/guid/components/QuickActionButtons', () => ({
  default: () => <div data-testid='guid-quick-actions' />,
}));

vi.mock('@/renderer/components/settings/SettingsModal/contents/FeedbackReportModal', () => ({
  default: () => null,
}));

vi.mock('@/renderer/components/chat/SpeechInputButton', () => ({
  default: () => null,
}));

vi.mock('@/renderer/components/synonBiomed/runtime/SynonBiomedSessionOptionsMenu', () => ({
  default: () => <div data-testid='guid-session-options' />,
}));

vi.mock('@/renderer/hooks/system/useLiveTranscriptInsertion', () => ({
  useLiveTranscriptInsertion: () => ({ handleLiveTranscript: vi.fn() }),
}));

vi.mock('@/renderer/hooks/system/useSpeechInput', () => ({
  appendSpeechTranscript: (prev: string, next: string) => `${prev}${next}`,
}));

vi.mock('@/renderer/utils/platform', () => ({
  openExternalUrl: vi.fn(),
  resolveExtensionAssetUrl: vi.fn(),
  resolveBackendAssetUrl: vi.fn((path: string) => path),
}));

vi.mock('@/renderer/pages/guid/utils/assistantDefaults', () => ({
  resolveGuidAssistantDefaults: (...args: unknown[]) => resolveGuidAssistantDefaultsMock(...args),
}));

const swrMock = vi.hoisted(() => ({
  useSWRMock: vi.fn(),
}));

const assistantDetailFixture = {
  prompts: {
    recommended: [],
    recommended_i18n: {
      'en-US': [
        'Create a three-page financial dashboard with profit, revenue mix, and conditional formatting highlights',
      ],
    },
  },
  defaults: {
    model: { mode: 'auto' },
    permission: { mode: 'auto' },
    skills: { mode: 'auto', value: [] },
    mcps: { mode: 'auto', value: [] },
  },
  preferences: {
    last_model_id: null,
    last_permission_value: null,
    last_skill_ids: [],
    last_disabled_builtin_skill_ids: [],
    last_mcp_ids: [],
  },
};

vi.mock('swr', async () => {
  const actual = await vi.importActual<typeof import('swr')>('swr');
  return {
    ...actual,
    default: swrMock.useSWRMock,
    mutate: vi.fn(),
  };
});

import GuidPage from '@/renderer/pages/guid/GuidPage';

const renderGuidPage = async () => {
  let view!: ReturnType<typeof render>;
  await act(async () => {
    view = render(<GuidPage />);
    await Promise.resolve();
  });
  return view;
};

describe('GuidPage', () => {
  beforeEach(() => {
    locationMock.state = null;
    locationMock.key = 'guid-location';
    navigateMock.mockReset();
    guidInputMock.input = '';
    guidInputMock.contextItems = [];
    guidInputMock.setContextItems.mockReset();
    sendMock.sendMessageHandler.mockReset();
    sendMock.sendMessageHandler.mockResolvedValue(true);
    sendMock.isButtonDisabled = false;
    swrMock.useSWRMock.mockReturnValue({ data: null });
    capturedGuidActionRowProps.length = 0;
    capturedAssistantSelectionAreaProps.length = 0;
    capturedGuidInputCardProps.length = 0;
    capturedGuidSendDeps.length = 0;
    loadSynonBiomedCatalogSummaryMock.mockReset();
    loadSynonBiomedCatalogSummaryMock.mockResolvedValue(null);
    useGuidAssistantSelectionMock.mockClear();
    resolveGuidAssistantDefaultsMock.mockReturnValue({
      disabledBuiltinSkillIds: [],
      skillMode: 'fixed',
      skillIds: [],
      mcpIds: [],
    });
    agentSelectionMock.currentAgentModeOptions = [];
    agentSelectionMock.currentAcpCachedModelInfo = null;
    agentSelectionMock.selectedAcpModel = null;
    agentSelectionMock.currentThoughtLevelOption = null;
    agentSelectionMock.selectedThoughtLevelValue = '';
    agentSelectionMock.selectedAssistantBackend = 'synonbiomed';
    agentSelectionMock.setSelectedAcpModel.mockReset();
    agentSelectionMock.setSelectedThoughtLevelValue.mockReset();
    agentSelectionMock.setSelectedMode.mockReset();
    agentSelectionMock.retryAssistantCatalog.mockReset();
    agentSelectionMock.assistantCatalogStatus = 'ready';
    agentSelectionMock.hasUsableAssistantCatalog = true;
    agentSelectionMock.assistants = [
      {
        id: 'synonbiomed:aidd-expert',
        source: 'generated',
        name: 'AIDD Expert',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 10,
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
      },
    ];
    agentSelectionMock.selectedAssistantId = 'synonbiomed:aidd-expert';
    agentSelectionMock.selectedAssistant = agentSelectionMock.assistants[0];
  });

  it('renders the v1.1 empty-task canvas without duplicate home-page modules', async () => {
    await renderGuidPage();

    expect(screen.getByTestId('guid-page')).toHaveAttribute('data-composer-position', 'centered');
    expect(screen.queryByLabelText('common.back')).not.toBeInTheDocument();
    expect(screen.queryByLabelText('Assistant Details')).not.toBeInTheDocument();
    expect(screen.queryByText('conversation.welcome.title')).not.toBeInTheDocument();
    expect(screen.queryByTestId('assistant-selection-area')).not.toBeInTheDocument();
    expect(screen.getByRole('img', { name: 'SYNON-Biomed' })).toHaveAttribute(
      'src',
      './branding/synon-biomed-lockup.png'
    );
    expect(screen.getByText('guid.emptyState.guidance')).toBeInTheDocument();
    const latestGuidActionRowProps = capturedGuidActionRowProps.at(-1);
    const latestGuidInputCardProps = capturedGuidInputCardProps.at(-1);

    expect(capturedAssistantSelectionAreaProps).toHaveLength(0);
    expect(capturedGuidActionRowProps.length).toBeGreaterThan(0);
    expect(latestGuidActionRowProps?.modelSelectorNode).toBeTruthy();
    expect(latestGuidActionRowProps?.permissionNode).toBeTruthy();
    expect(capturedGuidInputCardProps.length).toBeGreaterThan(0);
    expect(latestGuidInputCardProps).not.toHaveProperty('workspaceDir');
    expect(latestGuidInputCardProps).not.toHaveProperty('synonBiomedWorkspaceOptions');
  });

  it('uses the shared typed composer context for first-turn artifact, Skill, and MCP selections', async () => {
    swrMock.useSWRMock.mockImplementation((key: unknown) =>
      typeof key === 'string' && key.startsWith('guid.assistant.composer-capabilities.')
        ? {
            data: {
              skills: ['autodock-vina'],
              mcpStatuses: [{ id: 'bundled:pubmed', name: 'PubMed', status: 'loaded' }],
            },
          }
        : { data: null }
    );
    guidInputMock.contextItems = [
      {
        kind: 'artifact',
        artifactId: 'artifact-1',
        versionId: 'version-1',
        label: 'report.md',
      },
      { kind: 'skill', name: 'autodock-vina', label: 'autodock-vina' },
      { kind: 'mcp', serverId: 'bundled:pubmed', label: 'PubMed' },
    ];

    await renderGuidPage();

    const actionProps = capturedGuidActionRowProps.at(-1);
    expect(actionProps?.loadedSkills).toEqual(['autodock-vina']);
    expect(actionProps?.loadedMcpStatuses).toEqual([{ id: 'bundled:pubmed', name: 'PubMed', status: 'loaded' }]);
    expect(actionProps?.selectedSkillNames).toEqual(['autodock-vina']);
    expect(actionProps?.selectedMcpServerIds).toEqual(['bundled:pubmed']);
    expect(capturedGuidInputCardProps.at(-1)?.contextItems).toEqual(guidInputMock.contextItems);
    expect(capturedGuidSendDeps.at(-1)?.contextItems).toEqual(guidInputMock.contextItems);

    const onSelectSkill = actionProps?.onSelectSkill;
    if (typeof onSelectSkill !== 'function') throw new Error('Expected typed Skill selection handler');
    act(() => {
      (onSelectSkill as (name: string) => void)('autodock-vina');
    });
    const toggle = guidInputMock.setContextItems.mock.calls.at(-1)?.[0] as (
      items: typeof guidInputMock.contextItems
    ) => typeof guidInputMock.contextItems;
    expect(toggle(guidInputMock.contextItems).map((item) => item.kind)).toEqual(['artifact', 'mcp']);
  });

  it('starts every new conversation from the shared local, optional-features-off defaults', async () => {
    await renderGuidPage();

    await vi.waitFor(() => {
      const sessionOptionsNode = capturedGuidActionRowProps.at(-1)?.sessionOptionsNode;
      expect(React.isValidElement(sessionOptionsNode)).toBe(true);
      const sessionOptionsProps = (sessionOptionsNode as React.ReactElement<Record<string, unknown>>).props;
      expect(sessionOptionsProps.value).toMatchObject({
        delegation: false,
        autoReview: false,
        memory: false,
        targetAgent: 'AIDD_EXPERT',
      });
    });
    expect(capturedGuidSendDeps.at(-1)?.sessionComputeProviders).toEqual([]);
  });

  it('resets an unsent customized draft when a new-conversation navigation reuses the mounted route', async () => {
    const view = await renderGuidPage();
    const initialNode = capturedGuidActionRowProps.at(-1)?.sessionOptionsNode;
    if (!React.isValidElement(initialNode)) throw new Error('Expected session options node');
    const initialProps = initialNode.props as {
      onChange: (value: Record<string, unknown>) => void;
      onComputeSelectionChange: (providers: string[]) => void;
      value: Record<string, unknown>;
    };
    act(() => {
      initialProps.onChange({
        ...initialProps.value,
        delegation: true,
        autoReview: true,
        memory: true,
        targetAgent: 'STRUCTURAL_BIOLOGY_EXPERT',
      });
      initialProps.onComputeSelectionChange(['openfold3-service']);
    });

    locationMock.key = 'guid-new-conversation';
    locationMock.state = { resetAssistant: true };
    await act(async () => {
      view.rerender(<GuidPage />);
      await Promise.resolve();
    });

    await vi.waitFor(() => {
      const resetNode = capturedGuidActionRowProps.at(-1)?.sessionOptionsNode;
      expect(React.isValidElement(resetNode)).toBe(true);
      const resetProps = (resetNode as React.ReactElement<Record<string, unknown>>).props;
      expect(resetProps.value).toMatchObject({
        delegation: false,
        autoReview: false,
        memory: false,
        targetAgent: 'AIDD_EXPERT',
      });
      expect(capturedGuidSendDeps.at(-1)?.sessionComputeProviders).toEqual([]);
    });
  });

  it('docks the composer while sending and returns it to center when creation fails', async () => {
    guidInputMock.input = 'test task';
    let resolveSend!: (sent: boolean) => void;
    sendMock.sendMessageHandler.mockReturnValue(
      new Promise<boolean>((resolve) => {
        resolveSend = resolve;
      })
    );
    await renderGuidPage();

    fireEvent.click(screen.getByTestId('guid-action-row'));
    expect(screen.getByTestId('guid-page')).toHaveAttribute('data-composer-position', 'docked');

    await act(async () => resolveSend(false));
    expect(screen.getByTestId('guid-page')).toHaveAttribute('data-composer-position', 'centered');
  });

  it('shows a compact manual recovery action only after background catalog recovery is exhausted', async () => {
    agentSelectionMock.assistantCatalogStatus = 'error';
    agentSelectionMock.hasUsableAssistantCatalog = false;
    agentSelectionMock.selectedAssistantId = null;
    agentSelectionMock.assistants = [];

    await renderGuidPage();

    expect(screen.queryByTestId('guid-assistant-catalog-error')).not.toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.getByTestId('guid-page')).toBeInTheDocument();
    const recovery = screen.getByTestId('guid-assistant-catalog-recovery');
    expect(screen.getByRole('status')).toBe(recovery);
    expect(recovery).toHaveAttribute('aria-live', 'polite');
    expect(recovery).toHaveAttribute('aria-atomic', 'true');
    expect(recovery).toHaveTextContent('guid.onboarding.error.loadDescription');
    fireEvent.click(screen.getByRole('button', { name: 'common.retry' }));
    expect(agentSelectionMock.retryAssistantCatalog).toHaveBeenCalledOnce();
  });

  it('ignores legacy selectedAgentKey navigation state when preselecting an assistant', async () => {
    locationMock.state = {
      selectedAgentKey: 'bare:claude',
    };

    await renderGuidPage();

    expect(useGuidAssistantSelectionMock).toHaveBeenCalledWith(
      expect.objectContaining({
        preselectAssistantId: undefined,
      })
    );
  });

  it('consumes one-shot assistant state without dropping the selected project workspace', async () => {
    locationMock.state = {
      resetAssistant: true,
      workspace: 'synonbiomed://project/proj_stat6',
    };

    await renderGuidPage();

    await vi.waitFor(() =>
      expect(navigateMock).toHaveBeenCalledWith('/guid', {
        replace: true,
        state: { workspace: 'synonbiomed://project/proj_stat6' },
      })
    );
  });

  it('preserves the first-turn draft while one-shot new-task state is removed', async () => {
    locationMock.state = {
      resetAssistant: true,
      workspace: 'synonbiomed://project/proj_stat6',
    };
    const view = await renderGuidPage();

    await vi.waitFor(() => expect(navigateMock).toHaveBeenCalled());
    guidInputMock.setInput.mockClear();
    guidInputMock.setFiles.mockClear();
    guidInputMock.setLocalFiles.mockClear();
    guidInputMock.setContextItems.mockClear();
    guidInputMock.setDir.mockClear();

    locationMock.key = 'guid-location-cleaned';
    locationMock.state = { workspace: 'synonbiomed://project/proj_stat6' };
    await act(async () => {
      view.rerender(<GuidPage />);
      await Promise.resolve();
    });

    expect(guidInputMock.setInput).not.toHaveBeenCalled();
    expect(guidInputMock.setFiles).not.toHaveBeenCalled();
    expect(guidInputMock.setLocalFiles).not.toHaveBeenCalled();
    expect(guidInputMock.setContextItems).not.toHaveBeenCalled();
    expect(guidInputMock.setDir).not.toHaveBeenCalled();
  });

  it('does not render assistant prompt cards on the empty-task canvas', async () => {
    agentSelectionMock.assistants = [
      {
        id: 'synonbiomed:aidd-expert',
        source: 'generated',
        name: 'AIDD Expert',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 10,
        agent_id: 'AIDD_EXPERT',
        agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
        enabled_skills: [],
        custom_skill_names: [],
        disabled_builtin_skills: [],
        context_i18n: {},
        prompts: [],
        prompts_i18n: {
          'en-US': [
            'Create a three-page financial dashboard with profit, revenue mix, and conditional formatting highlights',
          ],
        },
        models: [],
        agent_status: 'online',
        deletable: false,
      },
    ];

    swrMock.useSWRMock.mockImplementation((key: string | null) => {
      if (key?.startsWith('guid.assistant.detail.')) {
        return {
          data: assistantDetailFixture,
        };
      }
      return { data: null };
    });

    await renderGuidPage();

    expect(
      screen.queryByRole('button', {
        name: /Create a three-page financial dashboard with profit/i,
      })
    ).not.toBeInTheDocument();
  });

  it('does not add default workflow suggestions below the composer', async () => {
    await renderGuidPage();

    expect(screen.queryByRole('button', { name: 'guid.defaultPrompts.capabilities' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'guid.defaultPrompts.skills' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'guid.defaultPrompts.tools' })).not.toBeInTheDocument();
  });

  it('keeps project selection in the sidebar instead of duplicating it below the composer', async () => {
    loadSynonBiomedCatalogSummaryMock.mockResolvedValue({
      product: 'Synon Biomed',
      backendStatus: 'healthy',
      runtimeAssetsDir: '/home/victor_1/synonbiomed-workbench/synonbiomed/runtime/assets',
      backendBaseUrl: 'http://127.0.0.1:8892',
      counts: {
        agents: 14,
        skills: 69,
        mcpServers: 2,
        thirdPartyAssets: 2,
        backendAgents: 14,
        seedProjects: 1,
        backendProjects: 1,
      },
      featuredAgents: [],
      featuredSkills: [],
      seedProjects: [
        {
          slug: 'crispr_screen',
          name: 'CRISPR Screen',
          manifestPath:
            '/home/victor_1/synonbiomed-workbench/synonbiomed/runtime/assets/seed/manifest_crispr_screen.json',
          rootFrameId: 'd3da0234-5a7c-4d1e-a4a8-8967a35666b9',
          artifactCount: 12,
          childFrameCount: 3,
          folderCount: 2,
        },
      ],
      backendProjects: [
        {
          projectId: 'proj_stat6',
          name: 'STAT6',
          description: 'STAT6 SBDD PPI workflow',
          conversationCount: 2,
          artifactCount: 23,
          createdAt: '2026-07-02T03:59:09.453Z',
          updatedAt: '2026-07-05T09:24:10.591Z',
          lastActiveAt: '2026-07-05T09:24:10.591Z',
        },
      ],
      mcpServers: [],
      thirdPartyAssets: [],
      integrationSurfaces: [],
    });

    await renderGuidPage();

    expect(capturedGuidInputCardProps.at(-1)).not.toHaveProperty('synonBiomedWorkspaceOptions');
    expect(loadSynonBiomedCatalogSummaryMock).not.toHaveBeenCalled();
  });

  it('does not seed skill defaults from the assistant list while detail is loading', async () => {
    agentSelectionMock.assistants = [
      {
        id: 'synonbiomed:aidd-expert',
        source: 'generated',
        name: 'AIDD Expert',
        name_i18n: {},
        description_i18n: {},
        enabled: true,
        sort_order: 10,
        agent_id: 'AIDD_EXPERT',
        agent: { type: 'synonbiomed', source: 'builtin', acp_backend: 'synonbiomed' },
        enabled_skills: ['stale-list-skill'],
        custom_skill_names: [],
        disabled_builtin_skills: ['stale-disabled-builtin'],
        context_i18n: {},
        prompts: [],
        prompts_i18n: {},
        models: [],
        agent_status: 'online',
        deletable: false,
      },
    ];
    swrMock.useSWRMock.mockReturnValue({ data: null });

    await renderGuidPage();

    await vi.waitFor(() => {
      const latestDeps = capturedGuidSendDeps.at(-1);
      expect(latestDeps).toMatchObject({
        guidEnabledSkills: undefined,
        guidDisabledBuiltinSkills: undefined,
      });
    });
  });

  it('applies a Synon Biomed assistant default model through runtime model metadata', async () => {
    swrMock.useSWRMock.mockReturnValue({ data: assistantDetailFixture });
    resolveGuidAssistantDefaultsMock.mockReturnValue({
      modelId: 'synonbiomed-deep-research',
      disabledBuiltinSkillIds: [],
      skillMode: 'fixed',
      skillIds: [],
      mcpIds: [],
    });
    agentSelectionMock.currentAcpCachedModelInfo = {
      current_model_id: 'synonbiomed-default',
      current_model_label: 'Synon Biomed Default',
      available_models: [
        { id: 'synonbiomed-default', label: 'Synon Biomed Default' },
        { id: 'synonbiomed-deep-research', label: 'Deep Research' },
      ],
    };
    await renderGuidPage();

    await vi.waitFor(() => {
      expect(agentSelectionMock.setSelectedAcpModel).toHaveBeenCalledWith('synonbiomed-deep-research', {
        persistPreference: false,
      });
    });
  });

  it('exposes the Synon Biomed draft model selector and forwards its model state to send', async () => {
    agentSelectionMock.selectedAcpModel = 'synonbiomed-deep-research';
    agentSelectionMock.selectedAssistantBackend = 'synonbiomed';
    agentSelectionMock.currentAcpCachedModelInfo = {
      current_model_id: 'synonbiomed-default',
      current_model_label: 'Synon Biomed Default',
      available_models: [
        { id: 'synonbiomed-default', label: 'Synon Biomed Default' },
        { id: 'synonbiomed-deep-research', label: 'Deep Research' },
      ],
    };
    agentSelectionMock.currentThoughtLevelOption = {
      id: 'thought_level',
      category: 'thought_level',
      currentValue: 'medium',
      options: [
        { value: 'low', label: 'Low' },
        { value: 'medium', label: 'Medium' },
        { value: 'high', label: 'High' },
      ],
    };
    agentSelectionMock.selectedThoughtLevelValue = 'high';

    await renderGuidPage();

    await vi.waitFor(() => {
      expect(capturedGuidActionRowProps.at(-1)?.modelSelectorNode).toMatchObject({
        props: {
          modelInfo: agentSelectionMock.currentAcpCachedModelInfo,
          selectedModelId: 'synonbiomed-deep-research',
          thoughtLevelOption: agentSelectionMock.currentThoughtLevelOption,
          selectedThoughtLevel: 'high',
        },
      });
      expect(capturedGuidSendDeps.at(-1)).toMatchObject({
        selectedAssistantBackend: 'synonbiomed',
        selectedAcpModel: 'synonbiomed-deep-research',
        currentAcpCachedModelInfo: agentSelectionMock.currentAcpCachedModelInfo,
      });
    });
  });
});
