import { act, fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const toolsModuleGate = vi.hoisted(() => ({
  release: undefined as (() => void) | undefined,
}));

vi.mock('@/renderer/pages/settings/settingsRouteLoaders', async () => {
  const react = await import('react');
  const page = (name: string) => async () => ({ default: () => react.createElement('h1', null, name) });
  const settingsRouteLoaders = {
    account: page('Account'),
    'plans-usage': page('Plans and usage'),
    experts: page('Experts'),
    skills: page('Skills'),
    tools: async () => {
      await new Promise<void>((resolve) => {
        toolsModuleGate.release = resolve;
      });
      return { default: () => react.createElement('h1', null, 'Tools') };
    },
    models: page('Models'),
    compute: page('Compute'),
    governance: page('Memory'),
    network: page('Network'),
    credentials: page('Credentials'),
    storage: page('Storage'),
    general: page('General'),
  };
  return {
    settingsRouteLoaders,
    isSettingsRouteId: (value: string | undefined | null) =>
      typeof value === 'string' && Object.hasOwn(settingsRouteLoaders, value),
  };
});

vi.mock('@/renderer/components/layout/AppLoader', () => ({ default: () => <div>Loading</div> }));

import SettingsRoute from '@/renderer/pages/settings/SettingsRoute';

const NavigationControls: React.FC = () => {
  const navigate = useNavigate();
  return (
    <>
      {['tools', 'models', 'compute', 'account', 'plans-usage'].map((section) => (
        <button key={section} type='button' onClick={() => void navigate(`/settings/${section}`)}>
          Go {section}
        </button>
      ))}
    </>
  );
};

const renderRoute = (initialEntry = '/settings/skills') =>
  render(
    <MemoryRouter initialEntries={[initialEntry]}>
      <NavigationControls />
      <Routes>
        <Route path='/settings/:section' element={<SettingsRoute />} />
      </Routes>
    </MemoryRouter>
  );

describe('SettingsRoute', () => {
  beforeEach(() => {
    toolsModuleGate.release = undefined;
  });

  it('replaces a cold module with one stable loading boundary instead of retaining stale settings content', async () => {
    renderRoute();
    expect(await screen.findByRole('heading', { name: 'Skills' })).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Go tools' }));
    await vi.waitFor(() => expect(toolsModuleGate.release).toBeTypeOf('function'));
    expect(screen.queryByRole('heading', { name: 'Skills' })).not.toBeInTheDocument();
    expect(screen.getByText('Loading')).toBeInTheDocument();

    await act(async () => toolsModuleGate.release?.());
    expect(await screen.findByRole('heading', { name: 'Tools' })).toBeInTheDocument();
  });

  it('redirects a removed settings route to the experts module', async () => {
    renderRoute('/settings/permissions');
    expect(await screen.findByRole('heading', { name: 'Experts' })).toBeInTheDocument();
  });

  it('uses the React Router section as the sole module authority', async () => {
    renderRoute();
    expect(await screen.findByRole('heading', { name: 'Skills' })).toBeInTheDocument();

    for (const [button, heading] of [
      ['Go models', 'Models'],
      ['Go compute', 'Compute'],
      ['Go account', 'Account'],
      ['Go plans-usage', 'Plans and usage'],
    ]) {
      fireEvent.click(screen.getByRole('button', { name: button }));
      expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument();
    }
  });
});
