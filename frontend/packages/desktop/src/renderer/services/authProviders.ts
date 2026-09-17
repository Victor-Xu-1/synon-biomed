export type AuthProviderCapability = {
  id: string;
  displayName: string;
  enabled: boolean;
  startPath: string;
};

export type AuthCapabilities = {
  localPassword: boolean;
  providers: AuthProviderCapability[];
};

const EMPTY_CAPABILITIES: AuthCapabilities = {
  localPassword: true,
  providers: [],
};

export async function loadAuthCapabilities(signal?: AbortSignal): Promise<AuthCapabilities> {
  try {
    const response = await fetch('/api/auth/providers', {
      method: 'GET',
      credentials: 'include',
      cache: 'no-store',
      signal,
    });
    if (!response.ok) return EMPTY_CAPABILITIES;
    const payload = (await response.json()) as Partial<AuthCapabilities> & { success?: boolean };
    if (!payload.success || !Array.isArray(payload.providers)) return EMPTY_CAPABILITIES;
    return {
      localPassword: payload.localPassword !== false,
      providers: payload.providers.filter(isAuthProviderCapability),
    };
  } catch {
    return EMPTY_CAPABILITIES;
  }
}

export function beginExternalLogin(provider: AuthProviderCapability, remember: boolean): void {
  if (typeof window === 'undefined' || !isAuthProviderCapability(provider)) return;
  const returnTo = `${window.location.pathname}${window.location.hash || '#/'}`;
  const query = new URLSearchParams({
    return_to: returnTo,
    remember: remember ? '1' : '0',
  });
  window.location.assign(`${provider.startPath}?${query.toString()}`);
}

function isAuthProviderCapability(value: unknown): value is AuthProviderCapability {
  if (typeof value !== 'object' || value === null) return false;
  const candidate = value as Record<string, unknown>;
  return (
    typeof candidate.id === 'string' &&
    /^[a-z0-9][a-z0-9_-]{0,63}$/u.test(candidate.id) &&
    typeof candidate.displayName === 'string' &&
    candidate.displayName.trim().length > 0 &&
    candidate.displayName.length <= 128 &&
    candidate.enabled === true &&
    isSafeAuthStartPath(candidate.startPath)
  );
}

function isSafeAuthStartPath(value: unknown): value is string {
  if (typeof value !== 'string' || !value.startsWith('/api/auth/') || value.includes('\\')) return false;
  try {
    const parsed = new URL(value, 'http://synon.local');
    return parsed.origin === 'http://synon.local' && parsed.pathname === value && !parsed.search && !parsed.hash;
  } catch {
    return false;
  }
}

export function consumeExternalAuthError(): string | null {
  if (typeof window === 'undefined') return null;
  const url = new URL(window.location.href);
  const code = url.searchParams.get('auth_error');
  if (!code) return null;
  url.searchParams.delete('auth_error');
  window.history.replaceState(null, '', `${url.pathname}${url.search}${url.hash}`);
  return code;
}
