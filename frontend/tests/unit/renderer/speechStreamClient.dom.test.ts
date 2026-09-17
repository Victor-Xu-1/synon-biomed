/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { SpeechStreamCallbacks, WebSocketLike } from '@renderer/services/speech/SpeechStreamClient';
import {
  CONNECT_TIMEOUT_MS,
  DONE_TIMEOUT_MS,
  STT_STREAM_CONNECT_FAILED,
  STT_STREAM_INTERRUPTED,
  STT_STREAM_TIMEOUT,
  startSpeechStream,
} from '@renderer/services/speech/SpeechStreamClient';

// ---------------------------------------------------------------------------
// Mock WebSocket
// ---------------------------------------------------------------------------

class MockWebSocket implements WebSocketLike {
  static instances: MockWebSocket[] = [];

  url: string;
  readyState = 0; // CONNECTING
  binaryType: BinaryType = 'blob';
  sentText: string[] = [];
  sentBinary: ArrayBuffer[] = [];
  /** All frames in send order (text strings and binary buffers interleaved). */
  sentAll: Array<string | ArrayBuffer> = [];
  closeCalls: Array<number | undefined> = [];
  private readonly listeners = {
    open: new Set<(event: Event) => void>(),
    message: new Set<(event: MessageEvent) => void>(),
    close: new Set<(event: CloseEvent) => void>(),
    error: new Set<(event: Event) => void>(),
  };

  constructor(url: string) {
    this.url = url;
    MockWebSocket.instances.push(this);
  }

  send(data: string | ArrayBuffer): void {
    if (typeof data === 'string') {
      this.sentText.push(data);
    } else {
      this.sentBinary.push(data);
    }
    this.sentAll.push(data);
  }

  close(code?: number): void {
    this.closeCalls.push(code);
    this.readyState = 3; // CLOSED
  }

  addEventListener(type: 'open', listener: (event: Event) => void): void;
  addEventListener(type: 'message', listener: (event: MessageEvent) => void): void;
  addEventListener(type: 'close', listener: (event: CloseEvent) => void): void;
  addEventListener(type: 'error', listener: (event: Event) => void): void;
  addEventListener(
    type: 'open' | 'message' | 'close' | 'error',
    listener: ((event: Event) => void) | ((event: MessageEvent) => void) | ((event: CloseEvent) => void)
  ): void {
    (this.listeners[type] as Set<typeof listener>).add(listener);
  }

  removeEventListener(type: 'open', listener: (event: Event) => void): void;
  removeEventListener(type: 'message', listener: (event: MessageEvent) => void): void;
  removeEventListener(type: 'close', listener: (event: CloseEvent) => void): void;
  removeEventListener(type: 'error', listener: (event: Event) => void): void;
  removeEventListener(
    type: 'open' | 'message' | 'close' | 'error',
    listener: ((event: Event) => void) | ((event: MessageEvent) => void) | ((event: CloseEvent) => void)
  ): void {
    (this.listeners[type] as Set<typeof listener>).delete(listener);
  }

  /** Simulate the server accepting the connection. */
  open(): void {
    this.readyState = 1; // OPEN
    const event = new Event('open');
    this.listeners.open.forEach((listener) => listener(event));
  }

  /** Simulate a server JSON text frame. */
  message(json: object): void {
    const event = new MessageEvent('message', { data: JSON.stringify(json) });
    this.listeners.message.forEach((listener) => listener(event));
  }

  /** Simulate a raw (possibly malformed) server text frame. */
  raw(data: string): void {
    const event = new MessageEvent('message', { data });
    this.listeners.message.forEach((listener) => listener(event));
  }

  /** Simulate the server closing the connection. */
  closeFromServer(code = 1006): void {
    this.readyState = 3;
    const event = new CloseEvent('close', { code });
    this.listeners.close.forEach((listener) => listener(event));
  }

