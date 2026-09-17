import { act, fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';
import { onboardingCompletionStorageKey } from '@/renderer/services/onboardingCompletionAuthority';
import { AUTH_RETURN_PATH_KEY } from '@/renderer/services/authSession';

const state = vi.hoisted(() => ({
  auth: { status: 'unauthenticated', user: null as null | { id: string }, failure: null },
  refresh: vi.fn(),
  workspaceLoads: 0,
}));

vi.mock('@/renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({
    ...state.auth,
    refresh: state.refresh,
  }),
}));

vi.mock('@/renderer/pages/login', () => ({ default: () => <h1>Login boundary</h1> }));
vi.mock('@/renderer/components/layout/routeModules', () => ({
  loadAuthenticatedWorkspaceRoute: () => {
    state.workspaceLoads += 1;
    return Promise.resolve({ default: () => <h1>Authenticated workspace</h1> });
  },
  loadGuidRoute: () => Promise.resolve({ default: () => null }),
  loadProjectRoute: () => Promise.resolve({ default: () => null }),
  loadArtifactPreviewRoute: () => Promise.resolve({ default: () => null }),
  loadSettingsRoute: () => Promise.resolve({ default: () => null }),
}));

import Router from '@/renderer/components/layout/Router';

describe('Router startup boundary', () => {
  afterEach(() => {
    sessionStorage.removeItem(onboardingCompletionStorageKey('owner-a'));
    sessionStorage.removeItem(AUTH_RETURN_PATH_KEY);
  });

  beforeEach(() => {
    state.auth = { status: 'unauthenticated', user: null, failure: null };
    state.refresh.mockReset();
    state.workspaceLoads = 0;
    window.location.hash = '#/login';
  });

  it('shows a calm recovery panel when the local service is unavailable', async () => {
    state.auth = { status: 'unavailable', user: null, failure: 'network' };
    window.location.hash = '#/settings/experts';

    await renderWithI18n(<Router />, 'zh-CN');

    expect(screen.getByTestId('auth-unavailable-panel')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '暂时无法连接工作台' })).toBeInTheDocument();
    expect(screen.getByRole('alert')).toHaveTextContent('连接失败，请稍后重试');
    expect(screen.getByText('你的页面和数据不会受影响，连接恢复后可继续使用。')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '重新连接' }));
    expect(state.refresh).toHaveBeenCalledOnce();
  });

  it('does not let a positive onboarding hint request the authenticated shell without backend auth', async () => {
    sessionStorage.setItem(onboardingCompletionStorageKey('owner-a'), 'true');
    await renderWithI18n(<Router />, 'en-US');
    expect(await screen.findByRole('heading', { name: 'Login boundary' })).toBeInTheDocument();
    expect(state.workspaceLoads).toBe(0);
  });

  it('loads the authenticated shell exactly once after backend auth succeeds', async () => {
    state.auth = { status: 'authenticated', user: { id: 'owner-a' }, failure: null };
    window.location.hash = '#/guid';
    let view!: Awaited<ReturnType<typeof renderWithI18n>>;
    await act(async () => {
      view = await renderWithI18n(<Router />, 'en-US');
      await Promise.resolve();
    });

    expect(await screen.findByRole('heading', { name: 'Authenticated workspace' })).toBeInTheDocument();
    await act(async () => view.rerender(<Router />));
    expect(state.workspaceLoads).toBe(1);
  });

  it('starts a fresh chat instead of reopening a remembered conversation after login', async () => {
    state.auth = { status: 'authenticated', user: { id: 'owner-a' }, failure: null };
    sessionStorage.setItem(onboardingCompletionStorageKey('owner-a'), 'true');
    sessionStorage.setItem(AUTH_RETURN_PATH_KEY, '/conversation/deleted-conversation');
    window.location.hash = '#/login';

    await act(async () => {
      await renderWithI18n(<Router />, 'zh-CN');
      await Promise.resolve();
    });

    await waitFor(() => expect(window.location.hash).toBe('#/guid'));
    expect(sessionStorage.getItem(AUTH_RETURN_PATH_KEY)).toBeNull();
  });
});
