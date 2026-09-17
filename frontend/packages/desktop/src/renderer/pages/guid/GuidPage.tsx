/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { buildGuidSlashCommands } from '@/common/chat/slash/guidSlashCommands';
import type { SlashCommandItem } from '@/common/chat/slash/types';
import type { IConversationMcpStatus, IMcpServer } from '@/common/config/storage';
import { resolveLocaleKey } from '@/common/utils';
import type { AssistantDetail } from '@/common/types/agent/assistantTypes';
import type { AgentModeOption } from '@/renderer/utils/synonBiomed/runtime/runtimeTypes';

import { useInputFocusRing } from '@/renderer/hooks/chat/useInputFocusRing';
import { useAuth } from '@/renderer/hooks/context/AuthContext';
import { getFuzzyMatchIndices, useSlashCommandController } from '@/renderer/hooks/chat/useSlashCommandController';
import SlashCommandMenu, { type SlashCommandMenuItem } from '@/renderer/components/chat/SlashCommandMenu';
import GuidActionRow from './components/GuidActionRow';
import GuidProjectFilesModal from './components/GuidProjectFilesModal';
import GuidInputCard from './components/GuidInputCard';
import SynonBiomedDraftModelSelector from '@/renderer/components/synonBiomed/runtime/SynonBiomedDraftModelSelector';
import { useGuidAssistantSelection } from './hooks/useGuidAssistantSelection';
import { useGuidInput } from './hooks/useGuidInput';
import { parseSynonBiomedProjectWorkspace, useGuidSend } from './hooks/useGuidSend';
import { useTypewriterPlaceholder } from './hooks/useTypewriterPlaceholder';
import { ensureBackendMcpCatalog } from '@/renderer/hooks/mcp/catalog';
import { resolveGuidAssistantDefaults } from './utils/assistantDefaults';
import SpeechInputButton from '@/renderer/components/chat/SpeechInputButton';
import SynonBiomedSessionOptionsMenu from '@/renderer/components/synonBiomed/runtime/SynonBiomedSessionOptionsMenu';
import SynonBiomedPermissionMenu from '@/renderer/components/synonBiomed/runtime/SynonBiomedPermissionMenu';
import SynonBiomedSendOptionsMenu, {
  type SynonBiomedSendIntent,
} from '@/renderer/components/synonBiomed/runtime/SynonBiomedSendOptionsMenu';
import { useOpenFileSelector } from '@/renderer/hooks/file/useOpenFileSelector';
import { loadAvailableSkillsWithSynonBiomed } from '@/renderer/services/skills/skillsCatalog';
import {
  loadSynonBiomedAssistantComposerCapabilities,
  type SynonBiomedComposerCapabilities,
  type SynonBiomedProjectArtifact,
} from '@/renderer/services/synonBiomedGateway';
import {
  addComposerContextItem,
  composerContextItemKey,
  removeComposerContextItem,
  type ComposerArtifactContext,
  type ComposerContextItem,
} from '@/renderer/components/chat/SendBox/composerCompositionModel';
import { appendSpeechTranscript } from '@/renderer/hooks/system/useSpeechInput';
import { useGuidSessionOptions } from './hooks/useGuidSessionOptions';
import { useLiveTranscriptInsertion } from '@/renderer/hooks/system/useLiveTranscriptInsertion';
import { ConfigProvider } from '@arco-design/web-react';
import type { AcpDerivedOption } from '@/renderer/hooks/synonBiomed/runtime/useAcpConfigOptions';
import { buildProjectArtifactContext } from './utils/guidProjectFilesModel';
import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { useLocation, useNavigate } from 'react-router';
import useSWR from 'swr';
import styles from './index.module.css';

const DEFAULT_GUID_PERMISSION_OPTIONS: AgentModeOption[] = [
  { value: 'default', label: 'Default' },
  { value: 'smart', label: 'Smart' },
  { value: 'bypassPermissions', label: 'Bypass Permissions' },
];