  /** Simulate a socket-level error event. */
  errorFromServer(): void {
    const event = new Event('error');
    this.listeners.error.forEach((listener) => listener(event));
  }
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

type MockCallbacks = { [K in keyof SpeechStreamCallbacks]: ReturnType<typeof vi.fn> };

const makeCallbacks = (): MockCallbacks => ({
  onReady: vi.fn(),
  onPartial: vi.fn(),
  onFinal: vi.fn(),
  onDone: vi.fn(),
  onError: vi.fn(),
});

const createSocket = (url: string): WebSocketLike => new MockWebSocket(url);

const lastSocket = (): MockWebSocket => MockWebSocket.instances[MockWebSocket.instances.length - 1];

const chunk = (...bytes: number[]): Uint8Array => new Uint8Array(bytes);

const setBackendPort = (port: number | undefined): void => {
  const w = window as Window & { __backendPort?: number };
  if (port === undefined) {
    delete w.__backendPort;
  } else {
    w.__backendPort = port;
  }
};

beforeEach(() => {
  vi.useFakeTimers();
  MockWebSocket.instances = [];
  setBackendPort(undefined);
});

afterEach(() => {
  vi.useRealTimers();
  setBackendPort(undefined);
  vi.restoreAllMocks();
});

// ---------------------------------------------------------------------------
// 1. Start frame
// ---------------------------------------------------------------------------

describe('native Synon Biomed handshake', () => {
  it('does not send a legacy start frame and carries language in the URL', () => {
    const callbacks = makeCallbacks();
    startSpeechStream({ languageHint: 'zh', callbacks, createSocket });
    const sock = lastSocket();
    expect(sock.sentText).toHaveLength(0);
    sock.open();
    expect(sock.sentText).toHaveLength(0);
    expect(sock.url).toContain('/api/voice/stream?language=zh');
  });

  it('omits the language query when absent or empty', () => {
    const callbacks = makeCallbacks();
    startSpeechStream({ callbacks, createSocket });
    const sock1 = lastSocket();
    sock1.open();
    expect(sock1.sentText).toHaveLength(0);
    expect(sock1.url).not.toContain('?language=');

    startSpeechStream({ languageHint: '', callbacks: makeCallbacks(), createSocket });
    const sock2 = lastSocket();
    sock2.open();
    expect(sock2.sentText).toHaveLength(0);
    expect(sock2.url).not.toContain('?language=');
  });
});

// ---------------------------------------------------------------------------
// 2. Chunk buffering
// ---------------------------------------------------------------------------

describe('chunk buffering', () => {
  it('buffers chunks until ready, flushes in order, then sends immediately', () => {
    const callbacks = makeCallbacks();
    const handle = startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();

    handle.sendChunk(chunk(1, 2)); // before open
    sock.open();
    handle.sendChunk(chunk(3, 4)); // after open, before ready
    expect(sock.sentBinary).toHaveLength(0);

    sock.message({ type: 'ready' });
    expect(callbacks.onReady).toHaveBeenCalledTimes(1);
    expect(sock.sentBinary).toHaveLength(2);
    expect(Array.from(new Uint8Array(sock.sentBinary[0]))).toEqual([1, 2]);
    expect(Array.from(new Uint8Array(sock.sentBinary[1]))).toEqual([3, 4]);

    handle.sendChunk(chunk(5, 6)); // after ready: immediate
    expect(sock.sentBinary).toHaveLength(3);
    expect(Array.from(new Uint8Array(sock.sentBinary[2]))).toEqual([5, 6]);
  });

  it('sends only the viewed bytes for a Uint8Array view with an offset', () => {
    const callbacks = makeCallbacks();
    const handle = startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    sock.message({ type: 'ready' });

    const backing = new Uint8Array([9, 9, 10, 11, 12, 13, 9, 9]);
    handle.sendChunk(backing.subarray(2, 6));
    expect(sock.sentBinary).toHaveLength(1);
    expect(sock.sentBinary[0].byteLength).toBe(4);
    expect(Array.from(new Uint8Array(sock.sentBinary[0]))).toEqual([10, 11, 12, 13]);
  });
});

// ---------------------------------------------------------------------------
// 3. Server message dispatch
// ---------------------------------------------------------------------------

describe('server message dispatch', () => {
  it('dispatches ready/partial/final in order', () => {
    const callbacks = makeCallbacks();
    const order: string[] = [];
    callbacks.onReady.mockImplementation(() => order.push('ready'));
    callbacks.onPartial.mockImplementation((text: string) => order.push(`partial:${text}`));
    callbacks.onFinal.mockImplementation((text: string) => order.push(`final:${text}`));

    startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    sock.message({ type: 'ready' });
    sock.message({ type: 'interim', text: 'he' });
    sock.message({ type: 'interim', text: 'hel' });
    sock.message({ type: 'transcript', text: 'hello' });
    sock.message({ type: 'transcript', text: 'world' });

    expect(order).toEqual(['ready', 'partial:he', 'partial:hel', 'final:hello', 'final:world']);
    expect(callbacks.onDone).not.toHaveBeenCalled();
    expect(callbacks.onError).not.toHaveBeenCalled();
  });

  it('ignores unknown/unparseable frames with a warning and keeps working', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const callbacks = makeCallbacks();
    startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    sock.message({ type: 'ready' });
    sock.raw('not json at all');
    sock.message({ type: 'mystery' });
    sock.message({ type: 'interim', text: 'still alive' });

    expect(warn).toHaveBeenCalled();
    expect(callbacks.onPartial).toHaveBeenCalledWith('still alive');
    expect(callbacks.onError).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// 4. Stop → done
// ---------------------------------------------------------------------------

describe('stop and done', () => {
  it('sends stop frame; done fires onDone exactly once and closes the socket', () => {
    const callbacks = makeCallbacks();
    const handle = startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    sock.message({ type: 'ready' });

    handle.stop();
    expect(sock.sentText).toContain('{"type":"stop"}');

    sock.message({ type: 'closed', reason: 'finalized' });
    expect(callbacks.onDone).toHaveBeenCalledTimes(1);
    expect(callbacks.onError).not.toHaveBeenCalled();
    expect(sock.closeCalls.length).toBeGreaterThanOrEqual(1);

    // Terminal: nothing fires afterwards.
    sock.message({ type: 'closed', reason: 'late' });
    sock.closeFromServer();
    vi.runAllTimers();
    expect(callbacks.onDone).toHaveBeenCalledTimes(1);
    expect(callbacks.onError).not.toHaveBeenCalled();
  });

  it('stop before open: stop frame is sent as soon as the socket opens', () => {
    const callbacks = makeCallbacks();
    const handle = startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    handle.stop();
    expect(sock.sentText).toHaveLength(0);
    sock.open();
    expect(sock.sentText).toEqual(['{"type":"stop"}']);
  });

  it('stop before ready: flushes buffered chunks before the stop frame', () => {
    const callbacks = makeCallbacks();
    const handle = startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    handle.sendChunk(chunk(7, 8));
    handle.stop();

    expect(sock.sentAll).toHaveLength(2);
    expect(sock.sentAll[0]).toBeInstanceOf(ArrayBuffer); // flushed chunk
    expect(sock.sentAll[1]).toBe('{"type":"stop"}');
  });
});

// ---------------------------------------------------------------------------
// 5. Server error frame
// ---------------------------------------------------------------------------

describe('server error frame', () => {
  it('fires onError(code, msg) once and ignores everything afterwards', () => {
    const callbacks = makeCallbacks();
    startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    sock.message({ type: 'ready' });
    sock.message({ type: 'error', code: 'upstream_error', message: 'upstream exploded' });

    expect(callbacks.onError).toHaveBeenCalledTimes(1);
    expect(callbacks.onError).toHaveBeenCalledWith('upstream_error', 'upstream exploded');
    expect(sock.closeCalls.length).toBeGreaterThanOrEqual(1);

    sock.message({ type: 'interim', text: 'late' });
    sock.closeFromServer();
    vi.runAllTimers();
    expect(callbacks.onPartial).not.toHaveBeenCalled();
    expect(callbacks.onError).toHaveBeenCalledTimes(1);
    expect(callbacks.onDone).not.toHaveBeenCalled();
  });
});

// ---------------------------------------------------------------------------
// 6/7. Timeouts
// ---------------------------------------------------------------------------

describe('timeouts', () => {
  it('connect timeout fires STT_STREAM_CONNECT_FAILED when the socket never opens', () => {
    const callbacks = makeCallbacks();
    startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();

    vi.advanceTimersByTime(CONNECT_TIMEOUT_MS - 1);
    expect(callbacks.onError).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);

    expect(callbacks.onError).toHaveBeenCalledTimes(1);
    expect(callbacks.onError.mock.calls[0][0]).toBe(STT_STREAM_CONNECT_FAILED);
    expect(sock.closeCalls.length).toBeGreaterThanOrEqual(1);
  });

  it('no connect timeout after the socket opens in time', () => {
    const callbacks = makeCallbacks();
    startSpeechStream({ callbacks, createSocket });
    lastSocket().open();
    vi.advanceTimersByTime(CONNECT_TIMEOUT_MS * 2);
    expect(callbacks.onError).not.toHaveBeenCalled();
  });

  it('done timeout after stop fires STT_STREAM_TIMEOUT', () => {
    const callbacks = makeCallbacks();
    const handle = startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    sock.message({ type: 'ready' });
    handle.stop();

    vi.advanceTimersByTime(DONE_TIMEOUT_MS - 1);
    expect(callbacks.onError).not.toHaveBeenCalled();
    vi.advanceTimersByTime(1);

    expect(callbacks.onError).toHaveBeenCalledTimes(1);
    expect(callbacks.onError.mock.calls[0][0]).toBe(STT_STREAM_TIMEOUT);
    expect(callbacks.onDone).not.toHaveBeenCalled();
    expect(sock.closeCalls.length).toBeGreaterThanOrEqual(1);
  });
});

// ---------------------------------------------------------------------------
// 8. Unexpected close / socket error
// ---------------------------------------------------------------------------

describe('unexpected interruption', () => {
  it('server close before done fires STT_STREAM_INTERRUPTED once', () => {
    const callbacks = makeCallbacks();
    startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    sock.message({ type: 'ready' });
    sock.closeFromServer(1006);

    expect(callbacks.onError).toHaveBeenCalledTimes(1);
    expect(callbacks.onError.mock.calls[0][0]).toBe(STT_STREAM_INTERRUPTED);
  });

  it('socket error event fires STT_STREAM_INTERRUPTED once, even when close follows', () => {
    const callbacks = makeCallbacks();
    startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    sock.errorFromServer();
    sock.closeFromServer(1006);

    expect(callbacks.onError).toHaveBeenCalledTimes(1);
    expect(callbacks.onError.mock.calls[0][0]).toBe(STT_STREAM_INTERRUPTED);
  });
});

// ---------------------------------------------------------------------------
// 9. Abort
// ---------------------------------------------------------------------------

describe('abort', () => {
  it('closes the socket and never invokes any callback', () => {
    const callbacks = makeCallbacks();
    const handle = startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    sock.open();
    handle.abort();

    expect(sock.closeCalls.length).toBeGreaterThanOrEqual(1);

    // Throw everything at it: timers, messages, close, error.
    vi.runAllTimers();
    sock.message({ type: 'ready' });
    sock.message({ type: 'closed', reason: 'done' });
    sock.message({ type: 'error', code: 'STT_X', message: 'x' });
    sock.closeFromServer();
    sock.errorFromServer();

    for (const fn of Object.values(callbacks)) {
      expect(fn).not.toHaveBeenCalled();
    }
  });

  it('abort before open also stays silent', () => {
    const callbacks = makeCallbacks();
    const handle = startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    handle.abort();
    vi.runAllTimers();
    sock.closeFromServer();
    for (const fn of Object.values(callbacks)) {
      expect(fn).not.toHaveBeenCalled();
    }
  });
});

// ---------------------------------------------------------------------------
// 10. URL derivation
// ---------------------------------------------------------------------------

describe('URL derivation', () => {
  it('WebUI browser mode (no __backendPort): same-origin /api/voice/stream', () => {
    const callbacks = makeCallbacks();
    startSpeechStream({ callbacks, createSocket });
    const sock = lastSocket();
    // jsdom serves over http://localhost:3000 — expect ws same-origin.
    expect(sock.url).toBe(`ws://${window.location.host}/api/voice/stream`);
  });

  it('Electron mode (__backendPort injected): ws://127.0.0.1:<port>/api/voice/stream', () => {
    setBackendPort(14512);
    const callbacks = makeCallbacks();
    startSpeechStream({ callbacks, createSocket });
    expect(lastSocket().url).toBe('ws://127.0.0.1:14512/api/voice/stream');
  });
});
