/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { ConversationTurnCompletedEvent } from '@/common/adapter/conversationRuntimeProtocol';
import type { IRuntimeStatusEvent } from '@/common/adapter/ipcBridge';
import type { TChatConversation, TChatConversationStatus, TConversationRuntimeSummary } from '@/common/config/storage';

export type ConversationTaskIndicatorState = 'idle' | 'running' | 'paused' | 'success' | 'attention';

const completedFrameStatuses = new Set(['completed', 'finished', 'success', 'succeeded']);

const activeFrameStatuses = new Set([
  'created',
  'pending',
  'queued',
  'processing',
  'running',
  'executing',
  'in_progress',
  'in-progress',
  'awaiting_user_response',
  'awaiting_plan_approval',
  'needs_input',
  'awaiting_input',
]);

const attentionFrameStatuses = new Set(['cancelled', 'canceled', 'failed', 'error', 'interrupted', 'stopped']);

const terminalIndicatorState = (status?: TChatConversationStatus): ConversationTaskIndicatorState | null => {
  if (status === 'finished') return 'success';
  if (status === 'error' || status === 'cancelled') return 'attention';
  return null;
};

export const isConversationTaskTerminalStatus = (
  status: TChatConversation['status']
): status is 'finished' | 'error' | 'cancelled' =>
  status === 'finished' || status === 'error' || status === 'cancelled';

const isGeneratingStreamMessage = (type: string): boolean =>
  type === 'content' ||
  type === 'start' ||
  type === 'thought' ||
  type === 'thinking' ||
  type === 'tool_group' ||
  type === 'acp_tool_call' ||
  type === 'acp_permission' ||
  type === 'permission' ||
  type === 'plan';

const isTerminalAgentStatus = (data: unknown): boolean => {
  if (!data || typeof data !== 'object') return false;
  const { status } = data as { status?: string };
  return status === 'error' || status === 'disconnected';
};

export const isConversationTerminalStreamMessage = (message: { type: string; data: unknown }): boolean =>
  message.type === 'finish' ||
  message.type === 'error' ||
  (message.type === 'agent_status' && isTerminalAgentStatus(message.data));

export const isConversationTerminalTurnState = (state: string): boolean =>
  state === 'ai_waiting_input' || state === 'error' || state === 'stopped';

export type ConversationTaskStreamGuardDecision = {
  markGenerating: boolean;
  clearCompleted: boolean;
  lateIgnored: boolean;
};

/** Stream packets are hints; only a real start may reopen a terminal task. */
export const getConversationTaskStreamGuardDecision = ({
  type,
  completed,
}: {
  type: string;
  completed: boolean;
}): ConversationTaskStreamGuardDecision => {
  if (!isGeneratingStreamMessage(type)) {
    return { markGenerating: false, clearCompleted: false, lateIgnored: false };
  }
  if (type === 'start') {
    return { markGenerating: true, clearCompleted: true, lateIgnored: false };
  }
  if (completed) {
    return { markGenerating: false, clearCompleted: false, lateIgnored: true };
  }
  return { markGenerating: true, clearCompleted: false, lateIgnored: false };
};

/**
 * A task remains unfinished while it is executing, starting, cancelling, or
 * waiting for a recoverable user/runtime boundary. These phases must share one
 * stable running projection; they are not terminal failures.
 */
export const isConversationRuntimeUnfinished = (runtime?: TConversationRuntimeSummary): boolean => {
  if (!runtime) return false;
  if (terminalIndicatorState(runtime.task_status)) return false;
  return (
    runtime.task_status === 'pending' ||
    runtime.task_status === 'running' ||
    runtime.is_processing ||
    runtime.has_task ||
    runtime.state !== 'idle'
  );
};

const isConversationRuntimeExplicitlyPaused = (runtime?: TConversationRuntimeSummary): boolean =>
  Boolean(
    runtime?.state === 'paused' &&
    runtime.task_status === 'pending' &&
    runtime.has_task === true &&
    runtime.is_processing === false &&
    runtime.pending_confirmations === 0 &&
    isConversationRuntimeUnfinished(runtime)
  );

export const projectConversationRuntimeFrameStatus = (runtime: TConversationRuntimeSummary): string | undefined => {
  if (runtime.task_status === 'finished') return 'completed';
  if (runtime.task_status === 'error') return 'failed';
  if (runtime.task_status === 'cancelled') return 'cancelled';
  if (isConversationRuntimeUnfinished(runtime)) return 'running';
  return undefined;
};

