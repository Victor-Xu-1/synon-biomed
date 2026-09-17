import { uuid } from '@/common/utils';
import { useAddEventListener } from '@/renderer/utils/emitter';
import { Message } from '@arco-design/web-react';
import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import useSWR from 'swr';
import {
  MAX_COMPOSER_CONTEXT_ITEMS,
  normalizeComposerContextItems,
  type ComposerContextItem,
} from '@/renderer/components/chat/SendBox/composerCompositionModel';
import {
  getRendererAccountOwnerId,
  registerRendererAccountReset,
  rendererAccountScopedKey,
} from '@/renderer/services/rendererAccountScope';

export type ConversationCommandQueueItem = {
  id: string;
  input: string;
  files: string[];
  contextItems: ComposerContextItem[];
  planMode?: boolean;
  pausedReason?: 'send_failed';
  created_at: number;
};

export type ConversationCommandQueueState = {
  items: ConversationCommandQueueItem[];
  isPaused: boolean;
  pauseReason?: 'interrupted';
};

export const MAX_QUEUED_COMMANDS = 20;
export const MAX_QUEUED_COMMAND_INPUT_LENGTH = 20_000;
export const MAX_QUEUED_COMMAND_FILES = 50;
export const MAX_QUEUED_COMMAND_CONTEXT_ITEMS = MAX_COMPOSER_CONTEXT_ITEMS;
export const MAX_QUEUED_COMMAND_STATE_BYTES = 256 * 1024;

export type QueueValidationFailureReason =
  | 'emptyInput'
  | 'inputTooLong'
  | 'tooManyFiles'
  | 'tooManyContextItems'
  | 'queueFull'
  | 'queueTooLarge';

type QueueValidationSuccess = {
  ok: true;
  nextStateBytes: number;
};

type QueueValidationFailure = {
  ok: false;
  reason: QueueValidationFailureReason;
};

const COMMAND_QUEUE_LOG_PREFIX = '[conversation-command-queue]';

const summarizeQueuedCommand = (item: ConversationCommandQueueItem): Record<string, unknown> => ({
  id: item.id,
  created_at: item.created_at,
  inputLength: item.input.length,
  fileCount: item.files.length,
  contextItemCount: item.contextItems.length,
});

const logCommandQueue = (conversation_id: string, event: string, payload: Record<string, unknown> = {}): void => {
  console.info(COMMAND_QUEUE_LOG_PREFIX, {
    conversation_id,
    event,
    ...payload,
  });
};

const createDefaultQueueState = (): ConversationCommandQueueState => ({
  items: [],
  isPaused: false,
});

const queueStore = new Map<string, ConversationCommandQueueState>();

const legacyStorageKey = (conversation_id: string): string => `conversation-command-queue/${conversation_id}`;
const getStorageKey = (conversation_id: string): string => {
  const ownerId = getRendererAccountOwnerId();
  return ownerId ? `conversation-command-queue/v2/${encodeURIComponent(ownerId)}/${conversation_id}` : '';
};
const getQueueingEnabledStorageKey = (): string => {
  const ownerId = getRendererAccountOwnerId();
  return ownerId ? `conversation-command-queue/v2/${encodeURIComponent(ownerId)}/enabled` : '';
};
const queueMemoryKey = (conversation_id: string): string => rendererAccountScopedKey(conversation_id);
registerRendererAccountReset('conversation-command-queue', () => queueStore.clear());
const measureQueueStateBytes = (state: ConversationCommandQueueState): number =>
  new TextEncoder().encode(JSON.stringify(state)).length;

const uniqueFiles = (files: string[]): string[] => Array.from(new Set(files.filter(Boolean)));
const isCommandEmpty = (item: Pick<ConversationCommandQueueItem, 'input' | 'files' | 'contextItems'>): boolean =>
  item.input.trim().length === 0 && item.files.length === 0 && item.contextItems.length === 0;

