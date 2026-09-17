/**
 * @vitest-environment node
 */

import { describe, expect, it } from 'vitest';
import { decodeMessageStreamPayload } from '@/common/adapter/messageStreamProtocol';

const basePayload = {
  type: 'message.stream',
  stream_type: 'text',
  data: 'delta',
  msg_id: 'assistant-conversation-1-1',
  turn_id: 'conversation-1',
  conversation_id: 'conversation-1',
  created_at: 1_784_704_500_000,
  position: 'left',
  status: 'pending',
  _event_id: 'web-stream-delta:event-1',
  _sequence: 41,
  _kind: 'fanout',
} as const;

describe('decodeMessageStreamPayload', () => {
  it('rejects transient parallel envelopes and provisional fields', () => {
    expect(decodeMessageStreamPayload({ ...basePayload, type: 'message.preview' })).toBeUndefined();
    expect(decodeMessageStreamPayload({ ...basePayload, provisional: true })).toBeUndefined();
  });
  it.each(['start', 'thinking', 'text', 'content', 'finish', 'error'] as const)(
    'preserves the explicit %s stream subtype without exposing the outer route as the message type',
    (streamType) => {
      expect(decodeMessageStreamPayload({ ...basePayload, stream_type: streamType })).toEqual({
        type: streamType,
        data: 'delta',
        msg_id: 'assistant-conversation-1-1',
        turn_id: 'conversation-1',
        conversation_id: 'conversation-1',
        created_at: 1_784_704_500_000,
        position: 'left',
        status: 'pending',
      });
    }
  );

  it('accepts a typed tool lifecycle update and rejects malformed tool data', () => {
    expect(
      decodeMessageStreamPayload({
        ...basePayload,
        stream_type: 'tool_call',
        data: {
          call_id: 'call-1',
          name: 'python',
          status: 'waiting',
          attempt: 2,
          operation_id: 'operation-1',
          parent_operation_id: 'parent-1',
          revision: 3,
        },
      })
    ).toMatchObject({
      type: 'tool_call',
      data: {
        call_id: 'call-1',
        name: 'python',
        status: 'waiting',
        attempt: 2,
        operation_id: 'operation-1',
        parent_operation_id: 'parent-1',
        revision: 3,
      },
    });
    expect(
      decodeMessageStreamPayload({
        ...basePayload,
        stream_type: 'tool_call',
        data: { call_id: 'call-1', name: 'python', status: 'successful' },
      })
    ).toBeUndefined();
    expect(
      decodeMessageStreamPayload({
        ...basePayload,
        stream_type: 'tool_call',
        data: {
          call_id: 'call-1',
          name: 'python',
          status: 'running',
          parent_operation_id: 'call-1',
        },
      })
    ).toBeUndefined();
    expect(
      decodeMessageStreamPayload({
        ...basePayload,
        stream_type: 'tool_call',
        data: {
          call_id: 'call-1',
          name: 'python',
          status: 'running',
          input: { payload: 'x'.repeat(65 * 1024) },
        },
      })
    ).toBeUndefined();
  });

  it('preserves an authoritative content reset', () => {
    const reset = decodeMessageStreamPayload({
      ...basePayload,
      replace: true,
      replace_scope: 'attempt',
      assistant_attempt_id: 'assistant-conversation-1-1',
    });
    expect(reset).toMatchObject({
      type: 'text',
      data: 'delta',
      replace: true,
      replace_scope: 'attempt',
      assistant_attempt_id: 'assistant-conversation-1-1',
    });
  });

  it.each([
    {
      ...basePayload,
      replace_scope: 'attempt',
      assistant_attempt_id: 'assistant-conversation-1-1',
    },
    { ...basePayload, replace: true, replace_scope: 'attempt' },
    {
      ...basePayload,
      replace: true,
      replace_scope: 'message',
      assistant_attempt_id: 'assistant-conversation-1-1',
    },
    {
      ...basePayload,
      stream_type: 'content',
      replace: true,
      replace_scope: 'attempt',
      assistant_attempt_id: 'assistant-conversation-1-1',
    },
  ])('rejects malformed attempt-level replacement authority', (payload) => {
    expect(decodeMessageStreamPayload(payload)).toBeUndefined();
  });

  it('preserves exact canonical artifact references on terminal events', () => {
    expect(
      decodeMessageStreamPayload({
        ...basePayload,
        stream_type: 'finish',
        artifact_refs: [
          {
            artifact_id: 'artifact-1',
            version_id: 'version-1',
            relation: 'produced',
            availability: 'available',
            attempt: 2,
            source_event_id: 9,
            ordinal: 0,
          },
        ],
      })
    ).toMatchObject({
      type: 'finish',
      artifact_refs: [
        {
          artifact_id: 'artifact-1',
          version_id: 'version-1',
          relation: 'produced',
        },
      ],
    });
  });

  it.each(['start', 'text', 'content', 'finish', 'error'] as const)(
    'preserves the shared Transcript publication boundary on %s projections',
    (streamType) => {
      expect(
        decodeMessageStreamPayload({
          ...basePayload,
          stream_type: streamType,
          source_publication_sequence: 42,
          publication_boundary_id: 'transcript-web:stream-a:42',
        })
      ).toMatchObject({
        type: streamType,
        source_publication_sequence: 42,
        publication_boundary_id: 'transcript-web:stream-a:42',
      });
    }
  );

  it('preserves a failed durable terminal state and rejects it on nonterminal streams', () => {
    expect(
      decodeMessageStreamPayload({
        ...basePayload,
        stream_type: 'error',
        status: 'error',
        terminal_status: 'failed',
        terminal_superseded: true,
      })
    ).toMatchObject({
      type: 'error',
      status: 'error',
      terminal_status: 'failed',
      terminal_superseded: true,
    });
    expect(decodeMessageStreamPayload({ ...basePayload, terminal_status: 'failed' })).toBeUndefined();
    expect(
      decodeMessageStreamPayload({
        ...basePayload,
        stream_type: 'finish',
        terminal_superseded: true,
      })
    ).toBeUndefined();
    expect(
      decodeMessageStreamPayload({
        ...basePayload,
        stream_type: 'error',
        terminal_status: 'unknown',
      })
    ).toBeUndefined();
  });

  it.each([
    [{ artifact_id: '', version_id: 'version-1', relation: 'produced' }],
    [{ artifact_id: 'artifact-1', version_id: '', relation: 'produced' }],
    [
      {
        artifact_id: 'artifact-1',
        version_id: 'version-1',
        relation: 'guessed',
      },
    ],
    [
      {
        artifact_id: 'artifact-1',
        version_id: 'version-1',
        relation: 'produced',
      },
      {
        artifact_id: 'artifact-1',
        version_id: 'version-1',
        relation: 'attached',
      },
    ],
  ])('rejects malformed or duplicate artifact references', (artifact_refs) => {
    expect(decodeMessageStreamPayload({ ...basePayload, artifact_refs })).toBeUndefined();
  });

  it.each([
    ['missing stream_type', (({ stream_type: _streamType, ...payload }) => payload)(basePayload)],
    ['unknown stream_type', { ...basePayload, stream_type: 'tool_batch' }],
    ['wrong outer route', { ...basePayload, type: 'text' }],
    ['missing message id', { ...basePayload, msg_id: '' }],
    ['whitespace message id', { ...basePayload, msg_id: ' assistant-1' }],
    ['control character in message id', { ...basePayload, msg_id: 'assistant\n1' }],
    ['oversized message id', { ...basePayload, msg_id: 'm'.repeat(257) }],
    ['missing conversation id', { ...basePayload, conversation_id: '' }],
    ['whitespace conversation id', { ...basePayload, conversation_id: 'conversation-1 ' }],
    ['whitespace turn id', { ...basePayload, turn_id: ' turn-1' }],
    ['invalid created_at', { ...basePayload, created_at: Number.POSITIVE_INFINITY }],
    ['invalid position', { ...basePayload, position: 'floating' }],
    ['invalid status', { ...basePayload, status: 'complete' }],
    ['invalid replace flag', { ...basePayload, replace: 'true' }],
    [
      'boundary without sequence',
      {
        ...basePayload,
        stream_type: 'finish',
        publication_boundary_id: 'boundary-1',
      },
    ],
    ['sequence without boundary', { ...basePayload, stream_type: 'finish', source_publication_sequence: 1 }],
  ])('fails closed for %s', (_label, payload) => {
    expect(decodeMessageStreamPayload(payload)).toBeUndefined();
  });

  it('does not infer a subtype from event metadata, status, replace, or payload data', () => {
    const { stream_type: _streamType, ...payload } = basePayload;
    expect(
      decodeMessageStreamPayload({
        ...payload,
        _event_id: 'web-stream-finish:runner-1',
        status: 'finish',
        replace: true,
        data: { type: 'finish' },
      })
    ).toBeUndefined();
  });
});
