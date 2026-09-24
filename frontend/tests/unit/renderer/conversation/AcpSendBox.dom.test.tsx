/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { act, fireEvent, screen, waitFor, type RenderOptions } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import React from 'react';
import { MemoryRouter } from 'react-router';
import { BackendHttpError } from '@/common/adapter/httpBridge';
import type { TMessage } from '@/common/chat/chatLib';
import type { TConversationRuntimeSummary } from '@/common/config/storage';
import AcpSendBox from '@/renderer/pages/conversation/platforms/acp/AcpSendBox';
import type { UseAcpMessageReturn } from '@/renderer/pages/conversation/platforms/acp/useAcpMessage';
import type { ComposerContextItem } from '@/renderer/components/chat/SendBox/composerCompositionModel';
import { renderWithI18n } from '../../i18nTestUtils';

let settleDefaultSessionOptions: (() => Promise<void>) | null = null;
let settleDefaultFramePlan: (() => Promise<void>) | null = null;
let queuedCommandSequence = 0;
const sendBoxProps = {
  current: null as { allowSendWhileLoading?: boolean; bottomHint?: React.ReactNode } | null,
};

const render = async (ui: React.ReactElement, options?: RenderOptions) => {
  let view!: Awaited<ReturnType<typeof renderWithI18n>>;
  await act(async () => {
    view = await renderWithI18n(ui, 'zh-CN', {
      ...options,
      wrapper: ({ children }) => <MemoryRouter>{children}</MemoryRouter>,
    });
    await settleDefaultSessionOptions?.();
    await settleDefaultFramePlan?.();
    await Promise.resolve();
  });
  return view;
};

const createDeferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason?: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, reject, resolve };
};

const sendMessageInvokeMock = vi.fn();
const stopInvokeMock = vi.fn();
const addOrUpdateMessageMock = vi.fn();
const updateMessageListMock = vi.fn();
const removeMessageByMsgIdMock = vi.fn();
const resetStateMock = vi.fn();
const setAiProcessingMock = vi.fn();
const messageErrorMock = vi.fn();
const messageSuccessMock = vi.fn();
const loadFramePlanMock = vi.fn();
const resumeFrameMock = vi.fn();
const requestFrameAuditMock = vi.fn();
const notifyRuntimeInvalidationMock = vi.fn();
const loadSessionOptionsMock = vi.fn();
const updateSessionConfigMock = vi.fn();
const loadAssistantsMock = vi.fn();
const saveConversationAsSkillMock = vi.fn();
const loadComputeProvidersMock = vi.fn();
const loadSessionComputeProvidersMock = vi.fn();
const setSessionComputeProviderMock = vi.fn();
const loadComposerCapabilitiesMock = vi.fn();
const getSelectedBranchMock = vi.fn();
const getBranchRevisionMock = vi.fn();
const loadBranchStateMock = vi.fn();
const selectBranchMock = vi.fn();
const fileAttachProps = { current: null as Record<string, unknown> | null };
const draftContextItemsMock = { current: [] as ComposerContextItem[] };
const sessionOptionsProps = { current: null as Record<string, unknown> | null };
const messageListMock = { current: [] as TMessage[] };
const runtimeOperationsProps = {
  current: null as {
    runtimeState?: string;
    runtimeAuthorityUnavailable?: boolean;
    onRetryRuntimeAuthority?: () => void;
    onStop?: () => void | Promise<void>;
    onReviewActivityChange?: (active: boolean) => void;
    onResumed?: (turnId: string, runtime: TConversationRuntimeSummary) => void;
    onRuntimeUpdated?: (turnId: string, runtime: TConversationRuntimeSummary) => void;
    taskCenterMetrics?: {
      total: {
        tokenUsage: number | null;
        modelCallCount: number | null;
        toolCallCount: number | null;
      };
      latest: {
        tokenUsage: number | null;
        modelCallCount: number | null;
        toolCallCount: number | null;
      };
    };
  } | null,
};
const mobileActionSheetOpen = { current: false };
const runtimeViewMock = {
  activeTurnId: 'turn-1' as string | null,
  markStopRequested: vi.fn(),
  markStopAcknowledged: vi.fn(),
  resetLocalGate: vi.fn(),
  markSendStarted: vi.fn(),
  markSendAccepted: vi.fn(),
  markSendFailed: vi.fn(),
  isProcessing: true,
  canSendMessage: false,
  hydrated: true,
  hydrationError: null as string | null,
  retryHydration: vi.fn(),
  state: 'running',
  view: {
    localStopping: false,
    hasBackendRuntime: true,
    hasTask: true,
    taskStatus: 'running' as 'pending' | 'running' | 'finished' | 'error' | 'cancelled' | null,
  },
};
const emitterEmitMock = vi.fn();
const setSendBoxHandlerMock = vi.fn();
const useAcpConfigOptionsMock = vi.fn();
const useAcpModelInfoMock = vi.fn();
const askSideQuestionMock = vi.fn();
const isMobileMock = { current: false };
const mobileActionSheetEntries = {
  current: [] as Array<{
    key: string;
    label?: React.ReactNode;
    description?: React.ReactNode;
    onClick?: () => void;
    submenu?: { onSelect?: (value: string) => void };
  }>,
};

vi.mock('@/common', () => ({
  ipcBridge: {
    acpConversation: {
      sendMessage: {
        invoke: (...args: unknown[]) => sendMessageInvokeMock(...args),
      },
    },
    conversation: {
      stop: {
        invoke: (...args: unknown[]) => stopInvokeMock(...args),
      },
    },
  },
}));

vi.mock('@/renderer/components/chat/SendBox', () => ({
  default: ({
    onSend,
    onChange,
    rightTools,
    placeholder,
    bottomHint,
    onStop,
    onMobilePlusClick,
    tools,
    disabled,
    allowSendWhileLoading,
  }: {
    onSend: (message: string) => Promise<void>;
    onStop?: () => Promise<void>;
    onChange?: (value: string) => void;
    rightTools?: React.ReactNode;
    placeholder?: string;
    bottomHint?: React.ReactNode;
    onMobilePlusClick?: () => void;
    tools?: React.ReactNode;
    disabled?: boolean;
    allowSendWhileLoading?: boolean;
  }) => (
    <div data-allow-send-while-loading={allowSendWhileLoading ? 'true' : 'false'}>
      {tools}
      {rightTools}
      <output data-testid='sendbox-placeholder'>{placeholder}</output>
      <output data-testid='sendbox-bottom-hint'>{bottomHint ?? ''}</output>
      <button type='button' onClick={() => onChange?.('hello')}>
        change
      </button>
      <button type='button' onClick={onMobilePlusClick}>
        mobile more
      </button>
      <button
        type='button'
        disabled={disabled}
        onClick={() => {
          onChange?.('');
          void onSend('Hello').catch(() => {});
        }}
      >
        send
      </button>
      {onStop ? (
        <button
          type='button'
          onClick={() => {
            void onStop();
          }}
        >
          stop
        </button>
      ) : null}
    </div>
  ),
}));

vi.mock('@/renderer/components/chat/BtwOverlay', () => ({
  default: () => null,
}));
vi.mock('@/renderer/components/chat/BtwOverlay/useBtwCommand', () => ({
  useBtwCommand: () => ({
    answer: '',
    isLoading: false,
    isOpen: false,
    question: '',
    ask: (...args: unknown[]) => askSideQuestionMock(...args),
    dismiss: vi.fn(),
  }),
}));
vi.mock('@/renderer/components/synonBiomed/runtime/SynonBiomedModelSelector', () => ({
  default: ({ conversation_id }: { conversation_id: string }) => (
    <button type='button' data-testid='composer-model-selector'>
      模型 {conversation_id}
    </button>
  ),
}));
vi.mock('@/renderer/components/synonBiomed/runtime/SynonBiomedSessionOptionsMenu', () => ({
  default: (props: Record<string, unknown>) => {
    sessionOptionsProps.current = props;
    return null;
  },
}));
vi.mock('@/renderer/components/synonBiomed/runtime/SynonBiomedBranchMenu', () => ({
  default: ({ rootFrameId }: { rootFrameId: string }) => (
    <button data-testid='branch-menu' data-root-frame-id={rootFrameId}>
      branches
    </button>
  ),
}));
vi.mock('@/renderer/components/synonBiomed/runtime/SynonBiomedRuntimeOperations', () => ({
  default: (props: {
    runtimeState?: string;
    runtimeAuthorityUnavailable?: boolean;
    onRetryRuntimeAuthority?: () => void;
    onStop?: () => void | Promise<void>;
    onReviewActivityChange?: (active: boolean) => void;
    onResumed?: (turnId: string, runtime: TConversationRuntimeSummary) => void;
    onRuntimeUpdated?: (turnId: string, runtime: TConversationRuntimeSummary) => void;
    taskCenterMetrics?: {
      total: {
        tokenUsage: number | null;
        modelCallCount: number | null;
        toolCallCount: number | null;
      };
      latest: {
        tokenUsage: number | null;
        modelCallCount: number | null;
        toolCallCount: number | null;
      };
    };
  }) => {
    runtimeOperationsProps.current = props;
    return null;
  },
}));
vi.mock('@/renderer/services/synonBiomedRuntimeOperations', () => ({
  loadSynonBiomedFramePlanArtifact: (...args: unknown[]) => loadFramePlanMock(...args),
  notifySynonBiomedRuntimeInvalidation: (...args: unknown[]) => notifyRuntimeInvalidationMock(...args),
  resumeSynonBiomedFrame: (...args: unknown[]) => resumeFrameMock(...args),
}));
vi.mock('@/renderer/services/skills/synonBiomedSkillLibrary', () => ({
  saveSynonBiomedConversationAsSkill: (...args: unknown[]) => saveConversationAsSkillMock(...args),
}));
vi.mock('@/renderer/services/synonBiomedAnnotations', () => ({
  requestSynonBiomedFrameAudit: (...args: unknown[]) => requestFrameAuditMock(...args),
}));
vi.mock('@/renderer/components/chat/CommandQueuePanel', () => ({
  default: () => null,
}));
vi.mock('@/renderer/components/chat/MobileActionSheet', () => ({
  default: ({
    entries,
    open,
  }: {
    entries?: Array<{
      key: string;
      submenu?: {
        onSelect?: (value: string) => void;
      };
    }>;
  }) => {
    mobileActionSheetEntries.current = entries ?? [];
    mobileActionSheetOpen.current = open ?? false;
    return null;
  },
  useAttachEntry: () => ({ entries: [], hiddenFileInput: null }),
}));
vi.mock('@/renderer/components/chat/ThoughtDisplay', () => ({
  default: () => null,
}));
vi.mock('@/renderer/components/media/FileAttachButton', () => ({
  default: (props: Record<string, unknown>) => {
    fileAttachProps.current = props;
    return null;
  },
}));
vi.mock('@/renderer/components/media/FilePreview', () => ({
  default: () => null,
}));
vi.mock('@/renderer/components/media/HorizontalFileList', () => ({
  default: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}));
