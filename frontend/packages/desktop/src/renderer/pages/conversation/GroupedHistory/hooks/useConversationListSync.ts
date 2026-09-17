/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import type { ConversationTurnCompletedEvent } from '@/common/adapter/conversationRuntimeProtocol';
import type { TChatConversation } from '@/common/config/storage';
import { addEventListener } from '@/renderer/utils/emitter';
import { useCallback, useEffect, useSyncExternalStore } from 'react';
import {
  getConversationTaskStreamGuardDecision,
  isConversationRuntimeUnfinished,
  isConversationTaskTerminalStatus,
  isConversationTerminalStreamMessage,
  isConversationTerminalTurnState,
  reduceConversationTaskStatus,
} from '../conversationTaskIndicatorModel';

type ConversationListSyncSnapshot = {
  conversations: TChatConversation[];
  generatingConversationIds: Set<string>;
  completionUnreadConversationIds: Set<string>;
  hasMoreConversations: boolean;
  isLoadingMoreConversations: boolean;
};

const CONVERSATION_LIST_REFRESH_DELAY_MS = 120;
const CONVERSATION_LIST_PAGE_LIMIT = 100;
const UNKNOWN_CONVERSATION_REFRESH_CAPACITY = 256;

export const createUnknownConversationRefreshGuard = (capacity = UNKNOWN_CONVERSATION_REFRESH_CAPACITY) => {
  const boundedCapacity = Math.max(1, Math.floor(capacity));
  const observed = new Set<string>();

  const remember = (conversationId: string): void => {
    const normalizedId = conversationId.trim();
    if (!normalizedId || observed.has(normalizedId)) return;
    observed.add(normalizedId);
    while (observed.size > boundedCapacity) {
      const oldest = observed.values().next().value as string | undefined;
      if (!oldest) break;
      observed.delete(oldest);
    }
  };

  const shouldRefresh = (conversationId: string, known: boolean): boolean => {
    const normalizedId = conversationId.trim();
    if (!normalizedId || known || observed.has(normalizedId)) return false;
    remember(normalizedId);
    return true;
  };

  return {
    shouldRefresh,
    remember,
    size: () => observed.size,
  };
};

export const createConversationListRefreshController = (
  load: () => Promise<void>,
  delayMs = CONVERSATION_LIST_REFRESH_DELAY_MS
) => {
  let timer: ReturnType<typeof setTimeout> | null = null;
  let inFlight: Promise<void> | null = null;
  let queued = false;

  const schedule = (delay = delayMs) => {
    if (timer !== null) return;
    timer = setTimeout(
      () => {
        timer = null;
        void run();
      },
      Math.max(0, delay)
    );
  };

  const run = (): Promise<void> => {
    if (inFlight) {
      queued = true;
      return inFlight;
    }
    const request = Promise.resolve().then(load);
    inFlight = request;
    void request
      .finally(() => {
        if (inFlight !== request) return;
        inFlight = null;
        if (queued) {
          queued = false;
          schedule(0);
        }
      })
      .catch((): void => undefined);
    return request;
  };

  return { run, schedule };
};

const listeners = new Set<() => void>();

let isStoreInitialized = false;
let conversationsState: TChatConversation[] = [];
let generatingConversationIdsState = new Set<string>();
let completionUnreadConversationIdsState = new Set<string>();
let completedConversationIdsState = new Set<string>();
let conversation_idsState = new Set<string>();
let activeConversationIdState: string | null = null;
let nextConversationCursorState: string | null = null;
let hasMoreConversationsState = false;
let isLoadingMoreConversationsState = false;
let snapshotState: ConversationListSyncSnapshot = {
  conversations: conversationsState,
  generatingConversationIds: generatingConversationIdsState,
  completionUnreadConversationIds: completionUnreadConversationIdsState,
  hasMoreConversations: hasMoreConversationsState,
  isLoadingMoreConversations: isLoadingMoreConversationsState,
};

const emitStoreChange = () => {
  snapshotState = {
    conversations: conversationsState,
    generatingConversationIds: generatingConversationIdsState,
    completionUnreadConversationIds: completionUnreadConversationIdsState,
    hasMoreConversations: hasMoreConversationsState,
    isLoadingMoreConversations: isLoadingMoreConversationsState,
  };
  listeners.forEach((listener) => listener());
};