const GuidPage: React.FC = () => {
  const { t, i18n } = useTranslation();
  const navigate = useNavigate();
  const location = useLocation();
  const { user } = useAuth();
  const guidContainerRef = useRef<HTMLDivElement>(null);
  const guidPlanModeRef = useRef(false);
  const [composerDocked, setComposerDocked] = useState(false);
  const { activeBorderColor, inactiveBorderColor, activeShadow, inactiveShadow, surfaceBackgroundColor } =
    useInputFocusRing();

  const localeKey = resolveLocaleKey(i18n.language);

  // --- Skills state ---
  // Skill metadata comes from the database-backed catalog. Built-in auto-inject
  // skills default checked; the rest are opt-in per conversation or pre-checked
  // by assistant defaults.
  const [allSkills, setAllSkills] = useState<Array<{ name: string; description: string; isAuto: boolean }>>([]);
  const [guidDisabledBuiltinSkills, setGuidDisabledBuiltinSkills] = useState<string[] | undefined>(undefined);
  const [guidEnabledSkills, setGuidEnabledSkills] = useState<string[] | undefined>(undefined);
  const [availableMcpServers, setAvailableMcpServers] = useState<IMcpServer[]>([]);
  const [guidSelectedMcpServerIds, setGuidSelectedMcpServerIds] = useState<string[] | undefined>(undefined);
  useEffect(() => {
    loadAvailableSkillsWithSynonBiomed()
      .then((availableSkills) => {
        setAllSkills(
          availableSkills.map((s) => ({
            name: s.name,
            description: s.description,
            isAuto: s.source === 'builtin' && s.is_auto_inject === true,
          }))
        );
      })
      .catch(() => setAllSkills([]));
  }, []);

  useEffect(() => {
    void ensureBackendMcpCatalog()
      .then(({ allServers }) => {
        setAvailableMcpServers(allServers);
      })
      .catch((error) => {
        console.error('[GuidPage] Failed to load MCP catalog:', error);
        setAvailableMcpServers([]);
      });
  }, []);

  // --- Hooks ---
  const navState = location.state as {
    resetAssistant?: boolean;
    selectedAssistantId?: string;
    selectLatestProject?: boolean;
  } | null;
  const resetAssistantRequested = navState?.resetAssistant === true;
  const preselectAssistantId = navState?.selectedAssistantId;
  const selectLatestProjectRequested = navState?.selectLatestProject === true;
  const agentSelection = useGuidAssistantSelection({
    resetAssistant: resetAssistantRequested,
    preselectAssistantId,
    locationKey: location.key,
  });

  const guidInput = useGuidInput({
    locationState: location.state as { workspace?: string } | null,
  });
  const appendSelectedFiles = useCallback(
    (files: string[]) => {
      guidInput.setFiles((prevFiles) => [...prevFiles, ...files]);
    },
    [guidInput.setFiles]
  );
  const { openFileSelector, onSlashBuiltinCommand } = useOpenFileSelector({
    onFilesSelected: appendSelectedFiles,
  });
  const projectWorkspace = useMemo(() => parseSynonBiomedProjectWorkspace(guidInput.dir), [guidInput.dir]);
  const [projectFilesOpen, setProjectFilesOpen] = useState(false);
  const openProjectFiles = useCallback(() => setProjectFilesOpen(true), []);
  const handleProjectArtifactsSelected = useCallback(
    (selectedArtifacts: SynonBiomedProjectArtifact[]) => {
      const contexts = selectedArtifacts
        .map(buildProjectArtifactContext)
        .filter((item): item is ComposerArtifactContext => item !== null);
      if (contexts.length === 0) return;
      guidInput.setContextItems((current) =>
        contexts.reduce((next, item) => addComposerContextItem(next, item), current)
      );
      setProjectFilesOpen(false);
    },
    [guidInput.setContextItems]
  );

  const resetMentionOpen = useCallback<React.Dispatch<React.SetStateAction<boolean>>>(() => {}, []);
  const resetMentionQuery = useCallback<React.Dispatch<React.SetStateAction<string | null>>>(() => {}, []);
  const resetMentionActiveIndex = useCallback<React.Dispatch<React.SetStateAction<number>>>(() => {}, []);

  const selectedAssistantId = agentSelection.selectedAssistantId;
  const hasSelectedAssistant = selectedAssistantId !== null;
  const selectedAssistantAgentId = agentSelection.selectedAssistant?.agent_id ?? 'OPERON';
  const { data: guidComposerCapabilities } = useSWR<SynonBiomedComposerCapabilities>(
    selectedAssistantId ? `guid.assistant.composer-capabilities.${selectedAssistantId}` : null,
    () => loadSynonBiomedAssistantComposerCapabilities(selectedAssistantId!)
  );
  const { sessionOptions, setSessionOptions, sessionComputeProviders, setSessionComputeProviders } =
    useGuidSessionOptions({
      baseAgentName: selectedAssistantAgentId,
      navigationKey: location.key,
      resetRequested: resetAssistantRequested,
    });
  const { data: selectedAssistantDetail } = useSWR(
    selectedAssistantId ? `guid.assistant.detail.${selectedAssistantId}.${localeKey}` : null,
    async (): Promise<AssistantDetail | null> =>
      ipcBridge.assistants.get
        .invoke({ id: selectedAssistantId!, locale: localeKey })
        .catch((_error: unknown): AssistantDetail | null => null)
  );
  const resolvedAssistantDefaults = useMemo(
    () => resolveGuidAssistantDefaults(selectedAssistantDetail),
    [selectedAssistantDetail]
  );

  const selectedSkillNames = useMemo(() => {
    const disabledBuiltinSkillSet = new Set(
      guidDisabledBuiltinSkills ?? resolvedAssistantDefaults.disabledBuiltinSkillIds
    );
    const enabledSkillSet = new Set(guidEnabledSkills ?? resolvedAssistantDefaults.skillIds);

    return allSkills
      .filter((skill) => (skill.isAuto ? !disabledBuiltinSkillSet.has(skill.name) : enabledSkillSet.has(skill.name)))
      .map((skill) => skill.name);
  }, [
    allSkills,
    guidDisabledBuiltinSkills,
    guidEnabledSkills,
    resolvedAssistantDefaults.disabledBuiltinSkillIds,
    resolvedAssistantDefaults.skillIds,
  ]);
  const skillDescriptionByName = useMemo(
    () => new Map(allSkills.map((skill) => [skill.name, skill.description])),
    [allSkills]
  );
  const selectedComposerSkillNames = useMemo(
    () => guidInput.contextItems.filter((item) => item.kind === 'skill').map((item) => item.name),
    [guidInput.contextItems]
  );
  const selectedComposerMcpServerIds = useMemo(
    () => guidInput.contextItems.filter((item) => item.kind === 'mcp').map((item) => item.serverId),
    [guidInput.contextItems]
  );
  const toggleComposerContextItem = useCallback(
    (item: ComposerContextItem) => {
      const key = composerContextItemKey(item);
      guidInput.setContextItems((current) =>
        current.some((candidate) => composerContextItemKey(candidate) === key)
          ? removeComposerContextItem(current, key)
          : addComposerContextItem(current, item)
      );
    },
    [guidInput.setContextItems]
  );
  const handleComposerSkillSelected = useCallback(
    (name: string) => toggleComposerContextItem({ kind: 'skill', name, label: name }),
    [toggleComposerContextItem]
  );
  const handleComposerMcpSelected = useCallback(
    (server: IConversationMcpStatus) =>
      toggleComposerContextItem({ kind: 'mcp', serverId: server.id, label: server.name }),
    [toggleComposerContextItem]
  );
  const guidBuiltinSlashCommands = useMemo<SlashCommandItem[]>(
    () => [
      {
        name: 'open',
        description: t('conversation.workspace.addFile'),
        kind: 'builtin',
        source: 'builtin',
      },
    ],
    [t]
  );
  const guidSlashCommands = useMemo(
    () =>
      buildGuidSlashCommands({
        builtinCommands: guidBuiltinSlashCommands,
        agentCommands: agentSelection.currentAgentAvailableCommands,
        selectedSkills: selectedSkillNames,
        descriptionByName: skillDescriptionByName,
        skillFallbackDescription: t('conversation.skills.slashHint'),
      }),
    [
      agentSelection.currentAgentAvailableCommands,
      guidBuiltinSlashCommands,
      selectedSkillNames,
      skillDescriptionByName,
      t,
    ]
  );
  const slashController = useSlashCommandController({
    input: guidInput.input,
    commands: guidSlashCommands,
    onExecuteBuiltin: (name) => {
      onSlashBuiltinCommand(name);
      guidInput.setInput('');
    },
    onSelectTemplate: (command) => {
      guidInput.setInput(`/${command.name} `);
    },
  });
  const slashMenuItems = useMemo<SlashCommandMenuItem[]>(
    () =>
      slashController.filteredCommands.map((command) => ({
        key: command.name,
        label: `/${command.name}`,
        description: command.description,
        badge: command.hint,
        highlightIndices: slashController.query
          ? getFuzzyMatchIndices(command.name, slashController.query)?.map((index) => index + 1)
          : undefined,
      })),
    [slashController.filteredCommands, slashController.query]
  );

  const send = useGuidSend({
    // Input state
    input: guidInput.input,
    setInput: guidInput.setInput,
    files: guidInput.files,
    setFiles: guidInput.setFiles,
    localFiles: guidInput.localFiles,
    setLocalFiles: guidInput.setLocalFiles,
    contextItems: guidInput.contextItems,
    setContextItems: guidInput.setContextItems,
    dir: guidInput.dir,
    setDir: guidInput.setDir,
    setLoading: guidInput.setLoading,
    loading: guidInput.loading,

    // Agent state
    selectedAssistantId: agentSelection.selectedAssistantId,
    selectedAssistantBackend: agentSelection.selectedAssistantBackend,
    selectedMode: agentSelection.selectedMode,
    selectedAcpModel: agentSelection.selectedAcpModel,
    selectedThoughtLevelValue: agentSelection.selectedThoughtLevelValue,
    currentAcpCachedModelInfo: agentSelection.currentAcpCachedModelInfo,

    guidDisabledBuiltinSkills,
    guidEnabledSkills,
    assistantDefaultSkillIds: resolvedAssistantDefaults.skillIds,
    assistantDefaultDisabledBuiltinSkillIds: resolvedAssistantDefaults.disabledBuiltinSkillIds,
    assistantDefaultSkillMode: resolvedAssistantDefaults.skillMode,
    availableMcpServers,
    selectedMcpServerIds: guidSelectedMcpServerIds,
    assistantDefaultMcpIds: resolvedAssistantDefaults.mcpIds,
    sessionOptions,
    sessionDefaults: agentSelection.sessionDefaults,
    sessionComputeProviders,
    planModeRef: guidPlanModeRef,

    // Mention state reset
    setMentionOpen: resetMentionOpen,
    setMentionQuery: resetMentionQuery,
    setMentionSelectorOpen: resetMentionOpen,
    setMentionActiveIndex: resetMentionActiveIndex,

    // Navigation
    ownerId: user?.id ?? '',
    navigate,
    t,
    localeKey,
  });

  // --- Coordinated handlers (depend on multiple hooks) ---
  const handleInputChange = useCallback(
    (value: string) => {
      guidInput.setInput(value);
    },
    [guidInput.setInput]
  );

  const runComposerSend = useCallback(() => {
    if (send.isButtonDisabled) return;

    setComposerDocked(true);
    void send.sendMessageHandler().then((sent) => {
      if (!sent) {
        setComposerDocked(false);
      }
      guidPlanModeRef.current = false;
    });
  }, [send.isButtonDisabled, send.sendMessageHandler]);

  const handleGuidSendOption = useCallback(
    (intent: SynonBiomedSendIntent) => {
      if (intent === 'plan_first') {
        guidPlanModeRef.current = true;
      }
      // branch_new_session: the landing draft already creates a fresh session;
      // side_chat is only meaningful inside an existing conversation.
      void runComposerSend();
    },
    [runComposerSend]
  );

  const handleInputKeyDown = useCallback(
    (event: React.KeyboardEvent) => {
      if (slashController.onKeyDown(event)) {
        return;
      }

      if (event.key === 'Enter' && !event.shiftKey) {
        event.preventDefault();
        if (
          !guidInput.input.trim() &&
          guidInput.files.length === 0 &&
          guidInput.localFiles.length === 0 &&
          guidInput.contextItems.length === 0
        )
          return;
        runComposerSend();
      }
    },
    [
      guidInput.contextItems.length,
      guidInput.files.length,
      guidInput.input,
      guidInput.localFiles.length,
      runComposerSend,
      slashController,
    ]
  );

  // Typewriter placeholder
  const typewriterPlaceholder = useTypewriterPlaceholder(t('conversation.welcome.placeholder'));

  // Sync disabledBuiltinSkills + enabledSkills from assistant detail defaults.
  useEffect(() => {
    if (!selectedAssistantId || !selectedAssistantDetail) {
      setGuidDisabledBuiltinSkills(undefined);
      setGuidEnabledSkills(undefined);
      return;
    }

    const resolvedDefaults = resolveGuidAssistantDefaults(selectedAssistantDetail);
    setGuidDisabledBuiltinSkills(
      resolvedDefaults.skillMode === 'fixed' ? resolvedDefaults.disabledBuiltinSkillIds : undefined
    );
    setGuidEnabledSkills(resolvedDefaults.skillMode === 'fixed' ? resolvedDefaults.skillIds : undefined);
  }, [selectedAssistantDetail, selectedAssistantId]);

  const appliedAssistantDefaultsKeyRef = useRef<string | null>(null);
  const manualModelSelectionAssistantRef = useRef<string | null>(null);
  const manualThoughtLevelSelectionAssistantRef = useRef<string | null>(null);
  useEffect(() => {
    if (!selectedAssistantId || !selectedAssistantDetail) {
      appliedAssistantDefaultsKeyRef.current = null;
      manualModelSelectionAssistantRef.current = null;
      manualThoughtLevelSelectionAssistantRef.current = null;
      return;
    }

    const signature = JSON.stringify({
      assistantId: selectedAssistantId,
      backend: agentSelection.selectedAssistantBackend,
      defaults: selectedAssistantDetail.defaults,
      preferences: {
        last_model_id: selectedAssistantDetail.preferences.last_model_id,
        last_permission_value: selectedAssistantDetail.preferences.last_permission_value,
        last_thought_level_value: selectedAssistantDetail.preferences.last_thought_level_value,
        last_mcp_ids: selectedAssistantDetail.preferences.last_mcp_ids,
      },
      availableModels: {
        acp: agentSelection.currentAcpCachedModelInfo?.available_models.map((model) => model.id) ?? [],
      },
      availableModes: agentSelection.currentAgentModeOptions.map((mode) => mode.value),
      availableThoughtLevels: agentSelection.currentThoughtLevelOption?.options.map((option) => option.value) ?? [],
    });
    if (appliedAssistantDefaultsKeyRef.current === signature) {
      return;
    }
    appliedAssistantDefaultsKeyRef.current = signature;

    const applyAssistantDefaults = async () => {
      const resolvedDefaults = resolveGuidAssistantDefaults(selectedAssistantDetail);
      const shouldApplyDefaultModel = manualModelSelectionAssistantRef.current !== selectedAssistantId;
      const shouldApplyDefaultThoughtLevel = manualThoughtLevelSelectionAssistantRef.current !== selectedAssistantId;

      if (shouldApplyDefaultModel && resolvedDefaults.modelId) {
        const availableModelIds = new Set(agentSelection.currentAcpCachedModelInfo?.available_models.map((m) => m.id));
        agentSelection.setSelectedAcpModel(
          availableModelIds.size === 0 || availableModelIds.has(resolvedDefaults.modelId)
            ? resolvedDefaults.modelId
            : null,
          { persistPreference: false }
        );
      } else if (shouldApplyDefaultModel) {
        agentSelection.setSelectedAcpModel(null, { persistPreference: false });
      }

      if (resolvedDefaults.permissionMode) {
        const availableModeIds = new Set(agentSelection.currentAgentModeOptions.map((mode) => mode.value));
        if (availableModeIds.size === 0 || availableModeIds.has(resolvedDefaults.permissionMode)) {
          agentSelection.setSelectedMode(resolvedDefaults.permissionMode, { persistPreference: false });
        } else {
          const fallbackMode = agentSelection.currentAgentModeOptions[0]?.value;
          if (fallbackMode) {
            agentSelection.setSelectedMode(fallbackMode, { persistPreference: false });
          }
        }
      }
      if (shouldApplyDefaultThoughtLevel && agentSelection.currentThoughtLevelOption) {
        const availableThoughtLevelValues = new Set(
          agentSelection.currentThoughtLevelOption.options.map((option) => option.value)
        );
        if (resolvedDefaults.thoughtLevel && availableThoughtLevelValues.has(resolvedDefaults.thoughtLevel)) {
          agentSelection.setSelectedThoughtLevelValue(resolvedDefaults.thoughtLevel, { persistPreference: false });
        } else {
          const fallbackThoughtLevel =
            agentSelection.currentThoughtLevelOption.currentValue ||
            agentSelection.currentThoughtLevelOption.options[0]?.value ||
            '';
          agentSelection.setSelectedThoughtLevelValue(fallbackThoughtLevel, { persistPreference: false });
        }
      }
      setGuidSelectedMcpServerIds(resolvedDefaults.mcpIds);
    };

    void applyAssistantDefaults().catch((error) => {
      console.error('[GuidPage] Failed to apply assistant defaults:', error);
    });
  }, [
    agentSelection.currentAcpCachedModelInfo?.available_models,
    agentSelection.currentAgentModeOptions,
    agentSelection.currentThoughtLevelOption,
    agentSelection.selectedAssistantBackend,
    agentSelection.setSelectedAcpModel,
    agentSelection.setSelectedMode,
    agentSelection.setSelectedThoughtLevelValue,
    selectedAssistantId,
    selectedAssistantDetail,
  ]);

  const setGuidSelectedAcpModel = useCallback(
    (model: React.SetStateAction<string | null>) => {
      manualModelSelectionAssistantRef.current = selectedAssistantId;
      agentSelection.setSelectedAcpModel(model, { persistPreference: !hasSelectedAssistant });
    },
    [agentSelection, hasSelectedAssistant, selectedAssistantId]
  );
  // Reset guid-local UI state before paint so same-route navigations do not
  // briefly show the previous draft or preset assistant layout. When a caller
  // navigates here with a `prefillPrompt` (e.g. "Create via chat" from the
  // scheduled tasks page), seed the input with it instead of clearing.
  //
  // The prefill is consumed once per navigation: a ref keyed on location.key
  // guards against re-seeding if the user later clears the input and returns to
  // this history entry (e.g. via back navigation), which would otherwise revive
  // the prompt from the still-present location.state.
  const consumedPrefillKeyRef = useRef<string | null>(null);
  // Removing one-shot navigation state below churns location.key. That second
  // pass is not a new-task request and must never wipe a prefilled prompt or a
  // file the user selected while the assistant/project catalog was settling.
  // This flag lets exactly that cleanup pass preserve the current draft.
  const skipNextClearRef = useRef(false);
  useLayoutEffect(() => {
    const prefillState = location.state as { prefillPrompt?: string; prefillFiles?: string[] } | null;
    const prefillPrompt = prefillState?.prefillPrompt;
    const prefillFiles = prefillState?.prefillFiles;
    if (prefillPrompt && consumedPrefillKeyRef.current !== location.key) {
      // Consume prompt + optional attachments (e.g. bug-report screenshots) once.
      consumedPrefillKeyRef.current = location.key;
      skipNextClearRef.current = true;
      guidInput.setInput(prefillPrompt);
      guidInput.setFiles(prefillFiles && prefillFiles.length > 0 ? prefillFiles : []);
      guidInput.setLocalFiles([]);
    } else if (skipNextClearRef.current) {
      // This pass is the state-clearing replace() right after a prefill — keep
      // the seeded input instead of clearing it.
      skipNextClearRef.current = false;
    } else {
      guidInput.setInput('');
      guidInput.setFiles([]);
      guidInput.setLocalFiles([]);
      guidInput.setContextItems([]);
    }
    guidInput.setLoading(false);
    if (!(location.state as { workspace?: string } | null)?.workspace) {
      guidInput.setDir('');
    }
  }, [
    guidInput.setDir,
    guidInput.setFiles,
    guidInput.setInput,
    guidInput.setLoading,
    guidInput.setLocalFiles,
    guidInput.setContextItems,
    location.key,
    location.state,
  ]);

  // Clear resetAssistant from location.state after the hook has consumed it,
  // so that re-renders don't re-trigger the reset logic.
  //
  // Must go through React Router's navigate — raw window.history.replaceState
  // with `location.pathname` would write the HashRouter virtual path (e.g.
  // '/guid') into the browser's real URL and strip the leading '#'. On the
  // next hard reload, the browser would then request '/guid' directly from
  // the dev server (which has no SPA fallback) and 404.
  useEffect(() => {
    if (!resetAssistantRequested && !preselectAssistantId && !selectLatestProjectRequested) return;
    skipNextClearRef.current = true;
    const currentState = (location.state ?? {}) as Record<string, unknown>;
    const {
      resetAssistant: _resetAssistant,
      selectedAssistantId: _selectedAssistantId,
      selectLatestProject: _selectLatestProject,
      ...persistentState
    } = currentState;
    navigate(`${location.pathname}${location.search}${location.hash}`, {
      replace: true,
      state: Object.keys(persistentState).length > 0 ? persistentState : null,
    });
  }, [
    resetAssistantRequested,
    preselectAssistantId,
    selectLatestProjectRequested,
    location.pathname,
    location.search,
    location.hash,
    location.state,
    navigate,
  ]);

  const modelSelectorNode = (
    <SynonBiomedDraftModelSelector
      modelInfo={agentSelection.currentAcpCachedModelInfo}
      selectedModelId={agentSelection.selectedAcpModel}
      onSelectModel={(modelId) => setGuidSelectedAcpModel(modelId)}
      thoughtLevelOption={agentSelection.currentThoughtLevelOption}
      selectedThoughtLevel={agentSelection.selectedThoughtLevelValue}
      onSelectThoughtLevel={(value) => agentSelection.setSelectedThoughtLevelValue(value)}
      onOpenModelSettings={() => navigate('/settings/models')}
    />
  );

  const permissionModes =
    agentSelection.currentAgentModeOptions.length > 0
      ? agentSelection.currentAgentModeOptions
      : DEFAULT_GUID_PERMISSION_OPTIONS;
  const permissionOption = useMemo<AcpDerivedOption>(() => {
    return {
      id: 'mode',
      category: 'mode',
      currentValue: agentSelection.selectedMode,
      options: permissionModes.map((mode) => ({
        value: mode.value,
        label: mode.label,
        description: mode.description,
      })),
    };
  }, [agentSelection.selectedMode, permissionModes]);

  const setGuidPermissionMode = useCallback(
    async (_optionId: string, value: string): Promise<unknown> => {
      agentSelection.setSelectedMode(value, { persistPreference: false });
      return [];
    },
    [agentSelection.setSelectedMode]
  );

  const handleSpeechTranscript = useCallback(
    (transcript: string) => {
      guidInput.setInput((prev) => appendSpeechTranscript(prev, transcript));
    },
    [guidInput.setInput]
  );
  const { handleLiveTranscript } = useLiveTranscriptInsertion(guidInput.setInput);

  // Build the action row
  const actionRowNode = (
    <GuidActionRow
      files={guidInput.files}
      localFiles={guidInput.localFiles}
      onFilesUploaded={guidInput.handleFilesUploaded}
      onLocalFilesSelected={guidInput.handleLocalFilesSelected}
      openFileSelector={openFileSelector}
      onSelectProjectFiles={openProjectFiles}
      loadedSkills={guidComposerCapabilities?.skills ?? []}
      loadedMcpStatuses={guidComposerCapabilities?.mcpStatuses ?? []}
      selectedSkillNames={selectedComposerSkillNames}
      selectedMcpServerIds={selectedComposerMcpServerIds}
      onSelectSkill={handleComposerSkillSelected}
      onSelectMcpServer={handleComposerMcpSelected}
      modelSelectorNode={modelSelectorNode}
      sessionOptionsNode={
        <SynonBiomedSessionOptionsMenu
          value={sessionOptions}
          onChange={setSessionOptions}
          computeSelection={sessionComputeProviders}
          onComputeSelectionChange={setSessionComputeProviders}
          disabled={guidInput.loading}
        />
      }
      permissionNode={
        <SynonBiomedPermissionMenu
          option={permissionOption}
          setStatus={{ state: 'idle' }}
          setConfigOption={setGuidPermissionMode}
          includeStandardModes
          disabled={guidInput.loading || !hasSelectedAssistant}
        />
      }
      speechInputNode={
        <SpeechInputButton
          disabled={guidInput.loading}
          onLiveTranscript={handleLiveTranscript}
          onTranscript={handleSpeechTranscript}
        />
      }
      sendOptionsNode={
        !guidInput.loading &&
        (guidInput.input.trim() ||
          guidInput.files.length > 0 ||
          guidInput.localFiles.length > 0 ||
          guidInput.contextItems.length > 0) ? (
          <SynonBiomedSendOptionsMenu
            disabled={guidInput.loading}
            hasDraft
            disabledIntentHints={{ side_chat: t('conversation.synonRuntime.sendBox.sideChatInConversation') }}
            onSelect={handleGuidSendOption}
          />
        ) : undefined
      }
      loading={guidInput.loading}
      isButtonDisabled={send.isButtonDisabled}
      onSend={runComposerSend}
    />
  );
  const slashCommandMenuNode = slashController.isOpen ? (
    <SlashCommandMenu
      title={t('messages.slash.title')}
      hint={t('messages.slash.hint')}
      items={slashMenuItems}
      activeIndex={slashController.activeIndex}
      loading={false}
      onHoverItem={slashController.setActiveIndex}
      onSelectItem={(item) => {
        const targetIndex = slashController.filteredCommands.findIndex((command) => command.name === item.key);
        if (targetIndex >= 0) {
          slashController.onSelectByIndex(targetIndex);
        }
      }}
      emptyText={t('messages.slash.empty')}
    />
  ) : null;

  return (
    <ConfigProvider getPopupContainer={() => guidContainerRef.current || document.body}>
      <div
        ref={guidContainerRef}
        className={`${styles.guidContainer} ${composerDocked ? styles.guidContainerDocked : styles.guidContainerCentered}`}
        data-testid='guid-page'
        data-composer-position={composerDocked ? 'docked' : 'centered'}
        data-workspace={guidInput.dir || undefined}
      >
        <div className={styles.guidLayout}>
          {!composerDocked ? (
            <section className={styles.guidWelcome} aria-label='SYNON-Biomed'>
              <img className={styles.guidWelcomeLogo} src='./branding/synon-biomed-lockup.png' alt='SYNON-Biomed' />
              <p className={styles.guidWelcomeGuidance}>{t('guid.emptyState.guidance')}</p>
            </section>
          ) : null}
          {agentSelection.assistantCatalogStatus === 'error' && !agentSelection.hasUsableAssistantCatalog ? (
            <div
              data-testid='guid-assistant-catalog-recovery'
              role='status'
              aria-live='polite'
              aria-atomic='true'
              className='mb-8px flex items-center justify-center gap-8px text-12px text-t-secondary'
            >
              <span>{t('guid.onboarding.error.loadDescription')}</span>
              <button
                type='button'
                className='rounded-4px px-4px py-2px font-500 text-primary-6 hover:bg-fill-2'
                onClick={() => void agentSelection.retryAssistantCatalog()}
              >
                {t('common.retry')}
              </button>
            </div>
          ) : null}
          <GuidInputCard
            input={guidInput.input}
            onInputChange={handleInputChange}
            onKeyDown={handleInputKeyDown}
            onPaste={guidInput.onPaste}
            onFocus={guidInput.handleTextareaFocus}
            onBlur={guidInput.handleTextareaBlur}
            placeholder={typewriterPlaceholder || t('conversation.welcome.placeholder')}
            isInputActive={guidInput.isInputFocused}
            isFileDragging={guidInput.isFileDragging}
            activeBorderColor={activeBorderColor}
            inactiveBorderColor={inactiveBorderColor}
            activeShadow={activeShadow}
            inactiveShadow={inactiveShadow}
            surfaceBackgroundColor={surfaceBackgroundColor}
            dragHandlers={guidInput.dragHandlers}
            files={guidInput.files}
            localFiles={guidInput.localFiles}
            contextItems={guidInput.contextItems}
            onRemoveFile={guidInput.handleRemoveFile}
            onRemoveLocalFile={guidInput.handleRemoveLocalFile}
            onRemoveContextItem={(key) =>
              guidInput.setContextItems((current) => removeComposerContextItem(current, key))
            }
            actionRow={actionRowNode}
            slashCommandMenu={slashCommandMenuNode}
          />
        </div>
      </div>
      <GuidProjectFilesModal
        visible={projectFilesOpen}
        projectId={projectWorkspace?.projectId}
        projectName={projectWorkspace?.projectName}
        onCancel={() => setProjectFilesOpen(false)}
        onSelect={handleProjectArtifactsSelected}
      />
    </ConfigProvider>
  );
};

export default GuidPage;
