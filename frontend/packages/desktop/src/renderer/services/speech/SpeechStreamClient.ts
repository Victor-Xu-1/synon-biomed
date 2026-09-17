/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * Streaming speech-to-text WebSocket client for Synon Biomed's
 * `GET /api/voice/stream` endpoint.
 *
 * Wire protocol:
 * - C→S binary frames: raw little-endian PCM16 chunks
 * - C→S text frame `{"type":"stop"}` when recording ends
 * - S→C text frames: `{"type":"ready"}` → `{"type":"interim"|"transcript","text"}`*
 *   → `{"type":"closed","reason"}`, or `{"type":"error","code","message"}` then close.
 */

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Max time to wait for the WebSocket to open. */
export const CONNECT_TIMEOUT_MS = 5000;
/** Max time to wait for `done`/`error` after the stop frame was sent. */
export const DONE_TIMEOUT_MS = 30000;

// Client-side synthetic error codes (server codes are STT_* from the backend).
export const STT_STREAM_CONNECT_FAILED = 'STT_STREAM_CONNECT_FAILED';
export const STT_STREAM_TIMEOUT = 'STT_STREAM_TIMEOUT';
export const STT_STREAM_INTERRUPTED = 'STT_STREAM_INTERRUPTED';

// WebSocket readyState values (mirrors WebSocket.CONNECTING/OPEN without
// depending on the global constructor, so injected mocks work in tests).
const WS_CONNECTING = 0;
const WS_OPEN = 1;

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type SpeechStreamCallbacks = {
  onReady: () => void;
  onPartial: (text: string) => void;
  onFinal: (text: string) => void;
  onDone: () => void;
  /** Terminal failure. code is an STT_* code (server) or a client-side synthetic code. */
  onError: (code: string, msg: string) => void;
};

export type SpeechStreamHandle = {
  sendChunk: (pcm: Uint8Array) => void;
  /** Send stop frame; onDone/onError follows. */
  stop: () => void;
  /** Tear down immediately without waiting (user cancelled). No callbacks after abort. */
  abort: () => void;
};

type WebSocketEventMap = {
  open: Event;
  message: MessageEvent;
  close: CloseEvent;
  error: Event;
};

/** Minimal structural WebSocket type so tests can inject a mock. */
export type WebSocketLike = {
  readyState: number;
  binaryType: BinaryType;
  send(data: string | ArrayBuffer): void;
  close(code?: number, reason?: string): void;
  addEventListener<K extends keyof WebSocketEventMap>(type: K, listener: (event: WebSocketEventMap[K]) => void): void;
  removeEventListener<K extends keyof WebSocketEventMap>(
    type: K,
    listener: (event: WebSocketEventMap[K]) => void
  ): void;
};

/** Shape of server text frames (fields validated at runtime). */
type ServerFrame = {
  type?: unknown;
  text?: unknown;
  code?: unknown;
  message?: unknown;
  reason?: unknown;
};

// ---------------------------------------------------------------------------
// URL derivation
// ---------------------------------------------------------------------------

/**
 * Mirror of httpBridge's (non-exported) getBackendPort / isWebUiBrowserMode /
 * getWsUrl — see packages/desktop/src/common/adapter/httpBridge.ts:
 * - Electron renderer: the preload bridge injects `window.__backendPort`,
 *   and the backend listens on loopback.
 * - WebUI browser: no preload, so no `__backendPort`; use same-origin URLs —
 *   web-host's static-server proxies/upgrades to the backend and session
 *   cookies ride along automatically.
 */
const getBackendPort = (): number => {
  if (typeof window !== 'undefined') {
    const w = window as Window & { __backendPort?: number };
    if (w.__backendPort) return w.__backendPort;
  }
  const g = globalThis as typeof globalThis & { __backendPort?: number };
  return g.__backendPort ?? 13400;
};

const isWebUiBrowserMode = (): boolean =>
  typeof window !== 'undefined' &&
  typeof document !== 'undefined' &&
  !(window as Window & { __backendPort?: number }).__backendPort;

/** Resolve the streaming endpoint URL for the current runtime mode. */
export const getSpeechStreamUrl = (languageHint?: string): string => {
  const language = languageHint?.trim();
  const query = language ? `?language=${encodeURIComponent(language)}` : '';
  if (isWebUiBrowserMode()) {
    const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    return `${proto}//${window.location.host}/api/voice/stream${query}`;
  }
  return `ws://127.0.0.1:${getBackendPort()}/api/voice/stream${query}`;
};

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

const defaultCreateSocket = (url: string): WebSocketLike => {
  const socket = new WebSocket(url);
  socket.binaryType = 'arraybuffer';
  return socket;
};

/**
 * Open a streaming transcription session.
 *
 * Chunks passed to `sendChunk` before the server `ready` ack are buffered in
 * order and flushed on `ready`. Exactly one terminal event fires per session:
 * `onDone`, `onError`, or none after `abort()`.
 */