const subscribeConversationListSync = (listener: () => void) => {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
};

const getConversationListSyncSnapshot = (): ConversationListSyncSnapshot => snapshotState;

const loadConversationPage = async (cursor?: string, append = false): Promise<void> => {
  if (append) {
    if (isLoadingMoreConversationsState) return;
    isLoadingMoreConversationsState = true;
    emitStoreChange();
  }
  try {
    const result = await ipcBridge.database.getUserConversations.invoke({
      limit: CONVERSATION_LIST_PAGE_LIMIT,
      ...(cursor ? { cursor } : {}),
    });
    const items = result?.items;
    if (!items || !Array.isArray(items)) throw new Error('conversation_list_page_invalid');
    const filteredData = items.filter((conv) => {
      // Legacy rows from the pre-provider-probe health check flow are hidden
      // from normal history. New health checks must not create conversations.
      const extra = conv.extra as { is_health_check?: boolean } | undefined;
      return extra?.is_health_check !== true;
    });
    if (append) {
      const existingIDs = new Set(conversationsState.map((conversation) => conversation.id));
      conversationsState = [
        ...conversationsState,
        ...filteredData.filter((conversation) => !existingIDs.has(conversation.id)),
      ];
      conversation_idsState = new Set([...conversation_idsState, ...items.map((conversation) => conversation.id)]);
    } else {
      conversationsState = reduceConversationTaskStatus(conversationsState, {
        type: 'list-refreshed',
        incoming: filteredData,
        completedConversationIds: completedConversationIdsState,
      });
      // Use all IDs from the page (including legacy health-check rows) so the
      // responseStream listener does not turn hidden rows into a refresh loop.
      conversation_idsState = new Set(items.map((conversation) => conversation.id));
    }
    const nextCursor = typeof result.next_cursor === 'string' && result.next_cursor ? result.next_cursor : null;
    hasMoreConversationsState = result.has_more === true && nextCursor !== null;
    nextConversationCursorState = hasMoreConversationsState ? nextCursor : null;
  } catch (error) {
    console.warn('[WorkspaceGroupedHistory] Failed to load conversations:', error);
    if (!append) {
      conversationsState = [];
      conversation_idsState = new Set();
      nextConversationCursorState = null;
      hasMoreConversationsState = false;
    }
  } finally {
    if (append) isLoadingMoreConversationsState = false;
    emitStoreChange();
  }
};

const loadConversations = () => loadConversationPage();

const conversationListRefresh = createConversationListRefreshController(loadConversations);
const unknownConversationRefreshGuard = createUnknownConversationRefreshGuard();

export function shouldRefreshConversationListAfterChange(input: {
  action: 'created' | 'updated' | 'deleted';
  known: boolean;
  active: boolean;
}): boolean {
  if (input.action === 'deleted' || input.action === 'created' || !input.known) return true;
  // Streaming publishes durable conversation updates throughout a long turn.
  // Runtime/stream reducers already update the visible row incrementally, so
  // re-reading and re-grouping the whole collection for every publication only
  // competes with transcript rendering. The terminal event performs one final
  // authoritative refresh.
  return !input.active;
}

const refreshConversations = () => {
  void conversationListRefresh.run();
};

const scheduleConversationRefresh = () => {
  conversationListRefresh.schedule();
};

const loadMoreConversationsState = () => {
  if (!hasMoreConversationsState || !nextConversationCursorState || isLoadingMoreConversationsState) return;
  void loadConversationPage(nextConversationCursorState, true);
};

const markGenerating = (conversation_id: string) => {
  if (generatingConversationIdsState.has(conversation_id)) {
    return;
  }

  generatingConversationIdsState = new Set(generatingConversationIdsState).add(conversation_id);
  emitStoreChange();
};

const clearGenerating = (conversation_id: string) => {
  if (!generatingConversationIdsState.has(conversation_id)) {
    return;
  }

  const next = new Set(generatingConversationIdsState);
  next.delete(conversation_id);
  generatingConversationIdsState = next;
  emitStoreChange();
};

const markCompletionUnread = (conversation_id: string) => {
  if (completionUnreadConversationIdsState.has(conversation_id)) {
    return;
  }

  completionUnreadConversationIdsState = new Set(completionUnreadConversationIdsState).add(conversation_id);
  emitStoreChange();
};

