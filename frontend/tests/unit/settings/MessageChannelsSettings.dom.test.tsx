import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import i18next from 'i18next';
import React from 'react';
import { I18nextProvider } from 'react-i18next';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import MessageChannelsSettings from '@/renderer/pages/settings/MessageChannelsSettings';
import enSettings from '@/renderer/services/i18n/locales/en-US/settings.json';
import zhSettings from '@/renderer/services/i18n/locales/zh-CN/settings.json';

const mocks = vi.hoisted(() => ({
  loadStatuses: vi.fn(),
  startQr: vi.fn(),
  pollQr: vi.fn(),
  unpair: vi.fn(),
}));

vi.mock('@/renderer/services/messageChannels', () => ({
  emptyMessageChannelStatuses: () => ({
    feishu: { configured: false, paired: false },
    wechat: { configured: false, paired: false },
  }),
  loadMessageChannelStatuses: mocks.loadStatuses,
  messageChannelErrorMessage: () => 'message channel request failed',
  pollMessageChannelQr: mocks.pollQr,
  startMessageChannelQr: mocks.startQr,
  unpairMessageChannel: mocks.unpair,
}));

vi.mock('@/renderer/pages/settings/components/SettingsPrimitives', () => ({
  SettingsSection: ({
    title,
    description,
    children,
  }: React.PropsWithChildren<{ title: string; description?: string }>) => (
    <section>
      <h2>{title}</h2>
      <p>{description}</p>
      {children}
    </section>
  ),
}));

const testI18n = i18next.createInstance();

describe('MessageChannelsSettings', () => {
  beforeEach(async () => {
    vi.clearAllMocks();
    await testI18n.init({
      lng: 'zh-CN',
      fallbackLng: 'zh-CN',
      resources: {
        'zh-CN': { translation: { settings: zhSettings } },
        'en-US': { translation: { settings: enSettings } },
      },
      interpolation: { escapeValue: false },
    });
    mocks.loadStatuses.mockResolvedValue({
      feishu: { configured: false, paired: false },
      wechat: { configured: false, paired: false },
    });
    mocks.startQr.mockResolvedValue({
      sessionKey: 'qr-session',
      qrCodeUrl: 'data:image/png;base64,local-qr',
    });
    mocks.pollQr
      .mockResolvedValueOnce({ status: 'wait', connected: false })
      .mockResolvedValue({ status: 'confirmed', connected: true });
    mocks.unpair.mockResolvedValue({ unpaired: true, pairedUsersRevoked: 1, restartScheduled: false });
  });

  it('keeps exactly Feishu and WeChat in the pairing surface', async () => {
    renderSettings();

    expect(screen.getByRole('heading', { name: '消息渠道' })).toBeInTheDocument();
    expect(screen.getByTestId('message-channel-card-feishu')).toBeInTheDocument();
    expect(screen.getByTestId('message-channel-card-wechat')).toBeInTheDocument();
    expect(screen.queryByText('钉钉')).not.toBeInTheDocument();
    expect(screen.queryByText('Telegram')).not.toBeInTheDocument();
    await waitFor(() => expect(mocks.loadStatuses).toHaveBeenCalledTimes(1));
  });

  it('renders a QR returned by the backend and completes pairing without exposing credentials', async () => {
    renderSettings();

    await waitFor(() => expect(mocks.loadStatuses).toHaveBeenCalledTimes(1));
    fireEvent.click(screen.getAllByRole('button', { name: '扫码配对' })[0]);

    expect(await screen.findByTestId('message-channel-qr-feishu')).toBeInTheDocument();
    expect(screen.getByTestId('message-channel-qr-feishu').querySelector('img')).toHaveAttribute(
      'src',
      'data:image/png;base64,local-qr'
    );
    await waitFor(() => expect(screen.getByTestId('message-channel-success-feishu')).toBeInTheDocument());
    expect(screen.queryByText('bot_token')).not.toBeInTheDocument();
    expect(screen.queryByText('client_secret')).not.toBeInTheDocument();
    expect(mocks.startQr).toHaveBeenCalledWith('feishu', expect.any(String), expect.any(AbortSignal));
    expect(mocks.pollQr).toHaveBeenCalledWith('feishu', 'qr-session', expect.any(AbortSignal));
  });

  it('requires confirmation before unpairing and refreshes the paired state afterward', async () => {
    let paired = true;
    mocks.loadStatuses.mockImplementation(async () => ({
      feishu: { configured: paired, paired },
      wechat: { configured: false, paired: false },
    }));
    mocks.unpair.mockImplementation(async () => {
      paired = false;
      return { unpaired: true, pairedUsersRevoked: 1, restartScheduled: false };
    });

    renderSettings();
    await waitFor(() => expect(mocks.loadStatuses).toHaveBeenCalledTimes(1));

    const card = screen.getByTestId('message-channel-card-feishu');
    fireEvent.click(screen.getByRole('button', { name: '取消配对' }));
    const dialog = await screen.findByRole('alertdialog', { name: '取消飞书配对' });
    expect(dialog).toHaveTextContent('取消飞书配对');
    expect(dialog).toHaveTextContent('清除当前工作台保存的飞书授权');
    expect(mocks.unpair).not.toHaveBeenCalled();

    const confirmButtons = screen.getAllByRole('button', { name: '取消配对' });
    fireEvent.click(confirmButtons[confirmButtons.length - 1]);

    await waitFor(() => expect(mocks.unpair).toHaveBeenCalledWith('feishu'));
    await waitFor(() => expect(mocks.loadStatuses).toHaveBeenCalledTimes(2));
    expect(card).not.toHaveTextContent('已配对');
    expect(screen.queryByRole('button', { name: '取消配对' })).not.toBeInTheDocument();
  });
});

function renderSettings() {
  return render(
    <I18nextProvider i18n={testI18n}>
      <MessageChannelsSettings />
    </I18nextProvider>
  );
}
