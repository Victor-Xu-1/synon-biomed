/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { decodeConversationRuntimeSummary } from '@/common/adapter/conversationRuntimeProtocol';
import type { TChatConversation, TConversationRuntimeSummary } from '@/common/config/storage';
import { redactErrorText } from '@/renderer/pages/conversation/platforms/acp/errorDiagnostics';
import { getConversationOrNull } from '@/renderer/pages/conversation/utils/conversationCache';
import {
  getRendererAccountScopeToken,
  registerRendererAccountReset,
  rendererAccountScopedKey,
} from '@/renderer/services/rendererAccountScope';
import { useCallback, useEffect, useLayoutEffect, useRef, useSyncExternalStore } from 'react';
import {
  conversationDeleted,
  getConversationRuntimeViewSnapshot,
  hydrateFailed,
  hydrateStarted,
  hydrateSucceeded,
  localSendAccepted,
  localSendFailed,
  localSendStarted,
  localStopAcknowledged,
  localStopRequested,
  resetLocalGate,
  subscribeConversationRuntimeView,
  turnCompleted,
  type ConversationRuntimeView,
  type ConversationRuntimeViewLogEntry,
} from './conversationRuntimeViewStore';

type UseConversationRuntimeViewReturn = {
  view: ConversationRuntimeView;
  hydrated: boolean;
  hydrationError: string | null;
  state: ConversationRuntimeView['state'];
  isProcessing: boolean;
  canSendMessage: boolean;
  activeTurnId: string | null;
  markSendStarted: () => void;
  markSendAccepted: (turn_id: string, runtime: TConversationRuntimeSummary, msg_id?: string) => void;
  markSendFailed: (reason: string) => void;
  markStopRequested: (turn_id: string) => void;
  markStopAcknowledged: (turn_id: string, runtime: TConversationRuntimeSummary) => void;
  resetLocalGate: (reason: string) => void;
  retryHydration: () => void;
};

const normalizeReason = (reason: string): string => redactErrorText(reason).trim().slice(0, 200) || 'unknown';

const logConversationRuntimeView = (entry: ConversationRuntimeViewLogEntry): void => {
  const rendererLogger = ipcBridge.application?.writeRendererLog;
  if (!rendererLogger) {
    return;
  }

  void rendererLogger
    .invoke({
      level: entry.level,
      tag: 'conversationRuntimeView',
      message: entry.event,
      data: entry.data,
    })
    .catch(() => {});
};

const flushRuntimeViewLogs = (logs: ConversationRuntimeViewLogEntry[]): void => {
  logs.forEach(logConversationRuntimeView);
};

type RuntimeHydrationIntent = {
  releasePendingLocalSend: boolean;
  completedTurnId: string | null;
};

type RuntimeHydrationCoordinator = {
  inFlight: Promise<void> | null;
  trailing: RuntimeHydrationIntent | null;
};

type RuntimeBinding = {
  references: number;
  reconcileTerminalFromAuthority: boolean;
  dispose: () => void;
};

const runtimeHydrationCoordinators = new Map<string, RuntimeHydrationCoordinator>();
const runtimeBindings = new Map<string, RuntimeBinding>();

function clearConversationRuntimeCoordinators(): void {
  runtimeHydrationCoordinators.clear();
  for (const binding of runtimeBindings.values()) binding.dispose();
  runtimeBindings.clear();
}

registerRendererAccountReset('conversation-runtime-coordinators', clearConversationRuntimeCoordinators);

const mergeRuntimeHydrationIntent = (
  current: RuntimeHydrationIntent | null,
  next: RuntimeHydrationIntent
): RuntimeHydrationIntent => ({
  releasePendingLocalSend: Boolean(current?.releasePendingLocalSend || next.releasePendingLocalSend),
  completedTurnId: next.completedTurnId ?? current?.completedTurnId ?? null,
});

const hydrateConversationRuntime = async (conversationId: string, intent: RuntimeHydrationIntent): Promise<void> => {
  const accountScope = getRendererAccountScopeToken();
  flushRuntimeViewLogs(hydrateStarted(conversationId));
  try {
    const conversation = await getConversationOrNull(conversationId);
    if (getRendererAccountScopeToken() !== accountScope) return;
    flushRuntimeViewLogs(
      hydrateSucceeded(conversationId, decodeConversationRuntimeSummary(conversation?.runtime), {
        releasePendingLocalSend: intent.releasePendingLocalSend,
        ...(intent.completedTurnId ? { completedTurnId: intent.completedTurnId } : {}),
      })
    );
  } catch (error) {
    if (getRendererAccountScopeToken() !== accountScope) return;
    const reason = error instanceof Error ? error.message : String(error);
    flushRuntimeViewLogs(hydrateFailed(conversationId, normalizeReason(reason)));
  }
};

