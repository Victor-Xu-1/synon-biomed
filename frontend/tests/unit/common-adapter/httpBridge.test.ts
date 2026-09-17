/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 *
 * Unit tests for common/adapter/httpBridge.ts (T3 in N3 test checklist).
 * Tests HTTP/WS bridge factories, error handling, and port resolution.
 *
 * @vitest-environment node
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import {
  getBaseUrl,
  httpGet,
  httpPost,
  httpPut,
  httpPatch,
  httpDelete,
  stubProvider,
  withResponseMap,
  BackendHttpError,
  bindRealtimeRuntime,
  isBackendHttpError,
  realtimeReconnectedEmitter,
  unavailableEmitter,
  wsEmitter,
  wsMappedEmitter,
  stubEmitter,
  httpRequest,
} from '@/common/adapter/httpBridge';
import type { RealtimeBusinessMessage, RealtimeRuntime } from '@/common/adapter/realtimeRuntime';
import type { RealtimeConnectionStatus } from '@/common/adapter/realtimeClient';

class FakeRuntime implements RealtimeRuntime {
  readonly businessSubscriptions: Array<{
    kind: RealtimeBusinessMessage['kind'];
    type: string;
    listener: (message: RealtimeBusinessMessage) => void;
    active: boolean;
  }> = [];
  readonly reconnectListeners = new Set<() => void>();
  readonly disposed = vi.fn();

  getSnapshot = () => ({
    identity: 'checking' as const,
    status: 'idle' as RealtimeConnectionStatus,
  });
  subscribeStatus = () => () => {};
  subscribe = (
    kind: RealtimeBusinessMessage['kind'],
    type: string,
    listener: (message: RealtimeBusinessMessage) => void
  ) => {
    const subscription = { kind, type, listener, active: true };
    this.businessSubscriptions.push(subscription);
    return () => {
      subscription.active = false;
    };
  };
  subscribeReconnected = (listener: () => void) => {
    this.reconnectListeners.add(listener);
    return () => this.reconnectListeners.delete(listener);
  };
  setIdentity = async () => {};
  dispose = () => this.disposed();

  business(message: RealtimeBusinessMessage) {
    for (const subscription of this.businessSubscriptions) {
      if (subscription.active && subscription.kind === message.kind && subscription.type === message.type) {
        subscription.listener(message);
      }
    }
  }

  reconnect() {
    for (const listener of this.reconnectListeners) listener();
  }
}

