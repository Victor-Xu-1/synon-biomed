/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 *
 * Unit tests for mockHttpBridge helper (T6 in N3 test checklist).
 * This file verifies the mock helper itself — domain tests (T1-T5) use the helper.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { createMockHttpBridge, resetMockHttpBridge, type MockHttpBridge } from './mockHttpBridge';

describe('mockHttpBridge helper', () => {
  let mock: MockHttpBridge;

  beforeEach(() => {
    mock = createMockHttpBridge();
  });

  it('createMockHttpBridge() returns object with frozen public API', () => {
    expect(mock).toBeDefined();
    expect(typeof mock.onGet).toBe('function');
    expect(typeof mock.onPost).toBe('function');
    expect(typeof mock.onPut).toBe('function');
    expect(typeof mock.onPatch).toBe('function');
    expect(typeof mock.onDelete).toBe('function');
    expect(typeof mock.emit).toBe('function');
    expect(Array.isArray(mock.calls)).toBe(true);
    expect(typeof mock.routeCount).toBe('number');
    expect(typeof mock.wsListenerCount).toBe('number');
    expect(typeof mock.reset).toBe('function');
    expect(typeof mock.asModule).toBe('function');
  });

  it('onGet registers a handler; asModule().httpGet(path).invoke() returns handler result', async () => {
    mock.onGet('/api/foo', () => ({ ok: true }));

    const module = mock.asModule();
    const result = await module.httpGet('/api/foo').invoke();

    expect(result).toEqual({ ok: true });
    expect(mock.calls).toHaveLength(1);
    expect(mock.calls[0]).toMatchObject({
      method: 'GET',
      path: '/api/foo',
      pathPattern: '/api/foo',
      params: {},
      query: {},
      body: undefined,
    });
  });

  it('onPost forwards body and returns handler result', async () => {
    mock.onPost('/api/items', (ctx) => {
      expect(ctx.body).toEqual({ id: 'a' });
      return { created: ctx.body };
    });

    const module = mock.asModule();
    const result = await module.httpPost('/api/items').invoke({ id: 'a' });

    expect(result).toEqual({ created: { id: 'a' } });
    expect(mock.calls).toHaveLength(1);
    expect(mock.calls[0].body).toEqual({ id: 'a' });
  });

  it(':param placeholder populates params map', async () => {
    mock.onGet('/api/synonbiomed/experts/:id', (ctx) => {
      expect(ctx.params.id).toBe('expert-1');
      return { expert: ctx.params.id };
    });

    const module = mock.asModule();
    const result = await module.httpGet('/api/synonbiomed/experts/expert-1').invoke();

    expect(result).toEqual({ expert: 'expert-1' });
    expect(mock.calls[0].params).toEqual({ id: 'expert-1' });
  });

  it('unmatched route throws "unexpected call" by default', async () => {
    mock.onGet('/api/foo', () => ({ ok: true }));

    const module = mock.asModule();
    await expect(module.httpGet('/api/bar').invoke()).rejects.toThrow(/unexpected call/);
  });

  it('unmatched option "warn" returns undefined and logs console.warn', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    mock = createMockHttpBridge({ unmatched: 'warn' });
    mock.onGet('/api/foo', () => ({ ok: true }));

    const module = mock.asModule();
    const result = await module.httpGet('/api/bar').invoke();

    expect(result).toBeUndefined();
    expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining('unmatched route GET /api/bar'));

    warnSpy.mockRestore();
  });

  it('emit() dispatches only to the exact composite realtime key', () => {
    const module = mock.asModule();
    const durable: unknown[] = [];
    const direct: unknown[] = [];

    const unsubscribe = module.wsEmitter('durable', 'test-event').on((payload: unknown) => durable.push(payload));
    module.wsEmitter('direct', 'test-event').on((payload: unknown) => direct.push(payload));

    mock.emit('durable', 'test-event', { data: 'hello' });
    expect(durable).toEqual([{ data: 'hello' }]);
    expect(direct).toEqual([]);

    mock.emit('direct', 'test-event', { data: 'world' });
    expect(direct).toEqual([{ data: 'world' }]);

    expect(mock.wsListenerCount).toBe(2);

    unsubscribe();
    mock.emit('durable', 'test-event', { data: 'ignored' });
    expect(durable).toHaveLength(1);
    expect(mock.wsListenerCount).toBe(1);
  });

  it('isolates a throwing realtime listener and continues the exact composite dispatch', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const module = mock.asModule();
    const received = vi.fn();
    module.wsEmitter('durable', 'test-event').on(() => {
      throw new Error('listener contained a private path');
    });
    module.wsEmitter('durable', 'test-event').on(received);

    expect(() => mock.emit('durable', 'test-event', { value: 7 })).not.toThrow();
    expect(received).toHaveBeenCalledWith({ value: 7 });
    expect(warn).toHaveBeenCalledWith('[mockHttpBridge] realtime listener failed', 'durable:test-event');
    expect(JSON.stringify(warn.mock.calls)).not.toContain('private path');
  });

  it('reset() clears routes, listeners, and calls', async () => {
    mock.onGet('/api/test', () => ({ ok: true }));
    const module = mock.asModule();
    module.wsEmitter('durable', 'event').on(() => {});

    await module.httpGet('/api/test').invoke();
    expect(mock.routeCount).toBe(1);
    expect(mock.wsListenerCount).toBe(1);
    expect(mock.calls).toHaveLength(1);

    mock.reset();
    expect(mock.routeCount).toBe(0);
    expect(mock.wsListenerCount).toBe(0);
    expect(mock.calls).toHaveLength(0);
  });

  it('resetMockHttpBridge() convenience function calls mock.reset()', () => {
    mock.onGet('/api/test', () => ({ ok: true }));
    expect(mock.routeCount).toBe(1);

    resetMockHttpBridge(mock);
    expect(mock.routeCount).toBe(0);
  });

  it('query string is parsed and stripped from path', async () => {
    mock.onGet('/api/search', (ctx) => {
      expect(ctx.query).toEqual({ q: 'test', page: '2' });
      return { results: [] };
    });

    const module = mock.asModule();
    await module.httpGet('/api/search?q=test&page=2').invoke();

    expect(mock.calls[0].query).toEqual({ q: 'test', page: '2' });
    expect(mock.calls[0].path).toBe('/api/search?q=test&page=2');
  });

  it('multiple :params are extracted correctly', async () => {
    mock.onGet('/api/users/:userId/posts/:postId', (ctx) => {
      expect(ctx.params.userId).toBe('u123');
      expect(ctx.params.postId).toBe('p456');
      return { user: ctx.params.userId, post: ctx.params.postId };
    });

    const module = mock.asModule();
    const result = await module.httpGet('/api/users/u123/posts/p456').invoke();

    expect(result).toEqual({ user: 'u123', post: 'p456' });
  });

  it('stubProvider returns default value and logs warning', async () => {
    const warnSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const module = mock.asModule();

    const provider = module.stubProvider('test-stub', 42);
    const result = await provider.invoke();

    expect(result).toBe(42);
    expect(warnSpy).toHaveBeenCalledWith(expect.stringContaining("stubProvider('test-stub')"), 42);

    warnSpy.mockRestore();
  });

  it('withResponseMap wraps invoke and applies mapper', async () => {
    mock.onGet('/api/data', () => ({ raw: 'abc' }));

    const module = mock.asModule();
    const inner = module.httpGet<{ raw: string }>('/api/data');
    const mapped = module.withResponseMap(inner, (data) => data.raw.toUpperCase());

    const result = await mapped.invoke();
    expect(result).toBe('ABC');
  });

  it('wsMappedEmitter applies transform to an explicit composite key', () => {
    const module = mock.asModule();
    const events: number[] = [];

    const emitter = module.wsMappedEmitter<number>(
      'durable',
      'raw-event',
      (raw: unknown) => (raw as { v: number }).v * 2
    );

    emitter.on((value: number) => {
      events.push(value);
    });

    mock.emit('durable', 'raw-event', { v: 3 });
    expect(events).toEqual([6]);

    mock.emit('durable', 'raw-event', { v: 5 });
    expect(events).toEqual([6, 10]);
  });

  it('models unavailable routes and reconnect without a business subscription', () => {
    const module = mock.asModule();
    const reconnected = vi.fn();
    const unavailable = module.unavailableEmitter('realtime_message_stream_quarantined');
    unavailable.on(() => {});
    module.realtimeReconnectedEmitter().on(reconnected);

    expect(mock.wsListenerCount).toBe(1);
    expect('emit' in unavailable).toBe(false);
    mock.emitReconnected();
    expect(reconnected).toHaveBeenCalledOnce();
  });

  it('path as function is resolved with params', async () => {
    mock.onGet('/api/items/xyz', () => ({ item: 'xyz' }));

    const module = mock.asModule();
    const result = await module
      .httpGet<{ item: string }, { id: string }>((params) => `/api/items/${params.id}`)
      .invoke({
        id: 'xyz',
      });

    expect(result).toEqual({ item: 'xyz' });
    expect(mock.calls[0].path).toBe('/api/items/xyz');
  });

  it('httpRequest direct call throws error', () => {
    const module = mock.asModule();
    expect(() => module.httpRequest('GET', '/api/test')).toThrow(/direct httpRequest calls not allowed/);
  });
});
