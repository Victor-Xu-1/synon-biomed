import { fireEvent, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const messageMocks = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
const accountInsightsMocks = vi.hoisted(() => ({ retry: vi.fn() }));
const securityMocks = vi.hoisted(() => ({
  retry: vi.fn(),
  revokeDevice: vi.fn(),
  revokeOtherDevices: vi.fn(),
  revokeAll: vi.fn(),
  logout: vi.fn(),
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return { ...actual, Message: messageMocks };
});

vi.mock('@/renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({ user: { id: 'user-victor', username: 'victor' }, logout: securityMocks.logout }),
}));

vi.mock('@/renderer/hooks/context/LayoutContext', () => ({
  useLayoutContext: () => ({ isMobile: false }),
}));

vi.mock('@/renderer/hooks/account/useSynonBiomedAccountInsights', () => ({
  useSynonBiomedAccountInsights: () => ({
    data: {
      metrics: {
        totalTasks: 113,
        completedTasks: 96,
        projectCount: 8,
        artifactCount: 183,
        currentStreak: 8,
        longestStreak: 12,
        completionRate: 96 / 113,
        recentTaskCount: 21,
        activeDayCount: 37,
      },
      activityDays: [
        { date: '2026-08-01', count: 1, tokenCount: 12_800, isFuture: false },
        { date: '2026-08-02', count: 3, tokenCount: 34_400, isFuture: false },
        { date: '2026-08-03', count: 0, tokenCount: 0, isFuture: false },
      ],
      topSkills: [{ name: 'manage-project-development', invocationCount: 101, lastUsedAt: null }],
      availability: { projects: true, skills: true, tokenUsage: true },
      loadedAt: '2026-08-09T08:00:00Z',
    },
    loading: false,
    error: null,
    retry: accountInsightsMocks.retry,
  }),
}));

vi.mock('@/renderer/hooks/account/useWebAccountSecurity', () => ({
  useWebAccountSecurity: () => ({
    data: {
      managed: true,
      loginMethods: ['google', 'wechat', 'local'],
      devices: [
        {
          id: 'device-current',
          authMethod: 'google',
          createdAt: '2026-08-30T08:00:00Z',
          lastSeenAt: '2026-08-30T08:10:00Z',
          expiresAt: '2026-09-29T08:00:00Z',
          remembered: true,
          userAgent: 'Chrome · Windows',
          networkClass: 'loopback',
          ipAddress: '127.0.0.1',
          current: true,
        },
        {
          id: 'device-other',
          authMethod: 'local',
          createdAt: '2026-08-29T08:00:00Z',
          lastSeenAt: '2026-08-29T08:10:00Z',
          expiresAt: '2026-08-30T10:00:00Z',
          remembered: false,
          userAgent: 'Firefox · Linux',
          networkClass: 'private',
          ipAddress: '192.168.1.24',
          current: false,
        },
      ],
      events: [
        {
          id: 'event-1',
          type: 'login_succeeded',
          authMethod: 'google',
          createdAt: '2026-08-30T08:00:00Z',
          success: true,
        },
      ],
    },
    loading: false,
    error: null,
    mutating: false,
    retry: securityMocks.retry,
    revokeDevice: securityMocks.revokeDevice,
    revokeOtherDevices: securityMocks.revokeOtherDevices,
    revokeAll: securityMocks.revokeAll,
  }),
}));

import AccountSettings from '@/renderer/pages/settings/AccountSettings';

