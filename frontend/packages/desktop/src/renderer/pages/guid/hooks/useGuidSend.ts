/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import type { IMcpServer } from '@/common/config/storage';
import { toSessionMcpServer } from '@/renderer/hooks/mcp/catalog';
import { emitter } from '@/renderer/utils/emitter';
import { updateWorkspaceTime } from '@/renderer/utils/workspace/workspaceHistory';
import { Message } from '@arco-design/web-react';
import { useCallback, useRef } from 'react';
import { type TFunction } from 'i18next';
import type { NavigateFunction } from 'react-router';
import { useSWRConfig } from 'swr';
import { prefetchConversationRoute } from '@/renderer/pages/conversation/conversationRoute';
import { rememberConversationRouteSnapshot } from '@/renderer/pages/conversation/utils/conversationCache';
import { getConversationCreateErrorMessage } from '@/renderer/pages/conversation/utils/conversationCreateError';
import type { AcpModelInfo } from '../types';
import {
  toSynonBiomedMessageSessionOptions,
  type SynonBiomedSessionOptions,
} from '@/renderer/services/synonBiomedSessionOptions';
import type { SynonBiomedSessionDefaults } from '@/renderer/services/synonBiomedSessionDefaults';
import { buildSynonBiomedConversationTitle } from '@/renderer/services/synonBiomedConversationTitle';
import { loadSynonBiomedProjects } from '@/renderer/services/synonBiomedGateway';
import {
  toOnboardingAttachedArtifactReference,
  uploadSynonBiomedProjectAttachment,
  type OnboardingArtifactCacheEntry,
  type OnboardingPendingUpload,
  type OnboardingUploadedArtifact,
} from '@/renderer/services/onboardingService';
import type { GuidLocalFile } from './useGuidInput';
import {
  buildComposerCapabilityPayload,
  normalizeComposerContextItems,
  type ComposerContextItem,
} from '@/renderer/components/chat/SendBox/composerCompositionModel';

export type GuidSendDeps = {
  // Input state
  input: string;
  setInput: React.Dispatch<React.SetStateAction<string>>;
  files: string[];
  setFiles: React.Dispatch<React.SetStateAction<string[]>>;
  localFiles: GuidLocalFile[];
  setLocalFiles: React.Dispatch<React.SetStateAction<GuidLocalFile[]>>;
  contextItems: ComposerContextItem[];
  setContextItems: React.Dispatch<React.SetStateAction<ComposerContextItem[]>>;
  dir: string;
  setDir: React.Dispatch<React.SetStateAction<string>>;
  setLoading: React.Dispatch<React.SetStateAction<boolean>>;
  loading: boolean;

  // Assistant state
  selectedAssistantId: string | null;
  selectedAssistantBackend: string;
  selectedMode: string;
  selectedAcpModel: string | null;
  selectedThoughtLevelValue?: string;
  currentAcpCachedModelInfo: AcpModelInfo | null;

  guidDisabledBuiltinSkills: string[] | undefined;
  guidEnabledSkills: string[] | undefined;
  assistantDefaultSkillIds?: string[];
  assistantDefaultDisabledBuiltinSkillIds?: string[];
  assistantDefaultSkillMode?: 'auto' | 'fixed';
  availableMcpServers: IMcpServer[];
  selectedMcpServerIds: string[] | undefined;
  assistantDefaultMcpIds?: string[];
  sessionOptions: SynonBiomedSessionOptions;
  sessionDefaults: SynonBiomedSessionDefaults;
  sessionComputeProviders: string[];
  planModeRef?: { current: boolean };

  // Mention state reset
  setMentionOpen: React.Dispatch<React.SetStateAction<boolean>>;
  setMentionQuery: React.Dispatch<React.SetStateAction<string | null>>;
  setMentionSelectorOpen: React.Dispatch<React.SetStateAction<boolean>>;
  setMentionActiveIndex: React.Dispatch<React.SetStateAction<number>>;

  // Navigation
  ownerId: string;
  navigate: NavigateFunction;
  t: TFunction;
  localeKey: string;
};

export type GuidSendResult = {
  handleSend: () => Promise<boolean>;
  sendMessageHandler: () => Promise<boolean>;
  isButtonDisabled: boolean;
};