vi.mock('@/renderer/services/synonBiomedGateway', () => ({
  loadSynonBiomedComposerCapabilities: (...args: unknown[]) => loadComposerCapabilitiesMock(...args),
}));
vi.mock('@/renderer/hooks/synonBiomed/runtime/useAcpModelInfo', () => ({
  useAcpModelInfo: (...args: unknown[]) => useAcpModelInfoMock(...args),
}));
vi.mock('@/renderer/hooks/synonBiomed/runtime/useAcpConfigOptions', () => ({
  classifyConfigSetError: () => 'unknown',
  useAcpConfigOptions: (...args: unknown[]) => useAcpConfigOptionsMock(...args),
}));
// The mocked draft hook keeps content in React state so typing flows through
// the composer like it does in production; tests seed it via
// sendBoxDraftSeed before rendering.
const sendBoxDraftSeed = { current: '' };
vi.mock('@/renderer/hooks/chat/useSendBoxDraft', () => ({
  getSendBoxDraftHook: () => () => {
    const [content, setContentState] = React.useState(sendBoxDraftSeed.current);
    return {
      data: {
        atPath: [],
        uploadFile: [],
        content,
        contextItems: draftContextItemsMock.current,
      },
      mutate: (updater: (current: { content: string }) => { content: string } | undefined) => {
        setContentState((current) => {
          const next = updater({ content: current });
          return next === undefined ? current : next.content;
        });
      },
    };
  },
}));
vi.mock('@/renderer/hooks/chat/useSendBoxFiles', () => ({
  useSendBoxFiles: () => ({
    handleFilesAdded: vi.fn(),
    clearFiles: vi.fn(),
  }),
  createSetUploadFile: () => vi.fn(),
}));
vi.mock('@/renderer/hooks/chat/useAutoTitle', () => ({
  useAutoTitle: () => ({
    checkAndUpdateTitle: vi.fn(),
  }),
}));
vi.mock('@/renderer/hooks/context/ConversationContext', () => ({
  useConversationContextSafe: () => null,
}));
vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: isMobileMock.current }),
}));
vi.mock('@/renderer/hooks/file/useOpenFileSelector', () => ({
  useOpenFileSelector: () => ({
    openFileSelector: vi.fn(),
    onSlashBuiltinCommand: vi.fn(),
  }),
}));
vi.mock('@/renderer/hooks/ui/useLatestRef', () => ({
  useLatestRef: <T,>(value: T) => ({ current: value }),
}));
vi.mock('@/renderer/pages/conversation/Messages/hooks', () => ({
  useAddOrUpdateMessage: () => addOrUpdateMessageMock,
  useMessageList: () => messageListMock.current,
  useUpdateMessageList: () => updateMessageListMock,
  useRemoveMessageByMsgId: () => removeMessageByMsgIdMock,
}));
vi.mock('@/renderer/pages/conversation/platforms/useConversationCommandQueue', () => ({
  createQueuedCommandItem: (input: Record<string, unknown>) => ({
    id: `command-${++queuedCommandSequence}`,
    ...input,
  }),
  shouldEnqueueConversationCommand: () => false,
  useConversationCommandQueue: () => ({
    items: [],
    isPaused: false,
    isInteractionLocked: false,
    hasPendingCommands: false,
    enqueue: vi.fn(),
    remove: vi.fn(),
    clear: vi.fn(),
    reorder: vi.fn(),
    pause: vi.fn(),
    resume: vi.fn(),
    lockInteraction: vi.fn(),
    unlockInteraction: vi.fn(),
    resetActiveExecution: vi.fn(),
  }),
}));
vi.mock('@/renderer/services/synonBiomedConversationBranches', () => ({
  getSelectedSynonBiomedBranch: (...args: unknown[]) => getSelectedBranchMock(...args),
  getSynonBiomedBranchSelectionRevision: (...args: unknown[]) => getBranchRevisionMock(...args),
  loadSynonBiomedConversationBranches: (...args: unknown[]) => loadBranchStateMock(...args),
  selectSynonBiomedBranch: (...args: unknown[]) => selectBranchMock(...args),
}));
vi.mock('@/renderer/pages/conversation/runtime/useConversationRuntimeView', () => ({
  useConversationRuntimeView: () => runtimeViewMock,
}));
vi.mock('@/renderer/pages/conversation/Preview/context/PreviewContext', () => ({
  usePreviewContext: () => ({
    setSendBoxHandler: setSendBoxHandlerMock,
  }),
}));
vi.mock('@/renderer/services/FileService', () => ({
  allSupportedExts: [],
}));
vi.mock('@/renderer/services/synonBiomedSessionOptions', () => ({
  createSynonBiomedSessionDefaults: (targetAgent = 'OPERON') => ({
    delegation: false,
    autoReview: false,
    memory: false,
    targetAgent,
    goalText: null,
    asRoutine: false,
  }),
  loadSynonBiomedSessionOptions: (...args: unknown[]) => loadSessionOptionsMock(...args),
  toSynonBiomedMessageSessionOptions: (
    value: Record<string, unknown>,
    overrides?: {
      planMode?: boolean;
      targetBranchId?: string | null;
      expectedBranchId?: string | null;
      expectedGeneration?: number;
    }
  ) => ({
    ...value,
    ...(overrides?.planMode === undefined ? {} : { plan_mode: overrides.planMode }),
    ...(overrides?.targetBranchId ? { target_branch_id: overrides.targetBranchId } : {}),
    ...(overrides?.expectedBranchId ? { expected_branch_id: overrides.expectedBranchId } : {}),
    ...(overrides?.expectedGeneration ? { expected_generation: overrides.expectedGeneration } : {}),
  }),
  updateSynonBiomedSessionConfig: (...args: unknown[]) => updateSessionConfigMock(...args),
}));
vi.mock('@/renderer/services/synonBiomedCatalog', () => ({
  loadSynonBiomedAssistants: (...args: unknown[]) => loadAssistantsMock(...args),
}));
vi.mock('@/renderer/services/synonBiomedCompute', () => ({
  loadSynonBiomedComputeProviders: (...args: unknown[]) => loadComputeProvidersMock(...args),
  loadSynonBiomedSessionComputeProviders: (...args: unknown[]) => loadSessionComputeProvidersMock(...args),
  setSynonBiomedSessionComputeProvider: (...args: unknown[]) => setSessionComputeProviderMock(...args),
}));
vi.mock('@/renderer/utils/emitter', () => ({
  emitter: {
    emit: (...args: unknown[]) => emitterEmitMock(...args),
  },
  useAddEventListener: vi.fn(),
}));
vi.mock('@/renderer/utils/file/fileSelection', () => ({
  mergeFileSelectionItems: vi.fn(),
}));
vi.mock('@/renderer/utils/file/messageFiles', () => ({
  buildDisplayMessage: (input: string) => input,
}));
vi.mock('@/renderer/pages/conversation/platforms/acp/useAcpInitialMessage', () => ({
  useAcpInitialMessage: vi.fn(),
}));
vi.mock('@arco-design/web-react', () => ({
  Spin: ({ children }: { children?: React.ReactNode }) => <span>{children}</span>,
  Popover: ({
    children,
    content,
    popupVisible,
  }: {
    children?: React.ReactNode;
    content?: React.ReactNode;
    popupVisible?: boolean;
  }) => (
    <>
      {children}
      {popupVisible ? content : null}
    </>
  ),
  Dropdown: ({
    children,
    droplist,
    popupVisible,
  }: {
    children?: React.ReactNode;
    droplist?: React.ReactNode;
    popupVisible?: boolean;
  }) => (
    <>
      {children}
      {popupVisible ? droplist : null}
    </>
  ),
  Tooltip: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
  Modal: ({ visible, title, children }: { visible?: boolean; title?: React.ReactNode; children?: React.ReactNode }) =>
    visible ? (
      <div role='dialog' aria-label={typeof title === 'string' ? title : undefined}>
        {children}
      </div>
    ) : null,
  Button: ({
    children,
    disabled,
    onClick,
    'aria-label': ariaLabel,
  }: {
    children?: React.ReactNode;
    disabled?: boolean;
    onClick?: () => void;
    'aria-label'?: string;
  }) => (
    <button type='button' aria-label={ariaLabel} disabled={disabled} onClick={onClick}>
      {children}
    </button>
  ),
  Message: {
    success: (...args: unknown[]) => messageSuccessMock(...args),
    error: (...args: unknown[]) => messageErrorMock(...args),
    useMessage: () => [{ success: vi.fn(), error: vi.fn() }, null],
  },
  Tag: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}));

