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
      'environments',
      'credentials',
      'storage',
      'general',
    ].includes(value),
}));

import SettingsSider from '@/renderer/pages/settings/components/SettingsSider';

describe('SettingsSider navigation', () => {
  it('collapses the four library routes into one scientific toolkit entry', async () => {
    window.location.hash = '/settings/skills';
    await renderWithI18n(
      <HashRouter>
        <SettingsSider />
      </HashRouter>,
      'en-US'
    );

    // Experts, skills, connectors and environments are one entry now.
    const toolkit = screen.getByRole('link', { name: 'Scientific Toolkit' });
    expect(screen.queryByRole('link', { name: 'Experts' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Skills' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Connectors' })).toBeNull();
    expect(screen.queryByRole('link', { name: 'Scientific environments' })).toBeNull();
    // The eight remaining entries are the toolkit row plus the untouched ones.
    expect(screen.getAllByRole('link').map((link) => link.textContent?.trim())).toEqual([
      'Scientific Toolkit',
      'Models',
      'Compute',
      'Memory',
      'Network',
      'Credentials',
      'Storage',
      'General',
    ]);

    // A deep link to any of the four legacy routes keeps that one entry lit.
    expect(toolkit).toHaveAttribute('href', '#/settings/experts');
    expect(toolkit).toHaveAttribute('aria-current', 'page');
    expect(routeLoaderMocks.preloadDuringIdle).toHaveBeenCalledWith('skills');
  });

  it('uses native hash links and commits repeated settings route changes', async () => {
    window.location.hash = '/settings/experts';
    await renderWithI18n(
      <HashRouter>
        <SettingsSider />
      </HashRouter>,
      'en-US'
    );

    const toolkit = screen.getByRole('link', { name: 'Scientific Toolkit' });
    const models = screen.getByRole('link', { name: 'Models' });

    expect(toolkit).toHaveAttribute('aria-current', 'page');

    fireEvent.pointerEnter(toolkit);
    expect(routeLoaderMocks.preload).toHaveBeenCalledWith('experts');

    fireEvent.click(models);
    await waitFor(() => expect(window.location.hash).toBe('#/settings/models'));
    await waitFor(() => expect(models).toHaveAttribute('aria-current', 'page'));
    await waitFor(() => expect(routeLoaderMocks.preloadDuringIdle).toHaveBeenCalledWith('models'));

    fireEvent.click(toolkit);
    await waitFor(() => expect(window.location.hash).toBe('#/settings/experts'));
    await waitFor(() => expect(toolkit).toHaveAttribute('aria-current', 'page'));
  });
});