type SynonBiomedProjectWorkspace = {
  workspace: string;
  projectId: string;
  projectName?: string;
  artifactCount?: number;
  conversationCount?: number;
};

const parseNonNegativeCount = (value: string | null): number | undefined => {
  if (!value) return undefined;
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : undefined;
};

export function parseSynonBiomedProjectWorkspace(workspace: string): SynonBiomedProjectWorkspace | null {
  if (!workspace.startsWith('synonbiomed://project/')) {
    return null;
  }

  try {
    const url = new URL(workspace);
    if (url.protocol !== 'synonbiomed:' || url.hostname !== 'project') {
      return null;
    }
    const projectId = decodeURIComponent(url.pathname.replace(/^\/+/, ''));
    if (!projectId) {
      return null;
    }
    const projectName = url.searchParams.get('name') || undefined;
    return {
      workspace,
      projectId,
      projectName,
      artifactCount: parseNonNegativeCount(url.searchParams.get('artifacts')),
      conversationCount: parseNonNegativeCount(url.searchParams.get('conversations')),
    };
  } catch {
    return null;
  }
}

function projectWorkspaceFromComposerArtifacts(
  contextItems: readonly ComposerContextItem[]
): SynonBiomedProjectWorkspace | null {
  const projectIds = new Set(
    normalizeComposerContextItems(contextItems)
      .filter((item) => item.kind === 'artifact' && item.projectId)
      .map((item) => (item.kind === 'artifact' ? item.projectId! : ''))
  );
  if (projectIds.size > 1) {
    throw new Error('Selected project artifacts must belong to one Synon Biomed project');
  }
  const projectId = projectIds.values().next().value as string | undefined;
  return projectId
    ? {
        workspace: `synonbiomed://project/${encodeURIComponent(projectId)}`,
        projectId,
      }
    : null;
}

function resolveGuidProjectAuthority(
  selectedProject: SynonBiomedProjectWorkspace | null,
  artifactProject: SynonBiomedProjectWorkspace | null,
  hasCustomWorkspace: boolean
): SynonBiomedProjectWorkspace | null {
  if (!artifactProject) return selectedProject;
  if (hasCustomWorkspace) {
    throw new Error('Project artifacts cannot be combined with a custom local workspace');
  }
  if (selectedProject && selectedProject.projectId !== artifactProject.projectId) {
    throw new Error('Selected project artifacts conflict with the active Synon Biomed project');
  }
  return selectedProject ?? artifactProject;
}

async function resolveGuidAttachmentProject(
  selectedProject: SynonBiomedProjectWorkspace | null,
  hasCustomWorkspace: boolean
): Promise<SynonBiomedProjectWorkspace> {
  if (selectedProject) return selectedProject;
  if (hasCustomWorkspace) {
    throw new Error('Local attachments require a Synon Biomed project workspace');
  }

  const projects = await loadSynonBiomedProjects();
  const personalWorkspace = projects.find((project) => project.name.trim().toLowerCase() === 'personal workspace');
  const fallbackProject = personalWorkspace ?? projects[0];
  if (!fallbackProject) {
    throw new Error('No Synon Biomed project is available for local attachments');
  }

  return {
    workspace: `synonbiomed://project/${encodeURIComponent(fallbackProject.projectId)}?name=${encodeURIComponent(fallbackProject.name)}`,
    projectId: fallbackProject.projectId,
    projectName: fallbackProject.name,
    artifactCount: fallbackProject.artifactCount,
    conversationCount: fallbackProject.conversationCount,
  };
}

/**
 * Hook that manages the send logic for Synon Biomed assistant conversations.
 */
function normalizeSynonBiomedEffort(value?: string): SynonBiomedSessionDefaults['effort'] {
  return value === 'low' || value === 'medium' || value === 'high' ? value : undefined;
}

