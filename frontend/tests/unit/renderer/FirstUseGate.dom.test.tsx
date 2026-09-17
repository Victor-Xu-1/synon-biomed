import { act, cleanup, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import React from 'react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const mocks = vi.hoisted(() => ({
  loadOnboardingCompletion: vi.fn(),
}));

vi.mock('@/renderer/services/onboardingService', () => ({
  loadOnboardingCompletion: mocks.loadOnboardingCompletion,
}));

vi.mock('@/renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'owner-a' } }),
}));

vi.mock('@/common/config/configService', () => ({
  configService: {
    get: vi.fn(() => undefined),
    set: vi.fn().mockResolvedValue(undefined),
    setLocal: vi.fn(),
    remove: vi.fn().mockResolvedValue(undefined),
    setBatch: vi.fn().mockResolvedValue(undefined),
    subscribe: vi.fn(() => () => undefined),
    initialize: vi.fn().mockResolvedValue(undefined),
    whenReady: vi.fn().mockResolvedValue(undefined),
    isInitialized: vi.fn(() => true),
    reset: vi.fn(),
  },
}));

import { FirstUseGate } from '@/renderer/components/layout/Router';
import {
  clearOnboardingCompletionOwnerSession,
  onboardingCompletionStorageKey,
} from '@/renderer/services/onboardingCompletionAuthority';

function GateRoutes({ initialEntry = '/guid' }: { initialEntry?: string }) {
  return (
    <MemoryRouter initialEntries={[initialEntry]}>
      <Routes>
        <Route path='/onboarding' element={<h1>Onboarding</h1>} />
        <Route element={<FirstUseGate />}>
          <Route path='/guid' element={<h1>Workspace</h1>} />
          <Route path='/conversation/:id' element={<h1>Conversation</h1>} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}

describe('FirstUseGate', () => {
  afterEach(() => {
    cleanup();
    clearOnboardingCompletionOwnerSession('owner-a');
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('single-flights the owner completion read across duplicate route renders', async () => {
    let resolveCompletion!: (complete: boolean) => void;
    mocks.loadOnboardingCompletion.mockReturnValue(
      new Promise<boolean>((resolve) => {
        resolveCompletion = resolve;
      })
    );

    const view = await renderWithI18n(<GateRoutes initialEntry='/conversation/conversation-a' />, 'en-US');

    await waitFor(() => expect(mocks.loadOnboardingCompletion).toHaveBeenCalledTimes(1));
    view.rerender(<GateRoutes initialEntry='/conversation/conversation-a' />);
    expect(mocks.loadOnboardingCompletion).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole('heading', { name: 'Conversation' })).not.toBeInTheDocument();

    await act(async () => resolveCompletion(true));
    expect(await screen.findByRole('heading', { name: 'Conversation' })).toBeInTheDocument();
  });

  it('renders a positive owner-session snapshot immediately, then closes on authoritative false', async () => {
    sessionStorage.setItem(onboardingCompletionStorageKey('owner-a'), 'true');
    let resolveCompletion!: (complete: boolean) => void;
    mocks.loadOnboardingCompletion.mockReturnValue(
      new Promise<boolean>((resolve) => {
        resolveCompletion = resolve;
      })
    );

    await renderWithI18n(<GateRoutes />, 'en-US');
    expect(await screen.findByRole('heading', { name: 'Workspace' })).toBeInTheDocument();
    await waitFor(() => expect(mocks.loadOnboardingCompletion).toHaveBeenCalledTimes(1));

    await act(async () => resolveCompletion(false));
    expect(await screen.findByRole('heading', { name: 'Onboarding' })).toBeInTheDocument();
  });

  it.each([
    [true, 'Workspace'],
    [false, 'Onboarding'],
  ])('routes an authoritative completion value of %s to %s', async (complete, heading) => {
    mocks.loadOnboardingCompletion.mockResolvedValueOnce(complete);
    await act(async () => {
      await renderWithI18n(<GateRoutes />, 'en-US');
    });
    expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument();
  });

  it('fails closed with a bounded retry state and recovers without exposing the raw error', async () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    mocks.loadOnboardingCompletion
      .mockRejectedValueOnce(new Error('secret-token=/private/user/path'))
      .mockResolvedValueOnce(false);
    const user = userEvent.setup();

    await act(async () => {
      await renderWithI18n(<GateRoutes />, 'en-US');
    });

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('Setup data is temporarily unavailable. Retry to continue.');
    expect(alert).not.toHaveTextContent('secret-token');
    expect(screen.queryByRole('heading', { name: 'Workspace' })).not.toBeInTheDocument();
    expect(consoleError).toHaveBeenCalledWith('[onboarding-completion] authoritative read failed');

    await user.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('heading', { name: 'Onboarding' })).toBeInTheDocument();
    expect(mocks.loadOnboardingCompletion).toHaveBeenCalledTimes(2);
  });
});
