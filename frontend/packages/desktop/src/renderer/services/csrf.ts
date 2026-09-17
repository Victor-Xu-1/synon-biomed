export const CSRF_COOKIE_NAME = 'synon_csrf';
export const CSRF_HEADER_NAME = 'X-Synon-CSRF-Token';

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);
const CSRF_REQUEST_TIMEOUT_MS = 8_000;
let csrfRefreshPromise: Promise<string | null> | null = null;

function validCsrfToken(token: string | null): token is string {
  return token !== null && token.length >= 32 && token.length <= 256;
}

export function readCsrfToken(): string | null {
  if (typeof document === 'undefined') return null;
  for (const part of document.cookie.split(';')) {
    const [rawName, ...rawValue] = part.trim().split('=');
    if (rawName === CSRF_COOKIE_NAME) {
      const value = rawValue.join('=');
      return value ? decodeURIComponent(value) : null;
    }
  }
  return null;
}

export function hasValidCsrfToken(): boolean {
  return validCsrfToken(readCsrfToken());
}

export function clearCsrfCookie(): void {
  if (typeof document === 'undefined') return;
  document.cookie = `${CSRF_COOKIE_NAME}=; Path=/; Max-Age=0; SameSite=Strict`;
}

async function fetchCsrfToken(fetchImpl: typeof fetch): Promise<string | null> {
  const controller = new AbortController();
  const timeoutId = setTimeout(() => controller.abort('csrf_request_timeout'), CSRF_REQUEST_TIMEOUT_MS);
  try {
    const response = await fetchImpl('/api/csrf', {
      method: 'GET',
      credentials: 'include',
      cache: 'no-store',
      signal: controller.signal,
    });
    const token = response.ok ? readCsrfToken() : null;
    return validCsrfToken(token) ? token : null;
  } catch {
    return null;
  } finally {
    clearTimeout(timeoutId);
  }
}

function waitForCsrfRefresh(refresh: Promise<string | null>, signal?: AbortSignal): Promise<string | null> {
  if (!signal) return refresh;
  if (signal.aborted) return Promise.reject(new DOMException('Aborted', 'AbortError'));
  return new Promise((resolve, reject) => {
    const abort = () => reject(new DOMException('Aborted', 'AbortError'));
    signal.addEventListener('abort', abort, { once: true });
    refresh.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort));
  });
}

export async function refreshCsrfToken(
  fetchImpl: typeof fetch = fetch,
  rejectedToken?: string | null,
  signal?: AbortSignal
): Promise<string | null> {
  if (!csrfRefreshPromise) {
    const started = fetchCsrfToken(fetchImpl);
    const tracked = started.finally(() => {
      if (csrfRefreshPromise === tracked) csrfRefreshPromise = null;
    });
    csrfRefreshPromise = tracked;
  }
  const token = await waitForCsrfRefresh(csrfRefreshPromise, signal);
  return validCsrfToken(token) && (!rejectedToken || token !== rejectedToken) ? token : null;
}

export async function ensureCsrfToken(fetchImpl: typeof fetch = fetch, signal?: AbortSignal): Promise<boolean> {
  try {
    return (await refreshCsrfToken(fetchImpl, null, signal)) !== null;
  } catch {
    return false;
  }
}

export function withCsrfHeader(input: RequestInfo | URL, init?: RequestInit): RequestInit | undefined {
  const method = (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase();
  if (SAFE_METHODS.has(method)) return init;

  const base = globalThis.location?.href;
  if (!base) return init;
  let target: URL;
  try {
    target = new URL(input instanceof Request ? input.url : String(input), base);
  } catch {
    return init;
  }
  if (target.origin !== globalThis.location.origin) return init;

  const token = readCsrfToken();
  if (!token) return init;
  const sourceHeaders = init?.headers ?? (input instanceof Request ? input.headers : undefined);
  const headers = new Headers(sourceHeaders);
  headers.set(CSRF_HEADER_NAME, token);
  return { ...init, headers };
}

/**
 * Attach the browser-session CSRF token to a same-origin XMLHttpRequest.
 *
 * Most renderer requests use the monitored `fetch` implementation above, but
 * streaming uploads still require XHR for progress events. Keeping the origin
 * check and header policy here gives every multipart XHR the same authority
 * boundary instead of duplicating security logic in individual services.
 */
export function applyCsrfHeaderToSameOriginXHR(xhr: XMLHttpRequest, target: string): void {
  if (typeof window === 'undefined') return;
  try {
    const targetUrl = new URL(target, window.location.href);
    if (targetUrl.origin !== window.location.origin) return;
    const token = readCsrfToken();
    if (token) xhr.setRequestHeader(CSRF_HEADER_NAME, token);
  } catch {
    // XHR reports malformed targets. Never attach a credential to an
    // unparseable or cross-origin URL.
  }
}