const normalizeQueueItem = (item: unknown): ConversationCommandQueueItem | null => {
  if (!item || typeof item !== 'object') {
    return null;
  }

  const candidate = item as Record<string, unknown>;
  if (
    typeof candidate.id !== 'string' ||
    typeof candidate.input !== 'string' ||
    !Array.isArray(candidate.files) ||
    !candidate.files.every((file) => typeof file === 'string') ||
    typeof candidate.created_at !== 'number' ||
    !Number.isFinite(candidate.created_at)
  ) {
    return null;
  }

  const normalizedItem: ConversationCommandQueueItem = {
    id: candidate.id,
    input: candidate.input,
    files: uniqueFiles(candidate.files),
    contextItems: normalizeComposerContextItems(candidate.contextItems),
    ...(candidate.planMode === true ? { planMode: true } : {}),
    ...(candidate.pausedReason === 'send_failed' ? { pausedReason: 'send_failed' as const } : {}),
    created_at: candidate.created_at,
  };

  if (
    isCommandEmpty(normalizedItem) ||
    normalizedItem.input.length > MAX_QUEUED_COMMAND_INPUT_LENGTH ||
    normalizedItem.files.length > MAX_QUEUED_COMMAND_FILES ||
    normalizedItem.contextItems.length > MAX_QUEUED_COMMAND_CONTEXT_ITEMS
  ) {
    return null;
  }

  return normalizedItem;
};

export const normalizeQueueState = (state: unknown): ConversationCommandQueueState => {
  if (!state || typeof state !== 'object') {
    return createDefaultQueueState();
  }

  const candidate = state as Partial<ConversationCommandQueueState>;
  const normalizedItems = Array.isArray(candidate.items)
    ? candidate.items.map(normalizeQueueItem).filter((item): item is ConversationCommandQueueItem => item !== null)
    : [];
  const items: ConversationCommandQueueItem[] = [];

  for (const item of normalizedItems.slice(0, MAX_QUEUED_COMMANDS)) {
    const nextItems = [...items, item];
    const nextState = {
      items: nextItems,
      isPaused: Boolean(candidate.isPaused),
      ...(candidate.pauseReason === 'interrupted' ? { pauseReason: 'interrupted' as const } : {}),
    };

    if (measureQueueStateBytes(nextState) > MAX_QUEUED_COMMAND_STATE_BYTES) {
      break;
    }

    items.push(item);
  }

  return {
    items,
    isPaused: items.length > 0 ? Boolean(candidate.isPaused) : false,
    ...(items.length > 0 && candidate.pauseReason === 'interrupted' ? { pauseReason: 'interrupted' as const } : {}),
  };
};

export const estimateQueueStateBytes = (state: ConversationCommandQueueState): number =>
  measureQueueStateBytes(normalizeQueueState(state));

export const createQueuedCommandItem = ({
  input,
  files,
  contextItems = [],
  planMode,
}: Pick<ConversationCommandQueueItem, 'input' | 'files' | 'planMode'> & {
  contextItems?: ComposerContextItem[];
}): ConversationCommandQueueItem => ({
  id: uuid(),
  input,
  files: uniqueFiles(files),
  contextItems: normalizeComposerContextItems(contextItems),
  ...(planMode === true ? { planMode: true } : {}),
  created_at: Date.now(),
});

const getQueueValidationFailureReason = (state: ConversationCommandQueueState): QueueValidationFailureReason | null => {
  if (state.items.length > MAX_QUEUED_COMMANDS) {
    return 'queueFull';
  }

  if (state.items.some(isCommandEmpty)) {
    return 'emptyInput';
  }

  if (state.items.some((item) => item.input.length > MAX_QUEUED_COMMAND_INPUT_LENGTH)) {
    return 'inputTooLong';
  }

  if (state.items.some((item) => item.files.length > MAX_QUEUED_COMMAND_FILES)) {
    return 'tooManyFiles';
  }

  if (state.items.some((item) => item.contextItems.length > MAX_QUEUED_COMMAND_CONTEXT_ITEMS)) {
    return 'tooManyContextItems';
  }

  if (measureQueueStateBytes(state) > MAX_QUEUED_COMMAND_STATE_BYTES) {
    return 'queueTooLarge';
  }

  return null;
};

