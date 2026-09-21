import { ipcBridge } from '@/common';
import type { IConversationMcpStatus, TChatConversation, TConversationRuntimeSummary } from '@/common/config/storage';
import { isBackendHttpError } from '@/common/adapter/httpBridge';
import { parseError, resolveLocaleKey, uuid } from '@/common/utils';
import SynonBiomedModelSelector from '@/renderer/components/synonBiomed/runtime/SynonBiomedModelSelector';
import SynonBiomedPermissionMenu from '@/renderer/components/synonBiomed/runtime/SynonBiomedPermissionMenu';
import SynonBiomedSendOptionsMenu, {
  type SynonBiomedSendIntent,
} from '@/renderer/components/synonBiomed/runtime/SynonBiomedSendOptionsMenu';
import SynonBiomedSessionOptionsMenu from '@/renderer/components/synonBiomed/runtime/SynonBiomedSessionOptionsMenu';
import ContextUsagePanel from '@/renderer/components/synonBiomed/runtime/ContextUsagePanel';
import SynonBiomedRuntimeOperations from '@/renderer/components/synonBiomed/runtime/SynonBiomedRuntimeOperations';
import CommandQueuePanel from '@/renderer/components/chat/CommandQueuePanel';
import BtwOverlay from '@/renderer/components/chat/BtwOverlay';
import { useBtwCommand } from '@/renderer/components/chat/BtwOverlay/useBtwCommand';
import MobileActionSheet, { useAttachEntry } from '@/renderer/components/chat/MobileActionSheet';
import SendBox from '@/renderer/components/chat/SendBox';
import ComposerContextChips from '@/renderer/components/chat/SendBox/ComposerContextChips';
import {
  buildComposerCapabilityPayload,
  removeComposerContextItem,
} from '@/renderer/components/chat/SendBox/composerCompositionModel';
import ThoughtDisplay from '@/renderer/components/chat/ThoughtDisplay';
import FileAttachButton from '@/renderer/components/media/FileAttachButton';
import GuidProjectFilesModal from '@/renderer/pages/guid/components/GuidProjectFilesModal';
import { loadSynonBiomedComposerCapabilities } from '@/renderer/services/synonBiomedGateway';
import FilePreview from '@/renderer/components/media/FilePreview';
import HorizontalFileList from '@/renderer/components/media/HorizontalFileList';
import { useAcpConfigOptions } from '@/renderer/hooks/synonBiomed/runtime/useAcpConfigOptions';
import { useAcpModelInfo } from '@/renderer/hooks/synonBiomed/runtime/useAcpModelInfo';
import { useAutoTitle } from '@/renderer/hooks/chat/useAutoTitle';
import type { FileOrFolderItem } from '@/renderer/hooks/chat/useSendBoxDraft';
import { useSendBoxFiles } from '@/renderer/hooks/chat/useSendBoxFiles';
import { useConversationContextSafe } from '@/renderer/hooks/context/ConversationContext';
import { useLayoutContext } from '@/renderer/hooks/context/LayoutContext';
import { useOpenFileSelector } from '@/renderer/hooks/file/useOpenFileSelector';
import { useLatestRef } from '@/renderer/hooks/ui/useLatestRef';
import {
  useAddOrUpdateMessage,
  useRemoveMessageByMsgId,
  useUpdateMessageList,
} from '@/renderer/pages/conversation/Messages/hooks';
import { SYNON_BIOMED_OPTIMISTIC_USER_MESSAGE_PREFIX } from '@/renderer/pages/conversation/Messages/optimisticUserMessage';
import {
  shouldEnqueueConversationCommand,
  createQueuedCommandItem,
  useConversationCommandQueue,
  type ConversationCommandQueueItem,
} from '@/renderer/pages/conversation/platforms/useConversationCommandQueue';
import { usePreviewContext } from '@/renderer/pages/conversation/Preview/context/PreviewContext';
import { useConversationRuntimeView } from '@/renderer/pages/conversation/runtime/useConversationRuntimeView';
import {
  markTerminalFailuresSuperseded,
  restoreTerminalFailures,
  type TerminalFailureRollback,
} from '@/renderer/pages/conversation/runtime/terminalFailureMessagesModel';
import { CHAT_SURFACE_WIDTH_CLASS } from '@/renderer/pages/conversation/utils/chatSurfaceWidth';
import { allSupportedExts } from '@/renderer/services/FileService';
import { loadSynonBiomedAssistants } from '@/renderer/services/synonBiomedCatalog';
import { createSynonBiomedBranchSession } from '@/renderer/services/synonBiomedAside';
import {
  requestSynonBiomedFrameAudit,
  type SynonBiomedVerificationCheck,
} from '@/renderer/services/synonBiomedAnnotations';
import {
  getSelectedSynonBiomedBranch,
  getSynonBiomedBranchSelectionRevision,
  loadSynonBiomedConversationBranches,
  selectSynonBiomedBranch,
} from '@/renderer/services/synonBiomedConversationBranches';
import {
  loadSynonBiomedComputeProviders,
  loadSynonBiomedSessionComputeProviders,
  setSynonBiomedSessionComputeProvider,
} from '@/renderer/services/synonBiomedCompute';
import { notifySynonBiomedRuntimeInvalidation } from '@/renderer/services/synonBiomedRuntimeOperations';
import {
  toSynonBiomedMessageSessionOptions,
  updateSynonBiomedSessionConfig,
} from '@/renderer/services/synonBiomedSessionOptions';
import { saveSynonBiomedConversationAsSkill } from '@/renderer/services/skills/synonBiomedSkillLibrary';
import { resolveAssistantName } from '@/renderer/utils/model/assistantDisplay';
import { emitter, useAddEventListener } from '@/renderer/utils/emitter';
import { mergeFileSelectionItems } from '@/renderer/utils/file/fileSelection';
import { buildDisplayMessage } from '@/renderer/utils/file/messageFiles';
import { Message, Tag } from '@arco-design/web-react';
import { useNavigate } from 'react-router';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import useSWR from 'swr';
import { buildSendFailureError } from './buildSendFailureError';
import { redactErrorText } from './errorDiagnostics';
import { buildSynonBiomedReviewRepairPrompt } from '@/renderer/components/synonBiomed/runtime/synonBiomedReviewRepair';
import { useAcpInitialMessage } from './useAcpInitialMessage';
import { applyStagedDraft, makeDraftRestoreIssueNotifier, type StagedDraftPayload } from './stagedDraftHandoff';
import type { UseAcpMessageReturn } from './useAcpMessage';
import { useAcpSendBoxDraftController } from './useAcpSendBoxDraftController';
import { useAcpMobileActionSheetController } from './useAcpMobileActionSheetController';
import { configErrorMessageKey, safeErrorDiagnostic } from './acpSendBoxDiagnostics';
import { useSynonBiomedSessionOptionsState } from './useSynonBiomedSessionOptionsState';
import { useAcpComposerContextSelection } from './useAcpComposerContextSelection';
import { useAcpTaskCenterMetrics } from './useAcpTaskCenterMetrics';
import type { CommandExecutionAuthority, DirectCommandRetry } from './acpCommandExecutionTypes';
import './conversationQueueDock.css';

