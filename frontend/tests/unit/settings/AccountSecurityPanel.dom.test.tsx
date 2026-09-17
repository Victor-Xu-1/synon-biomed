import { screen } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithI18n } from '../i18nTestUtils';

const securityMocks = vi.hoisted(() => ({
  state: {
    data: null as Record<string, unknown> | null,
    loading: false,
    error: null,
    mutating: false,
    retry: vi.fn(),
    revokeDevice: vi.fn(),
    revokeOtherDevices: vi.fn(),
    revokeAll: vi.fn(),
  },
}));

vi.mock('@/renderer/hooks/account/useWebAccountSecurity', () => ({
  useWebAccountSecurity: () => securityMocks.state,
}));

import AccountSecurityPanel from '@/renderer/pages/settings/account/AccountSecurityPanel';

const trustedCurrentDevice = {
  id: 'device-current',
  authMethod: 'local',
  createdAt: '2026-09-02T12:00:00Z',
  lastSeenAt: '2026-09-02T12:10:00Z',
  expiresAt: '2026-09-03T12:00:00Z',
  remembered: true,
  userAgent: 'Chrome · Windows',
  networkClass: 'loopback',
  ipAddress: '127.0.0.1',
  current: true,
  legacy: false,
};

const trustedOtherDevice = {
  ...trustedCurrentDevice,
  id: 'device-other',
  userAgent: 'Firefox · Linux',
  networkClass: 'private',
  ipAddress: '192.168.1.24',
  current: false,
};

const legacySession = (id: string, ipAddress: string) => ({
  ...trustedCurrentDevice,
  id,
  ipAddress,
  current: false,
  legacy: true,
});

describe('AccountSecurityPanel device projection', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    securityMocks.state.data = {
      managed: true,
      loginMethods: ['local'],
      devices: [
        trustedCurrentDevice,
        trustedOtherDevice,
        legacySession('legacy-one', '198.51.100.10'),
        legacySession('legacy-two', '198.51.100.11'),
      ],
      events: [],
    };
  });

  it('shows trusted devices once and summarizes legacy sessions without login counts or IPs', async () => {
    await renderWithI18n(<AccountSecurityPanel onSignedOut={vi.fn()} />, 'zh-CN');

    expect(screen.getAllByText('Chrome · Windows')).toHaveLength(1);
    expect(screen.getByText('Firefox · Linux')).toBeInTheDocument();
    expect(screen.getByText('当前')).toBeInTheDocument();
    expect(screen.getByText(/IP 127\.0\.0\.1/)).toBeInTheDocument();
    expect(screen.getByText(/IP 192\.168\.1\.24/)).toBeInTheDocument();
    expect(screen.queryByText(/198\.51\.100\.(10|11)/)).not.toBeInTheDocument();
    const legacyNotice = screen.getByRole('note', { name: '旧版会话提示' });
    expect(legacyNotice).toHaveTextContent('检测到旧版登录会话');
    expect(legacyNotice).not.toHaveTextContent(/\b2\b|2 个|2次/);
    expect(screen.getAllByRole('button', { name: '退出' })).toHaveLength(1);
    expect(screen.getByRole('button', { name: '退出其他设备' })).toBeEnabled();
  });

  it('does not show the legacy notice when every session belongs to a trusted device', async () => {
    securityMocks.state.data = {
      managed: true,
      loginMethods: ['local'],
      devices: [trustedCurrentDevice, trustedOtherDevice],
      events: [],
    };

    await renderWithI18n(<AccountSecurityPanel onSignedOut={vi.fn()} />, 'zh-CN');

    expect(screen.queryByRole('note', { name: '旧版会话提示' })).not.toBeInTheDocument();
  });
});