export const projectConversationRuntimeStatus = (
  runtime: TConversationRuntimeSummary,
  fallback?: TChatConversationStatus
): TChatConversationStatus | undefined => {
  if (runtime.task_status) return runtime.task_status;
  if (isConversationRuntimeUnfinished(runtime)) return 'running';
  return fallback;
};

const completedFrameStatusByTurnStatus = {
  finished: 'completed',
  error: 'failed',
  cancelled: 'cancelled',
} as const;

const conversationStatusByRuntimeTerminalStatus = {
  completed: 'finished',
  failed: 'error',
  cancelled: 'cancelled',
} as const;

const frameStatusByRuntimeTerminalStatus = {
  completed: 'completed',
  failed: 'failed',
  cancelled: 'cancelled',
} as const;

const terminalRuntimeSummary = (status: 'finished' | 'error' | 'cancelled'): TConversationRuntimeSummary => ({
  state: 'idle',
  can_send_message: true,
  has_task: false,
  task_status: status,
  is_processing: false,
  pending_confirmations: 0,
  turn_id: null,
});

/** A canonical start replaces the previous turn's terminal projection. */
export const mergeConversationTurnStarted = (
  conversations: TChatConversation[],
  conversationId: string
): TChatConversation[] => {
  let changed = false;
  const next = conversations.map((conversation): TChatConversation => {
    if (conversation.id !== conversationId) return conversation;
    changed = true;
    const runtime: TConversationRuntimeSummary = {
      state: 'running',
      can_send_message: false,
      has_task: true,
      task_status: 'running',
      is_processing: true,
      pending_confirmations: 0,
      turn_id: conversationId,
    };
    if (conversation.type !== 'acp') return { ...conversation, status: 'running', runtime };
    return {
      ...conversation,
      status: 'running',
      runtime,
      extra: { ...conversation.extra, frame_status: 'running' },
    };
  });
  return changed ? next : conversations;
};

/** Apply the authoritative turn terminal publication immediately. */
export const mergeConversationTurnCompleted = (
  conversations: TChatConversation[],
  event: Pick<ConversationTurnCompletedEvent, 'session_id' | 'status' | 'runtime'>
): TChatConversation[] => {
  let changed = false;
  const next = conversations.map((conversation): TChatConversation => {
    if (conversation.id !== event.session_id) return conversation;
    changed = true;
    if (conversation.type !== 'acp') {
      return { ...conversation, status: event.status, runtime: event.runtime };
    }
    return {
      ...conversation,
      status: event.status,
      runtime: event.runtime,
      extra: {
        ...conversation.extra,
        frame_status: completedFrameStatusByTurnStatus[event.status],
      },
    };
  });
  return changed ? next : conversations;
};

/**
 * Startup failures and explicit cancellation can publish without a completed
 * turn. Normalize them, including runtime, so stale processing cannot keep the
 * spinner after a terminal authority has arrived.
 */
export const mergeConversationRuntimeTerminalStatus = (
  conversations: TChatConversation[],
  event: Pick<IRuntimeStatusEvent, 'scope' | 'terminal_status'>
): TChatConversation[] => {
  if (event.scope.kind !== 'conversation' || !event.terminal_status) return conversations;

  const status = conversationStatusByRuntimeTerminalStatus[event.terminal_status];
  const runtime = terminalRuntimeSummary(status);
  let changed = false;
  const next = conversations.map((conversation): TChatConversation => {
    if (conversation.id !== event.scope.id) return conversation;
    changed = true;
    if (conversation.type !== 'acp') return { ...conversation, status, runtime };
    return {
      ...conversation,
      status,
      runtime,
      extra: {
        ...conversation.extra,
        frame_status: frameStatusByRuntimeTerminalStatus[event.terminal_status],
      },
    };
  });
  return changed ? next : conversations;
};

export const mergeConversationRuntimeReconciled = (
  conversations: TChatConversation[],
  conversationId: string,
  runtime: TConversationRuntimeSummary
): TChatConversation[] => {
  let changed = false;
  const next = conversations.map((conversation): TChatConversation => {
    if (conversation.id !== conversationId) return conversation;
    changed = true;
    const frameStatus = projectConversationRuntimeFrameStatus(runtime);
    const status = projectConversationRuntimeStatus(runtime, conversation.status);
    if (conversation.type !== 'acp' || !frameStatus) return { ...conversation, status, runtime };
    return {
      ...conversation,
      status,
      runtime,
      extra: { ...conversation.extra, frame_status: frameStatus },
    };
  });
  return changed ? next : conversations;
};