const AcpSendBox: React.FC<{
  conversation_id: string;
  backend: string;
  session_mode?: string;
  agent_name?: string;
  workspacePath?: string;
  messageState: UseAcpMessageReturn;
  onRuntimeMutation?: () => void;
  initialConversation?: TChatConversation;
  ownerId?: string;
}> = ({
  conversation_id,
  backend,
  session_mode,
  agent_name,
  workspacePath,
  messageState,
  onRuntimeMutation,
  initialConversation,
  ownerId = '',
}) => {
  const { aiProcessing, setAiProcessing, resetState, streamReady, hasThinkingMessage, slashCommands } = messageState;
  const { t, i18n } = useTranslation();
  const localeKey = resolveLocaleKey(i18n.language);
  const navigate = useNavigate();
  const { checkAndUpdateTitle } = useAutoTitle(ownerId, initialConversation);
  const {
    atPath,
    uploadFile,
    setAtPath,
    setUploadFile,
    content,
    setContent,
    contextItems,
    setContextItems,
    stagedPlanMode,
    setStagedPlanMode,
    stagedSessionOptions,
    setStagedSessionOptions,
  } = useAcpSendBoxDraftController(conversation_id, ownerId);
  // Staged plan mode and session options are durable in the draft; keep
  // synchronous views for the send path because the draft store resolves
  // asynchronously.
  const stagedPlanModeRef = useRef(stagedPlanMode);
  useEffect(() => {
    stagedPlanModeRef.current = stagedPlanMode;
  }, [stagedPlanMode]);
  const stagedSessionOptionsRef = useRef(stagedSessionOptions);
  useEffect(() => {
    stagedSessionOptionsRef.current = stagedSessionOptions;
  }, [stagedSessionOptions]);
  const layout = useLayoutContext();
  const isMobile = Boolean(layout?.isMobile);
  const conversationContext = useConversationContextSafe();
  const { data: composerCapabilities } = useSWR(
    conversation_id ? ['synon-biomed-composer-capabilities', conversation_id] : null,
    () => loadSynonBiomedComposerCapabilities(conversation_id),
    { keepPreviousData: false }
  );
  const loadedSkills = composerCapabilities?.skills ?? conversationContext?.loadedSkills ?? [];
  const projectId = conversationContext?.projectId;
  const loadedMcpStatuses =
    composerCapabilities?.mcpStatuses ??
    conversationContext?.loadedMcpStatuses ??
    (conversationContext?.loadedMcpServers ?? []).map<IConversationMcpStatus>((name) => ({
      id: name,
      name,
      status: 'loaded',
    }));
  const isSynonBiomedConversation =
    backend.trim().toLowerCase() === 'synonbiomed' || workspacePath?.startsWith('synonbiomed://') === true;
  const [isMobileSheetOpen, setIsMobileSheetOpen] = useState(false);
  const [currentMode, setCurrentMode] = useState<string | undefined>(session_mode);
  const {
    options: synonSessionOptions,
    setOptions: setSynonSessionOptions,
    loadState: sessionOptionsLoad,
  } = useSynonBiomedSessionOptionsState(conversation_id, agent_name);
  const applyStagedSessionOptions = useCallback(() => {
    const staged = stagedSessionOptionsRef.current;
    if (!staged) return;
    setSynonSessionOptions((current) => ({
      ...current,
      delegation: staged.delegation,
      autoReview: staged.autoReview,
      memory: staged.memory,
      targetAgent: staged.targetAgent.trim() || current.targetAgent,
    }));
  }, [setSynonSessionOptions]);
  // The runtime session-options load resolves asynchronously for a fresh
  // conversation; replay the staged selection once it settles so the loaded
  // defaults cannot silently replace the requesting page's choices.
  useEffect(() => {
    if (sessionOptionsLoad.conversationId !== conversation_id) return;
    if (sessionOptionsLoad.status === 'ready' || sessionOptionsLoad.status === 'retrying') {
      applyStagedSessionOptions();
    }
  }, [applyStagedSessionOptions, sessionOptionsLoad]);
  const [mobileSpecialists, setMobileSpecialists] = useState<Array<{ id: string; label: string; agentId: string }>>([]);
  const [mobileComputeProviders, setMobileComputeProviders] = useState<Array<{ name: string; label: string }>>([]);
  const [mobileEnabledCompute, setMobileEnabledCompute] = useState<string[]>([]);
  const [reviewInFlight, setReviewInFlight] = useState(false);
  const [reviewActive, setReviewActive] = useState(false);
  const [skillSaveInFlight, setSkillSaveInFlight] = useState(false);
  const [goalClearInFlight, setGoalClearInFlight] = useState(false);
  const {
    canSelectProjectFiles,
    handleArtifactReferenceSelected,
    handleLocalFilesSelected,
    handleMcpSelected,
    handleProjectArtifactsSelected,
    handleSkillSelected,
    openProjectFiles,
    projectFilesOpen,
    selectedMcpServerIds,
    selectedSkillNames,
    setProjectFilesOpen,
  } = useAcpComposerContextSelection(projectId, contextItems, setContextItems);
  const sendBoxAnchorRef = useRef<HTMLDivElement>(null);
  const mountedRef = useRef(true);
  const directCommandRetryRef = useRef<DirectCommandRetry | null>(null);
  const conversationAuthorityRef = useRef({ conversationId: conversation_id });
  const sendIntentRef = useRef<SynonBiomedSendIntent | null>(null);
  const terminalFailureRollbackRef = useRef<TerminalFailureRollback>([]);
  if (conversationAuthorityRef.current.conversationId !== conversation_id) {
    conversationAuthorityRef.current = { conversationId: conversation_id };
    directCommandRetryRef.current = null;
    terminalFailureRollbackRef.current = [];
  }
  const sideQuestionSupported = backend.trim().toLowerCase() === 'synonbiomed';
  const sideQuestion = useBtwCommand(conversation_id, sideQuestionSupported);
  const mobileModeChangeInFlightRef = useRef(false);
  const prepareRuntimeConfig = useCallback(async () => {}, []);
  const runtimeConfig = useAcpConfigOptions({
    conversation_id,
    prepareRuntime: prepareRuntimeConfig,
    enabled: true,
  });
  const runtimeMode = runtimeConfig.mode;
  const runtimeThoughtLevel = runtimeConfig.thoughtLevel;
  const handleThoughtLevelSetOption = useCallback(
    async (optionId: string, value: string) => {
      const requestAuthority = conversationAuthorityRef.current;
      try {
        await runtimeConfig.setConfigOption(optionId, value);
      } catch (error) {
        if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return false;
        throw error;
      }
      return mountedRef.current && conversationAuthorityRef.current === requestAuthority;
    },
    [runtimeConfig]
  );
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  useEffect(() => {
    setReviewInFlight(false);
    setReviewActive(false);
    setSkillSaveInFlight(false);
    setGoalClearInFlight(false);
    mobileModeChangeInFlightRef.current = false;
  }, [conversation_id]);

  // Drive the mobile sheet's model entry off the same source SynonBiomedModelSelector uses.
  const {
    model_info,
    canSwitch: canSwitchModel,
    isModelListLoading,
    modelListError,
    retryModelList,
    selectModel,
  } = useAcpModelInfo({
    conversation_id,
    backend,
    prepareRuntime: prepareRuntimeConfig,
    enabled: true,
    onSelectModelSuccess: () => Message.success(t('agent.model.switchSuccess')),
    onSelectModelFailed: (_modelId, error) => Message.error(t(configErrorMessageKey(error))),
  });
  useEffect(() => {
    if (!runtimeMode?.currentValue) return;
    setCurrentMode(runtimeMode.currentValue);
  }, [runtimeMode?.currentValue]);
  useEffect(() => {
    if (!isMobile) return;
    let active = true;
    void Promise.all([
      loadSynonBiomedAssistants(),
      loadSynonBiomedComputeProviders(),
      loadSynonBiomedSessionComputeProviders(conversation_id),
    ])
      .then(([assistants, providers, enabledProviders]) => {
        if (!active) return;
        setMobileSpecialists(
          assistants.map((assistant) => ({
            id: assistant.id,
            label: resolveAssistantName(assistant, localeKey, assistant.name),
            agentId: assistant.agent_id || assistant.name,
          }))
        );
        setMobileComputeProviders(
          providers
            .filter((provider) => provider.checked)
            .map((provider) => ({
              name: provider.name,
              label: provider.displayName,
            }))
        );
        setMobileEnabledCompute(enabledProviders);
      })
      .catch((error) =>
        console.warn('[AcpSendBox] Failed to load mobile session controls', safeErrorDiagnostic(error))
      );
    return () => {
      active = false;
    };
  }, [conversation_id, isMobile, localeKey]);

  const updatePersistentSessionOption = useCallback(
    async (key: 'autoReview' | 'memory', value: boolean) => {
      const requestAuthority = conversationAuthorityRef.current;
      try {
        await updateSynonBiomedSessionConfig(conversation_id, { [key]: value });
      } catch (error) {
        if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
        throw error;
      }
      if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
      setSynonSessionOptions((current) => ({ ...current, [key]: value }));
    },
    [conversation_id]
  );

  const handleClearGoal = useCallback(async () => {
    if (goalClearInFlight) return;
    const requestAuthority = conversationAuthorityRef.current;
    setGoalClearInFlight(true);
    try {
      await updateSynonBiomedSessionConfig(conversation_id, { goalText: null });
      if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
      setSynonSessionOptions((current) => ({ ...current, goalText: null }));
      Message.success(t('conversation.synonRuntime.sendBox.goalCleared'));
    } catch (error) {
      if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
      console.error('[AcpSendBox] Failed to clear conversation goal', safeErrorDiagnostic(error));
      Message.error(t('conversation.synonRuntime.sendBox.goalClearFailed'));
    } finally {
      if (mountedRef.current && conversationAuthorityRef.current === requestAuthority) setGoalClearInFlight(false);
    }
  }, [conversation_id, goalClearInFlight, t]);

  const handleSheetModeChange = useCallback(
    async (mode: string) => {
      if (!runtimeMode || mode === runtimeMode.currentValue || mobileModeChangeInFlightRef.current) return;
      const requestAuthority = conversationAuthorityRef.current;
      mobileModeChangeInFlightRef.current = true;
      try {
        await runtimeConfig.setConfigOption(runtimeMode.id, mode);
        if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
        setCurrentMode(mode);
        setIsMobileSheetOpen(false);
        Message.success(t('agentMode.switchSuccess'));
      } catch (error) {
        if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
        console.error('[AcpSendBox] Failed to switch mode via sheet:', safeErrorDiagnostic(error));
        Message.error(t(configErrorMessageKey(error)));
      } finally {
        if (mountedRef.current && conversationAuthorityRef.current === requestAuthority) {
          mobileModeChangeInFlightRef.current = false;
        }
      }
    },
    [runtimeConfig, runtimeMode, t]
  );

  const handleSessionComputeChange = useCallback(
    async (providerName: string, checked: boolean) => {
      const requestAuthority = conversationAuthorityRef.current;
      try {
        await setSynonBiomedSessionComputeProvider(conversation_id, providerName, checked);
        if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
        setMobileEnabledCompute((current) =>
          checked ? Array.from(new Set([...current, providerName])) : current.filter((name) => name !== providerName)
        );
      } catch (error) {
        if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
        console.error('[AcpSendBox] Failed to update session compute', safeErrorDiagnostic(error));
        Message.error(t('conversation.synonRuntime.sessionOptions.computeUpdateFailed'));
      }
    },
    [conversation_id, t]
  );

  const handleContentChange = useCallback(
    (val: string) => {
      // SendBox clears its controlled value synchronously immediately before
      // onSend. Preserve an ambiguous direct-send receipt through that exact
      // transition; a real non-empty edit still starts a new mutation.
      if (val !== '' && directCommandRetryRef.current?.item.input !== val) directCommandRetryRef.current = null;
      setContent(val);
    },
    [setContent]
  );
  const { setSendBoxHandler } = usePreviewContext();

  // Use useLatestRef to keep latest setters to avoid re-registering handler
  const setContentRef = useLatestRef(setContent);
  const contentRef = useLatestRef(content);
  const atPathRef = useLatestRef(atPath);

  const addOrUpdateMessage = useAddOrUpdateMessage(); // Move this here so it's available in useEffect
  const addOrUpdateMessageRef = useLatestRef(addOrUpdateMessage);
  const removeMessageByMsgId = useRemoveMessageByMsgId();
  const removeMessageByMsgIdRef = useLatestRef(removeMessageByMsgId);
  const updateMessageList = useUpdateMessageList();
  const runtimeView = useConversationRuntimeView(conversation_id, initialConversation);

  // Shared file handling logic
  const { handleFilesAdded, clearFiles } = useSendBoxFiles({
    atPath,
    uploadFile,
    setAtPath,
    setUploadFile,
  });
  const sessionOptionsReady =
    sessionOptionsLoad.conversationId === conversation_id && sessionOptionsLoad.status === 'ready';
  const commandQueueRuntimeGate = {
    hydrated: runtimeView.hydrated,
    canSendMessage: runtimeView.canSendMessage,
    isProcessing: runtimeView.isProcessing,
  };
  const isCancelling = runtimeView.state === 'cancelling';
  const runtimeUnavailable = !runtimeView.hydrated || Boolean(runtimeView.hydrationError);
  const isBusy =
    !runtimeUnavailable &&
    (isCancelling || commandQueueRuntimeGate.isProcessing || !commandQueueRuntimeGate.canSendMessage);
  const reviewBusy = reviewInFlight || reviewActive;

  const handleRequestReview = useCallback(async () => {
    if (isBusy || reviewInFlight || reviewActive) return;
    const requestAuthority = conversationAuthorityRef.current;
    setReviewInFlight(true);
    try {
      await requestSynonBiomedFrameAudit(conversation_id);
      if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
      notifySynonBiomedRuntimeInvalidation(conversation_id);
      Message.success(t('conversation.synonRuntime.sendBox.reviewStarted'));
      emitter.emit('chat.history.refresh');
      onRuntimeMutation?.();
    } catch (error) {
      if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
      console.error('[AcpSendBox] Failed to start manual review', safeErrorDiagnostic(error));
      Message.error(t('conversation.synonRuntime.sendBox.reviewStartFailed'));
    } finally {
      if (mountedRef.current && conversationAuthorityRef.current === requestAuthority) setReviewInFlight(false);
    }
  }, [conversation_id, isBusy, onRuntimeMutation, reviewActive, reviewInFlight, t]);

  // Register handler for adding text from preview panel to sendbox
  useEffect(() => {
    const handler = (text: string) => {
      // If there's existing content, add newline and new text; otherwise just set the text
      const new_content = content ? `${content}\n${text}` : text;
      setContentRef.current(new_content);
    };
    setSendBoxHandler(handler);
  }, [setSendBoxHandler, content]);

  // Listen for sendbox.fill event to append text to sendbox
  useAddEventListener(
    'sendbox.fill',
    (text: string) => {
      const prev = contentRef.current;
      setContentRef.current(prev ? `${prev}${text}` : text);
    },
    []
  );

  // Check for and send initial message from guid page
  // Check for and stage the initial message from the guid page.
  const stageInitialDraft = useCallback(
    (payload: StagedDraftPayload) => {
      applyStagedDraft(
        payload,
        {
          setContent,
          setUploadFile,
          setContextItems,
          setStagedSessionOptions,
          setStagedPlanMode,
        },
        {
          onStagedSessionOptions: (options) => {
            stagedSessionOptionsRef.current = options;
          },
          applyStagedSessionOptions,
        }
      );
    },
    [setContent, setUploadFile, setContextItems, setStagedSessionOptions, setStagedPlanMode, applyStagedSessionOptions]
  );
  const notifyDraftRestoreIssue = useMemo(() => makeDraftRestoreIssueNotifier(t), [t]);
  useAcpInitialMessage({
    conversation_id: conversation_id,
    backend,
    workspacePath,
    streamReady,
    setAiProcessing,
    resetState,
    markSendStarted: runtimeView.markSendStarted,
    markSendAccepted: runtimeView.markSendAccepted,
    markSendFailed: runtimeView.markSendFailed,
    checkAndUpdateTitle,
    addOrUpdateMessage: addOrUpdateMessageRef.current,
    onDraftPrefill: stageInitialDraft,
    onDraftRestoreIssue: notifyDraftRestoreIssue,
  });

  const resolveCommandExecutionAuthority = useCallback(
    async (planMode?: boolean): Promise<CommandExecutionAuthority> => {
      const targetBranchId = getSelectedSynonBiomedBranch(conversation_id);
      const targetBranchRevision = getSynonBiomedBranchSelectionRevision(conversation_id);
      const branchState = targetBranchId ? await loadSynonBiomedConversationBranches(conversation_id) : null;
      if (
        targetBranchId &&
        (getSynonBiomedBranchSelectionRevision(conversation_id) !== targetBranchRevision ||
          getSelectedSynonBiomedBranch(conversation_id) !== targetBranchId ||
          !branchState?.branches.some((branch) => branch.id === targetBranchId))
      ) {
        throw new Error('selected_branch_changed');
      }
      // A staged draft carries its requesting page's session options through
      // the durable draft; they win over the async runtime load for the first
      // send, while an explicit per-send choice (plan mode) always wins.
      const effectiveSessionOptions = stagedSessionOptionsRef.current
        ? {
            ...synonSessionOptions,
            delegation: stagedSessionOptionsRef.current.delegation,
            autoReview: stagedSessionOptionsRef.current.autoReview,
            memory: stagedSessionOptionsRef.current.memory,
            targetAgent: stagedSessionOptionsRef.current.targetAgent.trim() || synonSessionOptions.targetAgent,
          }
        : synonSessionOptions;
      return {
        targetBranchId,
        targetBranchRevision,
        sessionOptions: toSynonBiomedMessageSessionOptions(effectiveSessionOptions, {
          planMode: planMode ?? (stagedPlanModeRef.current ? true : undefined),
          targetBranchId,
          expectedBranchId: branchState?.activeBranchId,
          expectedGeneration: branchState?.generation,
        }),
      };
    },
    [conversation_id, synonSessionOptions]
  );

  const executeCommand = useCallback(
    async (
      {
        id,
        input,
        files,
        contextItems: commandContextItems,
        planMode,
      }: Pick<ConversationCommandQueueItem, 'id' | 'input' | 'files' | 'contextItems'> & {
        planMode?: boolean;
      },
      preparedAuthority?: CommandExecutionAuthority
    ) => {
      const displayMessage = buildDisplayMessage(input, files, workspacePath || '');
      const capabilityPayload = buildComposerCapabilityPayload(commandContextItems);
      const conversationAuthority = conversationAuthorityRef.current;

      try {
        void checkAndUpdateTitle(conversation_id, input);
        runtimeView.markSendStarted();
        setAiProcessing(true);
        // Surface the submitted message immediately. Durable history replaces
        // this optimistic row on the next canonical refresh; long-running
        // turns must never look empty while the model is still thinking.
        addOrUpdateMessageRef.current(
          {
            id: `${SYNON_BIOMED_OPTIMISTIC_USER_MESSAGE_PREFIX}${id}`,
            msg_id: `${SYNON_BIOMED_OPTIMISTIC_USER_MESSAGE_PREFIX}${id}`,
            conversation_id,
            type: 'text',
            position: 'right',
            status: 'pending',
            created_at: Date.now(),
            content: { content: displayMessage },
            artifact_refs: capabilityPayload.artifactRefs,
          },
          true
        );
        const commandAuthority = preparedAuthority ?? (await resolveCommandExecutionAuthority(planMode));
        const pendingRetry = directCommandRetryRef.current;
        if (pendingRetry?.item.id === id && pendingRetry.authority === undefined) {
          directCommandRetryRef.current = {
            ...pendingRetry,
            authority: commandAuthority,
          };
        }
        const result = await ipcBridge.acpConversation.sendMessage.invoke({
          input: displayMessage,
          conversation_id,
          loading_id: id,
          files,
          artifact_refs: capabilityPayload.artifactRefs,
          inject_skills: capabilityPayload.injectSkills,
          inject_mcp_server_ids: capabilityPayload.injectMcpServerIds,
          session_options: commandAuthority.sessionOptions,
        });
        if (!mountedRef.current || conversationAuthorityRef.current !== conversationAuthority) return;
        runtimeView.markSendAccepted(result.turn_id, result.runtime, result.msg_id);
        // The staged intents applied to this first accepted send and must not
        // leak into later manual sends: the frame now records them.
        if (stagedPlanModeRef.current) {
          stagedPlanModeRef.current = false;
          setStagedPlanMode(false);
        }
        if (stagedSessionOptionsRef.current) {
          stagedSessionOptionsRef.current = null;
          setStagedSessionOptions(null);
        }
        // The send response is the first authoritative identity for the user
        // message. Promote the optimistic row to that identity immediately so
        // the durable userCreated event cannot leave two identical rows on
        // screen while a long task is running.
        removeMessageByMsgIdRef.current(`${SYNON_BIOMED_OPTIMISTIC_USER_MESSAGE_PREFIX}${id}`);
        if (result.msg_id) {
          addOrUpdateMessageRef.current(
            {
              id: result.msg_id,
              msg_id: result.msg_id,
              conversation_id,
              type: 'text',
              position: 'right',
              status: 'finish',
              created_at: Date.now(),
              content: { content: displayMessage },
              artifact_refs: capabilityPayload.artifactRefs,
            },
            false
          );
        }
        if (
          commandAuthority.targetBranchId &&
          getSynonBiomedBranchSelectionRevision(conversation_id) === commandAuthority.targetBranchRevision &&
          getSelectedSynonBiomedBranch(conversation_id) === commandAuthority.targetBranchId
        ) {
          selectSynonBiomedBranch(conversation_id, null);
        }
        emitter.emit('chat.history.refresh');
      } catch (error: unknown) {
        removeMessageByMsgIdRef.current(`${SYNON_BIOMED_OPTIMISTIC_USER_MESSAGE_PREFIX}${id}`);
        const pendingRetry = directCommandRetryRef.current;
        if (
          pendingRetry?.item.id === id &&
          pendingRetry.authority?.targetBranchId &&
          isBackendHttpError(error) &&
          error.status === 409
        ) {
          // The mutation identity remains stable for an idempotent retry, but
          // an explicit branch conflict invalidates the cached CAS authority.
          // Resolve the latest active branch/generation before retrying.
          directCommandRetryRef.current = {
            ...pendingRetry,
            authority: undefined,
          };
        }
        if (!mountedRef.current || conversationAuthorityRef.current !== conversationAuthority) throw error;
        const errorMsg = redactErrorText(parseError(error) || t('common.unknownError'));
        runtimeView.markSendFailed(errorMsg);

        // Archived conversation (e.g. legacy Gemini). Backend signals this
        // via HTTP 410 + code='CONVERSATION_ARCHIVED' — identified by code,
        // not by substring matching.
        if (isBackendHttpError(error) && error.code === 'CONVERSATION_ARCHIVED') {
          Message.error({
            content: error.backendMessage || errorMsg,
            duration: 6000,
          });
          setAiProcessing(false);
          throw error;
        }

        const isAuthError =
          errorMsg.includes('[ACP-AUTH-') ||
          errorMsg.includes('authentication failed') ||
          errorMsg.includes('认证失败');
        const visibleError = isAuthError ? t('acp.auth.failed', { backend, error: errorMsg }) : errorMsg;
        addOrUpdateMessageRef.current(
          {
            id: uuid(),
            msg_id: uuid(),
            type: 'tips',
            position: 'center',
            conversation_id,
            created_at: Date.now(),
            content: {
              content: visibleError,
              type: 'error',
              error: buildSendFailureError(error, errorMsg),
            },
          },
          true
        );

        resetState();
        setAiProcessing(false);
        throw error;
      }

      if (files.length > 0) {
        emitter.emit('acp.workspace.refresh');
      }
    },
    [
      backend,
      checkAndUpdateTitle,
      conversation_id,
      resetState,
      resolveCommandExecutionAuthority,
      runtimeView,
      setAiProcessing,
      t,
      workspacePath,
    ]
  );

  const handleSaveAsSkill = useCallback(async () => {
    if (isBusy || skillSaveInFlight) return;
    const requestAuthority = conversationAuthorityRef.current;
    setSkillSaveInFlight(true);
    try {
      const result = await saveSynonBiomedConversationAsSkill(conversation_id);
      if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
      Message.success(t('conversation.attachMenu.saveAsSkillSuccess', { name: result.name }));
      emitter.emit('chat.history.refresh');
    } catch (error) {
      if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
      const detail = safeErrorDiagnostic(error);
      console.error('[AcpSendBox] Failed to save personal Skill', detail);
      Message.error(t('conversation.attachMenu.saveAsSkillFailed', { error: detail }));
    } finally {
      if (mountedRef.current && conversationAuthorityRef.current === requestAuthority) {
        setSkillSaveInFlight(false);
      }
    }
  }, [conversation_id, isBusy, skillSaveInFlight, t]);

  const {
    items: queuedCommands,
    isInterrupted: isCommandQueueInterrupted,
    isQueueingEnabled,
    hasPendingCommands,
    enqueue,
    remove,
    reorder,
    retry: retryQueuedCommand,
    sendNow,
    setQueueingEnabled,
    pause: pauseCommandQueue,
    resume: resumeCommandQueue,
    lockInteraction,
    unlockInteraction,
    resetActiveExecution,
  } = useConversationCommandQueue({
    conversation_id: conversation_id,
    enabled: true,
    isBusy,
    runtimeGate: commandQueueRuntimeGate,
    onExecute: executeCommand,
  });

  const onSendHandler = async (message: string) => {
    const atPathFiles = atPath.map((item) => (typeof item === 'string' ? item : item.path));
    const allFiles = Array.from(new Set([...uploadFile, ...atPathFiles].filter(Boolean)));
    const normalizedMessage = message.trim() || t('conversation.sendbox.contextOnlyPrompt');
    const commandContextItems = [...contextItems];
    const sendIntent = sendIntentRef.current;
    sendIntentRef.current = null;
    const planMode = sendIntent === 'plan_first' ? true : undefined;
    const signature = JSON.stringify({
      input: normalizedMessage,
      files: allFiles,
      contextItems: commandContextItems,
      branchId: getSelectedSynonBiomedBranch(conversation_id),
      branchRevision: getSynonBiomedBranchSelectionRevision(conversation_id),
      sessionOptions: synonSessionOptions,
      planMode,
    });
    const retry = directCommandRetryRef.current?.signature === signature ? directCommandRetryRef.current : null;
    if (retry) {
      try {
        await executeCommand(retry.item, retry.authority);
        if (directCommandRetryRef.current?.item.id === retry.item.id) directCommandRetryRef.current = null;
        clearFiles();
        setContextItems([]);
        emitter.emit('acp.selected.file.clear');
        return;
      } catch (error) {
        if (contentRef.current === '') setContent(retry.item.input);
        throw error;
      }
    }
    directCommandRetryRef.current = null;

    if (
      shouldEnqueueConversationCommand({
        enabled: isQueueingEnabled,
        isBusy,
        hasPendingCommands,
      })
    ) {
      enqueue({
        input: normalizedMessage,
        files: allFiles,
        contextItems: commandContextItems,
        planMode,
      });
      clearFiles();
      setContextItems([]);
      emitter.emit('acp.selected.file.clear');
      return;
    }

    const item = createQueuedCommandItem({
      input: normalizedMessage,
      files: allFiles,
      contextItems: commandContextItems,
      planMode,
    });
    directCommandRetryRef.current = { signature, item };
    try {
      await executeCommand(item);
      if (directCommandRetryRef.current?.item.id === item.id) directCommandRetryRef.current = null;
      clearFiles();
      setContextItems([]);
      emitter.emit('acp.selected.file.clear');
    } catch (error) {
      if (contentRef.current === '') setContent(item.input);
      throw error;
    }
  };

  const handleRepairAndRegenerate = async (checks: SynonBiomedVerificationCheck[]) => {
    await onSendHandler(buildSynonBiomedReviewRepairPrompt(checks, i18n.language));
  };

  const handleSendOption = useCallback(
    (intent: SynonBiomedSendIntent) => {
      const draft = contentRef.current;
      if (!draft.trim()) return;

      if (intent === 'plan_first') {
        sendIntentRef.current = 'plan_first';
        void onSendHandler(draft);
        return;
      }

      if (uploadFile.length > 0 || atPath.length > 0 || contextItems.length > 0) {
        Message.warning(t('conversation.sendbox.structuredContextNormalSend'));
        return;
      }

      if (intent === 'side_chat') {
        setContent('');
        emitter.emit('acp.selected.file.clear');
        void sideQuestion.ask(draft);
        return;
      }

      // branch_new_session: create a real branched session and send there.
      const requestAuthority = conversationAuthorityRef.current;
      setAiProcessing(true);
      void createSynonBiomedBranchSession(conversation_id, draft, {
        model: model_info?.current_model_id ?? null,
      })
        .then(({ frameId }) => {
          if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
          setContent('');
          emitter.emit('acp.selected.file.clear');
          void navigate(`/conversation/${encodeURIComponent(frameId)}`);
        })
        .catch((error: unknown) => {
          if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
          console.error('[AcpSendBox] Failed to create branched session:', safeErrorDiagnostic(error));
          Message.error(t('conversation.synonRuntime.sendBox.branchNewSessionFailed'));
        })
        .finally(() => {
          if (mountedRef.current && conversationAuthorityRef.current === requestAuthority) {
            setAiProcessing(false);
          }
        });
    },
    [
      atPath.length,
      contextItems.length,
      conversation_id,
      model_info,
      navigate,
      onSendHandler,
      setContent,
      sideQuestion,
      t,
      uploadFile.length,
    ]
  );

  const handleEditQueuedCommand = useCallback(
    (item: ConversationCommandQueueItem) => {
      remove(item.id);
      sendIntentRef.current = item.planMode ? 'plan_first' : null;
      setContent(item.input);
      setUploadFile(Array.from(new Set(item.files)));
      setContextItems(item.contextItems);
      setAtPath([]);
      emitter.emit('acp.selected.file.clear');
    },
    [remove, setAtPath, setContent, setContextItems, setUploadFile]
  );

  const handleOpenQueuedCommandInSideChat = useCallback(
    (item: ConversationCommandQueueItem) => {
      if (item.files.length > 0 || item.contextItems.length > 0) {
        Message.warning(t('conversation.commandQueue.sideChatAttachmentsUnsupported'));
        return;
      }
      remove(item.id);
      void sideQuestion.ask(item.input);
    },
    [remove, sideQuestion, t]
  );

  const appendSelectedFiles = useCallback(
    (files: string[]) => {
      setUploadFile((prev) => [...prev, ...files]);
    },
    [setUploadFile]
  );
  const { openFileSelector, onSlashBuiltinCommand } = useOpenFileSelector({
    onFilesSelected: appendSelectedFiles,
  });

  const { entries: attachEntries, hiddenFileInput: attachHiddenInput } = useAttachEntry({
    openFileSelector,
    onLocalFilesAdded: handleFilesAdded,
    onBrowserFilesAdded: canSelectProjectFiles ? handleLocalFilesSelected : undefined,
  });

  const sheetEntries = useAcpMobileActionSheetController({
    isMobile,
    runtimeMode,
    currentMode,
    runtimeThoughtLevel,
    runtimeConfig,
    canSwitchModel,
    model_info,
    isModelListLoading,
    modelListError,
    retryModelList,
    selectModel,
    synonSessionOptions,
    sessionOptionsReady,
    setSynonSessionOptions,
    updatePersistentSessionOption,
    mobileSpecialists,
    mobileComputeProviders,
    mobileEnabledCompute,
    handleSessionComputeChange,
    handleSheetModeChange,
    handleThoughtLevelSetOption,
    attachEntries,
    loadedSkills,
    loadedMcpStatuses,
    selectedSkillNames,
    selectedMcpServerIds,
    onSelectSkill: handleSkillSelected,
    onSelectMcpServer: handleMcpSelected,
    t,
    i18n,
  });

  useAddEventListener('acp.selected.file', setAtPath);
  useAddEventListener('acp.selected.file.append', (selectedItems: Array<string | FileOrFolderItem>) => {
    const merged = mergeFileSelectionItems(atPathRef.current, selectedItems);
    if (merged !== atPathRef.current) {
      setAtPath(merged as Array<string | FileOrFolderItem>);
    }
  });

  // Pause conversation handler: the backend preserves the checkpoint through cancellation.
  const handleStop = async (): Promise<void> => {
    const requestAuthority = conversationAuthorityRef.current;
    // Synon Biomed's compatibility cancel endpoint is frame-authority based:
    // the conversation id is the frame id and the legacy turn_id body field
    // is not used by the server. A queued/resumed task can briefly have a live
    // frame in the task center while the conversation runtime view has not yet
    // received its turn id. Keep the real stop action available in that race;
    // other ACP backends still fail closed when their turn authority is absent.
    const isSynonBiomedBackend = backend.trim().toLowerCase() === 'synonbiomed';
    const turnId = runtimeView.activeTurnId ?? (isSynonBiomedBackend ? conversation_id : null);
    if (!turnId) {
      runtimeView.resetLocalGate('stop_missing_turn');
      Message.error(t('conversation.synonRuntime.sendBox.stopMissingTurn'));
      return;
    }

    runtimeView.markStopRequested(turnId);
    try {
      const result = await ipcBridge.conversation.stop.invoke({
        conversation_id,
        turn_id: turnId,
      });
      if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
      runtimeView.markStopAcknowledged(turnId, result.runtime);
      resetActiveExecution('stop');
      if (queuedCommands.length > 0) pauseCommandQueue('interrupted');

      const stillProcessing = result.runtime.is_processing || result.runtime.state === 'cancelling';
      setAiProcessing(stillProcessing);
      if (!stillProcessing) {
        resetState();
      }
    } catch (error) {
      if (!mountedRef.current || conversationAuthorityRef.current !== requestAuthority) return;
      console.warn('[AcpSendBox] stop request failed', safeErrorDiagnostic(error));
      runtimeView.resetLocalGate('stop_failed');
      Message.error(t('conversation.synonRuntime.sendBox.stopFailed'));
    }
  };
  const handleRuntimeUpdated = useCallback(
    (turnId: string, runtime: TConversationRuntimeSummary, source: 'initial' | 'refresh' = 'refresh') => {
      terminalFailureRollbackRef.current = [];
      runtimeView.markSendAccepted(turnId, runtime);
      emitter.emit('synonbiomed.runtime.reconciled', conversation_id, runtime);
      setAiProcessing(runtime.is_processing && runtime.state !== 'waiting_confirmation');
      if (source !== 'initial') onRuntimeMutation?.();
    },
    [conversation_id, onRuntimeMutation, runtimeView, setAiProcessing]
  );
  const handleResumeStarted = useCallback(() => {
    updateMessageList((messages) => {
      const marked = markTerminalFailuresSuperseded(messages);
      if (marked.rollback.length > 0) {
        terminalFailureRollbackRef.current = marked.rollback;
      }
      return marked.messages;
    });
    runtimeView.markSendStarted();
  }, [runtimeView, updateMessageList]);
  const handleResumeFailed = useCallback(
    (reason: string) => {
      const rollback = terminalFailureRollbackRef.current;
      terminalFailureRollbackRef.current = [];
      if (rollback.length > 0) {
        updateMessageList((messages) => restoreTerminalFailures(messages, rollback));
      }
      runtimeView.markSendFailed(reason);
    },
    [runtimeView, updateMessageList]
  );
  const sendBoxWidthClass = CHAT_SURFACE_WIDTH_CLASS;
  const taskCenterMetrics = useAcpTaskCenterMetrics(messageState.tokenUsage?.total_tokens);

  return (
    <div
      ref={sendBoxAnchorRef}
      className={`${sendBoxWidthClass} conversation-composer w-full min-w-0 max-w-full box-border flex flex-col mt-auto mb-16px`}
    >
      <BtwOverlay
        answer={sideQuestion.answer}
        anchorEl={sendBoxAnchorRef.current}
        isLoading={sideQuestion.isLoading}
        isOpen={sideQuestion.isOpen}
        onDismiss={sideQuestion.dismiss}
        parentTaskRunning={isBusy}
        question={sideQuestion.question}
      />
      <SynonBiomedRuntimeOperations
        conversationId={conversation_id}
        runtimeState={runtimeView.state}
        terminalProjectionPending={runtimeView.view.terminalProjectionPending}
        onResumeStarted={handleResumeStarted}
        onResumeFailed={handleResumeFailed}
        onResumed={handleRuntimeUpdated}
        onRuntimeUpdated={handleRuntimeUpdated}
        runtimeAuthorityUnavailable={Boolean(runtimeView.hydrationError)}
        onRetryRuntimeAuthority={runtimeView.retryHydration}
        onStop={handleStop}
        onReviewActivityChange={setReviewActive}
        onRepairAndRegenerate={handleRepairAndRegenerate}
        taskCenterMetrics={taskCenterMetrics}
        onChooseModel={isMobile ? () => setIsMobileSheetOpen(true) : undefined}
        modelInfo={
          model_info
            ? {
                currentModelId: model_info.current_model_id,
                availableModels: model_info.available_models.map((model) => ({
                  id: model.id,
                  label: model.label || model.id,
                })),
              }
            : null
        }
      />
      {!sideQuestionSupported ? (
        <ThoughtDisplay running={aiProcessing && !hasThinkingMessage && runtimeView.state !== 'waiting_confirmation'} />
      ) : null}

      <CommandQueuePanel
        items={queuedCommands}
        isInterrupted={isCommandQueueInterrupted}
        isQueueingEnabled={isQueueingEnabled}
        isSendNowDisabled={runtimeUnavailable || isCancelling || isCommandQueueInterrupted}
        onInteractionLock={lockInteraction}
        onInteractionUnlock={unlockInteraction}
        onEdit={handleEditQueuedCommand}
        onOpenInSideChat={sideQuestionSupported ? handleOpenQueuedCommandInSideChat : undefined}
        onReorder={reorder}
        onRemove={remove}
        onRetry={(commandId) => void retryQueuedCommand(commandId)}
        onSendNow={(commandId) => void sendNow(commandId)}
        onQueueingChange={setQueueingEnabled}
        onResumeInterruptedQueue={resumeCommandQueue}
      />

      <SendBox
        onMobilePlusClick={isMobile ? () => setIsMobileSheetOpen(true) : undefined}
        value={content}
        onChange={handleContentChange}
        selectedWorkspaceItems={atPath}
        onSelectedWorkspaceItemsChange={(items) => {
          emitter.emit('acp.selected.file', items);
          setAtPath(items);
        }}
        loading={isBusy}
        disabled={runtimeUnavailable}
        allowSendWhileLoading
        placeholder={t('acp.sendbox.placeholder', {
          backend: agent_name || 'Synon Biomed',
        })}
        className='z-10 w-full min-w-0 max-w-full'
        onFilesAdded={handleFilesAdded}
        onBrowserFilesAdded={canSelectProjectFiles ? handleLocalFilesSelected : undefined}
        hasPendingAttachments={uploadFile.length > 0 || atPath.length > 0}
        hasPendingContext={contextItems.length > 0}
        onSelectSkillCommand={handleSkillSelected}
        onSelectArtifactReference={handleArtifactReferenceSelected}
        enableBtw={sideQuestionSupported}
        supportedExts={allSupportedExts}
        defaultMultiLine={!isMobile}
        lockMultiLine={!isMobile}
        tools={
          isMobile ? undefined : (
            <div className='flex min-w-0 items-center gap-4px'>
              <FileAttachButton
                openFileSelector={openFileSelector}
                onSelectProjectFiles={canSelectProjectFiles ? openProjectFiles : undefined}
                onLocalFilesAdded={handleFilesAdded}
                onLocalFilesSelected={canSelectProjectFiles ? handleLocalFilesSelected : undefined}
                loadedMcpStatuses={loadedMcpStatuses}
                loadedSkills={loadedSkills}
                selectedSkillNames={selectedSkillNames}
                selectedMcpServerIds={selectedMcpServerIds}
                onSelectSkill={handleSkillSelected}
                onSelectMcpServer={handleMcpSelected}
                synonBiomedV11
                onRequestReview={handleRequestReview}
                reviewLabelMode='manual'
                reviewInFlight={reviewBusy}
                reviewDisabled={isBusy}
                onSaveSkill={handleSaveAsSkill}
                saveAsSkillDisabled={isBusy || skillSaveInFlight}
              />
              <SynonBiomedSessionOptionsMenu
                rootFrameId={conversation_id}
                value={synonSessionOptions}
                onChange={setSynonSessionOptions}
                onPersistentOptionChange={updatePersistentSessionOption}
                onClearGoal={handleClearGoal}
                goalClearInFlight={goalClearInFlight}
                disabled={!sessionOptionsReady}
              />
              <SynonBiomedPermissionMenu
                option={runtimeMode}
                setStatus={runtimeConfig.setStatus}
                setConfigOption={runtimeConfig.setConfigOption}
                disabled={runtimeUnavailable}
              />
            </div>
          )
        }
        rightTools={
          <div className='flex items-center gap-8px min-w-0'>
            {isSynonBiomedConversation ? (
              <ContextUsagePanel
                conversationId={conversation_id}
                tokenUsage={messageState.tokenUsage}
                contextLimit={messageState.context_limit}
              />
            ) : null}
            <SynonBiomedModelSelector
              conversation_id={conversation_id}
              backend={backend}
              initialModelId={model_info?.current_model_id}
            />
          </div>
        }
        prefix={
          <>
            <ComposerContextChips
              items={contextItems}
              onRemove={(key) => setContextItems((current) => removeComposerContextItem(current, key))}
            />
            {uploadFile.length > 0 && (
              <HorizontalFileList>
                {uploadFile.map((path) => (
                  <FilePreview
                    key={path}
                    path={path}
                    onRemove={() => setUploadFile(uploadFile.filter((v) => v !== path))}
                  />
                ))}
              </HorizontalFileList>
            )}
            {atPath.some((item) => (typeof item === 'string' ? false : !item.isFile)) && (
              <div className='flex flex-wrap items-center gap-8px mb-8px'>
                {atPath.map((item) => {
                  if (typeof item === 'string') return null;
                  if (!item.isFile) {
                    return (
                      <Tag
                        key={item.path}
                        color='blue'
                        closable
                        onClose={() => {
                          const newAtPath = atPath.filter((v) => (typeof v === 'string' ? true : v.path !== item.path));
                          emitter.emit('acp.selected.file', newAtPath);
                          setAtPath(newAtPath);
                        }}
                      >
                        {item.name}
                      </Tag>
                    );
                  }
                  return null;
                })}
              </div>
            )}
          </>
        }
        onSend={onSendHandler}
        sendButtonSuffix={
          content.trim() || uploadFile.length > 0 || atPath.length > 0 || contextItems.length > 0 ? (
            <SynonBiomedSendOptionsMenu disabled={isBusy || runtimeUnavailable} hasDraft onSelect={handleSendOption} />
          ) : undefined
        }
        slash_commands={slashCommands}
        onSlashBuiltinCommand={onSlashBuiltinCommand}
        compactActions={false}
      ></SendBox>
      <GuidProjectFilesModal
        visible={projectFilesOpen}
        projectId={projectId}
        onCancel={() => setProjectFilesOpen(false)}
        onSelect={handleProjectArtifactsSelected}
      />
      {isMobile && (
        <>
          <MobileActionSheet
            open={isMobileSheetOpen}
            onClose={() => setIsMobileSheetOpen(false)}
            title={t('common.more')}
            entries={sheetEntries}
          />
          {attachHiddenInput}
        </>
      )}
    </div>
  );
};

export default AcpSendBox;
