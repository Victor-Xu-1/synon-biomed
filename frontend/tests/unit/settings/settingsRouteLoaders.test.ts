import { describe, expect, it } from 'vitest';

import { getSettingsWarmupOrder, type SettingsRouteId } from '@/renderer/pages/settings/settingsRouteLoaders';

describe('settings route warmup order', () => {
  it('warms only adjacent routes and leaves distant modules intent-loaded', () => {
    const order = getSettingsWarmupOrder('storage');

    expect(order).toEqual(['credentials', 'general']);
    expect(order).not.toContain('storage');
    expect(new Set(order).size).toBe(order.length);
    expect(order).toHaveLength(2);
  });

  it('warms one neighbor for an edge route', () => {
    const order = getSettingsWarmupOrder('experts');
    expect(order).toEqual(['skills'] satisfies SettingsRouteId[]);
  });
});
