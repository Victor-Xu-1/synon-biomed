/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { IMcpServer } from '@/common/config/storage';
import { readConversationRouteSnapshot } from '@/renderer/pages/conversation/utils/conversationCache';
import { useGuidSend, type GuidSendDeps } from '@/renderer/pages/guid/hooks/useGuidSend';

const createConversationInvokeMock = vi.fn();
const swrMutateMock = vi.fn();
const prefetchConversationRouteMock = vi.fn();
const { loadSynonBiomedProjectsMock, uploadSynonBiomedProjectAttachmentMock } = vi.hoisted(() => ({
  loadSynonBiomedProjectsMock: vi.fn(),
  uploadSynonBiomedProjectAttachmentMock: vi.fn(),
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    conversation: {
      create: {
        invoke: (...args: unknown[]) => createConversationInvokeMock(...args),
      },
    },
  },
}));

vi.mock('@/renderer/utils/emitter', () => ({
  emitter: {
    emit: vi.fn(),
  },
}));

vi.mock('@/renderer/pages/conversation/conversationRoute', () => ({
  prefetchConversationRoute: (...args: unknown[]) => prefetchConversationRouteMock(...args),
}));

vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedProjects: (...args: unknown[]) => loadSynonBiomedProjectsMock(...args),
}));

vi.mock('@/renderer/services/onboardingService', () => ({
  uploadSynonBiomedProjectAttachment: (...args: unknown[]) => uploadSynonBiomedProjectAttachmentMock(...args),
  toOnboardingAttachedArtifactReference: (artifact: {
    artifactId: string;
    versionId: string;
    filename: string;
    sizeBytes: number;
    checksum: string;
  }) => ({
    artifact_id: artifact.artifactId,
    version_id: artifact.versionId,
    relation: 'attached',
    availability: 'available',
    filename: artifact.filename,
    size_bytes: artifact.sizeBytes,
    checksum: artifact.checksum,
  }),
}));

vi.mock('swr', () => {
  const mutate = (...args: unknown[]) => swrMutateMock(...args);
  return { mutate, useSWRConfig: () => ({ mutate }) };
});

vi.mock('@/renderer/utils/workspace/workspaceHistory', () => ({
  updateWorkspaceTime: vi.fn(),
}));

vi.mock('@arco-design/web-react', () => ({
  Message: {
    warning: vi.fn(),
    error: vi.fn(),
  },
}));

const createDeps = (): GuidSendDeps => ({
  input: 'hello',
  setInput: vi.fn(),
  files: [],
  setFiles: vi.fn(),
  localFiles: [],
  setLocalFiles: vi.fn(),
  contextItems: [],
  setContextItems: vi.fn(),
  dir: '',
  setDir: vi.fn(),
  setLoading: vi.fn(),
  loading: false,
  selectedAssistantId: 'synonbiomed:aidd-expert',
  selectedAssistantBackend: 'synonbiomed',
  selectedMode: 'bypassPermissions',
  selectedAcpModel: 'synonbiomed-default',
  currentAcpCachedModelInfo: null,
  guidDisabledBuiltinSkills: undefined,
  guidEnabledSkills: undefined,
  assistantDefaultSkillIds: undefined,
  assistantDefaultDisabledBuiltinSkillIds: undefined,
  availableMcpServers: [{ id: 'mcp-user', name: 'User MCP', enabled: true, builtin: false } as IMcpServer],
  selectedMcpServerIds: ['mcp-user'],
  assistantDefaultMcpIds: undefined,
  sessionDefaults: { subagentModelId: 'delegate-model', effort: 'high' },
  sessionOptions: {
    delegation: false,
    autoReview: true,
    memory: true,
    targetAgent: 'OPERON',
  },
  sessionComputeProviders: [],
  setMentionOpen: vi.fn(),
  setMentionQuery: vi.fn(),
  setMentionSelectorOpen: vi.fn(),
  setMentionActiveIndex: vi.fn(),
  ownerId: 'owner-test',
  navigate: vi.fn(() => Promise.resolve()) as never,
  t: vi.fn((key: string, options?: { defaultValue?: string }) => options?.defaultValue || key) as never,
  localeKey: 'zh-CN',
});