export const useGuidSend = (deps: GuidSendDeps): GuidSendResult => {
  const { mutate: swrMutate } = useSWRConfig();
  const {
    input,
    setInput,
    files,
    setFiles,
    localFiles,
    setLocalFiles,
    contextItems,
    setContextItems,
    dir,
    setDir,
    setLoading,
    loading,
    selectedAssistantId,
    selectedAssistantBackend,
    selectedMode,
    selectedAcpModel,
    selectedThoughtLevelValue,
    currentAcpCachedModelInfo,
    guidDisabledBuiltinSkills,
    guidEnabledSkills,
    assistantDefaultSkillIds,
    assistantDefaultDisabledBuiltinSkillIds,
    assistantDefaultSkillMode,
    availableMcpServers,
    selectedMcpServerIds,
    assistantDefaultMcpIds,
    sessionOptions,
    sessionDefaults,
    sessionComputeProviders,
    planModeRef,
    setMentionOpen,
    setMentionQuery,
    setMentionSelectorOpen,
    setMentionActiveIndex,
    ownerId,
    navigate,
    t,
    localeKey,
  } = deps;
  const sendingRef = useRef(false);
  const uploadedAttachmentCacheRef = useRef(new WeakMap<File, OnboardingArtifactCacheEntry>());
  const pendingAttachmentUploadsRef = useRef(new WeakMap<File, OnboardingPendingUpload>());

  const handleSend = useCallback(async () => {
    if (!selectedAssistantId) {
      Message.warning(t('conversation.welcome.assistantUnavailable'));
      return false;
    }

    if (selectedAssistantBackend !== 'synonbiomed') {
      Message.error(t('conversation.welcome.synonBiomedOnly'));
      return false;
    }

    const messageInput = input.trim() || t('conversation.sendbox.contextOnlyPrompt');
    const capabilityPayload = buildComposerCapabilityPayload(contextItems);

    const synonBiomedProjectWorkspace = parseSynonBiomedProjectWorkspace(dir);
    const isCustomWorkspace = !!dir && !synonBiomedProjectWorkspace;
    const artifactProjectWorkspace = projectWorkspaceFromComposerArtifacts(contextItems);
    const selectedProjectWorkspace = resolveGuidProjectAuthority(
      synonBiomedProjectWorkspace,
      artifactProjectWorkspace,
      isCustomWorkspace
    );
    const attachmentProjectWorkspace =
      localFiles.length > 0 ? await resolveGuidAttachmentProject(selectedProjectWorkspace, isCustomWorkspace) : null;
    const conversationProjectWorkspace = attachmentProjectWorkspace ?? selectedProjectWorkspace;
    const finalWorkspace = conversationProjectWorkspace?.workspace ?? (dir || '');

    const assistantConversationId = selectedAssistantId;
    const conversationRouteReady = prefetchConversationRoute().catch((error: unknown) => {
      console.warn('[conversation-prefetch] New conversation route unavailable:', error);
    });
    const enabledSkillsToSend =
      guidEnabledSkills !== undefined
        ? guidEnabledSkills
        : assistantDefaultSkillMode === 'fixed'
          ? assistantDefaultSkillIds
          : undefined;
    const excludeBuiltinSkills =
      guidDisabledBuiltinSkills !== undefined
        ? guidDisabledBuiltinSkills
        : assistantDefaultSkillMode === 'fixed'
          ? assistantDefaultDisabledBuiltinSkillIds
          : undefined;
    const selectedAllMcpServerIds = selectedMcpServerIds ?? [];
    const selectedMcpServerIdSet = new Set(selectedAllMcpServerIds);
    const selectedUserMcpServerIds = availableMcpServers
      .filter((server) => selectedMcpServerIdSet.has(server.id) && server.builtin !== true)
      .map((server) => server.id);
    const selectedAllSessionMcpServers = availableMcpServers
      .filter((server) => selectedMcpServerIdSet.has(server.id))
      .map((server) => toSessionMcpServer(server));
    const selectedSessionMcpServers = availableMcpServers
      .filter((server) => selectedMcpServerIdSet.has(server.id) && server.builtin === true)
      .map((server) => toSessionMcpServer(server));
    const defaultSelectedMcpServerIds = assistantDefaultMcpIds;
    const defaultSelectedUserMcpServerIds = availableMcpServers
      .filter((server) => (defaultSelectedMcpServerIds ?? []).includes(server.id) && server.builtin !== true)
      .map((server) => server.id);
    const assistantOverrideMcpIds =
      selectedMcpServerIds !== undefined ? selectedAllMcpServerIds : defaultSelectedMcpServerIds;
    const selectedUserMcpServerIdsToSend =
      selectedMcpServerIds !== undefined ? selectedUserMcpServerIds : defaultSelectedUserMcpServerIds;
    const selectedSessionMcpServersToSend =
      selectedMcpServerIds !== undefined
        ? selectedAllSessionMcpServers
        : availableMcpServers
            .filter((server) => (defaultSelectedMcpServerIds ?? []).includes(server.id))
            .map((server) => toSessionMcpServer(server));

    const assistantOverrideModel = selectedAcpModel || currentAcpCachedModelInfo?.current_model_id || undefined;
    const assistantOverrides = {
      model: assistantOverrideModel,
      permission: selectedMode || undefined,
      thought_level: selectedThoughtLevelValue || undefined,
      ...(selectedMcpServerIds !== undefined || assistantDefaultMcpIds !== undefined
        ? { mcp_ids: assistantOverrideMcpIds ?? [] }
        : {}),
      ...(enabledSkillsToSend !== undefined ? { skill_ids: enabledSkillsToSend } : {}),
      ...(excludeBuiltinSkills !== undefined ? { disabled_builtin_skill_ids: excludeBuiltinSkills } : {}),
    };

    try {
      const uploadedArtifacts: OnboardingUploadedArtifact[] = [];
      for (const localFile of localFiles) {
        const projectId = attachmentProjectWorkspace!.projectId;
        const cached = uploadedAttachmentCacheRef.current.get(localFile.file);
        if (cached && cached.projectId !== projectId) {
          uploadedAttachmentCacheRef.current.delete(localFile.file);
        }
        const pending = pendingAttachmentUploadsRef.current.get(localFile.file);
        if (pending && pending.projectId !== projectId) {
          pendingAttachmentUploadsRef.current.delete(localFile.file);
        }
        let artifact = cached?.projectId === projectId ? cached.artifact : null;
        // Keep uploads ordered so the first message has deterministic artifact
        // references and only one bounded upload is active at a time.
        if (!artifact) {
          // eslint-disable-next-line no-await-in-loop
          artifact = await uploadSynonBiomedProjectAttachment(projectId, localFile.file, {
            pending: pending?.projectId === projectId ? pending : undefined,
            onInitialized: (next) => pendingAttachmentUploadsRef.current.set(localFile.file, next),
            onAbandoned: () => pendingAttachmentUploadsRef.current.delete(localFile.file),
          });
          uploadedAttachmentCacheRef.current.set(localFile.file, { projectId, artifact });
          pendingAttachmentUploadsRef.current.delete(localFile.file);
        }
        uploadedArtifacts.push(artifact);
      }

      const conversation = await ipcBridge.conversation.create.invoke({
        name: buildSynonBiomedConversationTitle(messageInput),
        assistant: {
          id: assistantConversationId,
          locale: localeKey,
          conversation_overrides: assistantOverrides,
        },
        extra: {
          workspace: finalWorkspace,
          ...(conversationProjectWorkspace
            ? {
                backend: 'synonbiomed',
                project_id: conversationProjectWorkspace.projectId,
                ...(conversationProjectWorkspace.projectName
                  ? { project_name: conversationProjectWorkspace.projectName }
                  : {}),
                ...(conversationProjectWorkspace.artifactCount !== undefined
                  ? { artifact_count: conversationProjectWorkspace.artifactCount }
                  : {}),
                ...(conversationProjectWorkspace.conversationCount !== undefined
                  ? { conversation_count: conversationProjectWorkspace.conversationCount }
                  : {}),
              }
            : {}),
          custom_workspace: isCustomWorkspace,
          default_files: files,
          ...(selectedMcpServerIds !== undefined || assistantDefaultMcpIds !== undefined
            ? {
                selected_mcp_server_ids: selectedUserMcpServerIdsToSend,
                selected_session_mcp_servers:
                  selectedMcpServerIds !== undefined ? selectedSessionMcpServers : selectedSessionMcpServersToSend,
              }
            : {}),
        },
      });
      if (!conversation || !conversation.id) {
        Message.error(t('conversation.createFailed'));
        return false;
      }

      if (isCustomWorkspace) {
        updateWorkspaceTime(finalWorkspace);
      }

      const effort = normalizeSynonBiomedEffort(selectedThoughtLevelValue) ?? sessionDefaults.effort;
      const combinedArtifactReferences = [
        ...capabilityPayload.artifactRefs,
        ...uploadedArtifacts.map(toOnboardingAttachedArtifactReference),
      ].filter(
        (reference, index, all) =>
          all.findIndex(
            (candidate) =>
              candidate.artifact_id === reference.artifact_id && candidate.version_id === reference.version_id
          ) === index
      );
      const initialMessage = {
        input: messageInput,
        files: files.length > 0 ? files : undefined,
        artifact_refs: combinedArtifactReferences.length > 0 ? combinedArtifactReferences : undefined,
        inject_skills: capabilityPayload.injectSkills.length > 0 ? capabilityPayload.injectSkills : undefined,
        inject_mcp_server_ids:
          capabilityPayload.injectMcpServerIds.length > 0 ? capabilityPayload.injectMcpServerIds : undefined,
        session_options: {
          ...toSynonBiomedMessageSessionOptions(sessionOptions, planModeRef?.current ? { planMode: true } : {}),
          model: assistantOverrideModel,
          subagent_model: sessionDefaults.subagentModelId,
          effort,
        },
        compute_providers: sessionComputeProviders,
      };
      sessionStorage.setItem(`acp_initial_message_${conversation.id}`, JSON.stringify(initialMessage));

      // The create response is the authoritative first conversation snapshot.
      // Hand it to the route before navigation so the new chat paints directly
      // instead of showing a second welcome/loading surface while re-fetching it.
      rememberConversationRouteSnapshot(ownerId, conversation, 'detail');
      await conversationRouteReady;
      await navigate(`/conversation/${conversation.id}`);

      // The new conversation is already authoritative once create returns.
      // Cache revalidation and sidebar refresh must not sit on the critical
      // click -> conversation paint path; run them after navigation instead.
      if (assistantConversationId) {
        void Promise.allSettled([
          swrMutate(`guid.assistant.detail.${assistantConversationId}.${localeKey}`),
          swrMutate('assistants.list'),
        ]);
      }
      emitter.emit('chat.history.refresh');
      if (conversationProjectWorkspace) emitter.emit('synonbiomed.projects.refresh');
      uploadedAttachmentCacheRef.current = new WeakMap<File, OnboardingArtifactCacheEntry>();
      pendingAttachmentUploadsRef.current = new WeakMap<File, OnboardingPendingUpload>();
      return true;
    } catch (error: unknown) {
      console.error('Failed to create ACP conversation:', error);
      throw error;
    }
  }, [
    input,
    files,
    localFiles,
    contextItems,
    dir,
    selectedAssistantId,
    selectedAssistantBackend,
    selectedMode,
    selectedAcpModel,
    selectedThoughtLevelValue,
    currentAcpCachedModelInfo,
    guidDisabledBuiltinSkills,
    guidEnabledSkills,
    assistantDefaultSkillIds,
    assistantDefaultDisabledBuiltinSkillIds,
    assistantDefaultSkillMode,
    availableMcpServers,
    selectedMcpServerIds,
    assistantDefaultMcpIds,
    sessionOptions,
    sessionDefaults,
    sessionComputeProviders,
    planModeRef,
    ownerId,
    navigate,
    t,
    localeKey,
  ]);

  const sendMessageHandler = useCallback(async (): Promise<boolean> => {
    if (loading || sendingRef.current) return false;
    sendingRef.current = true;
    setLoading(true);
    try {
      const sent = await handleSend();
      if (!sent) return false;
      setInput('');
      setMentionOpen(false);
      setMentionQuery(null);
      setMentionSelectorOpen(false);
      setMentionActiveIndex(0);
      setFiles([]);
      setLocalFiles([]);
      setContextItems([]);
      setDir('');
      return true;
    } catch (error) {
      console.error('Failed to send message:', error);
      Message.error(getConversationCreateErrorMessage(error, t));
      return false;
    } finally {
      sendingRef.current = false;
      setLoading(false);
    }
  }, [
    loading,
    handleSend,
    setLoading,
    setInput,
    setMentionOpen,
    setMentionQuery,
    setMentionSelectorOpen,
    setMentionActiveIndex,
    setFiles,
    setLocalFiles,
    setContextItems,
    setDir,
    t,
  ]);

  // Calculate button disabled state
  const hasMessageInput = Boolean(input.trim() || files.length > 0 || localFiles.length > 0 || contextItems.length > 0);
  const isButtonDisabled = loading || !hasMessageInput || !selectedAssistantId;

  return {
    handleSend,
    sendMessageHandler,
    isButtonDisabled,
  };
};
