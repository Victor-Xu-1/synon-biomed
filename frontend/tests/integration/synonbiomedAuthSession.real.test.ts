import { describe, expect, it } from 'vitest';
import { createSynonBiomedTestFetch, resolveSynonBiomedTestUsername } from './synonbiomedTestAuth';

const gatewayBaseUrl = process.env.SYNON_BIOMED_GATEWAY_URL ?? 'http://127.0.0.1:8766';

describe('Synon Biomed WebHost auth-session lifecycle', () => {
  it('marks an invalid local session precisely and accepts a fresh login', async () => {
    const authenticatedFetch = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const beforeLogout = await authenticatedFetch(`${gatewayBaseUrl}/api/auth/user`);
    expect(beforeLogout.status).toBe(200);
    await expect(beforeLogout.json()).resolves.toMatchObject({
      success: true,
      user: { username: resolveSynonBiomedTestUsername() },
    });

    const logout = await authenticatedFetch(`${gatewayBaseUrl}/logout`, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: '{}',
    });
    expect(logout.status).toBe(200);

    const expiredRequest = await authenticatedFetch(`${gatewayBaseUrl}/api/projects`);
    expect(expiredRequest.status).toBe(401);
    expect(expiredRequest.headers.get('x-synon-auth-required')).toBe('session');
    expect(expiredRequest.headers.get('cache-control')).toBe('no-store');

    const freshFetch = await createSynonBiomedTestFetch(gatewayBaseUrl);
    const afterLogin = await freshFetch(`${gatewayBaseUrl}/api/auth/user`);
    expect(afterLogin.status).toBe(200);
    await expect(afterLogin.json()).resolves.toMatchObject({
      success: true,
      user: { username: resolveSynonBiomedTestUsername() },
    });
  });
});