describe('useGuidSend', () => {
  beforeEach(() => {
    sessionStorage.clear();
    createConversationInvokeMock.mockReset();
    createConversationInvokeMock.mockResolvedValue({ id: 'conv-1' });
    prefetchConversationRouteMock.mockReset();
    prefetchConversationRouteMock.mockResolvedValue(undefined);
    swrMutateMock.mockReset();
    swrMutateMock.mockResolvedValue(undefined);
    loadSynonBiomedProjectsMock.mockReset();
    uploadSynonBiomedProjectAttachmentMock.mockReset();
    uploadSynonBiomedProjectAttachmentMock.mockResolvedValue({
      artifactId: 'artifact-1',
      versionId: 'version-1',
      filename: 'docking.csv',
      sizeBytes: 24,
      checksum: 'sha256:docking',
    });
  });

  it('uploads staged browser files to the selected project and carries artifact references into the first message', async () => {
    const file = new File(['compound_id,score\nA,1.2\n'], 'docking.csv', { type: 'text/csv' });
    const deps = createDeps();
    deps.dir = 'synonbiomed://project/proj-1?name=Personal%20Workspace';
    deps.localFiles = [{ id: 'local-1', file }];

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await result.current.handleSend();
    });

    expect(uploadSynonBiomedProjectAttachmentMock).toHaveBeenCalledWith(
      'proj-1',
      file,
      expect.objectContaining({
        onInitialized: expect.any(Function),
        onAbandoned: expect.any(Function),
      })
    );
    const createPayload = createConversationInvokeMock.mock.calls[0]?.[0];
    expect(createPayload.extra.project_id).toBe('proj-1');
    expect(JSON.parse(sessionStorage.getItem('acp_initial_message_conv-1') || '{}').artifact_refs).toEqual([
      {
        artifact_id: 'artifact-1',
        version_id: 'version-1',
        relation: 'attached',
        availability: 'available',
        filename: 'docking.csv',
        size_bytes: 24,
        checksum: 'sha256:docking',
      },
    ]);
  });

  it('routes exact generated artifacts, fixed Skills, and fixed MCP ids through the first typed message', async () => {
    const deps = createDeps();
    deps.contextItems = [
      {
        kind: 'artifact',
        artifactId: 'artifact-generated',
        versionId: 'version-generated',
        label: 'generated-report.md',
        contentType: 'text/markdown',
        sizeBytes: 512,
      },
      { kind: 'skill', name: 'autodock-vina', label: 'autodock-vina' },
      { kind: 'mcp', serverId: 'bundled:pubmed', label: 'PubMed' },
    ];

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await result.current.handleSend();
    });

    const stored = JSON.parse(sessionStorage.getItem('acp_initial_message_conv-1') || '{}');
    expect(stored.input).toBe('hello');
    expect(stored.artifact_refs).toEqual([
      {
        artifact_id: 'artifact-generated',
        version_id: 'version-generated',
        relation: 'attached',
        filename: 'generated-report.md',
        content_type: 'text/markdown',
        size_bytes: 512,
      },
    ]);
    expect(stored.inject_skills).toEqual(['autodock-vina']);
    expect(stored.inject_mcp_server_ids).toEqual(['bundled:pubmed']);
  });

  it('derives a direct Guid conversation project from project-file artifacts alone', async () => {
    const deps = createDeps();
    deps.dir = '';
    deps.contextItems = [
      {
        kind: 'artifact',
        artifactId: 'artifact-project-file',
        versionId: 'version-project-file',
        projectId: 'proj-source',
        label: 'multi-ligands.cdx',
      },
    ];

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await expect(result.current.handleSend()).resolves.toBe(true);
    });

    expect(createConversationInvokeMock.mock.calls[0]?.[0].extra).toEqual(
      expect.objectContaining({
        backend: 'synonbiomed',
        project_id: 'proj-source',
        workspace: 'synonbiomed://project/proj-source',
        custom_workspace: false,
      })
    );
    expect(loadSynonBiomedProjectsMock).not.toHaveBeenCalled();
  });

  it('uploads local files into the same project selected by project-file artifacts', async () => {
    const file = new File(['ligand'], 'ligand.cdx', { type: 'application/octet-stream' });
    const deps = createDeps();
    deps.localFiles = [{ id: 'local-ligand', file }];
    deps.contextItems = [
      {
        kind: 'artifact',
        artifactId: 'artifact-receptor',
        versionId: 'version-receptor',
        projectId: 'proj-docking',
        label: 'receptor.pdb',
      },
    ];

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await expect(result.current.handleSend()).resolves.toBe(true);
    });

    expect(uploadSynonBiomedProjectAttachmentMock).toHaveBeenCalledWith('proj-docking', file, expect.any(Object));
    expect(createConversationInvokeMock.mock.calls[0]?.[0].extra.project_id).toBe('proj-docking');
  });

  it('rejects project-file artifacts selected from more than one project', async () => {
    const deps = createDeps();
    deps.contextItems = [
      { kind: 'artifact', artifactId: 'artifact-a', versionId: 'version-a', projectId: 'project-a', label: 'a.cdx' },
      { kind: 'artifact', artifactId: 'artifact-b', versionId: 'version-b', projectId: 'project-b', label: 'b.pdb' },
    ];

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await expect(result.current.handleSend()).rejects.toThrow('must belong to one Synon Biomed project');
    });
    expect(createConversationInvokeMock).not.toHaveBeenCalled();
  });

  it('rejects project-file artifacts that conflict with the selected project workspace', async () => {
    const deps = createDeps();
    deps.dir = 'synonbiomed://project/project-active?name=Active';
    deps.contextItems = [
      {
        kind: 'artifact',
        artifactId: 'artifact-other',
        versionId: 'version-other',
        projectId: 'project-other',
        label: 'other.pdb',
      },
    ];

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await expect(result.current.handleSend()).rejects.toThrow('conflict with the active Synon Biomed project');
    });
    expect(createConversationInvokeMock).not.toHaveBeenCalled();
  });

  it('allows a context-only first turn and clears typed context only after creation succeeds', async () => {
    const deps = createDeps();
    deps.input = '';
    deps.contextItems = [
      {
        kind: 'artifact',
        artifactId: 'artifact-only',
        versionId: 'version-only',
        label: 'only.csv',
      },
    ];
    deps.t = vi.fn((key: string) =>
      key === 'conversation.sendbox.contextOnlyPrompt' ? 'Use selected context.' : key
    ) as never;

    const { result } = renderHook(() => useGuidSend(deps));
    expect(result.current.isButtonDisabled).toBe(false);
    await act(async () => {
      expect(await result.current.sendMessageHandler()).toBe(true);
    });

    expect(JSON.parse(sessionStorage.getItem('acp_initial_message_conv-1') || '{}').input).toBe(
      'Use selected context.'
    );
    expect(deps.setContextItems).toHaveBeenCalledWith([]);
  });

  it('uses Personal Workspace as the attachment project when a new draft has no selected workspace', async () => {
    const file = new File(['target,score\nTP53,1.0\n'], 'screen.csv', { type: 'text/csv' });
    loadSynonBiomedProjectsMock.mockResolvedValue([
      {
        projectId: 'project-personal',
        name: 'Personal Workspace',
        description: null,
        context: null,
        conversationCount: 0,
        artifactCount: 0,
        createdAt: null,
        updatedAt: null,
        lastActiveAt: null,
      },
    ]);
    const deps = createDeps();
    deps.localFiles = [{ id: 'local-1', file }];

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await result.current.handleSend();
    });

    expect(loadSynonBiomedProjectsMock).toHaveBeenCalledTimes(1);
    expect(uploadSynonBiomedProjectAttachmentMock).toHaveBeenCalledWith(
      'project-personal',
      file,
      expect.objectContaining({
        onInitialized: expect.any(Function),
        onAbandoned: expect.any(Function),
      })
    );
    expect(createConversationInvokeMock.mock.calls[0]?.[0].extra).toEqual(
      expect.objectContaining({
        project_id: 'project-personal',
        project_name: 'Personal Workspace',
        workspace: 'synonbiomed://project/project-personal?name=Personal%20Workspace',
      })
    );
  });

  it('stores draft session controls for the first Synon Biomed message', async () => {
    const deps = createDeps();
    deps.sessionOptions = {
      delegation: true,
      autoReview: false,
      memory: true,
      targetAgent: 'AIDD_EXPERT',
    };
    deps.sessionComputeProviders = ['local', 'ssh:hpc-a'];

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await result.current.handleSend();
    });

    expect(JSON.parse(sessionStorage.getItem('acp_initial_message_conv-1') || '{}')).toEqual({
      input: 'hello',
      session_options: {
        ultra_mode: true,
        verifier_mode: 'off',
        memory_mode: 'on',
        target_agent: 'AIDD_EXPERT',
        model: 'synonbiomed-default',
        subagent_model: 'delegate-model',
        effort: 'high',
      },
      compute_providers: ['local', 'ssh:hpc-a'],
    });
  });

  it('preserves the explicit plan-first control in the routed first message', async () => {
    const deps = createDeps();
    deps.planModeRef = { current: true };

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await result.current.handleSend();
    });

    const stored = JSON.parse(sessionStorage.getItem('acp_initial_message_conv-1') || '{}');
    expect(stored.session_options).toEqual(expect.objectContaining({ plan_mode: true }));
  });

  it('passes selected mode into assistant conversation overrides when creating a Synon Biomed conversation', async () => {
    const deps = createDeps();
    deps.selectedThoughtLevelValue = 'high';

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    expect(createConversationInvokeMock).toHaveBeenCalledTimes(1);
    const payload = createConversationInvokeMock.mock.calls[0][0];
    expect(payload.type).toBeUndefined();
    expect('model' in payload).toBe(false);
    expect(payload.assistant?.conversation_overrides?.permission).toBe('bypassPermissions');
    expect(payload.assistant?.conversation_overrides?.model).toBe('synonbiomed-default');
    expect(payload.assistant?.conversation_overrides?.thought_level).toBe('high');
    expect(payload.extra.backend).toBeUndefined();
    expect(payload.extra.agent_name).toBeUndefined();
    expect(payload.extra.agent_id).toBeUndefined();
    expect(payload.extra.custom_agent_id).toBeUndefined();
    expect(payload.extra.preset_rules).toBeUndefined();
    expect(payload.extra.preset_context).toBeUndefined();
    expect(payload.extra.session_mode).toBeUndefined();
    expect(payload.extra.current_model_id).toBeUndefined();
    expect(payload.extra.preset_assistant_id).toBeUndefined();
    expect(swrMutateMock).toHaveBeenCalledWith('guid.assistant.detail.synonbiomed:aidd-expert.zh-CN');
    expect(swrMutateMock).toHaveBeenCalledWith('assistants.list');
  });

  it('navigates without waiting for assistant cache revalidation', async () => {
    const deps = createDeps();
    let releaseRevalidation: (() => void) | undefined;
    swrMutateMock.mockImplementation(
      () =>
        new Promise<void>((resolve) => {
          releaseRevalidation = resolve;
        })
    );

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await expect(result.current.handleSend()).resolves.toBe(true);
    });

    expect(deps.navigate).toHaveBeenCalledWith('/conversation/conv-1');
    expect(prefetchConversationRouteMock).toHaveBeenCalledTimes(1);
    expect(swrMutateMock).toHaveBeenCalledTimes(2);
    releaseRevalidation?.();
  });

  it('hands the created conversation to the route cache before navigation', async () => {
    const deps = createDeps();
    createConversationInvokeMock.mockResolvedValueOnce({ id: 'conv-route-cache', name: 'New task' });
    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await expect(result.current.handleSend()).resolves.toBe(true);
    });

    expect(readConversationRouteSnapshot('owner-test', 'conv-route-cache')).toMatchObject({
      id: 'conv-route-cache',
      name: 'New task',
    });
    expect(deps.navigate).toHaveBeenCalledWith('/conversation/conv-route-cache');
  });

  it('uses the bounded workspace title while preserving the exact long first message', async () => {
    const task =
      'Build an evidence-traceable KRAS G12C resistance analysis with exact identifiers, artifacts, and review. '
        .repeat(8)
        .trim();
    const deps = createDeps();
    deps.input = task;

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    const payload = createConversationInvokeMock.mock.calls[0][0];
    expect(payload.name).toBe(`${task.slice(0, 57)}…`);
    expect(payload.name).toHaveLength(58);
    expect(JSON.parse(sessionStorage.getItem('acp_initial_message_conv-1') || '{}').input).toBe(task);
  });

  it('falls back to assistant default skill and MCP ids for preset conversations before local Guid overrides exist', async () => {
    const deps = createDeps();
    deps.guidEnabledSkills = undefined;
    deps.guidDisabledBuiltinSkills = undefined;
    deps.assistantDefaultSkillIds = ['assistant-skill'];
    deps.assistantDefaultDisabledBuiltinSkillIds = ['builtin-skill'];
    deps.assistantDefaultSkillMode = 'fixed';
    deps.selectedMcpServerIds = undefined;
    deps.assistantDefaultMcpIds = ['mcp-user'];

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    const payload = createConversationInvokeMock.mock.calls[0][0];
    expect(payload.assistant?.conversation_overrides?.skill_ids).toEqual(['assistant-skill']);
    expect(payload.assistant?.conversation_overrides?.disabled_builtin_skill_ids).toEqual(['builtin-skill']);
    expect(payload.assistant?.conversation_overrides?.mcp_ids).toEqual(['mcp-user']);
    expect(payload.extra.selected_mcp_server_ids).toEqual(['mcp-user']);
  });

  it('omits fixed skill overrides when the assistant owns automatic skill discovery', async () => {
    const deps = createDeps();
    Object.assign(deps, { assistantDefaultSkillMode: 'auto' });
    deps.guidEnabledSkills = undefined;
    deps.guidDisabledBuiltinSkills = undefined;
    deps.assistantDefaultSkillIds = ['catalog-skill-a', 'catalog-skill-b'];
    deps.assistantDefaultDisabledBuiltinSkillIds = [];

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    const overrides = createConversationInvokeMock.mock.calls[0][0].assistant?.conversation_overrides;
    expect(overrides).not.toHaveProperty('skill_ids');
    expect(overrides).not.toHaveProperty('disabled_builtin_skill_ids');
  });

  it('omits absent MCP overrides so automatic backend connector authority is preserved', async () => {
    const deps = createDeps();
    deps.selectedMcpServerIds = undefined;
    deps.assistantDefaultMcpIds = undefined;

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    const payload = createConversationInvokeMock.mock.calls[0][0];
    expect(payload.assistant?.conversation_overrides).not.toHaveProperty('mcp_ids');
    expect(payload.extra).not.toHaveProperty('selected_mcp_server_ids');
    expect(payload.extra).not.toHaveProperty('selected_session_mcp_servers');
  });

  it('preserves builtin MCP ids in assistant overrides while only sending user MCP ids to runtime selection', async () => {
    const deps = createDeps();
    deps.availableMcpServers = [
      { id: 'mcp-user', name: 'User MCP', enabled: true, builtin: false } as IMcpServer,
      { id: 'builtin-mcp', name: 'Builtin MCP', enabled: true, builtin: true } as IMcpServer,
    ];
    deps.selectedMcpServerIds = ['mcp-user', 'builtin-mcp'];

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    const payload = createConversationInvokeMock.mock.calls[0][0];
    expect(payload.assistant?.conversation_overrides?.mcp_ids).toEqual(['mcp-user', 'builtin-mcp']);
    expect(payload.extra.selected_mcp_server_ids).toEqual(['mcp-user']);
    expect(payload.extra.selected_session_mcp_servers).toEqual([expect.objectContaining({ id: 'builtin-mcp' })]);
  });

  it('does not write legacy preset_assistant_id for preset assistant sends', async () => {
    const deps = createDeps();

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    const payload = createConversationInvokeMock.mock.calls[0][0];
    expect(payload.assistant?.id).toBe('synonbiomed:aidd-expert');
    expect(payload.extra.preset_assistant_id).toBeUndefined();
  });

  it('forwards local skill overrides through assistant conversation overrides for Synon Biomed assistants', async () => {
    const deps = createDeps();
    deps.guidEnabledSkills = ['pdf-reader'];
    deps.guidDisabledBuiltinSkills = ['todo-tracker'];

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    const payload = createConversationInvokeMock.mock.calls[0][0];
    expect(payload.assistant?.id).toBe('synonbiomed:aidd-expert');
    expect(payload.assistant?.conversation_overrides?.skill_ids).toEqual(['pdf-reader']);
    expect(payload.assistant?.conversation_overrides?.disabled_builtin_skill_ids).toEqual(['todo-tracker']);
  });

  it('creates Synon Biomed project conversations with backend project identity instead of a custom folder', async () => {
    const deps = createDeps();
    deps.dir = 'synonbiomed://project/proj_stat6?name=STAT6&artifacts=23&conversations=2';

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    const payload = createConversationInvokeMock.mock.calls[0][0];
    expect(payload.extra.workspace).toBe('synonbiomed://project/proj_stat6?name=STAT6&artifacts=23&conversations=2');
    expect(payload.extra.custom_workspace).toBe(false);
    expect(payload.extra.backend).toBe('synonbiomed');
    expect(payload.extra.project_id).toBe('proj_stat6');
    expect(payload.extra.project_name).toBe('STAT6');
    expect(payload.extra.artifact_count).toBe(23);
    expect(payload.extra.conversation_count).toBe(2);
  });

  it('rejects legacy non-Synon assistants instead of creating conversations', async () => {
    const deps = createDeps();
    deps.selectedAssistantId = 'bare:legacy-cli';
    deps.selectedAssistantBackend = 'legacy-cli';
    deps.guidEnabledSkills = ['pdf-reader'];
    deps.guidDisabledBuiltinSkills = ['todo-tracker'];

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    expect(createConversationInvokeMock).not.toHaveBeenCalled();
  });

  it('does not create generated non-Synon assistant conversations', async () => {
    const deps = createDeps();
    deps.selectedAssistantId = 'bare:claude';
    deps.selectedAssistantBackend = 'claude';

    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      await result.current.handleSend();
    });

    expect(createConversationInvokeMock).not.toHaveBeenCalled();
  });

  it('does not create a conversation without assistant identity', async () => {
    const deps = createDeps();
    deps.selectedAssistantId = null;
    deps.selectedAssistantBackend = 'synonbiomed';

    const { result } = renderHook(() => useGuidSend(deps));

    expect(result.current.isButtonDisabled).toBe(true);

    await act(async () => {
      await result.current.handleSend();
    });

    expect(createConversationInvokeMock).not.toHaveBeenCalled();
  });

  it('preserves the draft when conversation creation returns no identity', async () => {
    createConversationInvokeMock.mockResolvedValueOnce(null);
    const deps = createDeps();
    const { result } = renderHook(() => useGuidSend(deps));

    await act(async () => {
      expect(await result.current.sendMessageHandler()).toBe(false);
    });

    await vi.waitFor(() => expect(createConversationInvokeMock).toHaveBeenCalledTimes(1));
    await vi.waitFor(() => expect(deps.setLoading).toHaveBeenLastCalledWith(false));
    expect(deps.setInput).not.toHaveBeenCalled();
    expect(deps.setFiles).not.toHaveBeenCalled();
    expect(deps.setContextItems).not.toHaveBeenCalled();
    expect(deps.setDir).not.toHaveBeenCalled();
  });

  it('reuses completed uploads and resumes an uncertain upload when send is retried', async () => {
    const first = new File(['first'], 'first.cdx', { type: 'application/octet-stream' });
    const second = new File(['second'], 'second.researchdata', { type: '' });
    let secondAttempt = 0;
    uploadSynonBiomedProjectAttachmentMock.mockImplementation(
      async (
        projectId: string,
        file: File,
        options: {
          pending?: { projectId: string; uploadId: string };
          onInitialized?: (pending: { projectId: string; uploadId: string }) => void;
        }
      ) => {
        if (file === first) {
          return {
            artifactId: 'artifact-first',
            versionId: 'version-first',
            filename: first.name,
            sizeBytes: first.size,
            checksum: 'a'.repeat(64),
          };
        }
        secondAttempt += 1;
        if (secondAttempt === 1) {
          options.onInitialized?.({ projectId, uploadId: 'upload-second' });
          throw new TypeError('Failed to fetch');
        }
        expect(options.pending).toEqual({ projectId, uploadId: 'upload-second' });
        return {
          artifactId: 'artifact-second',
          versionId: 'version-second',
          filename: second.name,
          sizeBytes: second.size,
          checksum: 'b'.repeat(64),
        };
      }
    );
    const deps = createDeps();
    deps.dir = 'synonbiomed://project/proj-1?name=Retry';
    deps.localFiles = [
      { id: 'local-first', file: first },
      { id: 'local-second', file: second },
    ];

    const { result } = renderHook(() => useGuidSend(deps));
    await act(async () => {
      await expect(result.current.handleSend()).rejects.toThrow('Failed to fetch');
    });
    await act(async () => {
      await expect(result.current.handleSend()).resolves.toBe(true);
    });

    expect(uploadSynonBiomedProjectAttachmentMock.mock.calls.filter(([, file]) => file === first)).toHaveLength(1);
    expect(uploadSynonBiomedProjectAttachmentMock.mock.calls.filter(([, file]) => file === second)).toHaveLength(2);
    expect(createConversationInvokeMock).toHaveBeenCalledTimes(1);
    expect(JSON.parse(sessionStorage.getItem('acp_initial_message_conv-1') || '{}').artifact_refs).toEqual([
      expect.objectContaining({ artifact_id: 'artifact-first', version_id: 'version-first' }),
      expect.objectContaining({ artifact_id: 'artifact-second', version_id: 'version-second' }),
    ]);
  });
});
