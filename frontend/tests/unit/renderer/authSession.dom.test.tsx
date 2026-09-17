import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthProvider, useAuth } from '@/renderer/hooks/context/AuthContext';
import {
  AUTH_RETURN_PATH_KEY,
  AUTH_SESSION_EXPIRED_KEY,
  consumeAuthResumeDestination,
  installAuthSessionFetchMonitor,
  isSafeAuthReturnPath,
} from '@/renderer/services/authSession';
import { CSRF_COOKIE_NAME, CSRF_HEADER_NAME, clearCsrfCookie } from '@/renderer/services/csrf';
import { getRendererAccountOwnerId, resetRendererAccountScopeForTest } from '@/renderer/services/rendererAccountScope';

const onboardingCompletion = vi.hoisted(() => ({
  prime: vi.fn(() => Promise.resolve()),
}));

vi.mock('@/renderer/services/onboardingCompletionAuthority', async (importOriginal) => ({
  ...(await importOriginal<typeof import('@/renderer/services/onboardingCompletionAuthority')>()),
  primeOnboardingCompletion: onboardingCompletion.prime,
}));

const Probe = () => {
  const { status, user, refresh } = useAuth();
  return (
    <div>
      <output data-testid='auth-status'>{status}</output>
      <output data-testid='auth-user'>{user?.username ?? ''}</output>
      <button type='button' onClick={() => void refresh()}>
        Refresh session
      </button>
    </div>
  );
};

const ImmediateMutationProbe = () => {
  React.useEffect(() => {
    void fetch('/api/conversations/frame-a/runtime/ensure', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ resume: true }),
    });
  }, []);
  return <output data-testid='immediate-mutation'>mounted</output>;
};

function setCsrfCookie(token = 'a'.repeat(43)): void {
  document.cookie = `${CSRF_COOKIE_NAME}=${token}; Path=/; SameSite=Strict`;
}

function csrfResponse(token = 'a'.repeat(43)): Response {
  setCsrfCookie(token);
  return new Response(null, { status: 204 });
}