/** Preserve a just-seen terminal state across a lagging list refresh. */
export const mergeConversationListRefresh = (
  current: TChatConversation[],
  incoming: TChatConversation[],
  completedConversationIds: ReadonlySet<string>
): TChatConversation[] => {
  if (!current.length) return incoming;
  const currentById = new Map(current.map((conversation) => [conversation.id, conversation]));
  let changed = false;
  const next = incoming.map((candidate): TChatConversation => {
    const projected = currentById.get(candidate.id);
    // The collection endpoint can briefly retain a processing Frame after the
    // selected task's transcript authority has published an explicit pause.
    // Keep that higher-fidelity runtime until a real turn-start event reopens
    // the task; otherwise the sidebar spins while the task center says paused.
    if (projected && isConversationRuntimeExplicitlyPaused(projected.runtime)) {
      changed = true;
      if (candidate.type === 'acp' && projected.type === 'acp') {
        return {
          ...candidate,
          status: projected.status,
          runtime: projected.runtime,
          extra: { ...candidate.extra, frame_status: projected.extra?.frame_status },
        };
      }
      return { ...candidate, status: projected.status, runtime: projected.runtime };
    }
    if (isConversationTaskTerminalStatus(candidate.status)) {
      return candidate;
    }
    if (!completedConversationIds.has(candidate.id)) return candidate;
    if (!projected || !isConversationTaskTerminalStatus(projected.status)) return candidate;
    changed = true;
    if (candidate.type === 'acp' && projected.type === 'acp') {
      return {
        ...candidate,
        status: projected.status,
        runtime: projected.runtime,
        extra: { ...candidate.extra, frame_status: projected.extra?.frame_status },
      };
    }
    return { ...candidate, status: projected.status, runtime: projected.runtime };
  });
  return changed ? next : incoming;
};

export type ConversationTaskStatusAction =
  | { type: 'turn-started'; conversationId: string }
  | {
      type: 'turn-completed';
      event: Pick<ConversationTurnCompletedEvent, 'session_id' | 'status' | 'runtime'>;
    }
  | { type: 'runtime-terminal'; event: Pick<IRuntimeStatusEvent, 'scope' | 'terminal_status'> }
  | { type: 'runtime-reconciled'; conversationId: string; runtime: TConversationRuntimeSummary }
  | {
      type: 'list-refreshed';
      incoming: TChatConversation[];
      completedConversationIds: ReadonlySet<string>;
    };

/**
 * The sole sidebar task-status reducer. Event subscriptions only translate
 * transport events into actions and never project row status themselves.
 */
export const reduceConversationTaskStatus = (
  conversations: TChatConversation[],
  action: ConversationTaskStatusAction
): TChatConversation[] => {
  switch (action.type) {
    case 'turn-started':
      return mergeConversationTurnStarted(conversations, action.conversationId);
    case 'turn-completed':
      return mergeConversationTurnCompleted(conversations, action.event);
    case 'runtime-terminal':
      return mergeConversationRuntimeTerminalStatus(conversations, action.event);
    case 'runtime-reconciled':
      return mergeConversationRuntimeReconciled(conversations, action.conversationId, action.runtime);
    case 'list-refreshed':
      return mergeConversationListRefresh(conversations, action.incoming, action.completedConversationIds);
  }
};

/**
 * Resolve the sidebar indicator from one ordered authority chain:
 * runtime snapshot -> persisted conversation task status -> live stream hint
 * -> legacy frame status. Internal resumable phases never become failures.
 */
export const resolveConversationTaskIndicatorState = (
  conversation: TChatConversation,
  isGenerating: boolean
): ConversationTaskIndicatorState => {
  const frameStatus =
    conversation.type === 'acp' && typeof conversation.extra?.frame_status === 'string'
      ? conversation.extra.frame_status.trim().toLowerCase()
      : '';
  const runtimeUnfinished = isConversationRuntimeUnfinished(conversation.runtime);
  const explicitlyPaused = isConversationRuntimeExplicitlyPaused(conversation.runtime);
  if (explicitlyPaused) return 'paused';
  const runtimeTerminal = terminalIndicatorState(conversation.runtime?.task_status);
  if (runtimeTerminal) return runtimeTerminal;
  const conversationTerminal = terminalIndicatorState(conversation.status);
  if (conversationTerminal) return conversationTerminal;
  if (runtimeUnfinished) return 'running';

  if (completedFrameStatuses.has(frameStatus)) return 'success';
  if (attentionFrameStatuses.has(frameStatus)) return 'attention';
  if (conversation.status === 'pending' || conversation.status === 'running') {
    return 'running';
  }

  // A terminal persisted state above deliberately wins over a late stream
  // packet. The list synchronizer clears that terminal guard on a real new
  // start before setting this live hint.
  if (isGenerating) return 'running';
  if (activeFrameStatuses.has(frameStatus)) return 'running';
  return 'idle';
};
