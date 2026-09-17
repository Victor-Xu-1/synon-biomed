export const REALTIME_MAX_INBOUND_BYTES = 512 * 1024;
const REALTIME_CONTROL_TYPES = new Set(['error', 'kernel_terminal_ack', 'realtime.cursorReset']);
const REALTIME_DIRECT_TYPES = new Set(['file_changed', 'file_deleted']);
const REALTIME_DELIVERY_KINDS = new Set(['composite', 'invalidate', 'fanout']);
type JsonRecord = Record<string, unknown>;

export type RealtimeProtocolMessage =
  | { kind: 'connected'; cursor: number; payload: JsonRecord }
  | { kind: 'pong'; payload: { type: 'pong' } }
  | { kind: 'control'; type: string; payload: JsonRecord }
  | { kind: 'direct'; type: string; payload: JsonRecord }
  | {
      kind: 'durable';
      type: string;
      eventId: string;
      sequence: number;
      deliveryKind: string;
      payload: JsonRecord;
    };

export type RealtimeProtocolFailure = 'non-text' | 'oversized' | 'malformed' | 'invalid' | 'unknown';
export type RealtimeProtocolResult =
  | { ok: true; message: RealtimeProtocolMessage }
  | { ok: false; reason: RealtimeProtocolFailure };

const isRecord = (value: unknown): value is JsonRecord =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

const isCursor = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) >= 0;
const isSequence = (value: unknown): value is number => Number.isSafeInteger(value) && (value as number) > 0;
const isText = (value: unknown): value is string => typeof value === 'string' && value.trim().length > 0;

const byteLength = (value: string): number => new TextEncoder().encode(value).byteLength;

export function parseRealtimeMessage(raw: unknown): RealtimeProtocolResult {
  if (typeof raw !== 'string') return { ok: false, reason: 'non-text' };
  if (raw.length > REALTIME_MAX_INBOUND_BYTES) return { ok: false, reason: 'oversized' };
  if (byteLength(raw) > REALTIME_MAX_INBOUND_BYTES) return { ok: false, reason: 'oversized' };

  let payload: unknown;
  try {
    payload = JSON.parse(raw);
  } catch {
    return { ok: false, reason: 'malformed' };
  }
  if (!isRecord(payload) || !isText(payload.type)) return { ok: false, reason: 'invalid' };

  if (payload.type === 'pong') {
    return Object.keys(payload).length === 1
      ? { ok: true, message: { kind: 'pong', payload: { type: 'pong' } } }
      : { ok: false, reason: 'invalid' };
  }
  if (payload.type === 'connected') {
    return isCursor(payload.cursor)
      ? { ok: true, message: { kind: 'connected', cursor: payload.cursor, payload } }
      : { ok: false, reason: 'invalid' };
  }

  const hasDurableField = '_event_id' in payload || '_sequence' in payload || '_kind' in payload;
  if (hasDurableField) {
    if (
      !isText(payload._event_id) ||
      !isSequence(payload._sequence) ||
      !isText(payload._kind) ||
      !REALTIME_DELIVERY_KINDS.has(payload._kind)
    ) {
      return { ok: false, reason: 'invalid' };
    }
    return {
      ok: true,
      message: {
        kind: 'durable',
        type: payload.type,
        eventId: payload._event_id,
        sequence: payload._sequence,
        deliveryKind: payload._kind,
        payload,
      },
    };
  }
  if (REALTIME_CONTROL_TYPES.has(payload.type)) {
    return { ok: true, message: { kind: 'control', type: payload.type, payload } };
  }
  if (REALTIME_DIRECT_TYPES.has(payload.type)) {
    return { ok: true, message: { kind: 'direct', type: payload.type, payload } };
  }
  return { ok: false, reason: 'unknown' };
}