const clearCompletionUnreadState = (conversation_id: string) => {
  if (!completionUnreadConversationIdsState.has(conversation_id)) {
    return;
  }

  const next = new Set(completionUnreadConversationIdsState);
  next.delete(conversation_id);
  completionUnreadConversationIdsState = next;
  emitStoreChange();
};

const markCompleted = (conversation_id: string) => {
  completedConversationIdsState = new Set(completedConversationIdsState).add(conversation_id);
};

const applyTurnStartedState = (conversation_id: string) => {
  let changed = false;
  if (completedConversationIdsState.has(conversation_id)) {
    const nextCompleted = new Set(completedConversationIdsState);
    nextCompleted.delete(conversation_id);
    completedConversationIdsState = nextCompleted;
  }

  const nextConversations = reduceConversationTaskStatus(conversationsState, {
    type: 'turn-started',
    conversationId: conversation_id,
  });
  if (nextConversations !== conversationsState) {
    conversationsState = nextConversations;
    changed = true;
  }
  if (!generatingConversationIdsState.has(conversation_id)) {
    generatingConversationIdsState = new Set(generatingConversationIdsState).add(conversation_id);
    changed = true;
  }
  if (changed) emitStoreChange();
};

const applyTurnCompletedState = (event: ConversationTurnCompletedEvent) => {
  let changed = false;
  const nextConversations = reduceConversationTaskStatus(conversationsState, {
    type: 'turn-completed',
    event,
  });
  if (nextConversations !== conversationsState) {
    conversationsState = nextConversations;
    changed = true;
  }

  if (
    isConversationTerminalTurnState(event.state) &&
    activeConversationIdState !== event.session_id &&
    !completionUnreadConversationIdsState.has(event.session_id)
  ) {
    completionUnreadConversationIdsState = new Set(completionUnreadConversationIdsState).add(event.session_id);
    changed = true;
  }

  markCompleted(event.session_id);
  if (generatingConversationIdsState.has(event.session_id)) {
    const nextGenerating = new Set(generatingConversationIdsState);
    nextGenerating.delete(event.session_id);
    generatingConversationIdsState = nextGenerating;
    changed = true;
  }

  if (changed) emitStoreChange();
};

const clearCompleted = (conversation_id: string) => {
  if (!completedConversationIdsState.has(conversation_id)) {
    return;
  }

  const next = new Set(completedConversationIdsState);
  next.delete(conversation_id);
  completedConversationIdsState = next;
};

const logLateStreamIgnored = (conversation_id: string, type: string) => {
  void ipcBridge.application.writeRendererLog
    .invoke({
      level: 'warn',
      tag: 'conversationRuntimeView',
      message: 'late_stream_ignored_for_runtime',
      data: {
        conversation_id,
        stream_type: type,
      },
    })
    .catch(() => {});
};

const setActiveConversationState = (conversation_id: string | null) => {
  activeConversationIdState = conversation_id;
};

