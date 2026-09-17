/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type {
  TChatConversationStatus,
  TConversationRuntimeStateKind,
  TConversationRuntimeSummary,
} from '../config/storage';
import type { AcpConfigOptionDto } from '../types/platform/acpTypes';

export type ConversationTurnCompletedEvent = {
  session_id: string;
  turn_id: string;
  status: 'finished' | 'error' | 'cancelled';
  root_frame_id?: string;
  frame_id?: string;
  state: 'ai_waiting_input' | 'error';
  detail: string;
  can_send_message: true;
  source_publication_sequence?: number;
  publication_boundary_id?: string;
  runtime: TConversationRuntimeSummary;
  workspace?: string;
  model?: {
    platform: string;
    name: string;
    use_model: string;
  };
  last_message: {
    id?: string;
    type?: string;
    content: unknown;
    status: 'finish' | 'error';
    created_at: number;
  };
};

export type ConversationSendAccepted = {
  msg_id: string;
  turn_id: string;
  runtime: TConversationRuntimeSummary;
};

export type ConversationRuntimeEnsure = {
  recovered: boolean;
  config_options: AcpConfigOptionDto[];
  runtime: TConversationRuntimeSummary;
};

const runtimeStates = new Set<TConversationRuntimeStateKind>([
  'idle',
  'starting',
  'running',
  'cancelling',
  'paused',
  'waiting_approval',
  'waiting_confirmation',
  'waiting_input',
]);
const taskStatuses = new Set<TChatConversationStatus>(['pending', 'running', 'finished', 'error', 'cancelled']);

const recordValue = (value: unknown): Record<string, unknown> | null =>
  value !== null && typeof value === 'object' && !Array.isArray(value) ? (value as Record<string, unknown>) : null;

const exactIdentifier = (value: unknown): value is string =>
  typeof value === 'string' && value.length > 0 && value.length <= 256 && value.trim() === value;

const exactString = (value: unknown, maxLength = 2_000): value is string =>
  typeof value === 'string' && value.length <= maxLength;

const invalidRuntime = (): never => {
  throw new Error('conversation_runtime_invalid');
};

export function decodeConversationRuntimeSummary(raw: unknown): TConversationRuntimeSummary {
  const value = recordValue(raw);
  if (!value || !runtimeStates.has(value.state as TConversationRuntimeStateKind)) return invalidRuntime();
  if (typeof value.can_send_message !== 'boolean' || typeof value.has_task !== 'boolean') return invalidRuntime();
  if (!taskStatuses.has(value.task_status as TChatConversationStatus)) return invalidRuntime();
  if (typeof value.is_processing !== 'boolean') return invalidRuntime();
  if (!Number.isSafeInteger(value.pending_confirmations) || (value.pending_confirmations as number) < 0) {
    return invalidRuntime();
  }
  if (value.turn_id !== null && !exactIdentifier(value.turn_id)) return invalidRuntime();

  const state = value.state as TConversationRuntimeStateKind;
  const taskStatus = value.task_status as TChatConversationStatus;
  const waitingConfirmation = state === 'waiting_confirmation';
  const waitingApproval = state === 'waiting_approval';
  const waitingInput = state === 'waiting_input';
  const paused = state === 'paused';
  const hasTask = state !== 'idle';
  const processing = hasTask && !waitingApproval && !waitingInput && !paused;
  const sendable = state === 'idle' || waitingApproval || waitingInput || paused;
  const valid =
    value.has_task === hasTask &&
    value.is_processing === processing &&
    value.can_send_message === sendable &&
    (hasTask ? value.turn_id !== null : value.turn_id === null) &&
    (waitingConfirmation || waitingApproval
      ? (value.pending_confirmations as number) > 0
      : value.pending_confirmations === 0) &&
    (waitingApproval || waitingInput
      ? taskStatus === 'pending'
      : paused
        ? taskStatus === 'pending'
        : state === 'starting'
          ? taskStatus === 'pending'
          : state === 'running' || state === 'cancelling'
            ? taskStatus === 'running'
            : waitingConfirmation
              ? taskStatus === 'pending'
              : taskStatus === 'finished' || taskStatus === 'error' || taskStatus === 'cancelled');
  if (!valid) return invalidRuntime();

  return {
    state,
    can_send_message: value.can_send_message,
    has_task: value.has_task,
    task_status: taskStatus,
    is_processing: value.is_processing,
    pending_confirmations: value.pending_confirmations as number,
    turn_id: value.turn_id as string | null,
  };
}

export function decodeConversationSendAccepted(raw: unknown): ConversationSendAccepted {
  const value = recordValue(raw);
  if (!value || !exactIdentifier(value.msg_id) || !exactIdentifier(value.turn_id)) {
    throw new Error('conversation_send_response_invalid');
  }
  const runtime = decodeConversationRuntimeSummary(value.runtime);
  if (
    runtime.turn_id !== value.turn_id ||
    (runtime.state !== 'starting' && runtime.state !== 'running') ||
    runtime.can_send_message
  ) {
    throw new Error('conversation_send_response_invalid');
  }
  return { msg_id: value.msg_id, turn_id: value.turn_id, runtime };
}