export const validateQueuedCommandItem = (
  item: ConversationCommandQueueItem,
  state: ConversationCommandQueueState
): QueueValidationSuccess | QueueValidationFailure => {
  const nextState = {
    ...state,
    items: [...state.items, item],
  };
  const failureReason = getQueueValidationFailureReason(nextState);
  if (failureReason) {
    return { ok: false, reason: failureReason };
  }
  const nextStateBytes = measureQueueStateBytes(nextState);
  return { ok: true, nextStateBytes };
};

const isQueueValidationFailure = (
  validation: QueueValidationSuccess | QueueValidationFailure
): validation is QueueValidationFailure => !validation.ok;

const readPersistedQueueState = (conversation_id: string): ConversationCommandQueueState => {
  const memoryKey = queueMemoryKey(conversation_id);
  if (queueStore.has(memoryKey)) {
    return queueStore.get(memoryKey) ?? createDefaultQueueState();
  }

  if (typeof window === 'undefined') {
    return createDefaultQueueState();
  }

  try {
    const storageKey = getStorageKey(conversation_id);
    if (!storageKey) return createDefaultQueueState();
    // Unscoped v1 queue records cannot be assigned safely after an account
    // switch. Retire them instead of exposing one user's draft to another.
    window.localStorage.removeItem(legacyStorageKey(conversation_id));
    window.sessionStorage.removeItem(legacyStorageKey(conversation_id));
    const durableStored = window.localStorage.getItem(storageKey);
    const sessionStored = window.sessionStorage.getItem(storageKey);
    const stored = durableStored ?? sessionStored;
    if (!stored) {
      return createDefaultQueueState();
    }

    const parsed = JSON.parse(stored) as unknown;
    const normalized = normalizeQueueState(parsed);
    if (!durableStored && sessionStored) {
      window.localStorage.setItem(storageKey, JSON.stringify(normalized));
      window.sessionStorage.removeItem(storageKey);
    }
    queueStore.set(memoryKey, normalized);
    logCommandQueue(conversation_id, 'restored', {
      itemCount: normalized.items.length,
      isPaused: normalized.isPaused,
    });
    return normalized;
  } catch (error) {
    console.warn('[conversation-command-queue] Failed to read persisted queue state:', error);
    return createDefaultQueueState();
  }
};

const removePersistedQueueState = (conversation_id: string): void => {
  queueStore.delete(queueMemoryKey(conversation_id));
  if (typeof window !== 'undefined') {
    try {
      const storageKey = getStorageKey(conversation_id);
      if (storageKey) {
        window.localStorage.removeItem(storageKey);
        window.sessionStorage.removeItem(storageKey);
      }
    } catch (error) {
      console.warn('[conversation-command-queue] Failed to remove persisted queue state:', error);
    }
  }
};

const persistQueueState = (conversation_id: string, state: ConversationCommandQueueState): void => {
  const normalized = normalizeQueueState(state);

  if (normalized.items.length === 0 && !normalized.isPaused) {
    removePersistedQueueState(conversation_id);
    return;
  }

  queueStore.set(queueMemoryKey(conversation_id), normalized);
  if (typeof window !== 'undefined') {
    try {
      const storageKey = getStorageKey(conversation_id);
      if (storageKey) window.localStorage.setItem(storageKey, JSON.stringify(normalized));
    } catch (error) {
      console.warn('[conversation-command-queue] Failed to persist queue state:', error);
    }
  }
};

const readQueueingEnabled = (): boolean => {
  if (typeof window === 'undefined') return true;
  const storageKey = getQueueingEnabledStorageKey();
  return !storageKey || window.localStorage.getItem(storageKey) !== 'false';
};

const persistQueueingEnabled = (enabled: boolean): void => {
  if (typeof window === 'undefined') return;
  const storageKey = getQueueingEnabledStorageKey();
  if (storageKey) window.localStorage.setItem(storageKey, String(enabled));
};

export const removeQueuedCommand = (
  items: ConversationCommandQueueItem[],
  commandId: string
): ConversationCommandQueueItem[] => items.filter((item) => item.id !== commandId);

