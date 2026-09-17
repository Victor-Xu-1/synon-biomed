/**
 * HTTP bridge factory and Electron event adapter for the Synon Biomed backend.
 *
 * Exported helpers produce objects with the same shape as @office-ai/platform bridge,
 * so existing renderer code works without changes.
 */

import type { RealtimeBusinessMessage, RealtimeRuntime } from './realtimeRuntime';
import { redactErrorText } from '../utils/errorRedaction';

// ---------------------------------------------------------------------------
// Base URL
// ---------------------------------------------------------------------------

declare global {
  interface Window {
    __backendPort?: number;
  }
}

/**
 * Resolve the backend port, honoring both renderer and main-process contexts.
 *
 * - Renderer (Electron): the preload bridge writes `window.__backendPort` before
 *   the first HTTP call, so reading from window is authoritative.
 * - Renderer (WebUI browser): no preload, so `window.__backendPort` is missing.
 *   Requests must go to the same origin that served the page; web-host's
 *   static-server reverse-proxies supported backend HTTP paths. Synon Biomed
 *   conversation updates are synchronized through its HTTP polling adapter.
 * - Main process: `window` is undefined. `src/index.ts` writes the port to
 *   `globalThis.__backendPort` immediately after `backendManager.start()`
 *   resolves, so any main-process ipcBridge caller (e.g. the one-shot
 *   assistant migration hook) hits the correct port.
 * - Fallback `13400` only applies when neither is initialized — the request
 *   will still fail cleanly with ECONNREFUSED rather than masking the bug.
 */
function getBackendPort(): number {
  if (typeof window !== 'undefined' && (window as Window).__backendPort) {
    return (window as Window).__backendPort as number;
  }
  const g = globalThis as typeof globalThis & { __backendPort?: number };
  return g.__backendPort ?? 13400;
}

/**
 * WebUI (browser) mode: no Electron preload, so `window.__backendPort` is not
 * injected. Use same-origin URLs; web-host's static-server handles the reverse
 * proxy to the backend.
 */
function isWebUiBrowserMode(): boolean {
  return typeof window !== 'undefined' && typeof document !== 'undefined' && !(window as Window).__backendPort;
}

export function getBaseUrl(): string {
  if (isWebUiBrowserMode()) {
    // Same-origin: calls like fetch(`${baseUrl}/api/foo`) resolve to `/api/foo`
    // on whatever host the page was served from.
    return '';
  }
  return `http://127.0.0.1:${getBackendPort()}`;
}

// ---------------------------------------------------------------------------
// Structured backend error
// ---------------------------------------------------------------------------

const BACKEND_ERROR_CODE_PATTERN = /^[A-Z][A-Z0-9_]{0,63}$/;
const MAX_DIAGNOSTIC_PATH_LENGTH = 256;
const MAX_BACKEND_ERROR_BODY_BYTES = 16 * 1024;
const MAX_BACKEND_ERROR_TEXT_LENGTH = 500;
const MAX_BACKEND_ERROR_COLLECTION_ITEMS = 32;
const MAX_BACKEND_ERROR_DEPTH = 4;
const OVERSIZED_BACKEND_ERROR = 'Backend error response exceeded the safe retention limit';
const UNAVAILABLE_BACKEND_ERROR = 'Backend error response was unavailable';
const SENSITIVE_FIELD_PATTERN = /(?:authorization|cookie|credential|password|passwd|secret|token|api[_-]?key)/i;

function backendErrorCodeForDiagnostic(code: string): string {
  return BACKEND_ERROR_CODE_PATTERN.test(code) ? code : '';
}