const initializeConversationListSyncStore = () => {
  if (isStoreInitialized) {
    return;
  }

  isStoreInitialized = true;
  refreshConversations();

  addEventListener('chat.history.refresh', scheduleConversationRefresh);
  ipcBridge.conversation.listChanged.on((event) => {
    const known = conversation_idsState.has(event.conversation_id);
    const row = conversationsState.find((conversation) => conversation.id === event.conversation_id);
    const active =
      generatingConversationIdsState.has(event.conversation_id) ||
      Boolean(row?.runtime && isConversationRuntimeUnfinished(row.runtime));
    unknownConversationRefreshGuard.remember(event.conversation_id);
    if (event.action === 'deleted') {
      clearGenerating(event.conversation_id);
      clearCompletionUnreadState(event.conversation_id);
      clearCompleted(event.conversation_id);
    }
    if (
      shouldRefreshConversationListAfterChange({
        action: event.action,
        known,
        active,
      })
    ) {
      scheduleConversationRefresh();
    }
  });
  ipcBridge.conversation.userCreated.on((event) => {
    if (
      unknownConversationRefreshGuard.shouldRefresh(
        event.conversation_id,
        conversation_idsState.has(event.conversation_id)
      )
    ) {
      scheduleConversationRefresh();
    }
    applyTurnStartedState(event.conversation_id);
  });
  ipcBridge.conversation.responseStream.on((message) => {
    const conversation_id = message.conversation_id;
    if (!conversation_id) {
      return;
    }

    if (unknownConversationRefreshGuard.shouldRefresh(conversation_id, conversation_idsState.has(conversation_id))) {
      scheduleConversationRefresh();
    }

    if (isConversationTerminalStreamMessage(message)) {
      const wasGenerating = generatingConversationIdsState.has(conversation_id);
      if (wasGenerating && activeConversationIdState !== conversation_id) {
        markCompletionUnread(conversation_id);
      }
      clearGenerating(conversation_id);
      return;
    }

    const decision = getConversationTaskStreamGuardDecision({
      type: message.type,
      completed: completedConversationIdsState.has(conversation_id),
    });
    if (decision.clearCompleted) {
      applyTurnStartedState(conversation_id);
    }
    if (decision.lateIgnored) {
      logLateStreamIgnored(conversation_id, message.type);
      return;
    }
    if (decision.markGenerating && !decision.clearCompleted) {
      markGenerating(conversation_id);
    }
  });
  ipcBridge.conversation.turnCompleted.on((event) => {
    applyTurnCompletedState(event);
    scheduleConversationRefresh();
  });
  ipcBridge.runtime.statusChanged.on((event) => {
    if (event.scope.kind !== 'conversation' || !event.terminal_status) return;

    const nextConversations = reduceConversationTaskStatus(conversationsState, {
      type: 'runtime-terminal',
      event,
    });
    let changed = nextConversations !== conversationsState;
    conversationsState = nextConversations;
    completedConversationIdsState = new Set(completedConversationIdsState).add(event.scope.id);
    if (generatingConversationIdsState.has(event.scope.id)) {
      const nextGenerating = new Set(generatingConversationIdsState);
      nextGenerating.delete(event.scope.id);
      generatingConversationIdsState = nextGenerating;
      changed = true;
    }
    if (changed) emitStoreChange();
    scheduleConversationRefresh();
  });
  addEventListener('synonbiomed.runtime.reconciled', (conversationId, runtime) => {
    const nextConversations = reduceConversationTaskStatus(conversationsState, {
      type: 'runtime-reconciled',
      conversationId,
      runtime,
    });
    let changed = nextConversations !== conversationsState;
    conversationsState = nextConversations;
    const unfinished = isConversationRuntimeUnfinished(runtime);
    if (unfinished) {
      clearCompleted(conversationId);
    } else if (isConversationTaskTerminalStatus(runtime.task_status)) {
      markCompleted(conversationId);
    }
    if (!unfinished && generatingConversationIdsState.has(conversationId)) {
      const nextGenerating = new Set(generatingConversationIdsState);
      nextGenerating.delete(conversationId);
      generatingConversationIdsState = nextGenerating;
      changed = true;
    }
    if (unfinished && !generatingConversationIdsState.has(conversationId)) {
      generatingConversationIdsState = new Set(generatingConversationIdsState).add(conversationId);
      changed = true;
    }
    if (changed) emitStoreChange();
  });
};

export const useConversationListSync = () => {
  useEffect(() => {
    initializeConversationListSyncStore();
  }, []);

  const {
    conversations,
    generatingConversationIds,
    completionUnreadConversationIds,
    hasMoreConversations,
    isLoadingMoreConversations,
  } = useSyncExternalStore(
    subscribeConversationListSync,
    getConversationListSyncSnapshot,
    getConversationListSyncSnapshot
  );

  const clearCompletionUnread = useCallback((conversation_id: string) => {
    clearCompletionUnreadState(conversation_id);
  }, []);

  const setActiveConversation = useCallback((conversation_id: string | null) => {
    setActiveConversationState(conversation_id);
  }, []);

  const loadMoreConversations = useCallback(() => {
    loadMoreConversationsState();
  }, []);

  const isConversationGenerating = useCallback(
    (conversation_id: string) => {
      return generatingConversationIds.has(conversation_id);
    },
    [generatingConversationIds]
  );

  const hasCompletionUnread = useCallback(
    (conversation_id: string) => {
      return completionUnreadConversationIds.has(conversation_id);
    },
    [completionUnreadConversationIds]
  );

  return {
    conversations,
    isConversationGenerating,
    hasCompletionUnread,
    clearCompletionUnread,
    setActiveConversation,
    hasMoreConversations,
    isLoadingMoreConversations,
    loadMoreConversations,
  };
};
