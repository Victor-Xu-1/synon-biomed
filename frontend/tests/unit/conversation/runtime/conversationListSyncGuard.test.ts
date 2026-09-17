/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, expect, it } from 'vitest';
import type { TChatConversation, TChatConversationStatus } from '@/common/config/storage';
import {
  getConversationTaskStreamGuardDecision,
  mergeConversationListRefresh,
  mergeConversationRuntimeTerminalStatus,
  mergeConversationRuntimeReconciled,
  mergeConversationTurnCompleted,
  mergeConversationTurnStarted,
  reduceConversationTaskStatus,
  resolveConversationTaskIndicatorState,
} from '@/renderer/pages/conversation/GroupedHistory/conversationTaskIndicatorModel';

const createConversation = (status: TChatConversationStatus = 'running'): TChatConversation => ({
  id: 'conversation-a',
  name: 'Task A',
  type: 'acp',
  created_at: 1,
  modified_at: 2,
  status,
  extra: {
    backend: 'synonbiomed',
    frame_status: status,
  },
});

const createTerminalEvent = (status: 'finished' | 'error' | 'cancelled') => ({
  session_id: 'conversation-a',
  status,
  runtime: {
    state: 'idle' as const,
    can_send_message: true,
    has_task: true,
    task_status: status,
    is_processing: false,
    pending_confirmations: 0,
    turn_id: null,
  },
});

describe('getConversationTaskStreamGuardDecision', () => {
  it('marks normal generating stream messages', () => {
    expect(getConversationTaskStreamGuardDecision({ type: 'content', completed: false })).toEqual({
      markGenerating: true,
      clearCompleted: false,
      lateIgnored: false,
    });
  });

  it('ignores late stream messages after turn completion', () => {
    expect(getConversationTaskStreamGuardDecision({ type: 'content', completed: true })).toEqual({
      markGenerating: false,
      clearCompleted: false,
      lateIgnored: true,
    });
  });

  it('allows a new start event to clear the completion guard', () => {
    expect(getConversationTaskStreamGuardDecision({ type: 'start', completed: true })).toEqual({
      markGenerating: true,
      clearCompleted: true,
      lateIgnored: false,
    });
  });

  it('ignores non-generating messages', () => {
    expect(
      getConversationTaskStreamGuardDecision({
        type: 'slash_commands_updated',
        completed: true,
      })
    ).toEqual({
      markGenerating: false,
      clearCompleted: false,
      lateIgnored: false,
    });
  });
});

describe('mergeConversationTurnCompleted', () => {
  it.each([
    ['finished', 'completed'],
    ['error', 'failed'],
    ['cancelled', 'cancelled'],
  ] as const)('immediately maps %s events to the sidebar read model', (status, frameStatus) => {
    const conversation = createConversation();
    const conversations = [conversation];
    const result = mergeConversationTurnCompleted(conversations, createTerminalEvent(status));

    expect(result).not.toBe(conversations);
    expect(result[0]).not.toBe(conversation);
    expect(result[0]).toMatchObject({
      id: 'conversation-a',
      status,
      runtime: {
        task_status: status,
        is_processing: false,
      },
      extra: {
        frame_status: frameStatus,
      },
    });
  });

  it('preserves the list and row identities when the event is not in the loaded page', () => {
    const conversation = createConversation();
    const conversations = [conversation];
    const event = {
      ...createTerminalEvent('finished'),
      session_id: 'conversation-not-loaded',
    };

    const result = mergeConversationTurnCompleted(conversations, event);

    expect(result).toBe(conversations);
    expect(result[0]).toBe(conversation);
  });
});

describe('conversation turn state transitions', () => {
  it('opens a new running turn over the previous terminal projection', () => {
    const previous = createConversation('error');
    previous.runtime = {
      state: 'idle',
      can_send_message: true,
      has_task: false,
      task_status: 'error',
      is_processing: false,
      pending_confirmations: 0,
      turn_id: null,
    };
    previous.extra = { ...previous.extra, frame_status: 'failed' };

    expect(mergeConversationTurnStarted([previous], previous.id)[0]).toMatchObject({
      status: 'running',
      runtime: {
        state: 'running',
        task_status: 'running',
        is_processing: true,
        has_task: true,
      },
      extra: { frame_status: 'running' },
    });
  });

  it('does not let a stale list refresh erase an observed terminal state', () => {
    const completed = mergeConversationTurnCompleted([createConversation()], createTerminalEvent('finished'));
    const staleIncoming = createConversation('running');
    staleIncoming.name = 'Fresh title';

    const result = mergeConversationListRefresh(completed, [staleIncoming], new Set(['conversation-a']));

    expect(result[0]).toMatchObject({
      name: 'Fresh title',
      status: 'finished',
      runtime: { task_status: 'finished', is_processing: false },
      extra: { frame_status: 'completed' },
    });
    expect(mergeConversationListRefresh(completed, [staleIncoming], new Set())[0]?.status).toBe('running');
  });

  it('does not let a stale processing list row erase an explicit paused runtime', () => {
    const paused = mergeConversationRuntimeReconciled([createConversation()], 'conversation-a', {
      state: 'paused',
      can_send_message: true,
      has_task: true,
      task_status: 'pending',
      is_processing: false,
      pending_confirmations: 0,
      turn_id: 'conversation-a',
    });
    const staleIncoming = createConversation('running');
    staleIncoming.runtime = {
      state: 'running',
      can_send_message: false,
      has_task: true,
      task_status: 'running',
      is_processing: true,
      pending_confirmations: 0,
      turn_id: 'conversation-a',
    };
    staleIncoming.extra = { ...staleIncoming.extra, frame_status: 'processing' };

    const refreshed = mergeConversationListRefresh(paused, [staleIncoming], new Set());

    expect(resolveConversationTaskIndicatorState(refreshed[0], true)).toBe('paused');
    expect(
      resolveConversationTaskIndicatorState(mergeConversationTurnStarted(refreshed, 'conversation-a')[0], false)
    ).toBe('running');
  });

  it('does not let a cancelled collection row override the selected paused runtime', () => {
    const paused = mergeConversationRuntimeReconciled([createConversation()], 'conversation-a', {
      state: 'paused',
      can_send_message: true,
      has_task: true,
      task_status: 'pending',
      is_processing: false,
      pending_confirmations: 0,
      turn_id: 'conversation-a',
    });
    const cancelled = createConversation('cancelled');
    cancelled.extra = { ...cancelled.extra, frame_status: 'cancelled' };

    const refreshed = mergeConversationListRefresh(paused, [cancelled], new Set());

    expect(resolveConversationTaskIndicatorState(refreshed[0], false)).toBe('paused');
  });
});

