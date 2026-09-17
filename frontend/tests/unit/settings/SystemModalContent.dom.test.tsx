/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { ConfigProvider } from '@arco-design/web-react';
import { SWRConfig } from 'swr';

const { systemInfoMock, updateSystemInfoMock, restartMock, showOpenMock, messageInfoMock, configServiceMock } =
  vi.hoisted(() => ({
    systemInfoMock: vi.fn(),
    updateSystemInfoMock: vi.fn(),
    restartMock: vi.fn(),
    showOpenMock: vi.fn(),
    messageInfoMock: vi.fn(),
    configServiceMock: {
      get: vi.fn(() => undefined),
      set: vi.fn(() => Promise.resolve()),
      setLocal: vi.fn(),
    },
  }));
const clientBusinessSettingsMocks = vi.hoisted(() => ({
  getClientBusinessSetting: vi.fn(),
  setClientBusinessSetting: vi.fn(() => Promise.resolve()),
}));

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (k: string) => k, i18n: { language: 'en' } }),
}));

vi.mock('@/renderer/utils/platform', () => ({
  isElectronDesktop: () => true,
}));

vi.mock('@/renderer/components/base/SynonScrollArea', () => ({
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}));

vi.mock('@/renderer/components/settings/LanguageSwitcher', () => ({
  default: () => <div>LanguageSwitcher</div>,
}));

vi.mock('@/renderer/components/settings/SettingsModal/contents/SystemModalContent/DevSettings', () => ({
  default: () => <div>DevSettings</div>,
}));

vi.mock('@/renderer/services/clientBusinessSettings', () => ({
  getClientBusinessSetting: clientBusinessSettingsMocks.getClientBusinessSetting,
  setClientBusinessSetting: clientBusinessSettingsMocks.setClientBusinessSetting,
  removeClientBusinessSetting: vi.fn(() => Promise.resolve()),
}));

vi.mock('@/common/config/configService', () => ({
  configService: configServiceMock,
}));

vi.mock('@/common', () => ({
  ipcBridge: {
    application: {
      systemInfo: { invoke: systemInfoMock },
      updateSystemInfo: { invoke: updateSystemInfoMock },
      restart: { invoke: restartMock },
      getStartOnBootStatus: { invoke: vi.fn(() => Promise.resolve({ success: false })) },
      getGpuStatus: { invoke: vi.fn(() => Promise.resolve({ success: false })) },
    },
    systemSettings: {
      getCloseToTray: { invoke: vi.fn(() => Promise.resolve(false)) },
      setCloseToTray: { invoke: vi.fn(() => Promise.resolve()) },
    },
    dialog: {
      showOpen: { invoke: showOpenMock },
    },
    shell: {
      openFolderWith: { invoke: vi.fn(() => Promise.resolve()) },
    },
  },
}));

vi.mock('@arco-design/web-react', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@arco-design/web-react')>();
  return {
    ...actual,
    Message: {
      ...actual.Message,
      info: messageInfoMock,
    },
    Modal: {
      ...actual.Modal,
      useModal: () => [
        {
          confirm: ({ onOk }: { onOk?: () => void }) => {
            onOk?.();
          },
        },
        null,
      ],
    },
  };
});

import SystemModalContent from '@/renderer/components/settings/SettingsModal/contents/SystemModalContent';

const renderContent = () =>
  render(
    <SWRConfig value={{ provider: () => new Map(), dedupingInterval: 0 }}>
      <ConfigProvider>
        <SystemModalContent />
      </ConfigProvider>
    </SWRConfig>
  );

describe('SystemModalContent Synon Biomed settings', () => {
  beforeEach(() => {
    cleanup();
    vi.clearAllMocks();
    configServiceMock.get.mockImplementation(() => undefined);
    configServiceMock.set.mockResolvedValue(undefined);
    clientBusinessSettingsMocks.getClientBusinessSetting.mockImplementation(async (key: string) => {
      if (key === 'acp.promptTimeout') return undefined;
      return undefined;
    });
    clientBusinessSettingsMocks.setClientBusinessSetting.mockResolvedValue(undefined);
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: vi.fn().mockImplementation((query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      })),
    });
    systemInfoMock.mockResolvedValue({
      cacheDir: '/cache',
      workDir: '/work',
      logDir: '/logs',
      platform: 'darwin',
      arch: 'arm64',
    });
    updateSystemInfoMock.mockResolvedValue(undefined);
    restartMock.mockResolvedValue({ restarted: true, manualRestartRequired: false });
    showOpenMock.mockResolvedValue(['/new-logs']);
  });

  it('does not render retired SynonAI directory settings or call legacy system-info endpoints', async () => {
    renderContent();

    expect(await screen.findByText('settings.language')).toBeInTheDocument();
    expect(screen.queryByText('settings.workDir')).not.toBeInTheDocument();
    expect(screen.queryByText('settings.logDir')).not.toBeInTheDocument();
    expect(systemInfoMock).not.toHaveBeenCalled();
    expect(updateSystemInfoMock).not.toHaveBeenCalled();
    expect(showOpenMock).not.toHaveBeenCalled();
  });

  it('loads the LLM prompt timeout from backend client settings', async () => {
    clientBusinessSettingsMocks.getClientBusinessSetting.mockImplementation(async (key: string) => {
      if (key === 'acp.promptTimeout') return 640;
      return undefined;
    });

    renderContent();

    expect(await screen.findByDisplayValue('640')).toBeInTheDocument();
    await waitFor(() => {
      expect(clientBusinessSettingsMocks.getClientBusinessSetting).not.toHaveBeenCalledWith('acp.agentIdleTimeout');
    });
  });

  it('does not fall back to legacy configService ACP timeout keys or agent idle settings', async () => {
    configServiceMock.get.mockImplementation((key: string) => {
      if (key === 'acp.promptTimeout') return 777;
      return undefined;
    });

    renderContent();

    expect(await screen.findByDisplayValue('300')).toBeInTheDocument();
    expect(configServiceMock.get).not.toHaveBeenCalledWith('acp.promptTimeout');
    expect(configServiceMock.get).not.toHaveBeenCalledWith('acp.agentIdleTimeout');
  });

  it('persists LLM prompt timeout changes through backend client settings', async () => {
    const user = userEvent.setup();
    renderContent();

    const timeoutInputs = await screen.findAllByRole('spinbutton');
    const promptTimeoutInput = timeoutInputs[0];
    expect(timeoutInputs).toHaveLength(1);

    await user.clear(promptTimeoutInput);
    await user.type(promptTimeoutInput, '450');
    fireEvent.blur(promptTimeoutInput);

    await waitFor(() => {
      expect(clientBusinessSettingsMocks.setClientBusinessSetting).toHaveBeenCalledWith('acp.promptTimeout', 450);
    });
    expect(clientBusinessSettingsMocks.setClientBusinessSetting).not.toHaveBeenCalledWith('acp.agentIdleTimeout', 7);
  });
});