const requestConversationRuntimeHydration = (
  conversationId: string,
  intent: RuntimeHydrationIntent,
  force: boolean
): Promise<void> => {
  if (!conversationId) return Promise.resolve();
  if (!force && getConversationRuntimeViewSnapshot(conversationId).hydrated) {
    return Promise.resolve();
  }

  const coordinatorKey = rendererAccountScopedKey(conversationId);
  let coordinator = runtimeHydrationCoordinators.get(coordinatorKey);
  if (!coordinator) {
    coordinator = { inFlight: null, trailing: null };
    runtimeHydrationCoordinators.set(coordinatorKey, coordinator);
  }
  if (coordinator.inFlight) {
    if (force) {
      coordinator.trailing = mergeRuntimeHydrationIntent(coordinator.trailing, intent);
    }
    return coordinator.inFlight;
  }

  const activeCoordinator = coordinator;
  const drain = async (current: RuntimeHydrationIntent): Promise<void> => {
    await hydrateConversationRuntime(conversationId, current);
    const trailing = activeCoordinator.trailing;
    activeCoordinator.trailing = null;
    if (trailing) {
      return drain(trailing);
    }
  };
  const inFlight = drain(intent).finally(() => {
    activeCoordinator.inFlight = null;
    const trailing = activeCoordinator.trailing;
    activeCoordinator.trailing = null;
    if (trailing) {
      void requestConversationRuntimeHydration(conversationId, trailing, true);
      return;
    }
    if (runtimeHydrationCoordinators.get(coordinatorKey) === activeCoordinator) {
      runtimeHydrationCoordinators.delete(coordinatorKey);
    }
  });
  activeCoordinator.inFlight = inFlight;
  return inFlight;
};

const acquireConversationRuntimeBinding = (
  conversationId: string,
  options: { reconcileTerminalFromAuthority?: boolean } = {}
): (() => void) => {
  const bindingKey = rendererAccountScopedKey(conversationId);
  const existing = runtimeBindings.get(bindingKey);
  if (existing) {
    existing.reconcileTerminalFromAuthority ||= options.reconcileTerminalFromAuthority === true;
    existing.references += 1;
    return () => {
      existing.references -= 1;
      if (existing.references > 0 || runtimeBindings.get(bindingKey) !== existing) return;
      runtimeBindings.delete(bindingKey);
      existing.dispose();
    };
  }

  const disposers: Array<() => void> = [];
  const turnCompletedEmitter = ipcBridge.conversation.turnCompleted;
  const listChangedEmitter = ipcBridge.conversation.listChanged;
  const runtimeStatusEmitter = ipcBridge.runtime.statusChanged;
  if (turnCompletedEmitter && listChangedEmitter && runtimeStatusEmitter) {
    disposers.push(
      turnCompletedEmitter.on((event) => {
        if (event.session_id !== conversationId) return;
        if (binding.reconcileTerminalFromAuthority) {
          // Transcript terminal publications can arrive late after a durable
          // auto-resume has already started a newer execution segment. Treat
          // them as invalidations and read the current Frame/runner authority
          // instead of letting an older terminal event regress the capsule.
          void requestConversationRuntimeHydration(
            conversationId,
            { releasePendingLocalSend: true, completedTurnId: conversationId },
            true
          );
          return;
        }
        flushRuntimeViewLogs(turnCompleted(conversationId, event.turn_id, event.runtime));
      }),
      listChangedEmitter.on((event) => {
        if (event.conversation_id !== conversationId || event.action !== 'deleted') return;
        flushRuntimeViewLogs(conversationDeleted(conversationId));
      }),
      runtimeStatusEmitter.on((event) => {
        if (event.scope.kind !== 'conversation' || event.scope.id !== conversationId) return;
        const terminal =
          event.terminal_status === 'completed' ||
          event.terminal_status === 'failed' ||
          event.terminal_status === 'cancelled';
        if (!terminal) return;
        void requestConversationRuntimeHydration(
          conversationId,
          {
            releasePendingLocalSend: true,
            completedTurnId: conversationId,
          },
          true
        );
      })
    );
  }

  const binding: RuntimeBinding = {
    references: 1,
    reconcileTerminalFromAuthority: options.reconcileTerminalFromAuthority === true,
    dispose: () => {
      disposers.forEach((dispose) => dispose());
    },
  };
  runtimeBindings.set(bindingKey, binding);
  return () => {
    binding.references -= 1;
    if (binding.references > 0 || runtimeBindings.get(bindingKey) !== binding) return;
    runtimeBindings.delete(bindingKey);
    binding.dispose();
  };
};