describe('AccountSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    window.localStorage.clear();
    window.history.replaceState(null, '', '#/settings/account');
    securityMocks.logout.mockResolvedValue(undefined);
    securityMocks.revokeDevice.mockResolvedValue({ revoked: 1, signedOut: false });
    securityMocks.revokeOtherDevices.mockResolvedValue({ revoked: 1, signedOut: false });
    securityMocks.revokeAll.mockResolvedValue({ revoked: 2, signedOut: true });
  });

  it('shows the editable profile without duplicating plan and billing content', async () => {
    const { container } = await renderWithI18n(<AccountSettings />, 'zh-CN');

    expect(screen.queryByRole('heading', { name: '个人账户' })).not.toBeInTheDocument();
    expect(screen.queryByText('管理个人资料，并查看基于本地工作记录生成的账户概览。')).not.toBeInTheDocument();
    expect(screen.getByText('Victor')).toBeInTheDocument();
    expect(screen.getByText('113')).toBeInTheDocument();
    expect(screen.getByText('183')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '工作活动' })).toBeInTheDocument();
    expect(screen.getByRole('img', { name: /Token 使用趋势/ })).toBeInTheDocument();
    expect(container.querySelector('.account-activity-chart__y-axis')).toBeInTheDocument();
    expect(container.querySelectorAll('.account-activity-chart__point')).toHaveLength(3);
    const chartMarkers = [...container.querySelectorAll('.account-activity-chart__marker')];
    expect(chartMarkers).toHaveLength(2);
    expect(chartMarkers.every((marker) => marker.tagName.toLowerCase() === 'span')).toBe(true);
    expect(container.querySelector('.account-activity-chart__line')?.tagName.toLowerCase()).toBe('path');
    const augustSecondPoint = container.querySelector<SVGElement>('[data-activity-key="2026-08-02"]');
    expect(augustSecondPoint).not.toBeNull();
    fireEvent.mouseEnter(augustSecondPoint!);
    expect(screen.getByRole('tooltip')).toHaveTextContent('2026年8月2日');
    expect(screen.getByRole('tooltip')).toHaveTextContent('3.4万 Token');
    expect(screen.getByRole('heading', { name: '账户安全' })).toBeInTheDocument();
    expect(screen.getByText('管理登录方式和已登录设备。')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '登录设备' })).toBeInTheDocument();
    expect(
      screen.getByText('按浏览器设备合并展示；同一设备重复登录不会生成重复条目，IP 为最近一次活动的连接地址。')
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: '退出其他设备' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '本地运行环境' })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '最近安全活动' })).not.toBeInTheDocument();
    expect(screen.queryByText(/最近安全活动|登录成功/)).not.toBeInTheDocument();
    expect(screen.getByText('Chrome · Windows')).toBeInTheDocument();
    expect(screen.getByText(/IP 127\.0\.0\.1/)).toBeInTheDocument();
    expect(screen.getByText(/IP 192\.168\.1\.24/)).toBeInTheDocument();
    expect(screen.getByText('Google')).toBeInTheDocument();
    expect(screen.getByText('微信')).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '最常用的 Skills' })).toBeInTheDocument();
    expect(screen.queryByText(/本地工作台已启用/)).not.toBeInTheDocument();
    expect(screen.queryByText('本地账户')).not.toBeInTheDocument();
    expect(screen.queryByText('套餐用量')).not.toBeInTheDocument();
    expect(screen.queryByText('账单与发票')).not.toBeInTheDocument();
    expect(container.querySelector('.account-management-workbench')).not.toBeInTheDocument();

    const profile = screen.getByTestId('account-profile-hero');
    const metrics = screen.getByTestId('account-metrics-strip');
    const skills = screen.getByRole('heading', { name: '最常用的 Skills' });
    const security = screen.getByRole('heading', { name: '账户安全' });
    expect(profile.compareDocumentPosition(metrics) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(skills.compareDocumentPosition(security) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('persists a changed username and restores it for the same account', async () => {
    const firstRender = await renderWithI18n(<AccountSettings />, 'zh-CN');

    fireEvent.click(screen.getByRole('button', { name: '修改用户名' }));
    fireEvent.change(screen.getByRole('textbox', { name: '用户名' }), {
      target: { value: '研究员 Victor' },
    });
    fireEvent.click(screen.getByRole('button', { name: '保存' }));

    expect(await screen.findByText('研究员 Victor')).toBeInTheDocument();
    expect(messageMocks.success).toHaveBeenCalledWith('用户名已保存。');

    firstRender.unmount();
    await renderWithI18n(<AccountSettings />, 'zh-CN');
    expect(screen.getByText('研究员 Victor')).toBeInTheDocument();
  });

  it('accepts a supported avatar and can remove it again', async () => {
    await renderWithI18n(<AccountSettings />, 'zh-CN');
    fireEvent.click(screen.getByRole('button', { name: '修改用户名' }));
    const avatarFile = new File([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10])], 'avatar.png', {
      type: 'image/png',
    });

    fireEvent.change(screen.getByTestId('account-avatar-input'), {
      target: { files: [avatarFile] },
    });

    await waitFor(() => expect(messageMocks.success).toHaveBeenCalledWith('头像已保存。'));
    expect(screen.getAllByRole('button', { name: '修改头像' }).some((button) => button.querySelector('img'))).toBe(
      true
    );

    fireEvent.click(screen.getByRole('button', { name: '移除头像' }));
    expect(messageMocks.success).toHaveBeenCalledWith('头像已移除。');
    expect(screen.getAllByRole('button', { name: '修改头像' }).some((button) => button.querySelector('img'))).toBe(
      false
    );
  });

  it('opens the privacy explanation and switches real activity aggregation modes', async () => {
    await renderWithI18n(<AccountSettings />, 'zh-CN');

    fireEvent.click(screen.getByRole('button', { name: '隐私说明' }));
    expect(await screen.findByRole('dialog')).toHaveTextContent('资料仅保存在当前浏览器');

    fireEvent.click(screen.getByRole('tab', { name: '每周' }));
    expect(screen.getByRole('tab', { name: '每周' })).toHaveAttribute('aria-selected', 'true');
  });
});
