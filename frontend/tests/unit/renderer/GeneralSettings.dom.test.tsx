import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import i18next from 'i18next';
import React from 'react';
import { I18nextProvider } from 'react-i18next';
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest';
import zhSettings from '@/renderer/services/i18n/locales/zh-CN/settings.json';

const testI18n = i18next.createInstance();

const mocks = vi.hoisted(() => ({
  loadContact: vi.fn(),
  selectTheme: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock('@/renderer/services/synonBiomedWorkspaceSettings', () => ({
  loadSynonBiomedContactEmail: mocks.loadContact,
  removeSynonBiomedContactEmail: vi.fn(),
  setSynonBiomedContactEmail: vi.fn(),
}));
vi.mock('@/renderer/hooks/context/ThemeContext', () => ({
  useThemeContext: () => ({
    activeTheme: null,
    activeId: 'system',
    selectTheme: mocks.selectTheme,
  }),
}));
vi.mock('@/renderer/services/messageChannels', () => ({
  emptyMessageChannelStatuses: () => ({
    feishu: { configured: false, paired: false },
    wechat: { configured: false, paired: false },
  }),
  loadMessageChannelStatuses: vi.fn().mockResolvedValue({
    feishu: { configured: false, paired: false },
    wechat: { configured: false, paired: false },
  }),
  messageChannelErrorMessage: () => 'message channel request failed',
  pollMessageChannelQr: vi.fn(),
  startMessageChannelQr: vi.fn(),
  unpairMessageChannel: vi.fn(),
}));
vi.mock('react-router', () => ({
  useNavigate: () => vi.fn(),
  useLocation: () => ({ pathname: '/settings/general' }),
}));
vi.mock('@/renderer/pages/settings/SynonBiomedModelsSettings', () => ({
  SynonBiomedModelsSettingsContent: () => <div data-testid='models-settings' />,
}));
vi.mock('@/renderer/components/settings/LanguageSwitcher', () => ({
  default: () => <div data-testid='language-switcher' />,
}));
vi.mock('@/renderer/pages/settings/components/ThirdPartyLicensesModal', () => ({
  default: () => null,
}));
vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: { ...actual.Message, success: mocks.success, error: mocks.error },
  };
});

import GeneralSettings from '@/renderer/pages/settings/GeneralSettings';

describe('GeneralSettings', () => {
  beforeAll(async () => {
    await testI18n.init({
      lng: 'zh-CN',
      fallbackLng: 'zh-CN',
      resources: {
        'zh-CN': { translation: { settings: zhSettings } },
      },
      interpolation: { escapeValue: false },
    });
  });

  beforeEach(() => {
    mocks.loadContact.mockResolvedValue(null);
    Object.defineProperty(window, 'matchMedia', {
      configurable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    });
  });
  afterEach(() => {
    cleanup();
    vi.clearAllMocks();
  });

  it('removes session defaults and authorization while keeping user-facing style names', async () => {
    renderGeneralSettings();
    expect(screen.queryByRole('heading', { name: '会话默认值' })).not.toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '授权' })).not.toBeInTheDocument();
    expect(screen.getByRole('heading', { name: '外观' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: '语音输入' })).not.toBeInTheDocument();
    expect(screen.queryByText('语音转文字')).not.toBeInTheDocument();
    expect(screen.queryByText('Claude')).not.toBeInTheDocument();
    expect(screen.queryByText('Codex')).not.toBeInTheDocument();

    fireEvent.click(screen.getByTestId('theme-family-select'));
    expect(await screen.findAllByText('暖色系')).not.toHaveLength(0);
    expect(screen.getByText('冷色系')).toBeInTheDocument();
    expect(screen.getByText('纯白色系')).toBeInTheDocument();
    expect(screen.getByText('夜间暗色系')).toBeInTheDocument();
    expect(screen.queryByTestId('theme-mode-select')).not.toBeInTheDocument();
    expect(screen.queryByText('Claude')).not.toBeInTheDocument();
    expect(screen.queryByText('Codex')).not.toBeInTheDocument();
  });
});

function renderGeneralSettings() {
  return render(
    <I18nextProvider i18n={testI18n}>
      <GeneralSettings />
    </I18nextProvider>
  );
}