export const useConversationRuntimeView = (
  conversation_id: string,
  initialConversation?: TChatConversation
): UseConversationRuntimeViewReturn => {
  const getSnapshot = useCallback(() => getConversationRuntimeViewSnapshot(conversation_id), [conversation_id]);
  const view = useSyncExternalStore(subscribeConversationRuntimeView, getSnapshot, getSnapshot);
  const routeSeededConversationRef = useRef<string | null>(null);
  const initialConversationExtra = initialConversation?.extra as { backend?: string; workspace?: string } | undefined;
  const reconcileTerminalFromAuthority =
    initialConversationExtra?.backend?.trim().toLowerCase() === 'synonbiomed' ||
    Boolean(initialConversationExtra?.workspace?.startsWith('synonbiomed://'));

  // The route already loaded the authoritative conversation before mounting the
  // chat tree. Seed the shared runtime store before passive hydration effects so
  // every nested consumer observes the same snapshot without another GET. This
  // is mount/navigation bootstrap state, not a live authority: a message refresh
  // can recreate the route object while it still contains the pre-AskUser
  // waiting snapshot, and replaying it would overwrite the accepted running
  // state published by the input-resolution response.
  useLayoutEffect(() => {
    if (!conversation_id || initialConversation?.id !== conversation_id || initialConversation.runtime === undefined) {
      return;
    }
    if (routeSeededConversationRef.current === conversation_id) return;
    try {
      const runtime = decodeConversationRuntimeSummary(initialConversation.runtime);
      routeSeededConversationRef.current = conversation_id;
      flushRuntimeViewLogs(hydrateSucceeded(conversation_id, runtime));
    } catch (error) {
      const reason = error instanceof Error ? error.message : String(error);
      flushRuntimeViewLogs(hydrateFailed(conversation_id, normalizeReason(reason)));
    }
  }, [conversation_id, initialConversation]);

  useEffect(() => {
    if (!conversation_id) {
      return;
    }
    void requestConversationRuntimeHydration(
      conversation_id,
      { releasePendingLocalSend: false, completedTurnId: null },
      false
    );
  }, [conversation_id]);

  useEffect(() => {
    if (!conversation_id) {
      return;
    }
    return acquireConversationRuntimeBinding(conversation_id, { reconcileTerminalFromAuthority });
  }, [conversation_id, reconcileTerminalFromAuthority]);

  const markSendStarted = useCallback(() => {
    flushRuntimeViewLogs(localSendStarted(conversation_id));
  }, [conversation_id]);

  const markSendAccepted = useCallback(
    (turn_id: string, runtime: TConversationRuntimeSummary, msg_id?: string) => {
      flushRuntimeViewLogs(localSendAccepted(conversation_id, turn_id, runtime, msg_id));
    },
    [conversation_id]
  );

  const markSendFailed = useCallback(
    (reason: string) => {
      flushRuntimeViewLogs(localSendFailed(conversation_id, normalizeReason(reason)));
    },
    [conversation_id]
  );

  const markStopRequested = useCallback(
    (turn_id: string) => {
      flushRuntimeViewLogs(localStopRequested(conversation_id, turn_id));
    },
    [conversation_id]
  );

  const markStopAcknowledged = useCallback(
    (turn_id: string, runtime: TConversationRuntimeSummary) => {
      flushRuntimeViewLogs(localStopAcknowledged(conversation_id, turn_id, runtime));
    },
    [conversation_id]
  );

  const resetLocalRuntimeGate = useCallback(
    (reason: string) => {
      flushRuntimeViewLogs(resetLocalGate(conversation_id, normalizeReason(reason)));
    },
    [conversation_id]
  );

  const retryHydration = useCallback(() => {
    void requestConversationRuntimeHydration(
      conversation_id,
      { releasePendingLocalSend: false, completedTurnId: null },
      true
    );
  }, [conversation_id]);

  return {
    view,
    hydrated: view.hydrated,
    hydrationError: view.hydrationError,
    state: view.state,
    isProcessing: view.isProcessing,
    canSendMessage: view.canSendMessage,
    activeTurnId: view.activeTurnId,
    markSendStarted,
    markSendAccepted,
    markSendFailed,
    markStopRequested,
    markStopAcknowledged,
    resetLocalGate: resetLocalRuntimeGate,
    retryHydration,
  };
};

export const logStreamTerminalObserved = (
  conversation_id: string,
  turn_id: string | undefined,
  platform: 'acp',
  stream_type: string
): void => {
  const rendererLogger = ipcBridge.application?.writeRendererLog;
  if (!rendererLogger) {
    return;
  }

  void rendererLogger
    .invoke({
      level: 'info',
      tag: 'conversationRuntimeView',
      message: 'stream_terminal_observed',
      data: {
        conversation_id,
        turn_id,
        platform,
        stream_type,
      },
    })
    .catch(() => {});
};
