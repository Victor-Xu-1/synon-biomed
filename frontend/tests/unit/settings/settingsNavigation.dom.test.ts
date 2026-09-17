import { waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import {
  navigateSettingsRoute,
  readSettingsRoute,
  subscribeToSettingsRoute,
} from '@/renderer/pages/settings/settingsNavigation';

describe('settingsNavigation', () => {
  it('uses the browser hash as the single navigation authority', async () => {
    window.location.hash = '#/settings/skills';
    const listener = vi.fn();
    const unsubscribe = subscribeToSettingsRoute(listener);

    navigateSettingsRoute('tools');

    expect(window.location.hash).toBe('#/settings/tools');
    expect(readSettingsRoute()).toBe('tools');
    await waitFor(() => expect(listener).toHaveBeenCalledWith('tools'));
    unsubscribe();
  });
});
