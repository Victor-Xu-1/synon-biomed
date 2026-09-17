import { describe, expect, it, vi } from 'vitest';

vi.mock('@renderer/utils/platform', () => ({
  isElectronDesktop: () => false,
}));

describe('registerPwa in development', () => {
  it('does not register a service worker for the Vite source host', async () => {
    const register = vi.fn();
    vi.stubGlobal('navigator', { serviceWorker: { register } });
    vi.stubGlobal('window', {
      isSecureContext: true,
      location: { hostname: '127.0.0.1', protocol: 'http:' },
    });

    const { registerPwa } = await import('@renderer/services/registerPwa');
    await expect(registerPwa()).resolves.toBeUndefined();
    expect(register).not.toHaveBeenCalled();

    vi.unstubAllGlobals();
  });
});
