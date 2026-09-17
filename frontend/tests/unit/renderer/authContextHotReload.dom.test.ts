import { afterEach, describe, expect, it, vi } from 'vitest';

type AuthContextTestHost = Window & {
  __synonBiomedAuthContextV1?: unknown;
};

describe('AuthContext hot-reload authority', () => {
  afterEach(() => {
    delete (window as AuthContextTestHost).__synonBiomedAuthContextV1;
    vi.resetModules();
  });

  it('preserves one context identity across module replacement in the same document', async () => {
    vi.resetModules();
    const first = await import('@/renderer/hooks/context/AuthContext');
    vi.resetModules();
    const second = await import('@/renderer/hooks/context/AuthContext');

    expect(second.AuthContext).toBe(first.AuthContext);
  });
});
