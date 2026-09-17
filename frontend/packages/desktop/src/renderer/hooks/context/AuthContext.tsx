import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import {
  clearAuthResumeState,
  installAuthSessionFetchMonitor,
  markAuthSessionExpired,
  rememberCurrentAuthRoute,
} from '@renderer/services/authSession';
import { registerWebHostAccount, type RegisterAccountParams } from '@renderer/services/webHostAuth';
import { clearCsrfCookie, ensureCsrfToken, hasValidCsrfToken } from '@renderer/services/csrf';
import { primeOnboardingCompletion } from '@renderer/services/onboardingCompletionAuthority';
import { setRendererAccountOwner } from '@renderer/services/rendererAccountScope';

export type AuthStatus = 'checking' | 'authenticated' | 'unauthenticated' | 'unavailable';
export type AuthFailureCode = 'timeout' | 'network' | 'server' | 'invalidResponse' | 'csrf';

export interface AuthUser {
  id: string;
  username: string;
  displayName?: string;
  email?: string;
  provider?: string;
}

interface LoginParams {
  username: string;
  password: string;
  remember?: boolean;
}

type LoginErrorCode =
  | 'invalidCredentials'
  | 'tooManyAttempts'
  | 'serverError'
  | 'networkError'
  | 'csrfError'
  | 'unknown';

interface LoginResult {
  success: boolean;
  code?: LoginErrorCode;
  shouldClearCache?: boolean;
}

export type RegisterResult = Awaited<ReturnType<typeof registerWebHostAccount>>;

interface AuthContextValue {
  ready: boolean;
  user: AuthUser | null;
  status: AuthStatus;
  failure: AuthFailureCode | null;
  login: (params: LoginParams) => Promise<LoginResult>;
  register: (params: RegisterAccountParams) => Promise<RegisterResult>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
  clearAuthCache: () => void;
}

type AuthContextHost = Window & {
  __synonBiomedAuthContextV1?: React.Context<AuthContextValue | undefined>;
};

// Vite can reconnect after the local backend or WSL restarts without replacing
// the document. Keep one context authority for that document so a hot-updated
// consumer cannot observe a new Context object while the mounted provider still
// owns the previous module instance.
const authContextHost = typeof window === 'undefined' ? undefined : (window as AuthContextHost);
export const AuthContext =
  authContextHost?.__synonBiomedAuthContextV1 ?? createContext<AuthContextValue | undefined>(undefined);
if (authContextHost) authContextHost.__synonBiomedAuthContextV1 = AuthContext;

const AUTH_USER_ENDPOINT = '/api/auth/user';
const AUTH_REFRESH_TIMEOUT_MS = 8_000;

type CurrentUserProbe =
  | { kind: 'authenticated'; user: AuthUser }
  | { kind: 'anonymous' }
  | { kind: 'unavailable'; code: AuthFailureCode };

// Clear expired auth cache including cookies and localStorage
// 清除过期的认证缓存，包括 Cookie 和 localStorage
function clearAuthCache(): void {
  if (typeof window === 'undefined') return;

  try {
    // Clear CSRF cookie
    clearCsrfCookie();

    // Clear localStorage auth-related items
    const keysToRemove: string[] = [];
    for (let i = 0; i < localStorage.length; i++) {
      const key = localStorage.key(i);
      if (key && (key.includes('auth') || key.includes('csrf') || key.includes('token'))) {
        keysToRemove.push(key);
      }
    }
    keysToRemove.forEach((key) => localStorage.removeItem(key));
  } catch (error) {
    console.error('Failed to clear auth cache:', error);
  }
}

async function fetchCurrentUser(signal?: AbortSignal): Promise<CurrentUserProbe> {
  try {
    const response = await fetch(AUTH_USER_ENDPOINT, {
      method: 'GET',
      credentials: 'include',
      signal,
    });

    if (response.status === 401) return { kind: 'anonymous' };
    if (!response.ok) return { kind: 'unavailable', code: 'server' };

    const data = (await response.json()) as {
      success: boolean;
      user?: AuthUser;
    };
    if (data.success && data.user?.id?.trim()) return { kind: 'authenticated', user: data.user };
    return { kind: 'unavailable', code: 'invalidResponse' };
  } catch {
    if (signal?.aborted) return { kind: 'unavailable', code: 'timeout' };
    console.error('[auth] current-user probe unavailable');
    return { kind: 'unavailable', code: 'network' };
  }
}

