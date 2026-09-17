/**
 * @vitest-environment node
 */

import { describe, expect, it, vi } from 'vitest';
import { createRendererRealtimeRuntime } from '@/common/adapter/realtimeRenderer';
import type { RealtimeSocket } from '@/common/adapter/realtimeClient';

class FakeSocket implements RealtimeSocket {
  readonly readyState = 0;
  readonly listeners = new Map<string, Set<(event: Event | MessageEvent) => void>>();
  readonly close = vi.fn();
  readonly send = vi.fn();

  addEventListener(type: string, listener: (event: Event | MessageEvent) => void): void {
    const listeners = this.listeners.get(type) ?? new Set();
    listeners.add(listener);
    this.listeners.set(type, listeners);
  }

  removeEventListener(type: string, listener: (event: Event | MessageEvent) => void): void {
    this.listeners.get(type)?.delete(listener);
  }
}

const sha256 = 'a'.repeat(64);

function strictSetTimer(this: unknown): ReturnType<typeof setTimeout> {
  if (this !== undefined) throw new Error('timer provider must not be invoked as an object method');
  return 1 as ReturnType<typeof setTimeout>;
}

function eventTarget<T extends object>(value: T): EventTarget & T {
  return Object.assign(new EventTarget(), value);
}

describe('renderer realtime runtime factory', () => {
  it('is lazy and opens the strict same-origin Web endpoint only after identity binding', async () => {
    const urls: string[] = [];
    const runtime = createRendererRealtimeRuntime({
      window: eventTarget({
        location: { protocol: 'https:', host: 'science.example', origin: 'https://science.example' },
      }),
      document: eventTarget({ visibilityState: 'visible' }),
      digestOwner: async () => sha256,
      isOnline: () => true,
      createSocket: (url) => {
        urls.push(url);
        return new FakeSocket();
      },
      setTimer: strictSetTimer as typeof setTimeout,
      clearTimer: vi.fn(),
    });

    expect(urls).toEqual([]);
    await runtime.setIdentity('owner-a');

    expect(urls).toEqual(['wss://science.example/api/events/ws?after_sequence=latest']);
    runtime.dispose();
  });

  it('uses only a validated loopback host port and fails closed for an invalid injected port', async () => {
    const validURLs: string[] = [];
    const valid = createRendererRealtimeRuntime({
      window: eventTarget({
        __backendPort: 8766,
        location: { protocol: 'https:', host: 'wrong.example', origin: 'https://wrong.example' },
      }),
      document: eventTarget({ visibilityState: 'visible' }),
      digestOwner: async () => sha256,
      isOnline: () => true,
      createSocket: (url) => {
        validURLs.push(url);
        return new FakeSocket();
      },
      setTimer: (() => 1) as typeof setTimeout,
      clearTimer: vi.fn(),
    });
    await valid.setIdentity('owner-a');
    expect(validURLs).toEqual(['ws://127.0.0.1:8766/api/events/ws?after_sequence=latest']);
    valid.dispose();

    const invalidURLs: string[] = [];
    const diagnostics: string[] = [];
    const invalid = createRendererRealtimeRuntime({
      window: eventTarget({
        __backendPort: 0,
        location: { protocol: 'https:', host: 'wrong.example', origin: 'https://wrong.example' },
      }),
      document: eventTarget({ visibilityState: 'visible' }),
      digestOwner: async () => sha256,
      isOnline: () => true,
      createSocket: (url) => {
        invalidURLs.push(url);
        return new FakeSocket();
      },
      onDiagnostic: ({ code }) => diagnostics.push(code),
    });
    await invalid.setIdentity('owner-a');

    expect(invalidURLs).toEqual([]);
    expect(invalid.getSnapshot().status).toBe('protocol-error');
    expect(diagnostics).toEqual(['realtime_invalid_endpoint']);
    invalid.dispose();
  });

  it('degrades a throwing storage boundary to memory-only without leaking error text', async () => {
    const diagnostics: string[] = [];
    const urls: string[] = [];
    const runtime = createRendererRealtimeRuntime({
      window: eventTarget({
        location: { protocol: 'http:', host: '127.0.0.1:8080', origin: 'http://127.0.0.1:8080' },
      }),
      document: eventTarget({ visibilityState: 'visible' }),
      getStorage: () => {
        throw new Error('private storage detail');
      },
      digestOwner: async () => sha256,
      isOnline: () => true,
      createSocket: (url) => {
        urls.push(url);
        return new FakeSocket();
      },
      onDiagnostic: ({ code }) => diagnostics.push(code),
      setTimer: (() => 1) as typeof setTimeout,
      clearTimer: vi.fn(),
    });

    await runtime.setIdentity('owner-a');

    expect(urls).toEqual(['ws://127.0.0.1:8080/api/events/ws?after_sequence=latest']);
    expect(JSON.stringify(diagnostics)).not.toContain('private storage detail');
    runtime.dispose();
  });
});