describe('Synon Biomed auth-session recovery', () => {
  const electronApi = window.electronAPI;

  beforeEach(() => {
    Object.defineProperty(window, 'electronAPI', { configurable: true, value: undefined });
    sessionStorage.clear();
    localStorage.clear();
    clearCsrfCookie();
    onboardingCompletion.prime.mockClear();
    resetRendererAccountScopeForTest();
    window.location.hash = '#/conversation/frame-42';
  });

  afterEach(() => {
    Object.defineProperty(window, 'electronAPI', { configurable: true, value: electronApi });
    vi.useRealTimers();
    vi.unstubAllGlobals();
    window.location.hash = '';
  });

  it.each(['/api/auth/user', '/api/csrf'])(
    'bounds a stalled %s request and allows a clean refresh',
    async (stalledPath) => {
      vi.useFakeTimers();
      let recovered = false;
      const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit): Promise<Response> => {
        const path = new URL(String(input), 'http://localhost').pathname;
        if (!recovered && path === stalledPath) {
          return new Promise<Response>((_resolve, reject) => {
            init?.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), {
              once: true,
            });
          });
        }
        if (path === '/api/auth/user') {
          return Promise.resolve(
            new Response(JSON.stringify({ success: true, user: { id: 'local', username: 'victor' } }), {
              status: 200,
              headers: { 'content-type': 'application/json' },
            })
          );
        }
        if (path === '/api/csrf') return Promise.resolve(csrfResponse());
        return Promise.resolve(new Response(null, { status: 404 }));
      });
      vi.stubGlobal('fetch', fetchMock);

      render(
        <AuthProvider>
          <Probe />
        </AuthProvider>
      );
      expect(screen.getByTestId('auth-status')).toHaveTextContent('checking');

      await act(async () => {
        await vi.advanceTimersByTimeAsync(8_000);
      });
      expect(screen.getByTestId('auth-status')).toHaveTextContent('unavailable');

      recovered = true;
      await act(async () => {
        fireEvent.click(screen.getByRole('button', { name: 'Refresh session' }));
        await Promise.resolve();
        await Promise.resolve();
      });
      expect(screen.getByTestId('auth-status')).toHaveTextContent('authenticated');
      expect(screen.getByTestId('auth-user')).toHaveTextContent('victor');
    }
  );

  it('expires only on a WebHost local-session signal, remembers the route and clears the user', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const path = new URL(String(input), 'http://localhost').pathname;
      if (path === '/api/auth/user') {
        return new Response(JSON.stringify({ success: true, user: { id: 'local', username: 'victor' } }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        });
      }
      if (path === '/api/csrf') {
        return csrfResponse();
      }
      return new Response(JSON.stringify({ success: false }), {
        status: 401,
        headers: { 'content-type': 'application/json', 'x-synon-auth-required': 'session' },
      });
    });
    vi.stubGlobal('fetch', fetchMock);

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>
    );

    await waitFor(() => expect(screen.getByTestId('auth-status')).toHaveTextContent('authenticated'));
    expect(screen.getByTestId('auth-user')).toHaveTextContent('victor');
    expect(getRendererAccountOwnerId()).toBe('local');

    await act(async () => {
      await fetch('/api/projects');
    });

    await waitFor(() => expect(screen.getByTestId('auth-status')).toHaveTextContent('unauthenticated'));
    expect(screen.getByTestId('auth-user')).toHaveTextContent('');
    expect(getRendererAccountOwnerId()).toBe('');
    expect(sessionStorage.getItem(AUTH_RETURN_PATH_KEY)).toBe('/conversation/frame-42');
    expect(sessionStorage.getItem(AUTH_SESSION_EXPIRED_KEY)).toBe('true');
    expect(consumeAuthResumeDestination()).toBe('/guid');
    expect(sessionStorage.getItem(AUTH_RETURN_PATH_KEY)).toBeNull();
    expect(sessionStorage.getItem(AUTH_SESSION_EXPIRED_KEY)).toBeNull();
  });

  it('does not treat a provider 401 or the initial auth probe as an expired local session', async () => {
    const expired = vi.fn();
    const target = {
      fetch: vi
        .fn()
        .mockResolvedValueOnce(new Response(null, { status: 401 }))
        .mockResolvedValueOnce(new Response(null, { status: 401, headers: { 'x-synon-auth-required': 'session' } })),
    };
    const uninstall = installAuthSessionFetchMonitor(expired, target);

    await target.fetch('/api/llm/providers');
    await target.fetch('/api/auth/user');

    expect(expired).not.toHaveBeenCalled();
    uninstall();
  });

  it('reuses one valid CSRF cookie across authenticated bootstrap and explicit refresh', async () => {
    const user = userEvent.setup();
    const paths: string[] = [];
    setCsrfCookie();
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const path = new URL(String(input), 'http://localhost').pathname;
        paths.push(path);
        if (path === '/api/auth/user') {
          return new Response(JSON.stringify({ success: true, user: { id: 'local', username: 'victor' } }), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          });
        }
        if (path === '/api/csrf') return csrfResponse();
        return new Response(null, { status: 404 });
      })
    );

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>
    );
    await waitFor(() => expect(screen.getByTestId('auth-status')).toHaveTextContent('authenticated'));
    await user.click(screen.getByRole('button', { name: 'Refresh session' }));
    await waitFor(() => expect(paths.filter((path) => path === '/api/auth/user')).toHaveLength(2));
    expect(paths.filter((path) => path === '/api/csrf')).toHaveLength(0);
    expect(screen.getByTestId('auth-status')).toHaveTextContent('authenticated');
    expect(screen.getByTestId('auth-user')).toHaveTextContent('victor');
  });

  it('primes owner-scoped onboarding while a missing-CSRF request is still pending', async () => {
    let resolveCsrf!: (response: Response) => void;
    const csrfPending = new Promise<Response>((resolve) => {
      resolveCsrf = resolve;
    });
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL): Promise<Response> => {
        const path = new URL(String(input), 'http://localhost').pathname;
        if (path === '/api/auth/user') {
          return Promise.resolve(
            new Response(JSON.stringify({ success: true, user: { id: 'local', username: 'victor' } }), {
              status: 200,
              headers: { 'content-type': 'application/json' },
            })
          );
        }
        if (path === '/api/csrf') return csrfPending;
        return Promise.resolve(new Response(null, { status: 404 }));
      })
    );

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>
    );

    await waitFor(() => expect(onboardingCompletion.prime).toHaveBeenCalledWith('local'));
    expect(screen.getByTestId('auth-status')).toHaveTextContent('checking');

    await act(async () => resolveCsrf(csrfResponse()));
    await waitFor(() => expect(screen.getByTestId('auth-status')).toHaveTextContent('authenticated'));
  });

  it('preserves the authenticated owner when a background CSRF refresh is temporarily unavailable', async () => {
    const user = userEvent.setup();
    let csrfRequests = 0;
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const path = new URL(String(input), 'http://localhost').pathname;
        if (path === '/api/auth/user') {
          return new Response(JSON.stringify({ success: true, user: { id: 'local', username: 'victor' } }), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          });
        }
        if (path === '/api/csrf') {
          csrfRequests += 1;
          return csrfRequests === 1 ? csrfResponse() : new Response(null, { status: 503 });
        }
        return new Response(null, { status: 404 });
      })
    );

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>
    );
    await waitFor(() => expect(screen.getByTestId('auth-status')).toHaveTextContent('authenticated'));
    clearCsrfCookie();
    await user.click(screen.getByRole('button', { name: 'Refresh session' }));
    await waitFor(() => expect(csrfRequests).toBe(2));
    expect(screen.getByTestId('auth-status')).toHaveTextContent('authenticated');
    expect(screen.getByTestId('auth-user')).toHaveTextContent('victor');
    expect(sessionStorage.getItem(AUTH_RETURN_PATH_KEY)).toBeNull();
    expect(sessionStorage.getItem(AUTH_SESSION_EXPIRED_KEY)).toBeNull();
  });

  it('accepts only internal non-login hash destinations', () => {
    expect(isSafeAuthReturnPath('/projects/project-a')).toBe(true);
    expect(isSafeAuthReturnPath('/login')).toBe(false);
    expect(isSafeAuthReturnPath('//attacker.example')).toBe(false);
    expect(isSafeAuthReturnPath('https://attacker.example')).toBe(false);
  });

  it('adds the double-submit token only to unsafe same-origin requests', async () => {
    document.cookie = `${CSRF_COOKIE_NAME}=${'a'.repeat(43)}; Path=/; SameSite=Strict`;
    const originalFetch = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    const target = { fetch: originalFetch as typeof fetch };
    const uninstall = installAuthSessionFetchMonitor(vi.fn(), target);

    await target.fetch('/api/settings/client', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
    });
    await target.fetch('/api/settings/client', { method: 'GET' });
    await target.fetch('https://example.test/api/settings/client', { method: 'PUT' });

    const mutationInit = originalFetch.mock.calls[0]?.[1] as RequestInit;
    expect(new Headers(mutationInit.headers).get(CSRF_HEADER_NAME)).toBe('a'.repeat(43));
    const getInit = originalFetch.mock.calls[1]?.[1] as RequestInit;
    expect(new Headers(getInit.headers).has(CSRF_HEADER_NAME)).toBe(false);
    const crossOriginInit = originalFetch.mock.calls[2]?.[1] as RequestInit;
    expect(new Headers(crossOriginInit.headers).has(CSRF_HEADER_NAME)).toBe(false);
    uninstall();
  });

  it('installs the authenticated fetch monitor before mounting request-producing children', async () => {
    const token = 'e'.repeat(43);
    setCsrfCookie(token);
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), 'http://localhost').pathname;
      if (path === '/api/auth/user') {
        return new Response(JSON.stringify({ success: true, user: { id: 'local', username: 'victor' } }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        });
      }
      if (path === '/api/conversations/frame-a/runtime/ensure') {
        expect(new Headers(init?.headers).get(CSRF_HEADER_NAME)).toBe(token);
        return new Response(null, { status: 204 });
      }
      return new Response(null, { status: 404 });
    });
    vi.stubGlobal('fetch', fetchMock);

    render(
      <AuthProvider>
        <ImmediateMutationProbe />
      </AuthProvider>
    );

    await screen.findByTestId('immediate-mutation');
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.some(([input]) =>
          new URL(String(input), 'http://localhost').pathname.endsWith('/runtime/ensure')
        )
      ).toBe(true)
    );
  });

  it('rejects an authenticated bootstrap when the CSRF endpoint does not establish a cookie', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        const path = new URL(String(input), 'http://localhost').pathname;
        if (path === '/api/auth/user') {
          return new Response(JSON.stringify({ success: true, user: { id: 'local', username: 'victor' } }), {
            status: 200,
            headers: { 'content-type': 'application/json' },
          });
        }
        return new Response(null, { status: path === '/api/csrf' ? 204 : 404 });
      })
    );

    render(
      <AuthProvider>
        <Probe />
      </AuthProvider>
    );
    await waitFor(() => expect(screen.getByTestId('auth-status')).toHaveTextContent('unavailable'));
    expect(screen.getByTestId('auth-user')).toHaveTextContent('');
  });

  it('refreshes a rotated CSRF token once and replays the rejected mutation exactly once', async () => {
    const oldToken = 'a'.repeat(43);
    const newToken = 'b'.repeat(43);
    setCsrfCookie(oldToken);
    let postRequests = 0;
    let refreshRequests = 0;
    const originalFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), 'http://localhost').pathname;
      if (path === '/api/csrf') {
        refreshRequests += 1;
        return csrfResponse(newToken);
      }
      postRequests += 1;
      const token = new Headers(init?.headers).get(CSRF_HEADER_NAME);
      if (postRequests === 1) {
        expect(token).toBe(oldToken);
        return new Response(null, {
          status: 403,
          headers: { 'x-synon-error-code': 'CSRF_INVALID' },
        });
      }
      expect(token).toBe(newToken);
      return new Response(null, { status: 204 });
    });
    const target = { fetch: originalFetch as typeof fetch };
    const uninstall = installAuthSessionFetchMonitor(vi.fn(), target);

    const response = await target.fetch('/api/conversations/frame-a/runtime/ensure', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({ resume: true }),
    });

    expect(response.status).toBe(204);
    expect(postRequests).toBe(2);
    expect(refreshRequests).toBe(1);
    expect(originalFetch.mock.calls[1]?.[0]).toBe('/api/csrf');
    uninstall();
  });

  it('singleflights concurrent CSRF recovery and never retries unrelated forbidden responses', async () => {
    const oldToken = 'c'.repeat(43);
    const newToken = 'd'.repeat(43);
    setCsrfCookie(oldToken);
    let refreshRequests = 0;
    const originalFetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(String(input), 'http://localhost').pathname;
      if (path === '/api/csrf') {
        refreshRequests += 1;
        await Promise.resolve();
        return csrfResponse(newToken);
      }
      const token = new Headers(init?.headers).get(CSRF_HEADER_NAME);
      if (path === '/api/forbidden') return new Response(null, { status: 403 });
      return token === oldToken
        ? new Response(null, { status: 403, headers: { 'x-synon-error-code': 'CSRF_INVALID' } })
        : new Response(null, { status: 204 });
    });
    const target = { fetch: originalFetch as typeof fetch };
    const uninstall = installAuthSessionFetchMonitor(vi.fn(), target);

    const recovered = await Promise.all(
      Array.from({ length: 8 }, () => target.fetch('/api/runtime/mutation', { method: 'POST', body: '{}' }))
    );
    const callsBeforeForbidden = originalFetch.mock.calls.length;
    const forbidden = await target.fetch('/api/forbidden', { method: 'POST' });

    expect(recovered.every((response) => response.status === 204)).toBe(true);
    expect(refreshRequests).toBe(1);
    expect(forbidden.status).toBe(403);
    expect(originalFetch.mock.calls).toHaveLength(callsBeforeForbidden + 1);
    uninstall();
  });
});