export const AuthProvider: React.FC<React.PropsWithChildren> = ({ children }) => {
  const [user, setUser] = useState<AuthUser | null>(null);
  const [status, setStatus] = useState<AuthStatus>('checking');
  const [failure, setFailure] = useState<AuthFailureCode | null>(null);
  const [ready, setReady] = useState(false);
  const abortRef = useRef<AbortController | null>(null);
  const generationRef = useRef(0);
  const statusRef = useRef<AuthStatus>('checking');

  const updateStatus = useCallback((nextStatus: AuthStatus) => {
    statusRef.current = nextStatus;
    setStatus(nextStatus);
  }, []);

  const beginAuthOperation = useCallback(() => {
    abortRef.current?.abort('auth_operation_superseded');
    const controller = new AbortController();
    const generation = generationRef.current + 1;
    generationRef.current = generation;
    abortRef.current = controller;
    return { controller, generation };
  }, []);

  const isCurrentAuthOperation = useCallback(
    (controller: AbortController, generation: number) =>
      abortRef.current === controller && generationRef.current === generation,
    []
  );

  const expireSession = useCallback(() => {
    if (statusRef.current !== 'authenticated') return;
    generationRef.current += 1;
    abortRef.current?.abort('auth_session_expired');
    rememberCurrentAuthRoute();
    markAuthSessionExpired();
    clearAuthCache();
    setRendererAccountOwner(null);
    setUser(null);
    setFailure(null);
    updateStatus('unauthenticated');
    setReady(true);
  }, [updateStatus]);

  useEffect(() => installAuthSessionFetchMonitor(expireSession), [expireSession]);

  const refresh = useCallback(async () => {
    const wasAuthenticated = statusRef.current === 'authenticated';
    const { controller, generation } = beginAuthOperation();
    if (!wasAuthenticated) updateStatus('checking');
    setFailure(null);
    const timeoutId = setTimeout(() => controller.abort('auth_refresh_timeout'), AUTH_REFRESH_TIMEOUT_MS);

    try {
      const probe = await fetchCurrentUser(controller.signal);
      if (!isCurrentAuthOperation(controller, generation)) return;
      if (probe.kind === 'authenticated') {
        void primeOnboardingCompletion(probe.user.id);
        const csrfReady = hasValidCsrfToken() || (await ensureCsrfToken(fetch, controller.signal));
        if (!isCurrentAuthOperation(controller, generation)) return;
        if (!csrfReady) {
          setFailure('csrf');
          if (!wasAuthenticated) updateStatus('unavailable');
          return;
        }
        setRendererAccountOwner(probe.user.id);
        setUser(probe.user);
        setFailure(null);
        updateStatus('authenticated');
      } else if (probe.kind === 'anonymous') {
        rememberCurrentAuthRoute();
        if (wasAuthenticated) markAuthSessionExpired();
        setRendererAccountOwner(null);
        setUser(null);
        setFailure(null);
        updateStatus('unauthenticated');
      } else {
        setFailure(probe.code);
        if (!wasAuthenticated) updateStatus('unavailable');
      }
    } finally {
      clearTimeout(timeoutId);
      if (isCurrentAuthOperation(controller, generation)) setReady(true);
    }
  }, [beginAuthOperation, isCurrentAuthOperation, updateStatus]);

  useEffect(() => {
    void refresh();
    return () => {
      generationRef.current += 1;
      abortRef.current?.abort('auth_provider_unmounted');
    };
  }, [refresh]);

  const login = useCallback(
    async ({ username, password, remember }: LoginParams): Promise<LoginResult> => {
      const { controller, generation } = beginAuthOperation();
      setFailure(null);
      try {
        const csrfTokenValid = hasValidCsrfToken();
        if (!csrfTokenValid) {
          clearAuthCache();
        }

        // Login is origin-checked and rate-limited. A successful response
        // establishes both the session and double-submit CSRF cookies.
        const response = await fetch('/login', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
          credentials: 'include',
          body: JSON.stringify({ username, password, remember }),
          signal: controller.signal,
        });

        if (!isCurrentAuthOperation(controller, generation)) return { success: false, code: 'networkError' };

        const data = (await response.json()) as {
          success: boolean;
          user?: AuthUser;
        };

        if (!response.ok || !data.success || !data.user) {
          let code: LoginErrorCode = 'unknown';
          let shouldClearCache = false;

          if (response.status === 401) {
            code = 'invalidCredentials';
          } else if (response.status === 403) {
            // CSRF validation failed - clear cache
            code = 'csrfError';
            shouldClearCache = true;
          } else if (response.status === 429) {
            code = 'tooManyAttempts';
          } else if (response.status >= 500) {
            code = 'serverError';
          }

          // Clear cache on CSRF-related errors
          if (shouldClearCache) {
            clearAuthCache();
          }

          return {
            success: false,
            code,
            shouldClearCache,
          };
        }

        if (!hasValidCsrfToken() && !(await ensureCsrfToken(fetch, controller.signal))) {
          return {
            success: false,
            code: 'csrfError',
          };
        }

        if (!isCurrentAuthOperation(controller, generation)) return { success: false, code: 'networkError' };

        setRendererAccountOwner(data.user.id);
        setUser(data.user);
        setFailure(null);
        updateStatus('authenticated');
        setReady(true);

        // Re-enable WebSocket reconnection after successful login (WebUI mode only)
        if (typeof window !== 'undefined') {
          const webWindow = window as Window & { __websocketReconnect?: () => void };
          webWindow.__websocketReconnect?.();
        }

        return { success: true };
      } catch (error) {
        if (controller.signal.aborted || !isCurrentAuthOperation(controller, generation)) {
          return { success: false, code: 'networkError' };
        }
        console.error('[auth] login request unavailable');

        // Check if error is related to CSRF token parsing
        const errorMessage = (error as Error).message;
        if (errorMessage?.includes('parse') || errorMessage?.includes('csrf') || errorMessage?.includes('cookie')) {
          // CSRF or cookie parsing error - clear cache
          clearAuthCache();
          return {
            success: false,
            code: 'csrfError',
            shouldClearCache: true,
          };
        }

        return {
          success: false,
          code: 'networkError',
        };
      }
    },
    [beginAuthOperation, isCurrentAuthOperation, updateStatus]
  );

  const register = useCallback(
    async (params: RegisterAccountParams): Promise<RegisterResult> => {
      const { controller, generation } = beginAuthOperation();
      setFailure(null);
      const result = await registerWebHostAccount(params, controller.signal);
      if (!isCurrentAuthOperation(controller, generation)) return { success: false, code: 'NETWORK_ERROR' };
      if (result.success) {
        setRendererAccountOwner(result.user.id);
        setUser(result.user);
        setFailure(null);
        updateStatus('authenticated');
        setReady(true);
      }
      return result;
    },
    [beginAuthOperation, isCurrentAuthOperation, updateStatus]
  );

  const logout = useCallback(async () => {
    const { controller, generation } = beginAuthOperation();
    try {
      await fetch('/logout', {
        method: 'POST',
        // Logout also needs CSRF token / 登出同样需要 CSRF Token
        headers: {
          'Content-Type': 'application/json',
        },
        credentials: 'include',
        body: JSON.stringify({}),
        signal: controller.signal,
      });
    } catch {
      if (!controller.signal.aborted) console.error('[auth] logout request unavailable');
    } finally {
      if (isCurrentAuthOperation(controller, generation)) {
        setRendererAccountOwner(null);
        setUser(null);
        setFailure(null);
        updateStatus('unauthenticated');
        clearAuthResumeState();
        // Clear cache on logout for security
        clearAuthCache();
      }
    }
  }, [beginAuthOperation, isCurrentAuthOperation, updateStatus]);

  const value = useMemo<AuthContextValue>(
    () => ({
      ready,
      user,
      status,
      failure,
      login,
      register,
      logout,
      refresh,
      clearAuthCache,
    }),
    [failure, login, logout, ready, refresh, register, status, user]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};

export function useAuth(): AuthContextValue {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
}
