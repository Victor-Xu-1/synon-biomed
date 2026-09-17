export type WebAccountDevice = {
  id: string;
  authMethod: string;
  createdAt: string;
  lastSeenAt: string;
  expiresAt: string;
  remembered: boolean;
  userAgent: string;
  ipAddress: string;
  networkClass: string;
  current: boolean;
  legacy?: boolean;
};

export type WebAccountSecurityEvent = {
  id: string;
  sessionId?: string;
  type: string;
  authMethod?: string;
  createdAt: string;
  success: boolean;
  userAgent?: string;
  networkClass?: string;
};

export type WebAccountSecurity = {
  managed: boolean;
  loginMethods: string[];
  devices: WebAccountDevice[];
  events: WebAccountSecurityEvent[];
};

type WebAccountSecurityPayload = Omit<WebAccountSecurity, 'devices'> & {
  sessions?: WebAccountDevice[];
};

type SecurityMutationResult = {
  revoked: number;
  signedOut: boolean;
};

async function readJSON<T>(response: Response): Promise<T> {
  const payload = (await response.json()) as T & { message?: string };
  if (!response.ok) {
    throw new Error(payload.message || `account_security_http_${response.status}`);
  }
  return payload;
}

export async function loadWebAccountSecurity(signal?: AbortSignal): Promise<WebAccountSecurity> {
  const response = await fetch('/api/account/security', {
    method: 'GET',
    credentials: 'include',
    cache: 'no-store',
    signal,
  });
  const payload = await readJSON<WebAccountSecurityPayload & { success: boolean }>(response);
  return {
    managed: payload.managed !== false,
    loginMethods: Array.isArray(payload.loginMethods) ? payload.loginMethods : [],
    devices: Array.isArray(payload.sessions) ? payload.sessions : [],
    events: Array.isArray(payload.events) ? payload.events : [],
  };
}

async function mutateSecurity(path: string, signal?: AbortSignal): Promise<SecurityMutationResult> {
  const init = withCsrfHeader(path, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json' },
    body: '{}',
    signal,
  });
  const response = await fetch(path, init);
  const payload = await readJSON<{ success: boolean; revoked?: number; signedOut?: boolean }>(response);
  return { revoked: payload.revoked ?? 0, signedOut: payload.signedOut === true };
}

export function revokeWebAccountDevice(deviceID: string, signal?: AbortSignal) {
  return mutateSecurity(`/api/account/security/sessions/${encodeURIComponent(deviceID)}/revoke`, signal);
}

export function revokeOtherWebAccountDevices(signal?: AbortSignal) {
  return mutateSecurity('/api/account/security/sessions/revoke-others', signal);
}

export function revokeAllWebAccountSessions(signal?: AbortSignal) {
  return mutateSecurity('/api/account/security/sessions/revoke-all', signal);
}
import { withCsrfHeader } from '@/renderer/services/csrf';