describe('mergeConversationRuntimeTerminalStatus', () => {
  it.each([
    ['completed', 'finished', 'completed'],
    ['failed', 'error', 'failed'],
    ['cancelled', 'cancelled', 'cancelled'],
  ] as const)('projects a startup %s terminal before any stream content', (terminalStatus, status, frameStatus) => {
    const result = mergeConversationRuntimeTerminalStatus([createConversation()], {
      scope: { kind: 'conversation', id: 'conversation-a' },
      terminal_status: terminalStatus,
    });

    expect(result[0]).toMatchObject({
      status,
      runtime: {
        state: 'idle',
        task_status: status,
        is_processing: false,
        has_task: false,
      },
      extra: { frame_status: frameStatus },
    });
    expect(resolveConversationTaskIndicatorState(result[0], true)).toBe(
      terminalStatus === 'completed' ? 'success' : 'attention'
    );
  });

  it('ignores MCP terminal events and unloaded conversations', () => {
    const conversations = [createConversation()];
    expect(
      mergeConversationRuntimeTerminalStatus(conversations, {
        scope: { kind: 'mcp', id: 'conversation-a' },
        terminal_status: 'failed',
      })
    ).toBe(conversations);
    expect(
      mergeConversationRuntimeTerminalStatus(conversations, {
        scope: { kind: 'conversation', id: 'conversation-b' },
        terminal_status: 'failed',
      })
    ).toBe(conversations);
  });
});

describe('reduceConversationTaskStatus', () => {
  it('is the single transition entry point for start, runtime, terminal, and list refresh actions', () => {
    const started = reduceConversationTaskStatus([createConversation('finished')], {
      type: 'turn-started',
      conversationId: 'conversation-a',
    });
    const reconciled = reduceConversationTaskStatus(started, {
      type: 'runtime-reconciled',
      conversationId: 'conversation-a',
      runtime: {
        state: 'waiting_input',
        can_send_message: true,
        has_task: true,
        task_status: 'pending',
        is_processing: false,
        pending_confirmations: 0,
        turn_id: 'turn-a',
      },
    });
    const completed = reduceConversationTaskStatus(reconciled, {
      type: 'runtime-terminal',
      event: {
        scope: { kind: 'conversation', id: 'conversation-a' },
        terminal_status: 'completed',
      },
    });
    const refreshed = reduceConversationTaskStatus(completed, {
      type: 'list-refreshed',
      incoming: [createConversation('running')],
      completedConversationIds: new Set(['conversation-a']),
    });

    expect(resolveConversationTaskIndicatorState(started[0], false)).toBe('running');
    expect(resolveConversationTaskIndicatorState(reconciled[0], false)).toBe('running');
    expect(resolveConversationTaskIndicatorState(completed[0], true)).toBe('success');
    expect(resolveConversationTaskIndicatorState(refreshed[0], true)).toBe('success');
  });
});

describe('mergeConversationRuntimeReconciled', () => {
  it('keeps a retained non-processing task in the unfinished running state', () => {
    const runtime = {
      state: 'idle' as const,
      can_send_message: true,
      has_task: true,
      task_status: 'running' as const,
      is_processing: false,
      pending_confirmations: 0,
      turn_id: 'turn-a',
    };
    const result = mergeConversationRuntimeReconciled([createConversation()], 'conversation-a', runtime);

    expect(result[0]).toMatchObject({
      status: 'running',
      runtime,
      extra: { frame_status: 'running' },
    });
  });

  it('maps active runtime and preserves unloaded rows', () => {
    const conversations = [createConversation()];
    const runtime = {
      state: 'running' as const,
      can_send_message: false,
      has_task: true,
      task_status: 'running' as const,
      is_processing: true,
      pending_confirmations: 0,
      turn_id: 'turn-a',
    };
    expect(mergeConversationRuntimeReconciled(conversations, 'conversation-a', runtime)[0]?.extra?.frame_status).toBe(
      'running'
    );
    expect(mergeConversationRuntimeReconciled(conversations, 'conversation-b', runtime)).toBe(conversations);
  });
});
