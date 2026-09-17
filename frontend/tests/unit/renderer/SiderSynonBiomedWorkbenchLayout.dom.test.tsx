/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { fireEvent, render, screen } from '@testing-library/react';
import React from 'react';
import { MemoryRouter, useLocation } from 'react-router';
import { describe, expect, it, vi } from 'vitest';

vi.mock('@arco-design/web-react', async () => {
  const actual = await vi.importActual<typeof import('@arco-design/web-react')>('@arco-design/web-react');
  return {
    ...actual,
    Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  };
});

vi.mock('@renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

vi.mock('@renderer/pages/conversation/Preview/context/PreviewContext', () => ({
  usePreviewContext: () => ({ closePreview: vi.fn() }),
}));

vi.mock('@renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({
    logout: vi.fn(),
    status: 'authenticated',
  }),
}));

vi.mock('@renderer/hooks/context/ThemeContext', () => ({
  useThemeContext: () => ({
    theme: 'light',
    setTheme: vi.fn(),
  }),
}));

vi.mock('@renderer/components/layout/Sider/SiderNav', () => ({
  SiderSearchEntry: () => <div data-testid='sider-search-entry' />,
}));

vi.mock('@renderer/components/layout/Sider/SiderFooter', () => ({
  default: ({ onAccountClick, onPlansUsageClick }: { onAccountClick: () => void; onPlansUsageClick: () => void }) => (
    <>
      <button type='button' data-testid='sider-footer' onClick={onAccountClick}>
        Account
      </button>
      <button type='button' data-testid='sider-plans-usage' onClick={onPlansUsageClick}>
        Plans and usage
      </button>
    </>
  ),
}));

vi.mock('@renderer/pages/conversation/GroupedHistory', () => ({
  default: () => (
    <div data-testid='grouped-history'>
      <div data-testid='pinned-section'>置顶</div>
      <div data-testid='project-section'>新建项目</div>
      <div data-testid='task-section'>新建任务</div>
    </div>
  ),
}));

vi.mock('@renderer/components/layout/routeModules', () => ({
  loadSettingsSider: async () => ({ default: () => <div data-testid='settings-sider' /> }),
  prefetchSettingsRoute: vi.fn().mockResolvedValue(undefined),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, options?: { defaultValue?: string }) =>
      key === 'settings.compute' ? '计算资源' : options?.defaultValue || key,
  }),
}));

import Sider, { SiderHistoryLoading } from '@/renderer/components/layout/Sider';

const LocationProbe: React.FC = () => {
  const location = useLocation();
  return <output data-testid='location-probe'>{location.pathname}</output>;
};

describe('Sider workbench layout', () => {
  it('uses a compact visible loading state while the conversation history is lazy-loaded', () => {
    render(<SiderHistoryLoading />);

    const loadingState = screen.getByTestId('sider-history-loading');
    expect(loadingState).toHaveAttribute('role', 'status');
    expect(loadingState).toHaveTextContent('common.loading');
    expect(loadingState).not.toHaveClass('min-h-200px');
  });

  it('keeps settings capability entries out of the project and task sidebar', async () => {
    render(
      <MemoryRouter initialEntries={['/guid']}>
        <Sider collapsed={false} />
      </MemoryRouter>
    );

    expect(await screen.findByTestId('project-section')).toBeInTheDocument();
    expect(screen.getByTestId('task-section')).toBeInTheDocument();
    expect(screen.queryByTestId('after-pinned-slot')).not.toBeInTheDocument();
    expect(screen.queryByTestId('after-projects-slot')).not.toBeInTheDocument();
    expect(screen.queryByTestId('sider-toolbar-new-task')).not.toBeInTheDocument();
    expect(document.querySelector('[data-capability-id]')).not.toBeInTheDocument();
    expect(screen.queryByText('Skill')).not.toBeInTheDocument();
    expect(screen.queryByText('MCP')).not.toBeInTheDocument();
  });

  it('keeps the project and task history inside an independently scrollable region', async () => {
    render(
      <MemoryRouter initialEntries={['/guid']}>
        <Sider collapsed={false} />
      </MemoryRouter>
    );

    const historyScroll = await screen.findByTestId('sider-history-scroll');
    expect(historyScroll).toHaveClass('flex-1', 'min-h-0', 'overflow-y-auto');
  });

  it('publishes account navigation inside the stable settings route boundary', async () => {
    window.history.replaceState(null, '', '#/settings/tools');
    render(
      <MemoryRouter initialEntries={['/settings/tools']}>
        <Sider collapsed={false} />
        <LocationProbe />
      </MemoryRouter>
    );

    await screen.findByTestId('settings-sider');

    fireEvent.click(screen.getByTestId('sider-footer'));

    expect(screen.getByTestId('location-probe')).toHaveTextContent('/settings/account');
  });

  it('publishes plan and usage navigation inside the stable settings route boundary', async () => {
    window.history.replaceState(null, '', '#/settings/account');
    render(
      <MemoryRouter initialEntries={['/settings/account']}>
        <Sider collapsed={false} />
        <LocationProbe />
      </MemoryRouter>
    );

    await screen.findByTestId('settings-sider');
    fireEvent.click(screen.getByTestId('sider-plans-usage'));

    expect(screen.getByTestId('location-probe')).toHaveTextContent('/settings/plans-usage');
  });
});
