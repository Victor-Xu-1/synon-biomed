import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  getRendererAccountScopeToken,
  registerRendererAccountReset,
  rendererAccountScopedKey,
  resetRendererAccountScopeForTest,
  setRendererAccountOwner,
} from '@/renderer/services/rendererAccountScope';

afterEach(() => resetRendererAccountScopeForTest());

describe('rendererAccountScope', () => {
  it('advances before resets and does nothing for the same normalized owner', () => {
    const observedScopes: string[] = [];
    const reset = vi.fn(() => observedScopes.push(getRendererAccountScopeToken()));
    const unregister = registerRendererAccountReset('renderer-account-scope-test', reset);
    try {
      expect(setRendererAccountOwner(' owner-a ')).toBe(true);
      const ownerAKey = rendererAccountScopedKey('frame-1');
      expect(setRendererAccountOwner('owner-a')).toBe(false);
      expect(setRendererAccountOwner('owner-b')).toBe(true);
      expect(rendererAccountScopedKey('frame-1')).not.toBe(ownerAKey);
      expect(reset).toHaveBeenCalledTimes(2);
      expect(observedScopes[0]).toContain('owner-a');
      expect(observedScopes[1]).toContain('owner-b');
    } finally {
      unregister();
    }
  });
});