export const reorderQueuedCommand = (
  items: ConversationCommandQueueItem[],
  activeCommandId: string,
  overCommandId: string
): ConversationCommandQueueItem[] => {
  const fromIndex = items.findIndex((item) => item.id === activeCommandId);
  const targetIndex = items.findIndex((item) => item.id === overCommandId);

  if (fromIndex === -1 || targetIndex === -1 || fromIndex === targetIndex) {
    return items;
  }

  const nextItems = [...items];
  const [movedItem] = nextItems.splice(fromIndex, 1);
  nextItems.splice(targetIndex, 0, movedItem);
  return nextItems;
};

export const restoreQueuedCommand = (
  items: ConversationCommandQueueItem[],
  failedItem: ConversationCommandQueueItem
): ConversationCommandQueueItem[] => [
  { ...failedItem, pausedReason: 'send_failed' },
  ...removeQueuedCommand(items, failedItem.id),
];

export const updateQueuedCommand = (
  items: ConversationCommandQueueItem[],
  commandId: string,
  updates: Partial<Pick<ConversationCommandQueueItem, 'input' | 'files' | 'contextItems' | 'planMode'>>
): ConversationCommandQueueItem[] =>
  items.map((item) =>
    item.id === commandId
      ? {
          ...item,
          ...updates,
          files: updates.files ? uniqueFiles(updates.files) : item.files,
          contextItems: updates.contextItems ? normalizeComposerContextItems(updates.contextItems) : item.contextItems,
        }
      : item
  );

export const shouldEnqueueConversationCommand = ({
  enabled = true,
  isBusy,
  hasPendingCommands,
}: {
  enabled?: boolean;
  isBusy: boolean;
  hasPendingCommands: boolean;
}): boolean => enabled && (isBusy || hasPendingCommands);

export type ConversationCommandQueueRuntimeGate = {
  hydrated: boolean;
  canSendMessage: boolean;
  isProcessing: boolean;
};

export type CommandQueueExecutionGate = {
  hydrated: boolean;
  canExecute: boolean;
  isProcessing: boolean;
};

export const getCommandQueueExecutionGate = ({
  isBusy,
  isHydrated = true,
  runtimeGate,
}: {
  isBusy: boolean;
  isHydrated?: boolean;
  runtimeGate?: ConversationCommandQueueRuntimeGate;
}): CommandQueueExecutionGate => {
  if (runtimeGate) {
    return {
      hydrated: runtimeGate.hydrated,
      canExecute: runtimeGate.canSendMessage && !runtimeGate.isProcessing,
      isProcessing: runtimeGate.isProcessing,
    };
  }

  return {
    hydrated: isHydrated,
    canExecute: !isBusy,
    isProcessing: isBusy,
  };
};

type UseConversationCommandQueueOptions = {
  conversation_id: string;
  enabled?: boolean;
  isBusy: boolean;
  isHydrated?: boolean;
  runtimeGate?: ConversationCommandQueueRuntimeGate;
  onExecute: (item: ConversationCommandQueueItem) => Promise<void>;
};

type EnqueueCommandInput = Pick<ConversationCommandQueueItem, 'input' | 'files' | 'planMode'> & {
  contextItems?: ComposerContextItem[];
};

const getQueueValidationMessage = (
  t: (key: string, options?: Record<string, unknown>) => string,
  reason: QueueValidationFailureReason
): string => {
  const warningKeyMap = {
    emptyInput: 'conversation.commandQueue.emptyInput',
    queueFull: 'conversation.commandQueue.queueFull',
    inputTooLong: 'conversation.commandQueue.inputTooLong',
    tooManyFiles: 'conversation.commandQueue.tooManyFiles',
    tooManyContextItems: 'conversation.commandQueue.tooManyContextItems',
    queueTooLarge: 'conversation.commandQueue.queueTooLarge',
  } as const;
  return t(warningKeyMap[reason], {
    count: MAX_QUEUED_COMMANDS,
    files: MAX_QUEUED_COMMAND_FILES,
  });
};