export function decodeConversationRuntimeEnsure(raw: unknown): ConversationRuntimeEnsure {
  const value = recordValue(raw);
  if (!value || typeof value.recovered !== 'boolean' || !Array.isArray(value.config_options)) {
    throw new Error('conversation_runtime_ensure_invalid');
  }
  return {
    recovered: value.recovered,
    config_options: value.config_options as AcpConfigOptionDto[],
    runtime: decodeConversationRuntimeSummary(value.runtime),
  };
}

export function decodeConversationCancelAcknowledgement(raw: unknown, expectedRootFrameId: string): void {
  const value = recordValue(raw);
  if (!value || value.root_frame_id !== expectedRootFrameId || !Array.isArray(value.cancelled_frames)) {
    throw new Error('conversation_cancel_response_invalid');
  }
  const frameIds = value.cancelled_frames;
  if (
    frameIds.length === 0 ||
    frameIds.some((frameId) => !exactIdentifier(frameId)) ||
    new Set(frameIds).size !== frameIds.length ||
    !frameIds.includes(expectedRootFrameId)
  ) {
    throw new Error('conversation_cancel_response_invalid');
  }
}

export function decodeConversationTurnCompleted(raw: unknown): ConversationTurnCompletedEvent {
  const value = recordValue(raw);
  const runtimeValue = value ? decodeConversationRuntimeSummary(value.runtime) : invalidRuntime();
  const model = value?.model === undefined ? undefined : recordValue(value.model);
  const lastMessage = value ? recordValue(value.last_message) : null;
  const rootFrameId = value?.root_frame_id;
  const frameId = value?.frame_id;
  if (!value || model === null || !lastMessage) throw new Error('turn_completed_invalid');
  if (!exactIdentifier(value.session_id) || value.turn_id !== value.session_id) {
    throw new Error('turn_completed_invalid');
  }
  if (
    (value.source_publication_sequence !== undefined &&
      (!Number.isSafeInteger(value.source_publication_sequence) ||
        (value.source_publication_sequence as number) < 1)) ||
    (value.publication_boundary_id !== undefined && !exactIdentifier(value.publication_boundary_id)) ||
    (value.source_publication_sequence === undefined) !== (value.publication_boundary_id === undefined)
  ) {
    throw new Error('turn_completed_invalid');
  }
  if (
    (rootFrameId !== undefined && !exactIdentifier(rootFrameId)) ||
    (frameId !== undefined && !exactIdentifier(frameId))
  ) {
    throw new Error('turn_completed_invalid');
  }
  if (value.status !== 'finished' && value.status !== 'error' && value.status !== 'cancelled') {
    throw new Error('turn_completed_invalid');
  }
  const expectedState = value.status === 'error' ? 'error' : 'ai_waiting_input';
  const expectedMessageStatus = value.status === 'error' ? 'error' : 'finish';
  if (value.state !== expectedState || value.can_send_message !== true) throw new Error('turn_completed_invalid');
  let messageId: string | undefined;
  if (lastMessage.id !== undefined) {
    if (!exactIdentifier(lastMessage.id)) throw new Error('turn_completed_invalid');
    messageId = lastMessage.id;
  }
  let messageType: string | undefined;
  if (lastMessage.type !== undefined) {
    if (!exactString(lastMessage.type, 128)) throw new Error('turn_completed_invalid');
    messageType = lastMessage.type;
  }
  if (
    runtimeValue.state !== 'idle' ||
    runtimeValue.task_status !== value.status ||
    !exactString(value.detail) ||
    (value.workspace !== undefined && !exactString(value.workspace)) ||
    (model !== undefined &&
      (!exactString(model.platform, 256) || !exactString(model.name, 256) || !exactString(model.use_model, 256))) ||
    lastMessage.status !== expectedMessageStatus ||
    !Number.isSafeInteger(lastMessage.created_at) ||
    (lastMessage.created_at as number) < 0
  ) {
    throw new Error('turn_completed_invalid');
  }

  return {
    session_id: value.session_id,
    turn_id: value.turn_id as string,
    status: value.status,
    state: expectedState,
    detail: value.detail,
    can_send_message: true,
    runtime: runtimeValue,
    ...(rootFrameId === undefined ? {} : { root_frame_id: rootFrameId as string }),
    ...(frameId === undefined ? {} : { frame_id: frameId as string }),
    ...(value.source_publication_sequence === undefined
      ? {}
      : { source_publication_sequence: value.source_publication_sequence as number }),
    ...(value.publication_boundary_id === undefined
      ? {}
      : { publication_boundary_id: value.publication_boundary_id as string }),
    ...(value.workspace === undefined ? {} : { workspace: value.workspace as string }),
    ...(model === undefined
      ? {}
      : {
          model: {
            platform: model.platform as string,
            name: model.name as string,
            use_model: model.use_model as string,
          },
        }),
    last_message: {
      ...(messageId === undefined ? {} : { id: messageId }),
      ...(messageType === undefined ? {} : { type: messageType }),
      content: lastMessage.content,
      status: expectedMessageStatus,
      created_at: lastMessage.created_at as number,
    },
  };
}
