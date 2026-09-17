import { describe, expect, it, vi } from 'vitest';
import { REALTIME_MAX_INBOUND_BYTES, parseRealtimeMessage } from '@/common/adapter/realtimeProtocol';

const encode = (value: unknown) => JSON.stringify(value);

describe('realtimeProtocol', () => {
  it('accepts authoritative connected cursors and exact pong messages', () => {
    expect(parseRealtimeMessage(encode({ type: 'connected', cursor: 0 }))).toMatchObject({
      ok: true,
      message: { kind: 'connected', cursor: 0 },
    });
    expect(parseRealtimeMessage(encode({ type: 'pong' }))).toEqual({
      ok: true,
      message: { kind: 'pong', payload: { type: 'pong' } },
    });
    expect(parseRealtimeMessage(encode({ type: 'pong', timestamp: 1 }))).toEqual({ ok: false, reason: 'invalid' });
  });

  it('requires safe non-negative cursors and positive durable sequences', () => {
    for (const cursor of [-1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
      expect(parseRealtimeMessage(encode({ type: 'connected', cursor }))).toEqual({ ok: false, reason: 'invalid' });
    }
    const durable = { type: 'text_chunk', _event_id: 'event-1', _sequence: 7, _kind: 'fanout' };
    for (const deliveryKind of ['none', 'unknown']) {
      expect(parseRealtimeMessage(encode({ ...durable, _kind: deliveryKind }))).toEqual({
        ok: false,
        reason: 'invalid',
      });
    }
    expect(parseRealtimeMessage(encode(durable))).toMatchObject({
      ok: true,
      message: { kind: 'durable', eventId: 'event-1', sequence: 7, deliveryKind: 'fanout' },
    });
    for (const sequence of [0, -1, 2.5, Number.MAX_SAFE_INTEGER + 1]) {
      expect(parseRealtimeMessage(encode({ ...durable, _sequence: sequence }))).toEqual({
        ok: false,
        reason: 'invalid',
      });
    }
  });

  it('separates allowlisted control and direct messages from durable events', () => {
    expect(parseRealtimeMessage(encode({ type: 'error', code: 'PARSE_ERROR' }))).toMatchObject({
      ok: true,
      message: { kind: 'control', type: 'error' },
    });
    expect(parseRealtimeMessage(encode({ type: 'file_changed', path: '/tmp/a' }))).toMatchObject({
      ok: true,
      message: { kind: 'direct', type: 'file_changed' },
    });
    expect(parseRealtimeMessage(encode({ type: 'message.preview', data: 'live' }))).toEqual({
      ok: false,
      reason: 'unknown',
    });
    expect(parseRealtimeMessage(encode({ type: 'realtime.cursorReset', cursor: 0 }))).toMatchObject({
      ok: true,
      message: { kind: 'control', type: 'realtime.cursorReset' },
    });
    expect(
      parseRealtimeMessage(
        encode({
          type: 'conversation.historyRebased',
          conversation_id: 'frame-a',
          activation_id: 'a'.repeat(64),
          _event_id: 'transcript-history-rebase:' + 'a'.repeat(64),
          _sequence: 8,
          _kind: 'invalidate',
        })
      )
    ).toMatchObject({ ok: true, message: { kind: 'durable', type: 'conversation.historyRebased' } });
    expect(parseRealtimeMessage(encode({ type: 'made_up' }))).toEqual({ ok: false, reason: 'unknown' });
  });

  it('rejects non-text, malformed, partial durable, and oversized input before dispatch', () => {
    expect(parseRealtimeMessage(new Uint8Array())).toEqual({ ok: false, reason: 'non-text' });
    expect(parseRealtimeMessage('{')).toEqual({ ok: false, reason: 'malformed' });
    expect(parseRealtimeMessage(encode({ type: 'text_chunk', _sequence: 1 }))).toEqual({
      ok: false,
      reason: 'invalid',
    });
    expect(parseRealtimeMessage('x'.repeat(REALTIME_MAX_INBOUND_BYTES + 1))).toEqual({
      ok: false,
      reason: 'oversized',
    });
  });

  it('fast-rejects long strings before encoding and enforces the UTF-8 multibyte boundary', () => {
    const encodeSpy = vi.spyOn(TextEncoder.prototype, 'encode');
    expect(parseRealtimeMessage('x'.repeat(REALTIME_MAX_INBOUND_BYTES + 1))).toEqual({
      ok: false,
      reason: 'oversized',
    });
    expect(encodeSpy).not.toHaveBeenCalled();

    const prefix = '{"type":"file_changed","path":"';
    const suffix = '"}';
    const count = Math.floor((REALTIME_MAX_INBOUND_BYTES - prefix.length - suffix.length) / 2);
    expect(parseRealtimeMessage(prefix + 'é'.repeat(count) + suffix).ok).toBe(true);
    expect(parseRealtimeMessage(prefix + 'é'.repeat(count + 1) + suffix)).toEqual({
      ok: false,
      reason: 'oversized',
    });
  });

  it('does not expose mutable protocol allowlists', async () => {
    const module = await import('@/common/adapter/realtimeProtocol');
    expect('REALTIME_CONTROL_TYPES' in module).toBe(false);
    expect('REALTIME_DIRECT_TYPES' in module).toBe(false);
  });
});