export const useConversationCommandQueue = ({
  conversation_id,
  enabled = true,
  isBusy,
  isHydrated = true,
  runtimeGate,
  onExecute,
}: UseConversationCommandQueueOptions) => {
  const { t } = useTranslation();
  const executionGate = getCommandQueueExecutionGate({ isBusy, isHydrated, runtimeGate });
  const { data = createDefaultQueueState(), mutate } = useSWR(
    [`/conversation-command-queue/${conversation_id}`, conversation_id, enabled],
    ([, id, is_enabled]) => (is_enabled ? readPersistedQueueState(id) : createDefaultQueueState())
  );

  const stateRef = useRef(data);
  const pausedRef = useRef(data.isPaused);
  const waitingForTurnStartRef = useRef(false);
  const waitingForTurnCompletionRef = useRef(false);
  const interactionLockedRef = useRef(false);
  const [isInteractionLocked, setIsInteractionLocked] = useState(false);
  const [executionGateVersion, setExecutionGateVersion] = useState(0);
  const [isQueueingEnabled, setQueueingEnabledState] = useState(readQueueingEnabled);

  useEffect(() => {
    stateRef.current = data;
  }, [data]);

  useEffect(() => {
    if (waitingForTurnStartRef.current && executionGate.isProcessing) {
      waitingForTurnStartRef.current = false;
      waitingForTurnCompletionRef.current = true;
      logCommandQueue(conversation_id, 'turn-started', {
        pendingItemCount: stateRef.current.items.length,
      });
      return;
    }

    if (waitingForTurnCompletionRef.current && executionGate.hydrated && executionGate.canExecute) {
      waitingForTurnCompletionRef.current = false;
      logCommandQueue(conversation_id, 'turn-finished', {
        pendingItemCount: stateRef.current.items.length,
      });
    }
  }, [conversation_id, executionGate.canExecute, executionGate.hydrated, executionGate.isProcessing]);

  useEffect(() => {
    pausedRef.current = data.isPaused;
  }, [data.isPaused]);

  useEffect(() => {
    interactionLockedRef.current = isInteractionLocked;
  }, [isInteractionLocked]);

  useEffect(() => {
    if (typeof window === 'undefined') return;
    const handleStorage = (event: StorageEvent) => {
      if (event.key === getStorageKey(conversation_id) && event.newValue) {
        try {
          const nextState = normalizeQueueState(JSON.parse(event.newValue) as unknown);
          queueStore.set(queueMemoryKey(conversation_id), nextState);
          stateRef.current = nextState;
          pausedRef.current = nextState.isPaused;
          void mutate(nextState, { revalidate: false });
        } catch (error) {
          console.warn('[conversation-command-queue] Ignored invalid cross-window queue state:', error);
        }
      } else if (event.key === getStorageKey(conversation_id) && event.newValue === null) {
        stateRef.current = createDefaultQueueState();
        pausedRef.current = false;
        void mutate(createDefaultQueueState(), { revalidate: false });
      } else if (event.key === getQueueingEnabledStorageKey()) {
        setQueueingEnabledState(event.newValue !== 'false');
      }
    };
    window.addEventListener('storage', handleStorage);
    return () => window.removeEventListener('storage', handleStorage);
  }, [conversation_id, mutate]);

  useEffect(() => {
    if (enabled) {
      return;
    }

    waitingForTurnStartRef.current = false;
    waitingForTurnCompletionRef.current = false;
    pausedRef.current = false;
    interactionLockedRef.current = false;
    stateRef.current = createDefaultQueueState();
    setIsInteractionLocked(false);
    removePersistedQueueState(conversation_id);
    void mutate(createDefaultQueueState(), { revalidate: false });
  }, [conversation_id, enabled, mutate]);

  const updateState = useCallback(
    (
      updater: (state: ConversationCommandQueueState) => ConversationCommandQueueState
    ): Promise<ConversationCommandQueueState | undefined> => {
      if (!enabled) {
        const nextState = createDefaultQueueState();
        stateRef.current = nextState;
        pausedRef.current = false;
        removePersistedQueueState(conversation_id);
        return Promise.resolve(nextState);
      }

      return mutate(
        (current) => {
          const nextState = normalizeQueueState(updater(current ?? createDefaultQueueState()));
          stateRef.current = nextState;
          pausedRef.current = nextState.isPaused;
          persistQueueState(conversation_id, nextState);
          return nextState;
        },
        { revalidate: false }
      );
    },
    [conversation_id, enabled, mutate]
  );

  const clear = useCallback(() => {
    waitingForTurnStartRef.current = false;
    waitingForTurnCompletionRef.current = false;
    pausedRef.current = false;
    logCommandQueue(conversation_id, 'cleared');
    void updateState(() => createDefaultQueueState());
  }, [conversation_id, updateState]);

  useAddEventListener(
    'conversation.deleted',
    (deletedConversationId) => {
      if (deletedConversationId !== conversation_id) {
        return;
      }
      clear();
      removePersistedQueueState(conversation_id);
    },
    [clear, conversation_id]
  );

  const enqueue = useCallback(
    ({ input, files, contextItems = [], planMode }: EnqueueCommandInput) => {
      if (!enabled) {
        return null;
      }

      const currentState = normalizeQueueState(stateRef.current);
      const item = createQueuedCommandItem({ input, files, contextItems, planMode });
      const validation = validateQueuedCommandItem(item, currentState);

      if (isQueueValidationFailure(validation)) {
        const reason: QueueValidationFailureReason = validation.reason;
        logCommandQueue(conversation_id, 'enqueue-rejected', {
          reason,
          item: summarizeQueuedCommand(item),
          currentItemCount: currentState.items.length,
        });
        Message.warning(getQueueValidationMessage(t, reason));
        return null;
      }

      const nextState: ConversationCommandQueueState = {
        ...currentState,
        items: [...currentState.items, item],
      };
      stateRef.current = nextState;
      logCommandQueue(conversation_id, 'enqueued', {
        item: summarizeQueuedCommand(item),
        currentItemCount: currentState.items.length,
      });
      void updateState(() => nextState);
      return item;
    },
    [conversation_id, enabled, t, updateState]
  );

  const remove = useCallback(
    (commandId: string) => {
      if (!enabled) {
        return;
      }

      logCommandQueue(conversation_id, 'removed', {
        commandId,
      });
      void updateState((state) => {
        const nextItems = removeQueuedCommand(state.items, commandId);
        return {
          items: nextItems,
          isPaused: nextItems.length > 0 ? state.isPaused : false,
          ...(nextItems.length > 0 && state.pauseReason ? { pauseReason: state.pauseReason } : {}),
        };
      });
    },
    [conversation_id, enabled, updateState]
  );

  const reorder = useCallback(
    (activeCommandId: string, overCommandId: string) => {
      if (!enabled) {
        return;
      }

      logCommandQueue(conversation_id, 'reordered', {
        activeCommandId,
        overCommandId,
      });
      void updateState((state) => ({
        isPaused: state.isPaused,
        ...(state.pauseReason ? { pauseReason: state.pauseReason } : {}),
        items: reorderQueuedCommand(state.items, activeCommandId, overCommandId),
      }));
    },
    [conversation_id, enabled, updateState]
  );

  const pause = useCallback(
    (reason: 'interrupted' = 'interrupted') => {
      if (!enabled) {
        return;
      }

      pausedRef.current = true;
      waitingForTurnStartRef.current = false;
      waitingForTurnCompletionRef.current = false;
      logCommandQueue(conversation_id, 'paused', {
        itemCount: data.items.length,
      });
      void updateState((state) => {
        if (state.items.length === 0) {
          pausedRef.current = false;
          return createDefaultQueueState();
        }
        return {
          ...state,
          isPaused: true,
          pauseReason: reason,
        };
      });
    },
    [conversation_id, data.items.length, enabled, updateState]
  );

  const resume = useCallback(() => {
    if (!enabled) {
      return;
    }

    pausedRef.current = false;
    logCommandQueue(conversation_id, 'resumed', {
      itemCount: data.items.length,
    });
    void updateState((state) => ({
      ...state,
      isPaused: state.items.length > 0 ? false : state.isPaused,
      pauseReason: undefined,
    }));
  }, [conversation_id, data.items.length, enabled, updateState]);

  const lockInteraction = useCallback(() => {
    if (!enabled) {
      return;
    }

    interactionLockedRef.current = true;
    logCommandQueue(conversation_id, 'interaction-locked', {
      itemCount: stateRef.current.items.length,
    });
    setIsInteractionLocked(true);
  }, [conversation_id, enabled]);

  const unlockInteraction = useCallback(() => {
    if (!enabled) {
      return;
    }

    interactionLockedRef.current = false;
    logCommandQueue(conversation_id, 'interaction-unlocked', {
      itemCount: stateRef.current.items.length,
    });
    setIsInteractionLocked(false);
  }, [conversation_id, enabled]);

  const resetActiveExecution = useCallback(
    (reason: 'stop' | 'external-reset') => {
      const hadPendingTurn = waitingForTurnStartRef.current || waitingForTurnCompletionRef.current;
      waitingForTurnStartRef.current = false;
      waitingForTurnCompletionRef.current = false;

      if (!hadPendingTurn) {
        return;
      }

      logCommandQueue(conversation_id, 'execution-reset', {
        reason,
        pendingItemCount: stateRef.current.items.length,
      });
      setExecutionGateVersion((version) => version + 1);
    },
    [conversation_id]
  );

  const executeQueuedCommand = useCallback(
    async (commandId: string, source: 'automatic' | 'send_now' | 'retry'): Promise<boolean> => {
      const currentState = normalizeQueueState(stateRef.current);
      const command = currentState.items.find((item) => item.id === commandId);
      if (!command) return false;

      const nextState: ConversationCommandQueueState = {
        items: removeQueuedCommand(currentState.items, commandId),
        isPaused: false,
      };
      stateRef.current = nextState;
      pausedRef.current = false;
      waitingForTurnStartRef.current = true;
      logCommandQueue(conversation_id, source, {
        item: summarizeQueuedCommand(command),
        remainingItemCount: nextState.items.length,
      });
      await updateState(() => nextState);

      try {
        await onExecute({ ...command, pausedReason: undefined });
        return true;
      } catch (error) {
        console.error('[conversation-command-queue] Failed to execute queued command:', error);
        waitingForTurnStartRef.current = false;
        waitingForTurnCompletionRef.current = false;
        const restoredState: ConversationCommandQueueState = {
          items: restoreQueuedCommand(stateRef.current.items, command),
          isPaused: false,
        };
        stateRef.current = restoredState;
        await updateState(() => restoredState);
        logCommandQueue(conversation_id, 'execute-failed', {
          source,
          item: summarizeQueuedCommand(command),
          error: error instanceof Error ? error.message : String(error),
        });
        Message.warning(t('conversation.commandQueue.pausedAfterFailure'));
        return false;
      }
    },
    [conversation_id, onExecute, t, updateState]
  );

  const sendNow = useCallback(
    (commandId: string) => executeQueuedCommand(commandId, 'send_now'),
    [executeQueuedCommand]
  );
  const retry = useCallback((commandId: string) => executeQueuedCommand(commandId, 'retry'), [executeQueuedCommand]);
  const setQueueingEnabled = useCallback((nextEnabled: boolean) => {
    setQueueingEnabledState(nextEnabled);
    persistQueueingEnabled(nextEnabled);
  }, []);

  useEffect(() => {
    if (
      !enabled ||
      !executionGate.hydrated ||
      pausedRef.current ||
      !executionGate.canExecute ||
      waitingForTurnStartRef.current ||
      waitingForTurnCompletionRef.current ||
      interactionLockedRef.current ||
      data.items.length === 0 ||
      data.items[0]?.pausedReason === 'send_failed'
    ) {
      return;
    }

    void executeQueuedCommand(data.items[0].id, 'automatic');
  }, [
    conversation_id,
    data.items,
    enabled,
    executionGateVersion,
    executionGate.canExecute,
    executionGate.hydrated,
    executionGate.isProcessing,
    executeQueuedCommand,
  ]);

  return {
    items: enabled ? data.items : [],
    isPaused: enabled ? data.isPaused : false,
    isInterrupted: enabled ? data.pauseReason === 'interrupted' : false,
    isQueueingEnabled,
    hasPendingCommands: enabled ? data.items.length > 0 : false,
    enqueue,
    remove,
    reorder,
    retry,
    sendNow,
    setQueueingEnabled,
    pause,
    resume,
    lockInteraction,
    unlockInteraction,
    resetActiveExecution,
  };
};