export const startSpeechStream = (options: {
  languageHint?: string;
  callbacks: SpeechStreamCallbacks;
  createSocket?: (url: string) => WebSocketLike;
}): SpeechStreamHandle => {
  const { callbacks } = options;
  const url = getSpeechStreamUrl(options.languageHint);

  let socket: WebSocketLike;
  try {
    socket = (options.createSocket ?? defaultCreateSocket)(url);
  } catch (error) {
    // Constructor threw (e.g. malformed URL) — report asynchronously so the
    // caller has its handle before the error callback fires.
    queueMicrotask(() =>
      callbacks.onError(STT_STREAM_CONNECT_FAILED, `WebSocket construction failed: ${String(error)}`)
    );
    return { sendChunk: () => {}, stop: () => {}, abort: () => {} };
  }

  let terminal = false;
  let readyReceived = false;
  let stopSent = false;
  let pendingStop = false;
  let warnedUnknownFrame = false;
  /** Chunks queued before socket open / server ready. */
  const pendingChunks: ArrayBuffer[] = [];

  let connectTimer: ReturnType<typeof setTimeout> | null = null;
  let doneTimer: ReturnType<typeof setTimeout> | null = null;

  const clearTimers = (): void => {
    if (connectTimer !== null) {
      clearTimeout(connectTimer);
      connectTimer = null;
    }
    if (doneTimer !== null) {
      clearTimeout(doneTimer);
      doneTimer = null;
    }
  };

  /** Enter the terminal state: no callback fires after this returns. */
  const enterTerminal = (): void => {
    terminal = true;
    clearTimers();
    socket.removeEventListener('open', handleOpen);
    socket.removeEventListener('message', handleMessage);
    socket.removeEventListener('close', handleClose);
    socket.removeEventListener('error', handleError);
    pendingChunks.length = 0;
  };

  const closeSocket = (): void => {
    if (socket.readyState === WS_CONNECTING || socket.readyState === WS_OPEN) {
      try {
        socket.close();
      } catch {
        // Already closing — nothing to do.
      }
    }
  };

  const failWith = (code: string, msg: string): void => {
    if (terminal) return;
    enterTerminal();
    callbacks.onError(code, msg);
    closeSocket();
  };

  const flushPendingChunks = (): void => {
    while (pendingChunks.length > 0) {
      socket.send(pendingChunks.shift()!);
    }
  };

  /** Flush buffered audio, send the stop frame, and arm the done timeout. */
  const sendStopFrame = (): void => {
    if (stopSent) return;
    stopSent = true;
    flushPendingChunks();
    socket.send(JSON.stringify({ type: 'stop' }));
    doneTimer = setTimeout(
      () => failWith(STT_STREAM_TIMEOUT, `No done/error within ${DONE_TIMEOUT_MS}ms after stop`),
      DONE_TIMEOUT_MS
    );
  };

  const warnUnknownFrame = (raw: string): void => {
    if (warnedUnknownFrame) return;
    warnedUnknownFrame = true;
    console.warn('[SpeechStreamClient] ignoring unrecognized server frame:', raw.slice(0, 200));
  };

  const handleOpen = () => {
    if (terminal) return;
    if (connectTimer !== null) {
      clearTimeout(connectTimer);
      connectTimer = null;
    }
    if (pendingStop) {
      pendingStop = false;
      sendStopFrame();
    }
  };

  const handleMessage = (event: MessageEvent) => {
    if (terminal) return;
    const raw: unknown = event.data;
    if (typeof raw !== 'string') return; // Server only speaks text frames.
    let frame: ServerFrame;
    try {
      frame = JSON.parse(raw) as ServerFrame;
    } catch {
      warnUnknownFrame(raw);
      return;
    }
    switch (frame.type) {
      case 'ready':
        readyReceived = true;
        flushPendingChunks();
        callbacks.onReady();
        break;
      case 'interim':
        callbacks.onPartial(typeof frame.text === 'string' ? frame.text : '');
        break;
      case 'transcript':
        callbacks.onFinal(typeof frame.text === 'string' ? frame.text : '');
        break;
      case 'closed':
        enterTerminal();
        callbacks.onDone();
        closeSocket();
        break;
      case 'error': {
        const code = typeof frame.code === 'string' && frame.code ? frame.code : STT_STREAM_INTERRUPTED;
        const msg = typeof frame.message === 'string' ? frame.message : '';
        enterTerminal();
        callbacks.onError(code, msg);
        closeSocket();
        break;
      }
      default:
        warnUnknownFrame(raw);
    }
  };

  const handleClose = () => {
    failWith(STT_STREAM_INTERRUPTED, 'Connection closed before transcription finished');
  };

  const handleError = () => {
    failWith(STT_STREAM_INTERRUPTED, 'WebSocket error');
  };

  socket.addEventListener('open', handleOpen);
  socket.addEventListener('message', handleMessage);
  socket.addEventListener('close', handleClose);
  socket.addEventListener('error', handleError);

  connectTimer = setTimeout(
    () => failWith(STT_STREAM_CONNECT_FAILED, `WebSocket did not open within ${CONNECT_TIMEOUT_MS}ms`),
    CONNECT_TIMEOUT_MS
  );

  const sendChunk = (pcm: Uint8Array): void => {
    if (terminal || stopSent) return;
    // Copy exactly the viewed range — pcm may be a view into a larger buffer.
    const payload = pcm.buffer.slice(pcm.byteOffset, pcm.byteOffset + pcm.byteLength) as ArrayBuffer;
    if (readyReceived && socket.readyState === WS_OPEN) {
      socket.send(payload);
    } else {
      pendingChunks.push(payload);
    }
  };

  const stop = (): void => {
    if (terminal || stopSent || pendingStop) return;
    if (socket.readyState === WS_OPEN) {
      sendStopFrame();
    } else {
      // Not open yet, so defer until onopen.
      pendingStop = true;
    }
  };

  const abort = (): void => {
    if (terminal) return;
    enterTerminal();
    closeSocket();
  };

  return { sendChunk, stop, abort };
};
