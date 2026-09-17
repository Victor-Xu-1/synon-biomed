import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  loadWebAccountSecurity,
  revokeAllWebAccountSessions,
  revokeOtherWebAccountDevices,
  revokeWebAccountDevice,
} from '@/renderer/services/account/webAccountSecurity';
import { loadAuthCapabilities } from '@/renderer/services/authProviders';
import { CSRF_HEADER_NAME } from '@/renderer/services/csrf';

describe('Web account auth and security services', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('loads only enabled, structurally valid authentication capabilities', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({
          success: true,
          localPassword: true,
          providers: [
            { id: 'google', displayName: 'Google', enabled: true, startPath: '/api/auth/oidc/google/start' },
            { id: 'wechat', displayName: 'WeChat', enabled: true, startPath: '/api/auth/oauth/wechat/start' },
            { id: '', displayName: 'Broken', enabled: true, startPath: '/api/auth/oidc/broken/start' },
            { id: 'disabled', displayName: 'Disabled', enabled: false, startPath: '/api/auth/oidc/disabled/start' },
            { id: 'unsafe', displayName: 'Unsafe', enabled: true, startPath: 'https://attacker.example/start' },
          ],
        }),
        { status: 200, headers: { 'Content-Type': 'application/json' } }
      )
    );
    vi.stubGlobal('fetch', fetchMock);

    await expect(loadAuthCapabilities()).resolves.toEqual({
      localPassword: true,
      providers: [
        { id: 'google', displayName: 'Google', enabled: true, startPath: '/api/auth/oidc/google/start' },
        { id: 'wechat', displayName: 'WeChat', enabled: true, startPath: '/api/auth/oauth/wechat/start' },
      ],
    });
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/auth/providers',
      expect.objectContaining({ method: 'GET', credentials: 'include', cache: 'no-store' })
    );
  });

  it('loads device state and uses bounded same-origin mutation endpoints', async () => {
    document.cookie = `synon_csrf=${'s'.repeat(43)}; Path=/; SameSite=Strict`;
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            success: true,
            managed: true,
            loginMethods: ['google'],
            sessions: [
              { id: 'session-1', current: true },
              { id: 'legacy-session', current: false, legacy: true },
            ],
            events: [{ id: 'event-1', type: 'login_succeeded' }],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } }
        )
      )
      .mockImplementation(() =>
        Promise.resolve(
          new Response(JSON.stringify({ success: true, revoked: 1, signedOut: false }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        )
      );
    vi.stubGlobal('fetch', fetchMock);

    const security = await loadWebAccountSecurity();
    expect(security.managed).toBe(true);
    expect(security.loginMethods).toEqual(['google']);
    expect(security.devices).toHaveLength(2);
    expect(security.devices[0]).toEqual(expect.objectContaining({ id: 'session-1', current: true }));
    expect(security.devices[0].legacy).toBeUndefined();
    expect(security.devices[1]).toEqual(expect.objectContaining({ id: 'legacy-session', legacy: true }));

    await revokeWebAccountDevice('device/unsafe');
    await revokeOtherWebAccountDevices();
    await revokeAllWebAccountSessions();

    expect(fetchMock.mock.calls.slice(1).map(([path]) => path)).toEqual([
      '/api/account/security/sessions/device%2Funsafe/revoke',
      '/api/account/security/sessions/revoke-others',
      '/api/account/security/sessions/revoke-all',
    ]);
    for (const [, init] of fetchMock.mock.calls.slice(1)) {
      expect(init).toEqual(
        expect.objectContaining({
          method: 'POST',
          credentials: 'include',
          body: '{}',
        })
      );
      expect(new Headers(init?.headers).get(CSRF_HEADER_NAME)).toBe('s'.repeat(43));
    }
  });

  it('surfaces server-side security errors instead of silently succeeding', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ success: false, message: 'device not found' }), {
          status: 404,
          headers: { 'Content-Type': 'application/json' },
        })
      )
    );
    await expect(revokeWebAccountDevice('missing')).rejects.toThrow('device not found');
  });
});
