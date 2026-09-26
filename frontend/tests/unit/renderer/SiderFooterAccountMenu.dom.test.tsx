import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const messageMocks = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }));
const updateMocks = vi.hoisted(() => ({ check: vi.fn() }));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
    Message: messageMocks,
  };
});

vi.mock('@/renderer/services/synonBiomedRuntimeUpdate', () => ({
  checkSynonBiomedRuntimeUpdate: updateMocks.check,
}));

import SiderFooter from '@/renderer/components/layout/Sider/SiderFooter';

const tooltipProps = { disabled: true } as never;

describe('SiderFooter account menu', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    updateMocks.check.mockResolvedValue({
      channel: 'local',
      current: '0.1.0',
      latest: null,
      checkedAt: '2026-08-09T08:00:00Z',
      error: null,
      autoUpdate: false,
      required: null,
    });
  });

  it('opens personal account actions and removes help and feedback', async () => {
    const onAccountClick = vi.fn();
    const onPlansUsageClick = vi.fn();
    const onSettingsClick = vi.fn();
    const onSettingsIntent = vi.fn();
    const onLogoutClick = vi.fn();

    await renderWithI18n(
      <SiderFooter
        isMobile={false}
        isSettings={false}
        collapsed={false}
        username='victor'
        siderTooltipProps={tooltipProps}
        onAccountClick={onAccountClick}
        onPlansUsageClick={onPlansUsageClick}
        onSettingsClick={onSettingsClick}
        onSettingsIntent={onSettingsIntent}
        showLogout
        onLogoutClick={onLogoutClick}
      />,
      'en-US'
    );

    const account = screen.getByRole('button', { name: 'Open account and settings' });
    expect(account).toHaveTextContent('Victor');
    expect(account).not.toHaveTextContent('Local workspace');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();

    fireEvent.click(account);
    expect(onSettingsIntent).toHaveBeenCalledTimes(1);
    expect(screen.getByRole('menu', { name: 'Account and settings' })).toBeInTheDocument();
    expect(screen.queryByRole('menuitem', { name: /Help and feedback/i })).not.toBeInTheDocument();

    const menuItems = screen.getAllByRole('menuitem');
    expect(menuItems).toHaveLength(5);
    for (const menuItem of menuItems.slice(1)) {
      expect(menuItem.querySelector('.sider-account-menu-icon-slot')).toBeInTheDocument();
      expect(menuItem.querySelector('.sider-account-menu-icon-slot > .i-icon')).toBeInTheDocument();
    }

    fireEvent.click(screen.getByTestId('synon-account-profile'));
    expect(onAccountClick).toHaveBeenCalledTimes(1);

    fireEvent.click(account);
    fireEvent.click(screen.getByRole('menuitem', { name: 'Plan and usage' }));
    expect(onPlansUsageClick).toHaveBeenCalledTimes(1);
    expect(onAccountClick).toHaveBeenCalledTimes(1);

    fireEvent.click(account);
    fireEvent.click(screen.getByRole('menuitem', { name: 'Settings' }));
    expect(onSettingsClick).toHaveBeenCalledTimes(1);

    fireEvent.click(account);
    fireEvent.click(screen.getByRole('menuitem', { name: 'Logout' }));
    expect(onLogoutClick).toHaveBeenCalledTimes(1);
  });

  it('keeps the account footer visually continuous with the sidebar', async () => {
    await renderWithI18n(
      <SiderFooter
        isMobile={false}
        isSettings={false}
        collapsed={false}
        username='victor'
        siderTooltipProps={tooltipProps}
        onAccountClick={vi.fn()}
        onPlansUsageClick={vi.fn()}
        onSettingsClick={vi.fn()}
      />,
      'en-US'
    );

    const footer = screen.getByRole('button', { name: 'Open account and settings' }).closest('.sider-footer');
    expect(footer).not.toBeNull();
    expect(footer).not.toHaveClass('border-t', 'border-solid', 'border-[var(--color-border-2)]');
  });

  it('checks the configured runtime update source from the menu', async () => {
    await renderWithI18n(
      <SiderFooter
        isMobile={false}
        isSettings={false}
        collapsed={false}
        username='victor'
        siderTooltipProps={tooltipProps}
        onAccountClick={vi.fn()}
        onPlansUsageClick={vi.fn()}
        onSettingsClick={vi.fn()}
      />,
      'en-US'
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open account and settings' }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Check for updates' }));

    await waitFor(() => expect(updateMocks.check).toHaveBeenCalledTimes(1));
    expect(messageMocks.success).toHaveBeenCalledWith('Version 0.1.0 is up to date.');
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('uses the saved profile avatar in the account trigger and menu', async () => {
    await renderWithI18n(
      <SiderFooter
        isMobile={false}
        isSettings={false}
        collapsed={false}
        username='Victor'
        avatarDataUrl='data:image/png;base64,iVBORw0KGgo='
        siderTooltipProps={tooltipProps}
        onAccountClick={vi.fn()}
        onPlansUsageClick={vi.fn()}
        onSettingsClick={vi.fn()}
      />,
      'en-US'
    );

    const account = screen.getByRole('button', { name: 'Open account and settings' });
    expect(account.querySelector('img')).toBeInTheDocument();
    fireEvent.click(account);
    expect(screen.getByTestId('synon-account-profile').querySelector('img')).toBeInTheDocument();
  });

  it('closes on escape and outside pointer input', async () => {
    await renderWithI18n(
      <SiderFooter
        isMobile={false}
        isSettings={false}
        collapsed={false}
        username='victor'
        siderTooltipProps={tooltipProps}
        onAccountClick={vi.fn()}
        onPlansUsageClick={vi.fn()}
        onSettingsClick={vi.fn()}
      />,
      'en-US'
    );

    const account = screen.getByRole('button', { name: 'Open account and settings' });
    fireEvent.click(account);
    fireEvent.keyDown(window, { key: 'Escape' });
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();

    fireEvent.click(account);
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('does not offer a theme toggle because settings owns theme selection', async () => {
    await renderWithI18n(
      <SiderFooter
        isMobile={false}
        isSettings={false}
        collapsed={false}
        username='victor'
        siderTooltipProps={tooltipProps}
        onAccountClick={vi.fn()}
        onPlansUsageClick={vi.fn()}
        onSettingsClick={vi.fn()}
      />,
      'en-US'
    );

    fireEvent.click(screen.getByRole('button', { name: 'Open account and settings' }));

    expect(screen.queryByRole('menuitem', { name: /Dark|Light/i })).not.toBeInTheDocument();
    expect(screen.getAllByRole('menuitem')).toHaveLength(4);
  });
});