describe('httpBridge', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.unstubAllGlobals();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  describe('getBaseUrl', () => {
    it('returns fallback URL in node environment with no globalThis.__backendPort', () => {
      const result = getBaseUrl();
      expect(result).toBe('http://127.0.0.1:13400');
    });

    it('reads port from globalThis.__backendPort when set', () => {
      (globalThis as { __backendPort?: number }).__backendPort = 23456;

      const result = getBaseUrl();

      expect(result).toBe('http://127.0.0.1:23456');

      delete (globalThis as { __backendPort?: number }).__backendPort;
    });

    it('reads port from window.__backendPort with priority', () => {
      (globalThis as { __backendPort?: number }).__backendPort = 11111;
      vi.stubGlobal('window', { __backendPort: 34567 });

      const result = getBaseUrl();

      expect(result).toBe('http://127.0.0.1:34567');

      delete (globalThis as { __backendPort?: number }).__backendPort;
    });

    it('returns empty string in WebUI mode (window + document, no __backendPort)', () => {
      vi.stubGlobal('window', {});
      vi.stubGlobal('document', {});

      const result = getBaseUrl();

      expect(result).toBe('');
    });
  });

  describe('httpGet', () => {
    it('constructs provider and invoke, provider is no-op', () => {
      const h = httpGet('/api/x');
      expect(h.provider).toBeTypeOf('function');
      expect(h.invoke).toBeTypeOf('function');

      // Provider should be no-op (no error)
      h.provider(() => Promise.resolve({ ok: true }));
    });

    it('invoke triggers fetch with GET, no body, unwraps data envelope', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { x: 1 } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      const result = await httpGet<{ x: number }>('/api/foo').invoke();

      expect(result).toEqual({ x: 1 });
      expect(fetchSpy).toHaveBeenCalledTimes(1);
      expect(fetchSpy.mock.calls[0][0]).toContain('/api/foo');
      expect(fetchSpy.mock.calls[0][1]?.method).toBe('GET');
      expect(fetchSpy.mock.calls[0][1]?.body).toBeUndefined();
    });

    it('aborts a bounded GET instead of leaving the caller loading forever', async () => {
      vi.useFakeTimers();
      const fetchSpy = vi.fn(
        (_url: string, init?: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), {
              once: true,
            });
          })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      const request = httpGet('/api/slow', { timeoutMs: 25 }).invoke();
      const rejection = expect(request).rejects.toThrow('GET /api/slow timed out');
      await vi.advanceTimersByTimeAsync(25);

      await rejection;
      expect((fetchSpy.mock.calls[0]?.[1] as RequestInit | undefined)?.signal?.aborted).toBe(true);
    });

    it('keeps the deadline active while consuming the response body', async () => {
      vi.useFakeTimers();
      const fetchSpy = vi.fn((_url: string, init?: RequestInit) =>
        Promise.resolve(
          new Response(
            new ReadableStream({
              start(controller) {
                init?.signal?.addEventListener(
                  'abort',
                  () => controller.error(new DOMException('Aborted', 'AbortError')),
                  { once: true }
                );
              },
            }),
            { status: 200, headers: { 'content-type': 'application/json' } }
          )
        )
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      const request = httpGet('/api/slow-body', { timeoutMs: 25 }).invoke();
      const rejection = expect(request).rejects.toThrow('GET /api/slow-body timed out');
      await vi.advanceTimersByTimeAsync(25);

      await rejection;
    });
  });

  describe('httpPost', () => {
    it('sends request bodies without writing private content to diagnostics', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { ok: true } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      const debugSpy = vi.spyOn(console, 'debug').mockImplementation(() => {});
      const privateBody = {
        prompt: 'private research subject',
        api_key: 'fixture-private-value',
      };

      await httpPost('/api/private?access_token=query-private').invoke(privateBody);

      expect(fetchSpy.mock.calls[0][1]?.body).toBe(JSON.stringify(privateBody));
      const diagnostics = JSON.stringify(debugSpy.mock.calls);
      expect(diagnostics).not.toContain('private research subject');
      expect(diagnostics).not.toContain('fixture-private-value');
      expect(diagnostics).not.toContain('query-private');
    });

    it('invoke serializes body and sends content-type header', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { created: true } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      const result = await httpPost<{ created: boolean }, { k: string }>('/api/x').invoke({
        k: 'v',
      });

      expect(result).toEqual({ created: true });
      expect(fetchSpy).toHaveBeenCalledTimes(1);
      expect(fetchSpy.mock.calls[0][1]?.method).toBe('POST');
      expect(fetchSpy.mock.calls[0][1]?.body).toBe('{"k":"v"}');
      expect(fetchSpy.mock.calls[0][1]?.headers).toEqual({ 'Content-Type': 'application/json' });
    });

    it('applies mapBody custom mapper', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { ok: true } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      await httpPost('/api/x', (p: string) => ({ wrapped: p })).invoke('raw');

      expect(fetchSpy.mock.calls[0][1]?.body).toBe('{"wrapped":"raw"}');
    });
  });

  describe('path as function', () => {
    it('resolves path with params', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { ok: true } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      await httpGet<{ ok: boolean }, { id: string }>((p) => `/api/${p.id}`).invoke({ id: 'abc' });

      expect(fetchSpy.mock.calls[0][0]).toContain('/api/abc');
    });
  });

  describe('error handling', () => {
    it('retains only redacted backend error details without leaking raw secrets', async () => {
      const sensitiveBody = {
        success: false,
        error: 'Access denied for researcher@example.org with Bearer fixture-private-value',
        code: 'ACCESS_DENIED',
        details: {
          authorization: 'Bearer private-token',
          api_key: 'fixture-private-value',
        },
      };
      vi.stubGlobal(
        'fetch',
        vi.fn().mockResolvedValue(
          new Response(JSON.stringify(sensitiveBody), {
            status: 403,
            headers: { 'Content-Type': 'application/json' },
          })
        )
      );
      const debugSpy = vi.spyOn(console, 'debug').mockImplementation(() => {});
      const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

      const error = await httpGet('/api/private?access_token=query-private')
        .invoke()
        .catch((reason: unknown) => reason);

      expect(error).toBeInstanceOf(BackendHttpError);
      expect((error as BackendHttpError).body).toEqual({
        success: false,
        error: 'Access denied for [email] with Bearer [REDACTED]',
        code: 'ACCESS_DENIED',
        details: { authorization: '[REDACTED]', api_key: '[REDACTED]' },
      });
      expect((error as BackendHttpError).backendMessage).toBe('Access denied for [email] with Bearer [REDACTED]');
      const diagnostics = JSON.stringify([...debugSpy.mock.calls, ...errorSpy.mock.calls]);
      expect(diagnostics).not.toContain('researcher@example.org');
      expect(diagnostics).not.toContain('fixture-private-value');
      expect(diagnostics).not.toContain('private-token');
      expect(diagnostics).not.toContain('query-private');
      expect((error as BackendHttpError).message).not.toContain('researcher@example.org');
      expect((error as BackendHttpError).message).not.toContain('fixture-private-value');
      expect((error as BackendHttpError).message).not.toContain('query-private');
      expect((error as BackendHttpError).message).toContain('ACCESS_DENIED');
    });

    it('silences a known transient backend error code without swallowing the error', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            success: false,
            error: 'conversation history is still being prepared',
            code: 'HISTORY_NOT_READY',
          }),
          {
            status: 503,
            headers: { 'Content-Type': 'application/json' },
          }
        )
      );
      vi.stubGlobal('fetch', fetchSpy);
      const debugSpy = vi.spyOn(console, 'debug').mockImplementation(() => {});
      const errorSpy = vi.spyOn(console, 'error').mockImplementation(() => {});

      const error = await httpGet('/api/history', { silentErrorCodes: ['HISTORY_NOT_READY'] })
        .invoke()
        .catch((reason: unknown) => reason);

      expect(error).toBeInstanceOf(BackendHttpError);
      expect((error as BackendHttpError).code).toBe('HISTORY_NOT_READY');
      expect(errorSpy).not.toHaveBeenCalled();
      expect(debugSpy).toHaveBeenCalledWith(expect.stringContaining('(silenced)'));
    });

    it('cancels an oversized backend error stream before retaining the complete body', async () => {
      let pulls = 0;
      let cancelled = false;
      const stream = new ReadableStream<Uint8Array>({
        pull(controller) {
          pulls += 1;
          controller.enqueue(new TextEncoder().encode('x'.repeat(4096)));
          if (pulls === 100) controller.close();
        },
        cancel() {
          cancelled = true;
        },
      });
      vi.stubGlobal(
        'fetch',
        vi.fn().mockResolvedValue(new Response(stream, { status: 500, headers: { 'Content-Type': 'text/plain' } }))
      );
      vi.spyOn(console, 'debug').mockImplementation(() => {});
      vi.spyOn(console, 'error').mockImplementation(() => {});

      const error = await httpGet('/api/x')
        .invoke()
        .catch((reason: unknown) => reason);

      expect(error).toBeInstanceOf(BackendHttpError);
      expect((error as BackendHttpError).body).toBe('Backend error response exceeded the safe retention limit');
      expect((error as BackendHttpError).backendMessage).toBe(
        'Backend error response exceeded the safe retention limit'
      );
      expect(cancelled).toBe(true);
      expect(pulls).toBeLessThan(10);
    });

    it('non-2xx response throws BackendHttpError with code/status/backendMessage', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            success: false,
            error: 'bad',
            code: 'X_BAD',
            details: { workspace_path: '/tmp/Archive ' },
          }),
          {
            status: 400,
            headers: { 'Content-Type': 'application/json' },
          }
        )
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});
      vi.spyOn(console, 'error').mockImplementation(() => {});

      try {
        await httpGet('/api/x').invoke();
        expect.fail('Should have thrown');
      } catch (e) {
        expect(e).toBeInstanceOf(BackendHttpError);
        const err = e as BackendHttpError;
        expect(err.status).toBe(400);
        expect(err.code).toBe('X_BAD');
        expect(err.backendMessage).toBe('bad');
        expect(err.details).toEqual({ workspace_path: '/tmp/Archive ' });
        expect(err.body).toEqual({
          success: false,
          error: 'bad',
          code: 'X_BAD',
          details: { workspace_path: '/tmp/Archive ' },
        });
      }
    });

    it('non-JSON error response captures raw text without double body consumption (#3249)', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response('Unauthorized', {
          status: 401,
          statusText: 'Unauthorized',
          headers: { 'Content-Type': 'text/plain' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});
      vi.spyOn(console, 'error').mockImplementation(() => {});

      try {
        await httpGet('/api/x').invoke();
        expect.fail('Should have thrown');
      } catch (e) {
        // Before the fix this threw TypeError "body stream already read" instead
        expect(e).toBeInstanceOf(BackendHttpError);
        const err = e as BackendHttpError;
        expect(err.status).toBe(401);
        expect(err.body).toBe('Unauthorized');
      }
    });

    it('empty error body falls back to empty string', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(new Response('', { status: 502 }));
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});
      vi.spyOn(console, 'error').mockImplementation(() => {});

      try {
        await httpGet('/api/x').invoke();
        expect.fail('Should have thrown');
      } catch (e) {
        expect(e).toBeInstanceOf(BackendHttpError);
        const err = e as BackendHttpError;
        expect(err.status).toBe(502);
        expect(err.body).toBe('');
      }
    });
  });

  describe('non-JSON response', () => {
    it('returns undefined when content-type is not JSON', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response('', {
          status: 200,
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      const result = await httpGet('/api/x').invoke();

      expect(result).toBeUndefined();
    });
  });

  describe('stubProvider', () => {
    it('returns default value and logs warning', async () => {
      const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});

      const provider = stubProvider('test', 42);
      const result = await provider.invoke();

      expect(result).toBe(42);
      expect(warnSpy).toHaveBeenCalledWith('[httpBridge] stub: test not yet implemented in backend');
    });
  });

  describe('withResponseMap', () => {
    it('wraps invoke and applies mapper', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { raw: 'abc' } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      const inner = httpGet<{ raw: string }>('/api/data');
      const mapped = withResponseMap(inner, (data) => data.raw.toUpperCase());

      const result = await mapped.invoke();
      expect(result).toBe('ABC');
    });
  });

  describe('BackendHttpError', () => {
    it('instanceof check works', () => {
      const err = new BackendHttpError({ method: 'GET', path: '/api/x', status: 500, body: {} });
      expect(err).toBeInstanceOf(BackendHttpError);
      expect(isBackendHttpError(err)).toBe(true);
    });

    it('duck-typing check works for compatible object', () => {
      const obj = {
        name: 'BackendHttpError',
        status: 500,
        code: 'X',
        backendMessage: 'msg',
      };
      expect(isBackendHttpError(obj)).toBe(true);
    });

    it('duck-typing returns false when status is missing', () => {
      const obj = {
        name: 'BackendHttpError',
        code: 'X',
      };
      expect(isBackendHttpError(obj)).toBe(false);
    });

    it('returns false for non-BackendHttpError', () => {
      expect(isBackendHttpError(new Error('other'))).toBe(false);
      expect(isBackendHttpError(null)).toBe(false);
      expect(isBackendHttpError('string')).toBe(false);
    });
  });

  describe('wsEmitter', () => {
    it('queues a subscription made before the composition root binds a runtime', () => {
      const listener = vi.fn();
      const unsubscribe = wsEmitter('durable', 'runtime.statusChanged').on(listener);
      const runtime = new FakeRuntime();
      const release = bindRealtimeRuntime(runtime);

      runtime.business({
        kind: 'durable',
        type: 'runtime.statusChanged',
        payload: { status: 'running' },
      });

      expect(listener).toHaveBeenCalledWith({ status: 'running' });
      unsubscribe();
      release();
    });

    it('routes by composite kind and type without crossing identical types', () => {
      const runtime = new FakeRuntime();
      const release = bindRealtimeRuntime(runtime);
      const durable = vi.fn();
      const direct = vi.fn();
      const unsubscribeDurable = wsEmitter<{ path: string }>('durable', 'file_changed').on(durable);
      const unsubscribeDirect = wsEmitter<{ path: string }>('direct', 'file_changed').on(direct);

      runtime.business({
        kind: 'durable',
        type: 'file_changed',
        eventId: 'event-1',
        sequence: 1,
        deliveryKind: 'fanout',
        payload: { type: 'file_changed', path: '/durable' },
      });
      runtime.business({
        kind: 'direct',
        type: 'file_changed',
        payload: { type: 'file_changed', path: '/direct' },
      });

      expect(durable).toHaveBeenCalledWith({ type: 'file_changed', path: '/durable' });
      expect(direct).toHaveBeenCalledWith({ type: 'file_changed', path: '/direct' });
      unsubscribeDurable();
      unsubscribeDirect();
      release();
    });

    it('rejects duplicate binding and makes stale release harmless', () => {
      const first = new FakeRuntime();
      const second = new FakeRuntime();
      const releaseFirst = bindRealtimeRuntime(first);
      expect(() => bindRealtimeRuntime(second)).toThrow('realtime_runtime_already_bound');
      releaseFirst();
      const releaseSecond = bindRealtimeRuntime(second);
      releaseFirst();

      const unsubscribe = wsEmitter('durable', 'conversation.listChanged').on(() => {});
      expect(first.businessSubscriptions).toHaveLength(0);
      expect(second.businessSubscriptions).toHaveLength(1);
      unsubscribe();
      releaseSecond();
    });

    it('detaches from a released runtime and reattaches once to its replacement', () => {
      const first = new FakeRuntime();
      const releaseFirst = bindRealtimeRuntime(first);
      const listener = vi.fn();
      const unsubscribe = wsEmitter('durable', 'conversation.listChanged').on(listener);
      releaseFirst();

      const second = new FakeRuntime();
      const releaseSecond = bindRealtimeRuntime(second);
      second.business({
        kind: 'durable',
        type: 'conversation.listChanged',
        payload: { revision: 2 },
      });

      expect(first.businessSubscriptions[0]?.active).toBe(false);
      expect(second.businessSubscriptions).toHaveLength(1);
      expect(listener).toHaveBeenCalledTimes(1);
      expect(listener).toHaveBeenCalledWith({ revision: 2 });
      unsubscribe();
      releaseSecond();
    });

    it('does not attach a queued subscription that was cancelled while unbound', () => {
      const listener = vi.fn();
      const unsubscribe = wsEmitter('durable', 'conversation.listChanged').on(listener);
      unsubscribe();

      const runtime = new FakeRuntime();
      const release = bindRealtimeRuntime(runtime);

      expect(runtime.businessSubscriptions).toHaveLength(0);
      release();
    });

    it('bounds cleanup diagnostics without leaking unsubscribe errors', () => {
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const runtime = new FakeRuntime();
      vi.spyOn(runtime, 'subscribe').mockImplementation(() => () => {
        throw new Error('unsubscribe contained a private path');
      });
      const release = bindRealtimeRuntime(runtime);
      const unsubscribeList = wsEmitter('durable', 'conversation.listChanged').on(() => {});
      const unsubscribeStatus = wsEmitter('durable', 'runtime.statusChanged').on(() => {});

      expect(release).not.toThrow();
      expect(warn).toHaveBeenCalledTimes(1);
      expect(warn).toHaveBeenCalledWith('[realtime]', 'realtime_subscription_cleanup_failed');
      expect(JSON.stringify(warn.mock.calls)).not.toContain('private path');
      unsubscribeList();
      unsubscribeStatus();
    });

    it('maps an explicitly keyed payload and uses only the dedicated reconnect signal', () => {
      const runtime = new FakeRuntime();
      const release = bindRealtimeRuntime(runtime);
      const mapped = vi.fn();
      const reconnected = vi.fn();
      const unsubscribeMapped = wsMappedEmitter<number>(
        'durable',
        'turn.completed',
        (raw) => (raw as { value: number }).value * 2
      ).on(mapped);
      const unsubscribeReconnected = realtimeReconnectedEmitter().on(reconnected);

      runtime.business({
        kind: 'durable',
        type: 'turn.completed',
        eventId: 'event-2',
        sequence: 2,
        deliveryKind: 'fanout',
        payload: { type: 'turn.completed', value: 3 },
      });
      runtime.reconnect();

      expect(mapped).toHaveBeenCalledWith(6);
      expect(reconnected).toHaveBeenCalledWith({ timestamp: expect.any(Number) });
      unsubscribeMapped();
      unsubscribeReconnected();
      release();
    });

    it('keeps unavailable events bounded and outside runtime subscriptions', () => {
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const runtime = new FakeRuntime();
      const release = bindRealtimeRuntime(runtime);
      const emitter = unavailableEmitter('realtime_message_stream_quarantined');

      emitter.on(() => {})();
      emitter.on(() => {})();

      expect(runtime.businessSubscriptions).toHaveLength(0);
      expect(warn).toHaveBeenCalledTimes(1);
      expect(warn).toHaveBeenCalledWith('[realtime]', 'realtime_message_stream_quarantined');
      release();
    });
  });

  describe('stubEmitter', () => {
    it('on returns harmless unsubscribe', () => {
      const e = stubEmitter('x');
      const off = e.on(() => {});
      expect(typeof off).toBe('function');
      off(); // Should not throw
    });
  });

  describe('httpRequest', () => {
    it('performs fetch and unwraps data envelope', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { result: 'ok' } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      const result = await httpRequest<{ result: string }>('GET', '/api/test');

      expect(result).toEqual({ result: 'ok' });
      expect(fetchSpy).toHaveBeenCalledWith(expect.stringContaining('/api/test'), {
        method: 'GET',
        headers: {},
        body: undefined,
        credentials: 'include',
        keepalive: undefined,
      });
    });

    it('sends JSON body for POST with content-type', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: {} }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      await httpRequest('POST', '/api/create', { key: 'value' });

      expect(fetchSpy.mock.calls[0][1]?.body).toBe('{"key":"value"}');
      expect(fetchSpy.mock.calls[0][1]?.headers).toEqual({ 'Content-Type': 'application/json' });
    });

    it('forwards caller cancellation to the real fetch', async () => {
      const fetchSpy = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), {
            once: true,
          });
        });
      });
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});
      const controller = new AbortController();

      const request = httpRequest('GET', '/api/cancel-me', undefined, {
        timeoutMs: 15_000,
        signal: controller.signal,
      });
      controller.abort('route_changed');

      await expect(request).rejects.toMatchObject({ name: 'AbortError' });
      const requestInit = fetchSpy.mock.calls[0]?.[1] as RequestInit | undefined;
      expect(requestInit?.signal?.aborted).toBe(true);
    });
  });

  describe('httpPut', () => {
    it('sends PUT request with body', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { updated: true } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      await httpPut<{ updated: boolean }, { id: string }>('/api/items/:id').invoke({ id: '123' });

      expect(fetchSpy.mock.calls[0][1]?.method).toBe('PUT');
    });
  });

  describe('httpPatch', () => {
    it('sends PATCH request with body', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { patched: true } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      await httpPatch('/api/items').invoke({ id: '123' });

      expect(fetchSpy.mock.calls[0][1]?.method).toBe('PATCH');
    });
  });

  describe('httpDelete', () => {
    it('sends DELETE request with no body', async () => {
      const fetchSpy = vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ data: { deleted: true } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        })
      );
      vi.stubGlobal('fetch', fetchSpy);
      vi.spyOn(console, 'debug').mockImplementation(() => {});

      await httpDelete('/api/items/123').invoke();

      expect(fetchSpy.mock.calls[0][1]?.method).toBe('DELETE');
      expect(fetchSpy.mock.calls[0][1]?.body).toBeUndefined();
    });
  });
});
