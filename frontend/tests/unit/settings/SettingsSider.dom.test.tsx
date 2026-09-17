import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { HashRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const routeLoaderMocks = vi.hoisted(() => ({
  preload: vi.fn(async () => ({ default: () => null })),
  preloadDuringIdle: vi.fn(() => vi.fn()),
}));

vi.mock('@/renderer/pages/settings/settingsRouteLoaders', () => ({
  preloadSettingsRoute: routeLoaderMocks.preload,
  preloadSettingsRoutesDuringIdle: routeLoaderMocks.preloadDuringIdle,
  isSettingsRouteId: (value: string | undefined | null) =>
    typeof value === 'string' &&
    [
      'account',
      'plans-usage',
      'experts',
      'skills',
      'tools',
      'models',
      'compute',
      'governance',
      'network',
      'credentials',
      'storage',
      'general',
    ].includes(value),
}));

import SettingsSider from '@/renderer/pages/settings/components/SettingsSider';

describe('SettingsSider navigation', () => {
  it('uses native hash links and commits repeated settings route changes', async () => {
    window.location.hash = '/settings/skills';
    await renderWithI18n(
      <HashRouter>
        <SettingsSider />
      </HashRouter>,
      'en-US'
    );

    const skills = screen.getByRole('link', { name: 'Skills' });
    const tools = screen.getByRole('link', { name: 'Tools' });
    const models = screen.getByRole('link', { name: 'Models' });

    expect(skills).toHaveAttribute('aria-current', 'page');
    expect(routeLoaderMocks.preloadDuringIdle).toHaveBeenCalledWith('skills');
    expect(tools).toHaveAttribute('href', '#/settings/tools');

    fireEvent.pointerEnter(tools);
    expect(routeLoaderMocks.preload).toHaveBeenCalledWith('tools');
    fireEvent.click(tools);
    await waitFor(() => expect(window.location.hash).toBe('#/settings/tools'));
    await waitFor(() => expect(tools).toHaveAttribute('aria-current', 'page'));
    await waitFor(() => expect(routeLoaderMocks.preloadDuringIdle).toHaveBeenCalledWith('tools'));

    fireEvent.click(models);
    await waitFor(() => expect(window.location.hash).toBe('#/settings/models'));
    await waitFor(() => expect(models).toHaveAttribute('aria-current', 'page'));
  });
});