function pathForDiagnostic(path: string): string {
  return path.split(/[?#]/, 1)[0].slice(0, MAX_DIAGNOSTIC_PATH_LENGTH);
}

/**
 * Error thrown by `httpRequest` when the backend returns a non-2xx response.
 * Carries the structured error envelope (`success: false, error, code`) so
 * callers can branch on `code` without parsing the stringified message.
 *
 * @example
 *   try { await ipcBridge.conversation.sendMessage.invoke(...); }
 *   catch (e) {
 *     if (isBackendHttpError(e) && e.code === 'CONVERSATION_ARCHIVED') { ... }
 *   }
 */
export class BackendHttpError extends Error {
  readonly status: number;
  /** Machine-readable error code from the backend `ErrorResponse.code`, or `''` when parse failed. */
  readonly code: string;
  /** Redacted, bounded backend message, or the bounded plain-text body when JSON parsing failed. */
  readonly backendMessage: string;
  /** Redacted, depth-bounded metadata from `ErrorResponse.details`, when present. */
  readonly details: unknown;
  /** Redacted, size-bounded parsed body (object on JSON response, string on text/non-JSON). */
  readonly body: unknown;

  constructor(params: { method: string; path: string; status: number; body: unknown }) {
    const { method, path, status } = params;
    const body = sanitizeBackendErrorValue(params.body);
    let code = '';
    let backendMessage = '';
    let details: unknown;
    if (body && typeof body === 'object') {
      const b = body as {
        code?: unknown;
        error?: unknown;
        message?: unknown;
        detail?: unknown;
        details?: unknown;
      };
      if (typeof b.code === 'string') code = backendErrorCodeForDiagnostic(b.code);
      if (typeof b.error === 'string') backendMessage = b.error;
      else if (typeof b.message === 'string') backendMessage = b.message;
      else if (typeof b.detail === 'string') backendMessage = b.detail;
      details = b.details;
    } else if (typeof body === 'string') {
      backendMessage = body;
    }
    const diagnosticCode = backendErrorCodeForDiagnostic(code);
    super(
      `Backend ${method} ${pathForDiagnostic(path)} failed (${status})${diagnosticCode ? ` [${diagnosticCode}]` : ''}`
    );
    this.name = 'BackendHttpError';
    this.status = status;
    this.code = code;
    this.backendMessage = backendMessage;
    this.details = details;
    this.body = body;
  }
}

function sanitizeBackendErrorValue(value: unknown, depth = 0, seen = new WeakSet<object>()): unknown {
  if (typeof value === 'string') return redactErrorText(value).slice(0, MAX_BACKEND_ERROR_TEXT_LENGTH);
  if (value === null || typeof value === 'number' || typeof value === 'boolean') return value;
  if (!value || typeof value !== 'object') return undefined;
  if (depth >= MAX_BACKEND_ERROR_DEPTH || seen.has(value)) return '[TRUNCATED]';
  seen.add(value);
  if (Array.isArray(value)) {
    return value
      .slice(0, MAX_BACKEND_ERROR_COLLECTION_ITEMS)
      .map((item) => sanitizeBackendErrorValue(item, depth + 1, seen));
  }
  const sanitized: Record<string, unknown> = {};
  for (const [key, item] of Object.entries(value).slice(0, MAX_BACKEND_ERROR_COLLECTION_ITEMS)) {
    sanitized[key] = SENSITIVE_FIELD_PATTERN.test(key)
      ? '[REDACTED]'
      : sanitizeBackendErrorValue(item, depth + 1, seen);
  }
  return sanitized;
}

async function readBackendErrorText(response: Response): Promise<string> {
  const declaredLength = Number(response.headers.get('Content-Length'));
  if (Number.isFinite(declaredLength) && declaredLength > MAX_BACKEND_ERROR_BODY_BYTES) {
    await response.body?.cancel().catch((): void => undefined);
    return OVERSIZED_BACKEND_ERROR;
  }
  const reader = response.body?.getReader();
  if (!reader) return '';
  const decoder = new TextDecoder();
  let totalBytes = 0;
  let text = '';
  try {
    while (true) {
      // Stream chunks are ordered and cannot be read safely in parallel.
      // eslint-disable-next-line no-await-in-loop
      const { done, value } = await reader.read();
      if (done) return text + decoder.decode();
      totalBytes += value.byteLength;
      if (totalBytes > MAX_BACKEND_ERROR_BODY_BYTES) {
        // Cancellation must settle before the bounded error escapes.
        // eslint-disable-next-line no-await-in-loop
        await reader.cancel().catch((): void => undefined);
        return OVERSIZED_BACKEND_ERROR;
      }
      text += decoder.decode(value, { stream: true });
    }
  } catch {
    await reader.cancel().catch((): void => undefined);
    return UNAVAILABLE_BACKEND_ERROR;
  } finally {
    reader.releaseLock();
  }
}

export function isBackendHttpError(error: unknown): error is BackendHttpError {
  // Prefer instanceof — fast path in production/bundled contexts.
  if (error instanceof BackendHttpError) return true;
  // Fallback: vite-dev HMR can split the module across chunks, breaking
  // instanceof. Detect by duck-typing on the shape produced by our
  // constructor.
  if (
    error &&
    typeof error === 'object' &&
    'name' in error &&
    (error as { name: unknown }).name === 'BackendHttpError' &&
    'status' in error &&
    typeof (error as { status: unknown }).status === 'number' &&
    'code' in error &&
    typeof (error as { code: unknown }).code === 'string'
  ) {
    return true;
  }
  return false;
}

// ---------------------------------------------------------------------------
// HTTP request helper
// ---------------------------------------------------------------------------

/**
 * Per-request overrides for `httpRequest`.
 *
 * `silentStatuses` lets known-soft failures (e.g. a runtime-scoped lookup
 * returning 404 before the agent has attached) skip the noisy `console.error`
 * and the Sentry breadcrumb that comes with it. The error is still thrown so
 * the caller's existing try/catch keeps working.
 * `silentErrorCodes` provides the same behavior for a specific backend error
 * code, which is useful when one HTTP status contains both transient and
 * user-visible failures.
 */
export type HttpRequestOptions = {
  silentStatuses?: number[];
  silentErrorCodes?: string[];
  keepalive?: boolean;
  timeoutMs?: number;
  /** Cancels the real fetch/body read; callers must not rely on response-time filtering alone. */
  signal?: AbortSignal;
};

export async function httpRequest<T>(
  method: string,
  path: string,
  body?: unknown,
  options?: HttpRequestOptions
): Promise<T> {
  const url = `${getBaseUrl()}${path}`;
  const headers: Record<string, string> = {};

  if (body !== undefined) {
    headers['Content-Type'] = 'application/json';
  }

  const diagnosticPath = pathForDiagnostic(path);
  console.debug(`[httpBridge] ${method} ${diagnosticPath}`, body !== undefined ? '(body omitted)' : '(no body)');

  const timeoutMs = options?.timeoutMs;
  const externalSignal = options?.signal;
  const controller = (timeoutMs && timeoutMs > 0) || externalSignal ? new AbortController() : null;
  let timedOut = false;
  const abortFromCaller = () => controller?.abort(externalSignal?.reason);
  if (controller && externalSignal) {
    if (externalSignal.aborted) abortFromCaller();
    else externalSignal.addEventListener('abort', abortFromCaller, { once: true });
  }
  const timeoutId =
    controller && timeoutMs && timeoutMs > 0
      ? setTimeout(() => {
          timedOut = true;
          controller.abort('http_request_timeout');
        }, timeoutMs)
      : undefined;
  let response: Response;
  try {
    response = await fetch(url, {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      credentials: 'include',
      keepalive: options?.keepalive,
      ...(controller ? { signal: controller.signal } : {}),
    });
  } catch (error) {
    if (timedOut) {
      if (timeoutId !== undefined) clearTimeout(timeoutId);
      externalSignal?.removeEventListener('abort', abortFromCaller);
      throw new Error(`[httpBridge] ${method} ${diagnosticPath} timed out`, { cause: error });
    }
    if (timeoutId !== undefined) clearTimeout(timeoutId);
    externalSignal?.removeEventListener('abort', abortFromCaller);
    throw error;
  }

  try {
    if (!response.ok) {
      const rawText = await readBackendErrorText(response);
      let errorBody: unknown;
      try {
        errorBody = JSON.parse(rawText);
      } catch {
        errorBody = rawText;
      }
      const error = new BackendHttpError({
        method,
        path,
        status: response.status,
        body: errorBody,
      });
      const diagnosticCode = backendErrorCodeForDiagnostic(error.code);
      const diagnostic = `[httpBridge] ${method} ${diagnosticPath} → ${response.status}${
        diagnosticCode ? ` [${diagnosticCode}]` : ''
      }`;
      const silenced =
        options?.silentStatuses?.includes(response.status) || options?.silentErrorCodes?.includes(error.code);
      if (silenced) {
        console.debug(`${diagnostic} (silenced)`);
      } else {
        console.error(diagnostic);
      }
      throw error;
    }

    console.debug(`[httpBridge] ${method} ${diagnosticPath} → ${response.status} OK`);

    const contentType = response.headers.get('Content-Type');
    if (!contentType?.includes('application/json')) {
      return undefined as T;
    }

    const json = await response.json();
    // Backend wraps in { success, data, ... } — unwrap when present
    if (json && typeof json === 'object' && 'data' in json) {
      return json.data as T;
    }
    return json as T;
  } catch (error) {
    if (timedOut) {
      throw new Error(`[httpBridge] ${method} ${diagnosticPath} timed out`, { cause: error });
    }
    throw error;
  } finally {
    if (timeoutId !== undefined) clearTimeout(timeoutId);
    externalSignal?.removeEventListener('abort', abortFromCaller);
  }
}

// ---------------------------------------------------------------------------
// Provider factories (same shape as bridge.buildProvider)
// ---------------------------------------------------------------------------

type ProviderLike<Data, Params> = {
  provider: (handler: (params: Params) => Promise<Data>) => void;
  invoke: Params extends undefined ? () => Promise<Data> : (params: Params) => Promise<Data>;
};

export function withResponseMap<Raw, Mapped, Params>(
  inner: ProviderLike<Raw, Params>,
  map: (data: Raw) => Mapped
): ProviderLike<Mapped, Params> {
  return {
    provider: () => {},
    invoke: (async (params?: Params) => {
      const raw = await (inner.invoke as (p?: Params) => Promise<Raw>)(params);
      return map(raw);
    }) as ProviderLike<Mapped, Params>['invoke'],
  };
}

export function httpGet<Data, Params = undefined>(
  path: string | ((params: Params) => string),
  options?: HttpRequestOptions
): ProviderLike<Data, Params> {
  return {
    provider: () => {},
    invoke: (async (params?: Params) => {
      const resolvedPath = typeof path === 'function' ? path(params!) : path;
      return httpRequest<Data>('GET', resolvedPath, undefined, options);
    }) as ProviderLike<Data, Params>['invoke'],
  };
}

export function httpPost<Data, Params = undefined>(
  path: string | ((params: Params) => string),
  mapBody?: (params: Params) => unknown,
  options?: HttpRequestOptions
): ProviderLike<Data, Params> {
  return {
    provider: () => {},
    invoke: (async (params?: Params) => {
      const resolvedPath = typeof path === 'function' ? path(params!) : path;
      const body = mapBody ? mapBody(params!) : params;
      return httpRequest<Data>('POST', resolvedPath, body, options);
    }) as ProviderLike<Data, Params>['invoke'],
  };
}

export function httpPut<Data, Params = undefined>(
  path: string | ((params: Params) => string),
  mapBody?: (params: Params) => unknown
): ProviderLike<Data, Params> {
  return {
    provider: () => {},
    invoke: (async (params?: Params) => {
      const resolvedPath = typeof path === 'function' ? path(params!) : path;
      const body = mapBody ? mapBody(params!) : params;
      return httpRequest<Data>('PUT', resolvedPath, body);
    }) as ProviderLike<Data, Params>['invoke'],
  };
}

export function httpPatch<Data, Params = undefined>(
  path: string | ((params: Params) => string),
  mapBody?: (params: Params) => unknown
): ProviderLike<Data, Params> {
  return {
    provider: () => {},
    invoke: (async (params?: Params) => {
      const resolvedPath = typeof path === 'function' ? path(params!) : path;
      const body = mapBody ? mapBody(params!) : params;
      return httpRequest<Data>('PATCH', resolvedPath, body);
    }) as ProviderLike<Data, Params>['invoke'],
  };
}

export function httpDelete<Data, Params = undefined>(
  path: string | ((params: Params) => string)
): ProviderLike<Data, Params> {
  return {
    provider: () => {},
    invoke: (async (params?: Params) => {
      const resolvedPath = typeof path === 'function' ? path(params!) : path;
      return httpRequest<Data>('DELETE', resolvedPath);
    }) as ProviderLike<Data, Params>['invoke'],
  };
}

/**
 * Stub provider for features not yet implemented in the backend.
 * Returns a sensible default value and logs a warning.
 */
export function stubProvider<Data, Params = undefined>(name: string, defaultValue: Data): ProviderLike<Data, Params> {
  return {
    provider: () => {},
    invoke: (async (_params?: Params) => {
      console.warn(`[httpBridge] stub: ${name} not yet implemented in backend`);
      return defaultValue;
    }) as ProviderLike<Data, Params>['invoke'],
  };
}

// ---------------------------------------------------------------------------
// Realtime runtime binding
// ---------------------------------------------------------------------------

type RuntimeBinding = { runtime: RealtimeRuntime };
type RuntimeSubscription = {
  active: boolean;
  binding: RuntimeBinding | null;
  unsubscribe: (() => void) | null;
  attach: (binding: RuntimeBinding, subscription: RuntimeSubscription) => () => void;
};
type RealtimeBridgeState = {
  binding: RuntimeBinding | null;
  subscriptions: Set<RuntimeSubscription>;
  cleanupFailureDiagnosed: boolean;
};

// Vite can evaluate this module more than once while applying an HMR update.
// Keep the single composition-root binding and logical subscriptions in a
// realm-wide slot so old and new module instances do not disagree about
// whether the runtime is available.
const REALTIME_BRIDGE_STATE = Symbol.for('synon-biomed.realtime-bridge-state.v1');
const realtimeBridgeGlobal = globalThis as typeof globalThis & {
  [key: symbol]: RealtimeBridgeState | undefined;
};
const realtimeBridgeState =
  realtimeBridgeGlobal[REALTIME_BRIDGE_STATE] ??
  (realtimeBridgeGlobal[REALTIME_BRIDGE_STATE] = {
    binding: null,
    subscriptions: new Set<RuntimeSubscription>(),
    cleanupFailureDiagnosed: false,
  });

const safeUnsubscribe = (unsubscribe: () => void): void => {
  try {
    unsubscribe();
  } catch {
    if (!realtimeBridgeState.cleanupFailureDiagnosed) {
      realtimeBridgeState.cleanupFailureDiagnosed = true;
      console.warn('[realtime]', 'realtime_subscription_cleanup_failed');
    }
  }
};

const detachRuntimeSubscription = (subscription: RuntimeSubscription, binding?: RuntimeBinding): void => {
  if (binding && subscription.binding !== binding) return;
  const unsubscribe = subscription.unsubscribe;
  subscription.binding = null;
  subscription.unsubscribe = null;
  if (unsubscribe) safeUnsubscribe(unsubscribe);
};

const attachRuntimeSubscription = (subscription: RuntimeSubscription, binding: RuntimeBinding): void => {
  if (!subscription.active || subscription.binding) return;
  subscription.binding = binding;
  try {
    subscription.unsubscribe = subscription.attach(binding, subscription);
  } catch (error) {
    subscription.binding = null;
    subscription.unsubscribe = null;
    throw error;
  }
};

const createRuntimeSubscription = (attach: RuntimeSubscription['attach']): (() => void) => {
  const subscription: RuntimeSubscription = {
    active: true,
    binding: null,
    unsubscribe: null,
    attach,
  };
  realtimeBridgeState.subscriptions.add(subscription);
  if (realtimeBridgeState.binding) attachRuntimeSubscription(subscription, realtimeBridgeState.binding);
  return () => {
    if (!subscription.active) return;
    subscription.active = false;
    realtimeBridgeState.subscriptions.delete(subscription);
    detachRuntimeSubscription(subscription);
  };
};

export function bindRealtimeRuntime(runtime: RealtimeRuntime): () => void {
  if (realtimeBridgeState.binding) throw new Error('realtime_runtime_already_bound');
  const binding: RuntimeBinding = { runtime };
  realtimeBridgeState.binding = binding;
  try {
    for (const subscription of realtimeBridgeState.subscriptions) {
      attachRuntimeSubscription(subscription, binding);
    }
  } catch (error) {
    realtimeBridgeState.binding = null;
    for (const subscription of realtimeBridgeState.subscriptions) {
      detachRuntimeSubscription(subscription, binding);
    }
    throw error;
  }
  return () => {
    if (realtimeBridgeState.binding !== binding) return;
    realtimeBridgeState.binding = null;
    for (const subscription of realtimeBridgeState.subscriptions) {
      detachRuntimeSubscription(subscription, binding);
    }
  };
}

// ---------------------------------------------------------------------------
// Emitter factory (same shape as bridge.buildEmitter)
// ---------------------------------------------------------------------------

type EmitterLike<Params> = {
  on: (callback: Params extends undefined ? () => void : (params: Params) => void) => () => void;
};

export function wsEmitter<Params = undefined>(
  kind: RealtimeBusinessMessage['kind'],
  eventType: string
): EmitterLike<Params> {
  return {
    on: (callback: (params: Params) => void) =>
      createRuntimeSubscription((binding, subscription) =>
        binding.runtime.subscribe(kind, eventType, (message) => {
          if (subscription.active && subscription.binding === binding && realtimeBridgeState.binding === binding) {
            callback(message.payload as Params);
          }
        })
      ),
  };
}

export function wsMappedEmitter<Params = undefined>(
  kind: RealtimeBusinessMessage['kind'],
  eventType: string,
  transform: (raw: unknown) => Params
): EmitterLike<Params> {
  const inner = wsEmitter<unknown>(kind, eventType);
  return {
    on: (callback: (params: Params) => void) => inner.on((raw) => callback(transform(raw))),
  };
}

export function realtimeReconnectedEmitter(): EmitterLike<{ timestamp: number }> {
  return {
    on: (callback) =>
      createRuntimeSubscription((binding, subscription) =>
        binding.runtime.subscribeReconnected(() => {
          if (subscription.active && subscription.binding === binding && realtimeBridgeState.binding === binding) {
            callback({ timestamp: Date.now() });
          }
        })
      ),
  };
}

export function unavailableEmitter<Params = undefined>(code: string): EmitterLike<Params> {
  let diagnosed = false;
  return {
    on: () => {
      if (!diagnosed) {
        diagnosed = true;
        console.warn('[realtime]', code);
      }
      return () => {};
    },
  };
}

/**
 * Stub emitter for events not yet implemented in the backend.
 */
export function stubEmitter<Params = undefined>(_name: string): EmitterLike<Params> {
  return {
    on: () => () => {},
  };
}
