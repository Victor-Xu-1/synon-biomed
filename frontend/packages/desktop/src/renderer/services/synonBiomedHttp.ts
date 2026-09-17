export type SynonBiomedGatewayOptions = {
  baseUrl?: string;
  fetchImpl?: typeof fetch;
  signal?: AbortSignal;
  /** Abort a request that has not produced a response within this bound. */
  timeoutMs?: number;
};

type RequestSignal = {
  signal?: AbortSignal;
  cleanup: () => void;
};

const noOp = () => {};

const createRequestSignal = (signal: AbortSignal | undefined, timeoutMs: number | undefined): RequestSignal => {
  if (timeoutMs === undefined || !Number.isFinite(timeoutMs) || timeoutMs <= 0) {
    return { signal, cleanup: () => {} };
  }

  const controller = new AbortController();
  let timer: ReturnType<typeof setTimeout> | undefined;
  let cleaned = false;
  let abortFromCaller: () => void = noOp;
  const cleanup = () => {
    if (cleaned) return;
    cleaned = true;
    if (timer !== undefined) clearTimeout(timer);
    signal?.removeEventListener('abort', abortFromCaller);
  };
  abortFromCaller = () => {
    if (!controller.signal.aborted) {
      controller.abort(new DOMException('The request was aborted', 'AbortError'));
    }
    cleanup();
  };

  timer = setTimeout(() => {
    if (!controller.signal.aborted) {
      controller.abort(new DOMException('Synon Biomed request timed out', 'TimeoutError'));
    }
    cleanup();
  }, timeoutMs);
  if (signal) {
    signal.addEventListener('abort', abortFromCaller, { once: true });
    if (signal.aborted) abortFromCaller();
  }

  return { signal: controller.signal, cleanup };
};

const ERROR_CODE_PATTERN = /^[A-Z][A-Z0-9_]{0,63}$/;
const MAX_DIAGNOSTIC_PATH_LENGTH = 256;

const safeMethod = (method: string | undefined): string => {
  const normalized = (method ?? 'GET').toUpperCase();
  return /^[A-Z]+$/.test(normalized) ? normalized : 'REQUEST';
};

const safePath = (path: string): string =>
  (path.split(/[?#]/, 1)[0].replace(/[^\x20-\x7e]/g, '') || '/').slice(0, MAX_DIAGNOSTIC_PATH_LENGTH);

const parseResponseBody = (text: string): unknown => {
  if (!text) return '';
  try {
    return JSON.parse(text) as unknown;
  } catch {
    return text;
  }
};

export class SynonBiomedHttpError extends Error {
  readonly status: number;
  readonly code: string;
  readonly backendMessage: string;
  readonly details: unknown;
  readonly body: unknown;

  constructor(params: { method?: string; path: string; status: number; body: unknown }) {
    const { method, path, status, body } = params;
    let code = '';
    let backendMessage = '';
    let details: unknown;
    if (body && typeof body === 'object' && !Array.isArray(body)) {
      const record = body as Record<string, unknown>;
      code = typeof record.code === 'string' ? record.code : '';
      backendMessage =
        typeof record.error === 'string'
          ? record.error
          : typeof record.detail === 'string'
            ? record.detail
            : typeof record.message === 'string'
              ? record.message
              : '';
      details = record.details;
    } else if (typeof body === 'string') {
      backendMessage = body;
    }
    const diagnosticCode = ERROR_CODE_PATTERN.test(code) ? code : '';
    super(
      `Synon Biomed ${safeMethod(method)} ${safePath(path)} failed (${status})${diagnosticCode ? ` [${diagnosticCode}]` : ''}`
    );
    this.name = 'SynonBiomedHttpError';
    this.status = status;
    this.code = code;
    this.backendMessage = backendMessage;
    this.details = details;
    this.body = body;
  }
}

export const isSynonBiomedHttpError = (error: unknown): error is SynonBiomedHttpError =>
  error instanceof SynonBiomedHttpError ||
  Boolean(
    error &&
    typeof error === 'object' &&
    'name' in error &&
    error.name === 'SynonBiomedHttpError' &&
    'status' in error &&
    typeof error.status === 'number' &&
    'code' in error &&
    typeof error.code === 'string'
  );

export async function requestSynonBiomedJson<T>(
  path: string,
  init: RequestInit,
  options: SynonBiomedGatewayOptions
): Promise<T> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const requestSignal = createRequestSignal(init.signal ?? options.signal, options.timeoutMs);
  try {
    const response = await fetchImpl(`${options.baseUrl?.replace(/\/+$/, '') ?? ''}${path}`, {
      ...init,
      credentials: 'include',
      headers: { Accept: 'application/json', ...init.headers },
      signal: requestSignal.signal,
    });
    const text = await response.text();
    if (!response.ok) {
      throw new SynonBiomedHttpError({
        method: init.method,
        path,
        status: response.status,
        body: parseResponseBody(text),
      });
    }
    return (text ? JSON.parse(text) : {}) as T;
  } finally {
    requestSignal.cleanup();
  }
}
