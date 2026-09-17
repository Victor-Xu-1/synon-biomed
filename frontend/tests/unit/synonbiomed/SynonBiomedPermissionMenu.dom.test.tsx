import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { describe, expect, it, vi } from 'vitest';

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      ...actual.Message,
      success: vi.fn(),
      error: vi.fn(),
    },
  };
});

import SynonBiomedPermissionMenu from '@/renderer/components/synonBiomed/runtime/SynonBiomedPermissionMenu';
import { renderWithI18n } from '../i18nTestUtils';

const permissionOption = {
  id: 'mode',
  category: 'mode',
  currentValue: 'default',
  options: [{ value: 'default', label: 'Default', description: null }],
};

describe('SynonBiomedPermissionMenu', () => {
  it('renders one task-wide permission standard and persists the selected mode', async () => {
    const setConfigOption = vi.fn().mockResolvedValue([]);
    await renderWithI18n(
      <SynonBiomedPermissionMenu
        option={permissionOption}
        setStatus={{ state: 'idle' }}
        setConfigOption={setConfigOption}
        includeStandardModes
      />,
      'zh-CN'
    );

    const trigger = screen.getByTestId('synon-biomed-permission-selector');
    expect(trigger).toHaveAttribute('aria-label', '权限 · 请求批准');
    expect(trigger).toHaveClass('composer-icon-control');
    fireEvent.click(trigger);
    expect(screen.getByRole('menu')).toHaveAttribute('aria-label', '授权模式');
    expect(screen.getByRole('menu')).toHaveClass('app-overlay-menu', 'composer-control-menu');
    expect(screen.getByTestId('synon-biomed-permission-option-bypassPermissions')).toHaveAttribute(
      'aria-label',
      expect.stringContaining('后台自动批准所有工具操作')
    );
    expect(screen.getByTestId('synon-biomed-permission-option-bypassPermissions')).toHaveAttribute(
      'title',
      '后台自动批准所有工具操作，不显示权限弹窗'
    );
    expect(screen.getByTestId('synon-biomed-permission-option-default')).toHaveAttribute(
      'aria-label',
      expect.stringContaining('请求批准')
    );
    expect(screen.getByTestId('synon-biomed-permission-option-smart')).toHaveAttribute(
      'aria-label',
      expect.stringContaining('帮我批准')
    );
    expect(
      screen.getByTestId('synon-biomed-permission-option-default').querySelector('[data-permission-icon="request"]')
    ).toBeInTheDocument();
    expect(
      screen.getByTestId('synon-biomed-permission-option-smart').querySelector('[data-permission-icon="smart"]')
    ).toBeInTheDocument();
    expect(
      screen
        .getByTestId('synon-biomed-permission-option-bypassPermissions')
        .querySelector('[data-permission-icon="full"]')
    ).toBeInTheDocument();

    fireEvent.click(screen.getByTestId('synon-biomed-permission-option-bypassPermissions'));
    await waitFor(() => expect(setConfigOption).toHaveBeenCalledWith('mode', 'bypassPermissions'));
    expect(
      screen.getByTestId('synon-biomed-permission-selector').querySelector('[data-permission-icon="full"]')
    ).toBeInTheDocument();
  });

  it('does not invent runtime modes that the active conversation did not advertise', async () => {
    await renderWithI18n(
      <SynonBiomedPermissionMenu
        option={permissionOption}
        setStatus={{ state: 'idle' }}
        setConfigOption={vi.fn().mockResolvedValue([])}
      />,
      'zh-CN'
    );

    fireEvent.click(screen.getByTestId('synon-biomed-permission-selector'));
    expect(screen.getByTestId('synon-biomed-permission-option-default')).toBeInTheDocument();
    expect(screen.queryByTestId('synon-biomed-permission-option-bypassPermissions')).not.toBeInTheDocument();
  });
});