const makeMessageState = (): UseAcpMessageReturn => ({
  thought: { subject: '', description: '' },
  setThought: vi.fn(),
  running: true,
  hasHydratedRunningState: true,
  acpStatus: null,
  aiProcessing: false,
  setAiProcessing: setAiProcessingMock,
  resetState: resetStateMock,
  tokenUsage: null,
  context_limit: 0,
  hasThinkingMessage: false,
  slashCommands: [],
  fetchSlashCommands: vi.fn(),
});

describe('AcpSendBox', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    queuedCommandSequence = 0;
    sendBoxDraftSeed.current = '';
    sendBoxProps.current = null;
    isMobileMock.current = false;
    mobileActionSheetEntries.current = [];
    mobileActionSheetOpen.current = false;
    fileAttachProps.current = null;
    draftContextItemsMock.current = [];
    sessionOptionsProps.current = null;
    messageListMock.current = [];
    runtimeOperationsProps.current = null;
    const defaultFramePlan = createDeferred<null>();
    loadFramePlanMock.mockReturnValue(defaultFramePlan.promise);
    settleDefaultFramePlan = async () => {
      defaultFramePlan.resolve(null);
      await defaultFramePlan.promise;
    };
    requestFrameAuditMock.mockResolvedValue({ accepted: true });
    const defaultSessionOptions = createDeferred<Record<string, unknown>>();
    const defaultSessionOptionsValue = {
      delegation: false,
      autoReview: false,
      memory: false,
      targetAgent: 'OPERON',
      goalText: null,
      asRoutine: false,
    };
    loadSessionOptionsMock.mockReturnValue(defaultSessionOptions.promise);
    settleDefaultSessionOptions = async () => {
      defaultSessionOptions.resolve(defaultSessionOptionsValue);
      await defaultSessionOptions.promise;
    };
    updateSessionConfigMock.mockResolvedValue(undefined);
    loadAssistantsMock.mockImplementation(() => new Promise(() => {}));
    saveConversationAsSkillMock.mockResolvedValue({ name: 'session-skill', scope: 'personal' });
    loadComputeProvidersMock.mockImplementation(() => new Promise(() => {}));
    loadSessionComputeProvidersMock.mockImplementation(() => new Promise(() => {}));
    setSessionComputeProviderMock.mockResolvedValue(undefined);
    loadComposerCapabilitiesMock.mockResolvedValue({ skills: [], mcpStatuses: [] });
    getSelectedBranchMock.mockReturnValue(null);
    getBranchRevisionMock.mockReturnValue(0);
    loadBranchStateMock.mockResolvedValue(null);
    askSideQuestionMock.mockResolvedValue(undefined);
    runtimeViewMock.activeTurnId = 'turn-1';
    runtimeViewMock.isProcessing = true;
    runtimeViewMock.canSendMessage = false;
    runtimeViewMock.hydrated = true;
    runtimeViewMock.hydrationError = null;
    runtimeViewMock.state = 'running';
    runtimeViewMock.view.hasTask = true;
    runtimeViewMock.view.hasBackendRuntime = true;
    runtimeViewMock.view.taskStatus = 'running';
    resumeFrameMock.mockResolvedValue({
      snapshot: { rootFrameId: 'conv-1' },
      runtime: {
        state: 'starting',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'turn-resumed',
      },
    });
    stopInvokeMock.mockResolvedValue({
      runtime: {
        state: 'idle',
        can_send_message: true,
        has_task: true,
        task_status: 'cancelled',
        is_processing: false,
        pending_confirmations: 0,
        turn_id: null,
      },
    });
    useAcpConfigOptionsMock.mockReturnValue({
      setStatus: { state: 'idle' },
      mode: null,
      model: null,
      thoughtLevel: null,
      reload: vi.fn(),
      setConfigOption: vi.fn(),
    });
    useAcpModelInfoMock.mockReturnValue({
      model_info: null,
      canSwitch: false,
      isModelListLoading: false,
      modelListError: null,
      retryModelList: vi.fn().mockResolvedValue(undefined),
      selectModel: vi.fn(),
    });
  });

  it('blocks sending and delegates runtime recovery to the permanent status center', async () => {
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = false;
    runtimeViewMock.hydrationError = 'gateway unavailable';
    loadFramePlanMock.mockImplementationOnce(() => new Promise(() => {}));

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(runtimeOperationsProps.current?.runtimeAuthorityUnavailable).toBe(true);
    expect(screen.queryByRole('alert', { name: '运行状态不可用' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'send' })).toBeDisabled();
    runtimeOperationsProps.current?.onRetryRuntimeAuthority?.();
    expect(runtimeViewMock.retryHydration).toHaveBeenCalledOnce();
    expect(sendMessageInvokeMock).not.toHaveBeenCalled();
  });

  it('delegates the runtime-authoritative task state and stop action to the single status center', async () => {
    runtimeViewMock.isProcessing = true;
    runtimeViewMock.state = 'running';
    const messageState = makeMessageState();
    messageState.aiProcessing = false;
    messageState.tokenUsage = { total_tokens: 24_576 };
    messageState.context_limit = 131_072;
    messageListMock.current = [
      {
        id: 'user-task-1',
        msg_id: 'user-task-1',
        type: 'text',
        position: 'right',
        conversation_id: 'conv-1',
        created_at: 1,
        content: { content: 'Run the task' },
      } as TMessage,
      {
        id: 'tool-1',
        msg_id: 'tool-1',
        type: 'tool_call',
        position: 'left',
        conversation_id: 'conv-1',
        created_at: 2,
        content: { call_id: 'call-1', name: 'bash', args: {}, status: 'completed' },
      } as TMessage,
    ];

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={messageState} />);

    expect(runtimeOperationsProps.current?.runtimeState).toBe('running');
    expect(runtimeOperationsProps.current?.onStop).toBeTypeOf('function');
    expect(runtimeOperationsProps.current?.taskCenterMetrics).toEqual({
      total: { tokenUsage: 24_576, modelCallCount: null, toolCallCount: 1 },
      latest: { tokenUsage: 24_576, modelCallCount: null, toolCallCount: 1 },
    });
    expect(screen.queryByTestId('synon-biomed-runtime-status')).not.toBeInTheDocument();
  });

  it('keeps the composer available for queued follow-up tasks while the current task runs', async () => {
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(screen.getByTestId('sendbox-bottom-hint')).toHaveTextContent('');
    expect(screen.getByTestId('sendbox-placeholder').parentElement).toHaveAttribute(
      'data-allow-send-while-loading',
      'true'
    );
  });

  it('keeps the composer disabled until the initial runtime authority is hydrated', async () => {
    runtimeViewMock.hydrated = false;
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = true;
    loadFramePlanMock.mockImplementationOnce(() => new Promise(() => {}));

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(screen.getByRole('button', { name: 'send' })).toBeDisabled();
    expect(screen.queryByRole('alert', { name: '运行状态不可用' })).not.toBeInTheDocument();
  });

  it('keeps the core composer available and retries when optional session settings fail', async () => {
    vi.useFakeTimers();
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = true;
    const failed = createDeferred<Record<string, unknown>>();
    loadSessionOptionsMock.mockReturnValueOnce(failed.promise).mockResolvedValueOnce({
      delegation: false,
      autoReview: false,
      memory: true,
      targetAgent: 'OPERON',
      goalText: null,
      asRoutine: false,
    });
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));

    try {
      await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);
      expect(screen.queryByText('正在加载会话选项')).not.toBeInTheDocument();
      await act(async () => {
        failed.reject(new Error('memory_request_failed_503'));
        await failed.promise.catch(() => undefined);
      });

      expect(screen.queryByRole('alert', { name: '无法加载会话选项。' })).not.toBeInTheDocument();
      expect(screen.queryByText('正在加载会话选项')).not.toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'send' })).toBeEnabled();

      await act(async () => {
        await vi.advanceTimersByTimeAsync(1_000);
      });
      expect(loadSessionOptionsMock).toHaveBeenCalledTimes(2);
      expect(sessionOptionsProps.current?.value).toMatchObject({ memory: true });
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not apply stale session options after switching conversations', async () => {
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = true;
    const stale = createDeferred<Record<string, unknown>>();
    const current = createDeferred<Record<string, unknown>>();
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    loadSessionOptionsMock.mockImplementation((frameId: string) =>
      frameId === 'conv-1' ? stale.promise : current.promise
    );

    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    await act(async () => {
      current.resolve({
        delegation: false,
        autoReview: true,
        memory: true,
        targetAgent: 'OPERON',
        goalText: null,
        asRoutine: false,
      });
      await current.promise;
    });
    await waitFor(() => expect(sessionOptionsProps.current?.value).toMatchObject({ memory: true }));

    await act(async () => {
      stale.resolve({
        delegation: false,
        autoReview: true,
        memory: false,
        targetAgent: 'OPERON',
        goalText: null,
        asRoutine: false,
      });
      await stale.promise;
    });

    expect(sessionOptionsProps.current?.value).toMatchObject({ memory: true });
  });

  it('keeps the task running and exposes recovery when stop fails', async () => {
    stopInvokeMock.mockRejectedValueOnce(new Error('gateway unavailable'));

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    await act(async () => {
      await runtimeOperationsProps.current?.onStop?.();
    });

    expect(runtimeViewMock.markStopRequested).toHaveBeenCalledWith('turn-1');
    expect(runtimeViewMock.resetLocalGate).toHaveBeenCalledWith('stop_failed');
    expect(resetStateMock).not.toHaveBeenCalled();
    expect(setAiProcessingMock).not.toHaveBeenCalled();
    expect(messageErrorMock).toHaveBeenCalledWith('停止请求失败，任务仍可能继续运行。请重试或刷新运行状态。');
  });

  it('exposes one authoritative cancellation handler through the status center only', async () => {
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(screen.queryByRole('button', { name: 'stop' })).not.toBeInTheDocument();
    await act(async () => {
      await runtimeOperationsProps.current?.onStop?.();
    });

    expect(runtimeOperationsProps.current?.onStop).toBeTypeOf('function');
    expect(stopInvokeMock).toHaveBeenCalledWith({
      conversation_id: 'conv-1',
      turn_id: 'turn-1',
    });
    expect(runtimeViewMock.markStopRequested).toHaveBeenCalledWith('turn-1');
    expect(runtimeViewMock.markStopAcknowledged).toHaveBeenCalledOnce();
  });

  it('hydrates authoritative messages after a runtime mutation completes', async () => {
    const onRuntimeMutation = vi.fn();
    await render(
      <AcpSendBox
        conversation_id='conv-1'
        backend='synonbiomed'
        messageState={makeMessageState()}
        onRuntimeMutation={onRuntimeMutation}
      />
    );

    act(() => {
      runtimeOperationsProps.current?.onResumed?.('turn-2', {
        state: 'failed',
        can_send_message: true,
        has_task: true,
        task_status: 'failed',
        is_processing: false,
        pending_confirmations: 0,
        turn_id: 'turn-2',
      });
    });

    expect(onRuntimeMutation).toHaveBeenCalledOnce();
    expect(runtimeViewMock.markSendAccepted).toHaveBeenCalledWith(
      'turn-2',
      expect.objectContaining({ state: 'failed' })
    );
  });

  it('applies a passive runtime refresh through the authoritative conversation gate', async () => {
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    act(() => {
      runtimeOperationsProps.current?.onRuntimeUpdated?.('turn-3', {
        state: 'idle',
        can_send_message: true,
        has_task: false,
        task_status: 'finished',
        is_processing: false,
        pending_confirmations: 0,
        turn_id: null,
      });
    });

    expect(runtimeViewMock.markSendAccepted).toHaveBeenCalledWith(
      'turn-3',
      expect.objectContaining({ state: 'idle', task_status: 'finished' })
    );
    expect(emitterEmitMock).toHaveBeenCalledWith(
      'synonbiomed.runtime.reconciled',
      'conv-1',
      expect.objectContaining({ state: 'idle', task_status: 'finished' })
    );
  });

  it('removes resume from session settings and leaves recovery to runtime operations', async () => {
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = true;
    runtimeViewMock.state = 'idle';
    runtimeViewMock.view.hasTask = false;
    runtimeViewMock.view.hasBackendRuntime = true;
    runtimeViewMock.view.taskStatus = 'error';
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(sessionOptionsProps.current).not.toHaveProperty('onResume');
    expect(sessionOptionsProps.current).not.toHaveProperty('resumeInFlight');
    expect(runtimeOperationsProps.current?.onResumed).toBeTypeOf('function');
    expect(resumeFrameMock).not.toHaveBeenCalled();
  });

  it('keeps polling while the backend acknowledges a cancelling runtime', async () => {
    stopInvokeMock.mockResolvedValueOnce({
      runtime: {
        state: 'cancelling',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        pending_confirmations: 0,
        turn_id: 'turn-1',
      },
    });

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    await act(async () => {
      await runtimeOperationsProps.current?.onStop?.();
    });

    expect(runtimeViewMock.markStopAcknowledged).toHaveBeenCalledWith(
      'turn-1',
      expect.objectContaining({ state: 'cancelling', is_processing: true })
    );
    expect(setAiProcessingMock).toHaveBeenCalledWith(true);
    expect(resetStateMock).not.toHaveBeenCalled();
  });

  it('does not apply a completed stop request to a replacement conversation', async () => {
    const pendingStop = createDeferred<{ runtime: Record<string, unknown> }>();
    stopInvokeMock.mockReturnValueOnce(pendingStop.promise);
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );

    await act(async () => {
      void runtimeOperationsProps.current?.onStop?.();
      await Promise.resolve();
    });
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    runtimeViewMock.markStopAcknowledged.mockClear();
    setAiProcessingMock.mockClear();
    resetStateMock.mockClear();

    await act(async () => {
      pendingStop.resolve({
        runtime: {
          state: 'idle',
          can_send_message: true,
          has_task: true,
          task_status: 'cancelled',
          is_processing: false,
          pending_confirmations: 0,
          turn_id: null,
        },
      });
      await pendingStop.promise;
    });

    expect(runtimeViewMock.markStopAcknowledged).not.toHaveBeenCalled();
    expect(setAiProcessingMock).not.toHaveBeenCalled();
    expect(resetStateMock).not.toHaveBeenCalled();
  });

  it('does not expose a failed stop request after switching conversations', async () => {
    const pendingStop = createDeferred<never>();
    stopInvokeMock.mockReturnValueOnce(pendingStop.promise);
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );

    await act(async () => {
      void runtimeOperationsProps.current?.onStop?.();
      await Promise.resolve();
    });
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    runtimeViewMock.resetLocalGate.mockClear();
    messageErrorMock.mockClear();

    await act(async () => {
      pendingStop.reject(new Error('old stop failed'));
      await pendingStop.promise.catch(() => undefined);
    });

    expect(runtimeViewMock.resetLocalGate).not.toHaveBeenCalled();
    expect(messageErrorMock).not.toHaveBeenCalled();
  });

  it('does not fake a stopped state when the active turn id is unavailable', async () => {
    runtimeViewMock.activeTurnId = null;

    await render(<AcpSendBox conversation_id='conv-1' backend='claude' messageState={makeMessageState()} />);

    expect(screen.queryByTestId('synon-biomed-context-usage-trigger')).not.toBeInTheDocument();

    await act(async () => {
      await runtimeOperationsProps.current?.onStop?.();
    });

    expect(stopInvokeMock).not.toHaveBeenCalled();
    expect(runtimeViewMock.resetLocalGate).toHaveBeenCalledWith('stop_missing_turn');
    expect(resetStateMock).not.toHaveBeenCalled();
    expect(messageErrorMock).toHaveBeenCalledWith('当前任务缺少运行标识，无法停止。请刷新运行状态后重试。');
  });

  it('cancels a Synon Biomed frame when the live task is ahead of the conversation turn projection', async () => {
    runtimeViewMock.activeTurnId = null;

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    await act(async () => {
      await runtimeOperationsProps.current?.onStop?.();
    });

    expect(stopInvokeMock).toHaveBeenCalledWith({
      conversation_id: 'conv-1',
      turn_id: 'conv-1',
    });
    expect(runtimeViewMock.markStopRequested).toHaveBeenCalledWith('conv-1');
    expect(runtimeViewMock.markStopAcknowledged).toHaveBeenCalledOnce();
    expect(messageErrorMock).not.toHaveBeenCalled();
  });

  it('resets ACP loading state when sendMessage fails before any stream error arrives', async () => {
    sendMessageInvokeMock.mockRejectedValue(
      new BackendHttpError({
        method: 'POST',
        path: '/api/conversations/conv-1/messages',
        status: 400,
        body: {
          success: false,
          code: 'WORKSPACE_PATH_RUNTIME_UNAVAILABLE',
          error: 'Workspace path is unavailable during execution: /tmp/missing',
          details: { workspace_path: '/tmp/missing' },
        },
      })
    );

    await render(
      <AcpSendBox
        conversation_id='conv-1'
        backend='claude'
        workspacePath='/tmp/missing'
        messageState={makeMessageState()}
      />
    );
    await waitFor(() => expect(screen.getByRole('button', { name: 'send' })).toBeEnabled());

    await act(async () => {
      screen.getByRole('button', { name: 'send' }).click();
    });

    await waitFor(() => {
      expect(resetStateMock).toHaveBeenCalledTimes(1);
    });
  });

  it('adds authentication failures to the local conversation without using a realtime event bus', async () => {
    const authenticationError = new Error('[ACP-AUTH-401] authentication failed');
    sendMessageInvokeMock.mockRejectedValueOnce(authenticationError);

    await act(async () => {
      await render(<AcpSendBox conversation_id='conv-1' backend='codex' messageState={makeMessageState()} />);
    });

    await act(async () => {
      screen.getByRole('button', { name: 'send' }).click();
    });

    await waitFor(() =>
      expect(addOrUpdateMessageMock.mock.calls.filter(([message]) => message?.type === 'tips')).toHaveLength(1)
    );
    expect(addOrUpdateMessageMock).toHaveBeenCalledWith(
      expect.objectContaining({
        conversation_id: 'conv-1',
        position: 'center',
        type: 'tips',
        content: expect.objectContaining({
          content: expect.stringContaining('codex 认证失败'),
          error: expect.objectContaining({
            message: expect.stringContaining('[ACP-AUTH-401]'),
          }),
          type: 'error',
        }),
      }),
      true
    );
    expect(emitterEmitMock).not.toHaveBeenCalledWith(expect.stringContaining('response'), expect.anything());
    expect(runtimeViewMock.markSendFailed).toHaveBeenCalledWith('[ACP-AUTH-401] authentication failed');
    expect(resetStateMock).toHaveBeenCalledTimes(1);
    expect(setAiProcessingMock).toHaveBeenLastCalledWith(false);
  });

  it('redacts secrets and personal identifiers from local send failure details', async () => {
    sendMessageInvokeMock.mockRejectedValueOnce(
      new Error('provider rejected sk-live-secret-value for alice@example.com token=private-token-value')
    );
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'send' })).toBeEnabled());

    await act(async () => {
      screen.getByRole('button', { name: 'send' }).click();
    });
    await waitFor(() =>
      expect(addOrUpdateMessageMock.mock.calls.filter(([message]) => message?.type === 'tips')).toHaveLength(1)
    );

    const failureCall = addOrUpdateMessageMock.mock.calls.find(([message]) => message?.type === 'tips');
    expect(failureCall).toBeDefined();
    const payload = JSON.stringify(failureCall);
    expect(payload).toContain('[REDACTED_KEY]');
    expect(payload).toContain('[email]');
    expect(payload).not.toContain('live-secret-value');
    expect(payload).not.toContain('alice@example.com');
    expect(payload).not.toContain('private-token-value');
  });

  it('redacts a failed stop diagnostic before writing renderer logs', async () => {
    const consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    stopInvokeMock.mockRejectedValueOnce(
      new Error('stop failed for sk-live-secret-value and alice@example.com token=private-token-value')
    );
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    try {
      await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

      await act(async () => {
        await runtimeOperationsProps.current?.onStop?.();
      });

      const diagnostic = JSON.stringify(consoleWarn.mock.calls);
      expect(diagnostic).toContain('[REDACTED_KEY]');
      expect(diagnostic).toContain('[email]');
      expect(diagnostic).not.toContain('live-secret-value');
      expect(diagnostic).not.toContain('alice@example.com');
      expect(diagnostic).not.toContain('private-token-value');
    } finally {
      consoleWarn.mockRestore();
    }
  });

  it('does not apply a completed send request to a replacement conversation', async () => {
    const pendingSend = createDeferred<{
      msg_id: string;
      turn_id: string;
      runtime: { state: string; is_processing: boolean };
    }>();
    sendMessageInvokeMock.mockReturnValueOnce(pendingSend.promise);
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );

    await act(async () => {
      screen.getByRole('button', { name: 'send' }).click();
      await Promise.resolve();
    });
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    runtimeViewMock.markSendAccepted.mockClear();
    emitterEmitMock.mockClear();

    await act(async () => {
      pendingSend.resolve({
        msg_id: 'message-old',
        turn_id: 'turn-old',
        runtime: { state: 'running', is_processing: true },
      });
      await pendingSend.promise;
    });

    expect(runtimeViewMock.markSendAccepted).not.toHaveBeenCalled();
    expect(emitterEmitMock).not.toHaveBeenCalledWith('chat.history.refresh');
  });

  it('does not expose a failed send request after switching conversations', async () => {
    const pendingSend = createDeferred<never>();
    sendMessageInvokeMock.mockReturnValueOnce(pendingSend.promise);
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );

    await act(async () => {
      screen.getByRole('button', { name: 'send' }).click();
      await Promise.resolve();
    });
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    runtimeViewMock.markSendFailed.mockClear();
    addOrUpdateMessageMock.mockClear();
    resetStateMock.mockClear();
    setAiProcessingMock.mockClear();

    await act(async () => {
      pendingSend.reject(new Error('old send failed'));
      await pendingSend.promise.catch(() => undefined);
    });

    expect(runtimeViewMock.markSendFailed).not.toHaveBeenCalled();
    expect(addOrUpdateMessageMock).not.toHaveBeenCalled();
    expect(resetStateMock).not.toHaveBeenCalled();
    expect(setAiProcessingMock).not.toHaveBeenCalled();
  });

  it('uses container-responsive fluid width instead of a fixed max width', async () => {
    await act(async () => {
      await render(
        <AcpSendBox
          conversation_id='conv-1'
          backend='codex'
          workspacePath='/tmp/workspace'
          messageState={makeMessageState()}
        />
      );
      await Promise.resolve();
    });

    const wrapper = screen.getByRole('button', { name: 'send' }).parentElement?.parentElement;
    expect(wrapper?.className).toContain('chat-surface-fluid');
    expect(wrapper?.className).toContain('conversation-composer');
    expect(wrapper?.className).toContain('box-border');
    expect(wrapper?.className).toContain('min-w-0');
    expect(wrapper?.className).not.toContain('w-[calc(100%-24px)]');
    expect(wrapper?.className).not.toContain('md:w-[calc(100%-clamp(80px,10vw,240px))]');
    expect(wrapper?.className).not.toContain('max-w-800px');
  });

  it('matches the v1.1 composer placeholder and keeps model selection beside the input', async () => {
    await act(async () => {
      await render(
        <AcpSendBox
          conversation_id='conv-1'
          backend='synonbiomed'
          agent_name='OPERON'
          workspacePath='/tmp/workspace'
          messageState={makeMessageState()}
        />
      );
      await Promise.resolve();
    });

    expect(screen.getByTestId('sendbox-placeholder')).toHaveTextContent(
      '输入你的问题 - @ 引用产物，# 引用会话，/ 调用 Skills，Ctrl+K 搜索...'
    );
    expect(screen.getByTestId('sendbox-bottom-hint')).toHaveTextContent('');
    expect(screen.getByTestId('composer-model-selector')).toHaveTextContent('模型 conv-1');
  });

  it('exposes one primary send path without a secondary send-mode menu', async () => {
    sendMessageInvokeMock.mockResolvedValueOnce({
      msg_id: 'message-1',
      turn_id: 'turn-1',
      runtime: { state: 'running', is_processing: true },
    });
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'send' })).toBeEnabled());

    expect(screen.getAllByRole('button', { name: 'send' })).toHaveLength(1);
    expect(screen.queryByRole('button', { name: 'Plan first' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: '侧边对话' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Branch in new session' })).not.toBeInTheDocument();

    await act(async () => {
      screen.getByRole('button', { name: 'send' }).click();
      await Promise.resolve();
    });

    expect(sendMessageInvokeMock).toHaveBeenCalledWith(
      expect.objectContaining({ conversation_id: 'conv-1', input: 'Hello' })
    );
  });

  it('promotes the accepted user message and removes its optimistic duplicate', async () => {
    sendMessageInvokeMock.mockResolvedValueOnce({
      msg_id: 'message-1',
      turn_id: 'turn-1',
      runtime: { state: 'running', is_processing: true },
    });
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    await act(async () => {
      screen.getByRole('button', { name: 'send' }).click();
      await Promise.resolve();
    });

    expect(removeMessageByMsgIdMock).toHaveBeenCalledWith(expect.stringMatching(/^synonbiomed-optimistic-user:/));
    expect(addOrUpdateMessageMock).toHaveBeenLastCalledWith(
      expect.objectContaining({
        id: 'message-1',
        msg_id: 'message-1',
        status: 'finish',
      }),
      false
    );
  });

  it('sends files, exact artifact versions, fixed Skills, and fixed MCP connectors through one request', async () => {
    draftContextItemsMock.current = [
      {
        kind: 'artifact',
        artifactId: 'artifact-1',
        versionId: 'version-2',
        label: 'cohort.csv',
        contentType: 'text/csv',
        sizeBytes: 42,
      },
      { kind: 'skill', name: 'single-cell-analysis', label: 'single-cell-analysis' },
      { kind: 'mcp', serverId: 'pubmed', label: 'PubMed' },
    ];
    sendMessageInvokeMock.mockResolvedValueOnce({
      msg_id: 'message-context',
      turn_id: 'turn-context',
      runtime: { state: 'running', is_processing: true },
    });
    await render(<AcpSendBox conversation_id='conv-context' backend='synonbiomed' messageState={makeMessageState()} />);

    await act(async () => {
      screen.getByRole('button', { name: 'send' }).click();
      await Promise.resolve();
    });

    expect(sendMessageInvokeMock).toHaveBeenCalledWith(
      expect.objectContaining({
        conversation_id: 'conv-context',
        input: 'Hello',
        artifact_refs: [
          expect.objectContaining({
            artifact_id: 'artifact-1',
            version_id: 'version-2',
            relation: 'attached',
          }),
        ],
        inject_skills: ['single-cell-analysis'],
        inject_mcp_server_ids: ['pubmed'],
      })
    );
  });

  it('loads conversation-authorized Skill and MCP identities into the composer picker', async () => {
    loadComposerCapabilitiesMock.mockResolvedValueOnce({
      skills: ['single-cell-analysis'],
      mcpStatuses: [{ id: 'pubmed-id', name: 'PubMed', status: 'loaded' }],
    });
    await render(
      <AcpSendBox conversation_id='conv-capabilities' backend='synonbiomed' messageState={makeMessageState()} />
    );

    await waitFor(() => {
      expect(fileAttachProps.current?.loadedSkills).toEqual(['single-cell-analysis']);
      expect(fileAttachProps.current?.loadedMcpStatuses).toEqual([
        { id: 'pubmed-id', name: 'PubMed', status: 'loaded' },
      ]);
    });
    expect(loadComposerCapabilitiesMock).toHaveBeenCalledWith('conv-capabilities');
  });

  it('binds a selected branch continuation to one stable queued command identity', async () => {
    getSelectedBranchMock.mockReturnValue('br_00000002');
    loadBranchStateMock.mockResolvedValue({
      activeBranchId: 'br_00000001',
      selectedBranchId: 'br_00000002',
      generation: 7,
      branches: [
        { id: 'br_00000001', active: true },
        { id: 'br_00000002', active: false },
      ],
    });
    sendMessageInvokeMock.mockResolvedValueOnce({
      msg_id: 'message-branch',
      turn_id: 'turn-branch',
      runtime: { state: 'running', is_processing: true },
    });
    await render(<AcpSendBox conversation_id='conv-branch' backend='synonbiomed' messageState={makeMessageState()} />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'send' })).toBeEnabled());

    await act(async () => {
      screen.getByRole('button', { name: 'send' }).click();
      await Promise.resolve();
    });

    expect(sendMessageInvokeMock).toHaveBeenCalledWith(
      expect.objectContaining({
        conversation_id: 'conv-branch',
        loading_id: 'command-1',
        session_options: expect.objectContaining({
          target_branch_id: 'br_00000002',
          expected_branch_id: 'br_00000001',
          expected_generation: 7,
        }),
      })
    );
  });

  it('reuses the direct command identity after an ambiguous response failure', async () => {
    sendMessageInvokeMock.mockRejectedValueOnce(new Error('response_lost_after_commit')).mockResolvedValueOnce({
      msg_id: 'message-retry',
      turn_id: 'turn-retry',
      runtime: { state: 'running', is_processing: true },
    });
    await render(<AcpSendBox conversation_id='conv-retry' backend='synonbiomed' messageState={makeMessageState()} />);
    await waitFor(() => expect(screen.getByRole('button', { name: 'send' })).toBeEnabled());

    screen.getByRole('button', { name: 'send' }).click();
    await waitFor(() => expect(sendMessageInvokeMock).toHaveBeenCalledTimes(1));
    screen.getByRole('button', { name: 'send' }).click();
    await waitFor(() => expect(sendMessageInvokeMock).toHaveBeenCalledTimes(2));

    const first = sendMessageInvokeMock.mock.calls[0]?.[0] as {
      loading_id?: string;
    };
    const second = sendMessageInvokeMock.mock.calls[1]?.[0] as {
      loading_id?: string;
    };
    expect(first.loading_id).toBe('command-1');
    expect(second.loading_id).toBe(first.loading_id);
    expect(queuedCommandSequence).toBe(1);
  });

  it('refreshes stale branch authority after an explicit conflict while preserving the mutation identity', async () => {
    getSelectedBranchMock.mockReturnValue('br_00000002');
    loadBranchStateMock
      .mockResolvedValueOnce({
        activeBranchId: 'br_00000001',
        selectedBranchId: 'br_00000002',
        generation: 7,
        branches: [
          { id: 'br_00000001', active: true },
          { id: 'br_00000002', active: false },
        ],
      })
      .mockResolvedValueOnce({
        activeBranchId: 'br_00000003',
        selectedBranchId: 'br_00000002',
        generation: 8,
        branches: [
          { id: 'br_00000002', active: false },
          { id: 'br_00000003', active: true },
        ],
      });
    sendMessageInvokeMock
      .mockRejectedValueOnce(
        new BackendHttpError({
          method: 'POST',
          path: '/api/conversations/conv-branch-retry/messages',
          status: 409,
          body: {
            code: 'CONFLICT',
            error: 'Conversation branch changed; refresh and retry',
          },
        })
      )
      .mockResolvedValueOnce({
        msg_id: 'message-branch-retry',
        turn_id: 'turn-branch-retry',
        runtime: { state: 'running', is_processing: true },
      });
    await render(
      <AcpSendBox conversation_id='conv-branch-retry' backend='synonbiomed' messageState={makeMessageState()} />
    );
    await waitFor(() => expect(screen.getByRole('button', { name: 'send' })).toBeEnabled());

    screen.getByRole('button', { name: 'send' }).click();
    await waitFor(() => expect(sendMessageInvokeMock).toHaveBeenCalledTimes(1));
    screen.getByRole('button', { name: 'send' }).click();
    await waitFor(() => expect(sendMessageInvokeMock).toHaveBeenCalledTimes(2));

    const first = sendMessageInvokeMock.mock.calls[0]?.[0] as {
      loading_id?: string;
      session_options?: Record<string, unknown>;
    };
    const second = sendMessageInvokeMock.mock.calls[1]?.[0] as {
      loading_id?: string;
      session_options?: Record<string, unknown>;
    };
    expect(first.loading_id).toBe('command-1');
    expect(second.loading_id).toBe(first.loading_id);
    expect(first.session_options).toMatchObject({
      target_branch_id: 'br_00000002',
      expected_branch_id: 'br_00000001',
      expected_generation: 7,
    });
    expect(second.session_options).toMatchObject({
      target_branch_id: 'br_00000002',
      expected_branch_id: 'br_00000003',
      expected_generation: 8,
    });
    expect(loadBranchStateMock).toHaveBeenCalledTimes(2);
    expect(queuedCommandSequence).toBe(1);
  });

  it('does not render the notebook entry in the composer', async () => {
    await act(async () => {
      await render(
        <AcpSendBox
          conversation_id='conv-1'
          backend='synonbiomed'
          workspacePath='/tmp/workspace'
          messageState={makeMessageState()}
        />
      );
      await Promise.resolve();
    });

    expect(screen.queryByRole('button', { name: '笔记本' })).not.toBeInTheDocument();
    expect(screen.queryByTestId('notebook-dock-badge')).not.toBeInTheDocument();
  });

  it('does not expose a composer-global branch selector on mobile', async () => {
    isMobileMock.current = true;
    await render(<AcpSendBox conversation_id='conv-mobile' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(screen.queryByTestId('branch-menu')).not.toBeInTheDocument();
  });

  it('projects model-list failure and retry into the existing mobile action sheet', async () => {
    const retryModelList = vi.fn().mockResolvedValue(undefined);
    isMobileMock.current = true;
    useAcpModelInfoMock.mockReturnValue({
      model_info: null,
      canSwitch: false,
      isModelListLoading: false,
      modelListError: new Error('api_key=mobile-secret-value'),
      retryModelList,
      selectModel: vi.fn(),
    });

    await act(async () => {
      await render(
        <AcpSendBox
          conversation_id='conv-1'
          backend='synonbiomed'
          workspacePath='/tmp/workspace'
          messageState={makeMessageState()}
        />
      );
      await Promise.resolve();
    });

    const modelEntry = mobileActionSheetEntries.current.find((entry) => entry.key === 'model');
    expect(modelEntry?.label).toBe('模型列表不可用');
    expect(String(modelEntry?.description)).not.toContain('mobile-secret-value');
    modelEntry?.onClick?.();
    await waitFor(() => expect(retryModelList).toHaveBeenCalledOnce());
  });

  it('keeps a read-only model identity in the mobile sheet when switching is unavailable', async () => {
    isMobileMock.current = true;

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(mobileActionSheetEntries.current.find((entry) => entry.key === 'model')).toMatchObject({
      label: '模型',
      meta: '选择模型',
      disabled: true,
    });
  });

  it('keeps plan access out of the mobile add sheet because the task status center owns it', async () => {
    isMobileMock.current = true;
    loadFramePlanMock.mockResolvedValue({
      artifactId: 'artifact-plan-1',
      versionId: 'version-1',
      filename: 'plan_conv-1.json',
      createdAt: '2026-07-14T00:00:00Z',
    });

    await render(
      <AcpSendBox
        conversation_id='conv-1'
        backend='synonbiomed'
        workspacePath='/tmp/workspace'
        messageState={makeMessageState()}
      />
    );

    const keys = mobileActionSheetEntries.current.map((entry) => entry.key);
    expect(keys).not.toContain('view-plan');
    expect(keys).not.toContain('request-review');
    expect(keys).not.toContain('save-skill');
    expect(loadFramePlanMock).not.toHaveBeenCalled();
  });

  it('does not run duplicate plan discovery or retry logic in the composer', async () => {
    isMobileMock.current = true;
    loadFramePlanMock.mockRejectedValue(new Error('plan unavailable'));

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(mobileActionSheetEntries.current.find((entry) => entry.key === 'view-plan')).toBeUndefined();
    expect(loadFramePlanMock).not.toHaveBeenCalled();
  });

  it('uses the observed runtime contract for mobile send mode and suppresses duplicate selection', async () => {
    isMobileMock.current = true;
    let resolveSet!: (value: unknown[]) => void;
    const setConfigOption = vi.fn(
      () =>
        new Promise<unknown[]>((resolve) => {
          resolveSet = resolve;
        })
    );
    useAcpConfigOptionsMock.mockReturnValue({
      mode: {
        id: 'mode',
        category: 'mode',
        currentValue: 'send',
        options: [
          { value: 'send', label: '直接发送' },
          { value: 'plan', label: '先制定计划' },
        ],
      },
      model: null,
      thoughtLevel: null,
      setStatus: { state: 'idle' },
      setConfigOption,
      reload: vi.fn(),
    });

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);
    await act(async () => {
      screen.getByRole('button', { name: 'mobile more' }).click();
    });
    expect(mobileActionSheetOpen.current).toBe(true);
    const modeEntry = mobileActionSheetEntries.current.find((entry) => entry.key === 'send-mode');
    expect(modeEntry?.label).toBe('发送模式');
    act(() => {
      modeEntry?.submenu?.onSelect?.('plan');
      modeEntry?.submenu?.onSelect?.('plan');
    });
    expect(setConfigOption).toHaveBeenCalledTimes(1);
    expect(setConfigOption).toHaveBeenCalledWith('mode', 'plan');

    await act(async () => resolveSet([]));
    expect(messageSuccessMock).toHaveBeenCalled();
    expect(mobileActionSheetOpen.current).toBe(false);
  });

  it('keeps the mobile sheet recoverable when send mode update fails', async () => {
    isMobileMock.current = true;
    const setConfigOption = vi.fn().mockRejectedValue(new Error('config_not_observed'));
    useAcpConfigOptionsMock.mockReturnValue({
      mode: {
        id: 'mode',
        category: 'mode',
        currentValue: 'send',
        options: [{ value: 'plan', label: '先制定计划' }],
      },
      model: null,
      thoughtLevel: null,
      setStatus: { state: 'idle' },
      setConfigOption,
      reload: vi.fn(),
    });

    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);
    await act(async () => {
      mobileActionSheetEntries.current.find((entry) => entry.key === 'send-mode')?.submenu?.onSelect?.('plan');
      await Promise.resolve();
    });
    expect(setConfigOption).toHaveBeenCalledOnce();
    expect(messageErrorMock).toHaveBeenCalled();
  });

  it('places the real frame audit in the v1.1 add menu and removes the separate global branch selector', async () => {
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = true;
    runtimeViewMock.state = 'idle';
    runtimeViewMock.view.taskStatus = 'finished';
    loadFramePlanMock.mockResolvedValue({
      artifactId: 'artifact-plan-desktop',
      versionId: null,
      filename: 'plan_conv-1.json',
      createdAt: null,
    });
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);
    expect(fileAttachProps.current?.onViewPlan).toBeUndefined();
    expect(fileAttachProps.current?.onRequestReview).toEqual(expect.any(Function));
    expect(fileAttachProps.current?.reviewInFlight).toBe(false);
    expect(fileAttachProps.current?.reviewDisabled).toBe(false);
    expect(sessionOptionsProps.current?.onRequestReview).toBeUndefined();
    expect(sessionOptionsProps.current?.reviewInFlight).toBeUndefined();
    expect(sessionOptionsProps.current?.reviewDisabled).toBeUndefined();
    expect(fileAttachProps.current?.reviewLabelMode).toBe('manual');
    expect(fileAttachProps.current?.onSaveSkill).toEqual(expect.any(Function));
    expect(screen.queryByTestId('branch-menu')).not.toBeInTheDocument();
    const requestReview = fileAttachProps.current?.onRequestReview;
    expect(requestReview).toEqual(expect.any(Function));
    if (typeof requestReview !== 'function') throw new Error('Expected a frame audit handler');
    await act(async () => {
      await requestReview();
    });
    expect(requestFrameAuditMock).toHaveBeenCalledWith('conv-1');
    expect(notifyRuntimeInvalidationMock).toHaveBeenCalledWith('conv-1');
    expect(sendMessageInvokeMock).not.toHaveBeenCalled();
  });

  it('keeps manual review state on the add menu only', async () => {
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);
    expect(runtimeOperationsProps.current?.onReviewActivityChange).toEqual(expect.any(Function));

    await act(async () => {
      runtimeOperationsProps.current?.onReviewActivityChange?.(true);
    });
    expect(fileAttachProps.current?.reviewInFlight).toBe(true);
    expect(sessionOptionsProps.current?.reviewInFlight).toBeUndefined();
    expect(sessionOptionsProps.current?.onRequestReview).toBeUndefined();

    await act(async () => {
      runtimeOperationsProps.current?.onReviewActivityChange?.(false);
    });
    expect(fileAttachProps.current?.reviewInFlight).toBe(false);
  });

  it('saves the current session through the personal Skill service', async () => {
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = true;
    runtimeViewMock.state = 'idle';
    runtimeViewMock.view.taskStatus = 'finished';
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);
    expect(fileAttachProps.current?.saveAsSkillDisabled).toBe(false);
    const saveAsSkill = fileAttachProps.current?.onSaveSkill;
    if (typeof saveAsSkill !== 'function') throw new Error('Expected a save-as-skill handler');

    await act(async () => {
      saveAsSkill();
      await Promise.resolve();
    });

    await waitFor(() => expect(saveConversationAsSkillMock).toHaveBeenCalledWith('conv-1'));
    expect(sendMessageInvokeMock).not.toHaveBeenCalled();
  });

  it('does not expose a completed review request after switching conversations', async () => {
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = true;
    runtimeViewMock.state = 'idle';
    runtimeViewMock.view.taskStatus = 'finished';
    const pendingReview = createDeferred<{ accepted: boolean }>();
    requestFrameAuditMock.mockReturnValueOnce(pendingReview.promise);
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );
    const requestReview = fileAttachProps.current?.onRequestReview;
    if (typeof requestReview !== 'function') throw new Error('Expected a frame audit handler');

    let request!: Promise<void>;
    await act(async () => {
      request = requestReview() as Promise<void>;
      await Promise.resolve();
    });
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    messageSuccessMock.mockClear();
    emitterEmitMock.mockClear();

    await act(async () => {
      pendingReview.resolve({ accepted: true });
      await request;
    });

    expect(messageSuccessMock).not.toHaveBeenCalled();
    expect(emitterEmitMock).not.toHaveBeenCalledWith('chat.history.refresh');
    expect(notifyRuntimeInvalidationMock).not.toHaveBeenCalled();
  });

  it('does not expose a failed review request after switching conversations', async () => {
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = true;
    runtimeViewMock.state = 'idle';
    runtimeViewMock.view.taskStatus = 'finished';
    const pendingReview = createDeferred<never>();
    requestFrameAuditMock.mockReturnValueOnce(pendingReview.promise);
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );
    const requestReview = fileAttachProps.current?.onRequestReview;
    if (typeof requestReview !== 'function') throw new Error('Expected a frame audit handler');

    let request!: Promise<void>;
    await act(async () => {
      request = requestReview() as Promise<void>;
      await Promise.resolve();
    });
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    messageErrorMock.mockClear();

    await act(async () => {
      pendingReview.reject(new Error('old review failed'));
      await request;
    });

    expect(messageErrorMock).not.toHaveBeenCalled();
  });

  it('does not apply an old persistent option response to a replacement conversation', async () => {
    const pendingUpdate = createDeferred<void>();
    updateSessionConfigMock.mockReturnValueOnce(pendingUpdate.promise);
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );
    const updateOption = sessionOptionsProps.current?.onPersistentOptionChange;
    if (typeof updateOption !== 'function') throw new Error('Expected a persistent option handler');

    let request!: Promise<void>;
    await act(async () => {
      request = updateOption('autoReview', true) as Promise<void>;
      await Promise.resolve();
    });
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);

    await act(async () => {
      pendingUpdate.resolve();
      await request;
    });

    expect((sessionOptionsProps.current?.value as { autoReview?: boolean } | undefined)?.autoReview).toBe(false);
  });

  it('does not expose a completed goal clear after switching conversations', async () => {
    const pendingUpdate = createDeferred<void>();
    updateSessionConfigMock.mockReturnValueOnce(pendingUpdate.promise);
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );
    const clearGoal = sessionOptionsProps.current?.onClearGoal;
    if (typeof clearGoal !== 'function') throw new Error('Expected a clear goal handler');

    let request!: Promise<void>;
    await act(async () => {
      request = clearGoal() as Promise<void>;
      await Promise.resolve();
    });
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    messageSuccessMock.mockClear();

    await act(async () => {
      pendingUpdate.resolve();
      await request;
    });

    expect(messageSuccessMock).not.toHaveBeenCalled();
  });

  it('does not expose a completed mode change after switching conversations', async () => {
    const pendingMode = createDeferred<unknown[]>();
    const setConfigOption = vi.fn().mockReturnValueOnce(pendingMode.promise);
    isMobileMock.current = true;
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    useAcpConfigOptionsMock.mockReturnValue({
      mode: {
        id: 'session_mode',
        category: 'mode',
        currentValue: 'send',
        options: [
          { value: 'send', label: 'Send' },
          { value: 'plan', label: 'Plan' },
        ],
      },
      model: null,
      thoughtLevel: null,
      setStatus: { state: 'idle' },
      setConfigOption,
      reload: vi.fn(),
    });
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );

    mobileActionSheetEntries.current.find((entry) => entry.key === 'send-mode')?.submenu?.onSelect?.('plan');
    await act(async () => Promise.resolve());
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    messageSuccessMock.mockClear();

    await act(async () => {
      pendingMode.resolve([]);
      await pendingMode.promise;
    });

    expect(messageSuccessMock).not.toHaveBeenCalled();
  });

  it('does not apply an old compute response to a replacement conversation', async () => {
    const pendingCompute = createDeferred<void>();
    isMobileMock.current = true;
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    loadAssistantsMock.mockResolvedValue([]);
    loadComputeProvidersMock.mockResolvedValue([{ name: 'local-cpu', displayName: 'Local CPU', checked: true }]);
    loadSessionComputeProvidersMock.mockResolvedValue([]);
    setSessionComputeProviderMock.mockReturnValueOnce(pendingCompute.promise);
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );
    await waitFor(() =>
      expect(mobileActionSheetEntries.current.find((entry) => entry.key === 'session-compute')).toBeDefined()
    );

    mobileActionSheetEntries.current.find((entry) => entry.key === 'session-compute')?.submenu?.onSelect?.('local-cpu');
    await act(async () => Promise.resolve());
    loadAssistantsMock.mockImplementationOnce(() => new Promise(() => {}));
    loadComputeProvidersMock.mockImplementationOnce(() => new Promise(() => {}));
    loadSessionComputeProvidersMock.mockImplementationOnce(() => new Promise(() => {}));
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);

    await act(async () => {
      pendingCompute.resolve();
      await pendingCompute.promise;
    });

    expect(mobileActionSheetEntries.current.find((entry) => entry.key === 'session-compute')?.meta).toBe('0');
  });

  it('does not expose an old compute failure after switching conversations', async () => {
    const pendingCompute = createDeferred<never>();
    isMobileMock.current = true;
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    loadAssistantsMock.mockResolvedValue([]);
    loadComputeProvidersMock.mockResolvedValue([{ name: 'local-cpu', displayName: 'Local CPU', checked: true }]);
    loadSessionComputeProvidersMock.mockResolvedValue([]);
    setSessionComputeProviderMock.mockReturnValueOnce(pendingCompute.promise);
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );
    await waitFor(() =>
      expect(mobileActionSheetEntries.current.find((entry) => entry.key === 'session-compute')).toBeDefined()
    );

    mobileActionSheetEntries.current.find((entry) => entry.key === 'session-compute')?.submenu?.onSelect?.('local-cpu');
    await act(async () => Promise.resolve());
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    messageErrorMock.mockClear();

    await act(async () => {
      pendingCompute.reject(new Error('old compute failed'));
      await pendingCompute.promise.catch(() => undefined);
    });

    expect(messageErrorMock).not.toHaveBeenCalled();
  });

  it('keeps ACP config options enabled on desktop without rendering a standalone thought selector', async () => {
    useAcpConfigOptionsMock.mockReturnValue({
      setStatus: { state: 'idle' },
      mode: null,
      model: null,
      thoughtLevel: {
        id: 'reasoning_effort',
        category: 'thought_level',
        currentValue: 'high',
        options: [{ value: 'high', label: 'High' }],
      },
      reload: vi.fn(),
      setConfigOption: vi.fn(),
    });

    await act(async () => {
      await render(
        <AcpSendBox
          conversation_id='conv-1'
          backend='codex'
          workspacePath='/tmp/workspace'
          messageState={makeMessageState()}
        />
      );
      await Promise.resolve();
    });

    expect(useAcpConfigOptionsMock).toHaveBeenCalledWith(expect.objectContaining({ enabled: true }));
    expect(screen.queryByTestId('mock-thought-selector')).not.toBeInTheDocument();
  });

  it('applies runtime thought level from the mobile action sheet without persisting a global preference', async () => {
    isMobileMock.current = true;
    const setConfigOption = vi.fn().mockResolvedValue([]);
    useAcpConfigOptionsMock.mockReturnValue({
      mode: null,
      model: null,
      thoughtLevel: {
        id: 'reasoning_effort',
        category: 'thought_level',
        currentValue: 'medium',
        options: [
          { value: 'medium', label: 'Medium' },
          { value: 'high', label: 'High' },
        ],
      },
      setStatus: { state: 'idle' },
      setConfigOption,
      reload: vi.fn(),
      isLoading: false,
      configOptions: [],
    });

    await render(
      <AcpSendBox
        conversation_id='conv-1'
        backend='codex'
        workspacePath='/tmp/workspace'
        messageState={makeMessageState()}
      />
    );

    await act(async () => {
      mobileActionSheetEntries.current.find((entry) => entry.key === 'thought-level')?.submenu?.onSelect?.('high');
    });

    // This branch dropped global-preference persistence: only the runtime
    // config option is set; nothing is saved to a global agent preference.
    await waitFor(() => {
      expect(setConfigOption).toHaveBeenCalledWith('reasoning_effort', 'high');
    });
  });

  it('does not apply runtime thought level when observed confirmation fails', async () => {
    isMobileMock.current = true;
    const setConfigOption = vi.fn().mockRejectedValue(new Error('command_ack'));
    useAcpConfigOptionsMock.mockReturnValue({
      mode: null,
      model: null,
      thoughtLevel: {
        id: 'reasoning_effort',
        category: 'thought_level',
        currentValue: 'medium',
        options: [{ value: 'high', label: 'High' }],
      },
      setStatus: { state: 'idle' },
      setConfigOption,
      reload: vi.fn(),
      isLoading: false,
      configOptions: [],
    });

    await render(
      <AcpSendBox
        conversation_id='conv-1'
        backend='codex'
        workspacePath='/tmp/workspace'
        messageState={makeMessageState()}
      />
    );

    await act(async () => {
      mobileActionSheetEntries.current.find((entry) => entry.key === 'thought-level')?.submenu?.onSelect?.('high');
    });

    await waitFor(() => {
      expect(setConfigOption).toHaveBeenCalledWith('reasoning_effort', 'high');
    });
  });

  it('does not expose a completed thought-level change after switching conversations', async () => {
    const pendingChange = createDeferred<unknown[]>();
    const setConfigOption = vi.fn().mockReturnValueOnce(pendingChange.promise);
    isMobileMock.current = true;
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    useAcpConfigOptionsMock.mockReturnValue({
      mode: null,
      model: null,
      thoughtLevel: {
        id: 'reasoning_effort',
        category: 'thought_level',
        currentValue: 'medium',
        options: [{ value: 'high', label: 'High' }],
      },
      setStatus: { state: 'idle' },
      setConfigOption,
      reload: vi.fn(),
    });
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );

    mobileActionSheetEntries.current.find((entry) => entry.key === 'thought-level')?.submenu?.onSelect?.('high');
    await act(async () => Promise.resolve());
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    messageSuccessMock.mockClear();

    await act(async () => {
      pendingChange.resolve([]);
      await pendingChange.promise;
    });

    expect(messageSuccessMock).not.toHaveBeenCalled();
  });

  it('does not expose a failed thought-level change after switching conversations', async () => {
    const pendingChange = createDeferred<never>();
    const setConfigOption = vi.fn().mockReturnValueOnce(pendingChange.promise);
    isMobileMock.current = true;
    loadFramePlanMock.mockImplementation(() => new Promise(() => {}));
    useAcpConfigOptionsMock.mockReturnValue({
      mode: null,
      model: null,
      thoughtLevel: {
        id: 'reasoning_effort',
        category: 'thought_level',
        currentValue: 'medium',
        options: [{ value: 'high', label: 'High' }],
      },
      setStatus: { state: 'idle' },
      setConfigOption,
      reload: vi.fn(),
    });
    const view = await render(
      <AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />
    );

    mobileActionSheetEntries.current.find((entry) => entry.key === 'thought-level')?.submenu?.onSelect?.('high');
    await act(async () => Promise.resolve());
    view.rerender(<AcpSendBox conversation_id='conv-2' backend='synonbiomed' messageState={makeMessageState()} />);
    messageErrorMock.mockClear();

    await act(async () => {
      pendingChange.reject(new Error('old thought level failed'));
      await pendingChange.promise.catch(() => undefined);
    });

    expect(messageErrorMock).not.toHaveBeenCalled();
  });

  it('swaps the context-usage ring for the optimize-prompt action while typing', async () => {
    runtimeViewMock.isProcessing = false;
    runtimeViewMock.canSendMessage = true;
    runtimeViewMock.state = 'idle';
    runtimeViewMock.view.taskStatus = 'finished';
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(screen.getByTestId('synon-biomed-context-usage-trigger')).toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-trigger')).not.toBeInTheDocument();

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'change' }));
    });

    expect(screen.queryByTestId('synon-biomed-context-usage-trigger')).not.toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-optimize-prompt-trigger')).toBeInTheDocument();
  });

  it('keeps the ring while running and shows both icons when typing during a run', async () => {
    runtimeViewMock.isProcessing = true;
    runtimeViewMock.canSendMessage = false;
    runtimeViewMock.state = 'running';
    await render(<AcpSendBox conversation_id='conv-1' backend='synonbiomed' messageState={makeMessageState()} />);

    expect(screen.getByTestId('synon-biomed-context-usage-trigger')).toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-optimize-prompt-trigger')).not.toBeInTheDocument();

    await act(async () => {
      fireEvent.click(screen.getByRole('button', { name: 'change' }));
    });

    expect(screen.getByTestId('synon-biomed-context-usage-trigger')).toBeInTheDocument();
    expect(screen.getByTestId('synon-biomed-optimize-prompt-trigger')).toBeInTheDocument();
  });
});
