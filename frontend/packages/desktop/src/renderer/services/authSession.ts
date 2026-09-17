import { CSRF_HEADER_NAME, refreshCsrfToken, withCsrfHeader } from './csrf';

export const AUTH_SESSION_REQUIRED_HEADER = 'x-synon-auth-required';
export const AUTH_RETURN_PATH_KEY = 'synonbiomed.auth.returnPath';
export const AUTH_SESSION_EXPIRED_KEY = 'synonbiomed.auth.sessionExpired';
const CSRF_INVALID_CODE = 'CSRF_INVALID';
const ERROR_CODE_HEADER = 'x-synon-error-code';
const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS']);

type FetchTarget = {
  fetch: typeof fetch;
};

function requestPath(input: RequestInfo | URL): string {
  const rawUrl = input instanceof Request ? input.url : String(input);
  try {
    return new URL(rawUrl, globalThis.location?.href ?? 'http://localhost/').pathname;
  } catch {
    return '';
  }
}

function requestMethod(input: RequestInfo | URL, init?: RequestInit): string {
  return (init?.method ?? (input instanceof Request ? input.method : 'GET')).toUpperCase();
}

function sameOriginRequest(input: RequestInfo | URL): boolean {
  const base = globalThis.location?.href;
  if (!base) return false;
  try {
    const rawUrl = input instanceof Request ? input.url : String(input);
    return new URL(rawUrl, base).origin === globalThis.location.origin;
  } catch {
    return false;
  }
}

function isCsrfBootstrapRequest(input: RequestInfo | URL): boolean {
  const path = requestPath(input);
  return path === '/api/csrf' || path === '/login' || path === '/register' || path.startsWith('/api/auth/');
}

function canReplayBody(input: RequestInfo | URL, init?: RequestInit): boolean {
  if (input instanceof Request) return !input.bodyUsed;
  const body = init?.body;
  return !(typeof ReadableStream !== 'undefined' && body instanceof ReadableStream);
}

function csrfRecoveryRequired(response: Response, input: RequestInfo | URL, init?: RequestInit): boolean {
  return (
    response.status === 403 &&
    response.headers.get(ERROR_CODE_HEADER) === CSRF_INVALID_CODE &&
    !SAFE_METHODS.has(requestMethod(input, init)) &&
    sameOriginRequest(input) &&
    !isCsrfBootstrapRequest(input) &&
    canReplayBody(input, init)
  );
}

function isAuthBootstrapRequest(input: RequestInfo | URL): boolean {
  const path = requestPath(input);
  return path === '/login' || path === '/logout' || path.startsWith('/api/auth/');
}

export function isLocalAuthSessionRequired(response: Response): boolean {
  return response.status === 401 && response.headers.get(AUTH_SESSION_REQUIRED_HEADER) === 'session';
}

export function installAuthSessionFetchMonitor(
  onSessionExpired: () => void,
  target: FetchTarget = globalThis
): () => void {
  const originalFetch = target.fetch;
  const monitoredFetch: typeof fetch = async (input, init) => {
    const firstInput = input instanceof Request ? input.clone() : input;
    const replayInput = input instanceof Request ? input.clone() : input;
    const firstInit = withCsrfHeader(firstInput, init);
    let response = await originalFetch(firstInput, firstInit);
    if (csrfRecoveryRequired(response, firstInput, firstInit)) {
      const rejectedToken = new Headers(firstInit?.headers).get(CSRF_HEADER_NAME);
      const signal = init?.signal ?? (input instanceof Request ? input.signal : undefined);
      const refreshedToken = await refreshCsrfToken(originalFetch, rejectedToken, signal);
      if (refreshedToken) {
        response = await originalFetch(replayInput, withCsrfHeader(replayInput, init));
      }
    }
    if (!isAuthBootstrapRequest(input) && isLocalAuthSessionRequired(response)) {
      onSessionExpired();
    }
    return response;
  };

  target.fetch = monitoredFetch;
  return () => {
    if (target.fetch === monitoredFetch) {
      target.fetch = originalFetch;
    }
  };
}

export function isSafeAuthReturnPath(path: string | null | undefined): path is string {
  return Boolean(path && path.startsWith('/') && !path.startsWith('//') && path !== '/login');
}

export function rememberCurrentAuthRoute(): void {
  if (typeof window === 'undefined') return;
  const path = window.location.hash.startsWith('#') ? window.location.hash.slice(1) : '';
  if (isSafeAuthReturnPath(path)) {
    sessionStorage.setItem(AUTH_RETURN_PATH_KEY, path);
  }
}

export function markAuthSessionExpired(): void {
  if (typeof window === 'undefined') return;
  sessionStorage.setItem(AUTH_SESSION_EXPIRED_KEY, 'true');
}

export function hasExpiredAuthSession(): boolean {
  return typeof window !== 'undefined' && sessionStorage.getItem(AUTH_SESSION_EXPIRED_KEY) === 'true';
}

export function consumeAuthResumeDestination(): string {
  if (typeof window === 'undefined') return '/guid';
  sessionStorage.removeItem(AUTH_RETURN_PATH_KEY);
  sessionStorage.removeItem(AUTH_SESSION_EXPIRED_KEY);
  return '/guid';
}

export function clearAuthResumeState(): void {
  if (typeof window === 'undefined') return;
  sessionStorage.removeItem(AUTH_RETURN_PATH_KEY);
  sessionStorage.removeItem(AUTH_SESSION_EXPIRED_KEY);
}
