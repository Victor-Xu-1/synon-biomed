import {
  decodeConversationCancelAcknowledgement,
  decodeConversationRuntimeEnsure,
  decodeConversationRuntimeSummary,
  decodeConversationSendAccepted,
  decodeConversationTurnCompleted,
} from '@/common/adapter/conversationRuntimeProtocol';
import { describe, expect, it } from 'vitest';

const runtime = (overrides: Record<string, unknown> = {}) => ({
  state: 'idle',
  can_send_message: true,
  has_task: false,
  task_status: 'finished',
  is_processing: false,
  pending_confirmations: 0,
  turn_id: null,
  ...overrides,
});

const completed = (overrides: Record<string, unknown> = {}) => ({
  session_id: 'conversation-1',
  turn_id: 'conversation-1',
  status: 'finished',
  state: 'ai_waiting_input',
  detail: 'completed',
  can_send_message: true,
  runtime: runtime(),
  workspace: '',
  model: { platform: 'synon-go', name: 'model-1', use_model: 'model-1' },
  last_message: {
    id: 'assistant-1',
    type: 'content',
    content: 'done',
    status: 'finish',
    created_at: 1_784_704_500_000,
  },
  ...overrides,
});

describe('conversation runtime protocol', () => {
  it.each([
    runtime(),
    runtime({
      state: 'starting',
      can_send_message: false,
      has_task: true,
      task_status: 'pending',
      is_processing: true,
      turn_id: 'turn-1',
    }),
    runtime({
      state: 'running',
      can_send_message: false,
      has_task: true,
      task_status: 'running',
      is_processing: true,
      turn_id: 'turn-1',
    }),
    runtime({
      state: 'cancelling',
      can_send_message: false,
      has_task: true,
      task_status: 'running',
      is_processing: true,
      turn_id: 'turn-1',
    }),
    runtime({
      state: 'paused',
      can_send_message: true,
      has_task: true,
      task_status: 'pending',
      is_processing: false,
      turn_id: 'turn-1',
    }),
    runtime({
      state: 'waiting_approval',
      can_send_message: true,
      has_task: true,
      task_status: 'pending',
      is_processing: false,
      pending_confirmations: 1,
      turn_id: 'turn-1',
    }),
    runtime({
      state: 'waiting_confirmation',
      can_send_message: false,
      has_task: true,
      task_status: 'pending',
      is_processing: true,
      pending_confirmations: 1,
      turn_id: 'turn-1',
    }),
    runtime({
      state: 'waiting_input',
      can_send_message: true,
      has_task: true,
      task_status: 'pending',
      is_processing: false,
      turn_id: 'turn-1',
    }),
    runtime({ task_status: 'error' }),
    runtime({ task_status: 'cancelled' }),
  ])('accepts an internally consistent runtime summary', (value) => {
    expect(decodeConversationRuntimeSummary(value)).toEqual(value);
  });

  it.each([
    ['missing', undefined],
    ['unknown state', runtime({ state: 'unknown' })],
    ['unsafe pending count', runtime({ pending_confirmations: Number.MAX_SAFE_INTEGER + 1 })],
    ['negative pending count', runtime({ pending_confirmations: -1 })],
    ['idle with active turn', runtime({ turn_id: 'turn-1' })],
    ['running but sendable', runtime({ state: 'running', task_status: 'running', turn_id: 'turn-1' })],
    [
      'waiting without confirmation',
      runtime({
        state: 'waiting_confirmation',
        can_send_message: false,
        has_task: true,
        task_status: 'pending',
        is_processing: true,
        turn_id: 'turn-1',
      }),
    ],
    [
      'waiting input but not sendable',
      runtime({
        state: 'waiting_input',
        can_send_message: false,
        has_task: true,
        task_status: 'pending',
        is_processing: false,
        turn_id: 'turn-1',
      }),
    ],
    [
      'paused but not sendable',
      runtime({
        state: 'paused',
        can_send_message: false,
        has_task: true,
        task_status: 'pending',
        is_processing: false,
        turn_id: 'turn-1',
      }),
    ],
    [
      'approval but not sendable',
      runtime({
        state: 'waiting_approval',
        can_send_message: false,
        has_task: true,
        task_status: 'pending',
        is_processing: false,
        pending_confirmations: 1,
        turn_id: 'turn-1',
      }),
    ],
    [
      'finished active task',
      runtime({ state: 'running', can_send_message: false, has_task: true, is_processing: true, turn_id: 'turn-1' }),
    ],
  ])('fails closed for %s', (_name, value) => {
    expect(() => decodeConversationRuntimeSummary(value)).toThrow('conversation_runtime_invalid');
  });

  it('requires a send acknowledgement to own one active runtime turn', () => {
    const accepted = {
      msg_id: 'message-1',
      turn_id: 'turn-1',
      runtime: runtime({
        state: 'running',
        can_send_message: false,
        has_task: true,
        task_status: 'running',
        is_processing: true,
        turn_id: 'turn-1',
      }),
    };
    expect(decodeConversationSendAccepted(accepted)).toEqual(accepted);
    expect(() => decodeConversationSendAccepted({ ...accepted, turn_id: 'turn-2' })).toThrow(
      'conversation_send_response_invalid'
    );
    expect(() => decodeConversationSendAccepted({ ...accepted, runtime: runtime() })).toThrow(
      'conversation_send_response_invalid'
    );
  });

  it('validates runtime ensure and cancellation acknowledgement boundaries', () => {
    const ensured = { recovered: true, config_options: [], runtime: runtime() };
    expect(decodeConversationRuntimeEnsure(ensured)).toEqual(ensured);
    expect(() => decodeConversationRuntimeEnsure({ ...ensured, config_options: null })).toThrow(
      'conversation_runtime_ensure_invalid'
    );
    expect(() => decodeConversationRuntimeEnsure({ ...ensured, runtime: runtime({ has_task: true }) })).toThrow(
      'conversation_runtime_invalid'
    );

    expect(() =>
      decodeConversationCancelAcknowledgement(
        { root_frame_id: 'root-1', cancelled_frames: ['root-1', 'child-1'] },
        'root-1'
      )
    ).not.toThrow();
    expect(() =>
      decodeConversationCancelAcknowledgement({ root_frame_id: 'root-2', cancelled_frames: ['root-2'] }, 'root-1')
    ).toThrow('conversation_cancel_response_invalid');
    expect(() =>
      decodeConversationCancelAcknowledgement({ root_frame_id: 'root-1', cancelled_frames: ['child-1'] }, 'root-1')
    ).toThrow('conversation_cancel_response_invalid');
  });

  it('decodes the exact completed and failed terminal envelopes', () => {
    expect(decodeConversationTurnCompleted(completed())).toMatchObject({
      status: 'finished',
      state: 'ai_waiting_input',
      runtime: { task_status: 'finished' },
    });
    expect(
      decodeConversationTurnCompleted(
        completed({
          status: 'error',
          state: 'error',
          runtime: runtime({ task_status: 'error' }),
          last_message: { ...completed().last_message, status: 'error' },
        })
      )
    ).toMatchObject({ status: 'error', state: 'error', runtime: { task_status: 'error' } });
  });

  it('retains explicit root and child frame scope from a terminal envelope', () => {
    const decoded = decodeConversationTurnCompleted(
      completed({ root_frame_id: 'root-frame', frame_id: 'child-frame' })
    );
    expect(decoded).toMatchObject({ root_frame_id: 'root-frame', frame_id: 'child-frame' });
  });

  it('decodes the canonical Transcript terminal envelope without legacy model or workspace fields', () => {
    const transcriptTerminal = completed({
      status: 'cancelled',
      terminal_status: 'cancelled',
      runtime: runtime({ task_status: 'cancelled' }),
      workspace: undefined,
      model: undefined,
      source_publication_sequence: 42,
      publication_boundary_id: 'transcript-web:stream-a:42',
    });

    expect(decodeConversationTurnCompleted(transcriptTerminal)).toEqual({
      session_id: 'conversation-1',
      turn_id: 'conversation-1',
      status: 'cancelled',
      state: 'ai_waiting_input',
      detail: 'completed',
      can_send_message: true,
      runtime: runtime({ task_status: 'cancelled' }),
      source_publication_sequence: 42,
      publication_boundary_id: 'transcript-web:stream-a:42',
      last_message: transcriptTerminal.last_message,
    });
  });

  it.each([
    ['camel-case aliases', completed({ session_id: undefined, sessionId: 'conversation-1' })],
    ['mismatched owner', completed({ turn_id: 'turn-2' })],
    ['guessed state', completed({ state: 'unknown' })],
    [
      'runtime still active',
      completed({
        runtime: runtime({
          state: 'running',
          can_send_message: false,
          has_task: true,
          task_status: 'running',
          is_processing: true,
          turn_id: 'conversation-1',
        }),
      }),
    ],
    ['failed event with finished runtime', completed({ status: 'error', state: 'error' })],
    ['invalid timestamp', completed({ last_message: { ...completed().last_message, created_at: 1.5 } })],
  ])('rejects %s rather than synthesizing a terminal event', (_name, value) => {
    expect(() => decodeConversationTurnCompleted(value)).toThrow();
  });
});
