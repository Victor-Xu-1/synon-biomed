import { act, renderHook } from '@testing-library/react';
import React from 'react';
import { I18nextProvider } from 'react-i18next';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { mcpService } from '@/common/adapter/ipcBridge';
import type { IMcpServer } from '@/common/config/storage';
import { useMcpOAuth } from '@/renderer/hooks/mcp/useMcpOAuth';
import { createTestI18n, type TestLanguage } from '../../i18nTestUtils';

vi.mock('@/common/adapter/ipcBridge', () => ({
  mcpService: {
    checkOAuthStatus: { invoke: vi.fn() },
    loginMcpOAuth: { invoke: vi.fn() },
    logoutMcpOAuth: { invoke: vi.fn() },
  },
}));

const server = {
  id: 'mcp-1',
  name: 'Remote MCP',
  transport: { type: 'http', url: 'https://mcp.example.test/api' },
} as IMcpServer;

const renderOAuthHook = async (language: TestLanguage = 'en-US') => {
  const i18n = await createTestI18n(language);
  return renderHook(() => useMcpOAuth(), {
    wrapper: ({ children }) => React.createElement(I18nextProvider, { i18n }, children),
  });
};

describe('useMcpOAuth', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('opens the server-issued authorization URL and waits for real authentication', async () => {
    const popup = {
      opener: window,
      location: { replace: vi.fn() },
      close: vi.fn(),
    } as unknown as Window;
    vi.spyOn(window, 'open').mockReturnValue(popup);
    vi.mocked(mcpService.loginMcpOAuth.invoke).mockResolvedValue({
      success: true,
      pending: true,
      auth_url: 'https://auth.example.test/authorize?state=server-issued',
      redirect_uri: 'http://127.0.0.1:54321/callback',
    });
    vi.mocked(mcpService.checkOAuthStatus.invoke)
      .mockResolvedValueOnce({ authenticated: false })
      .mockResolvedValueOnce({ authenticated: true });

    const { result } = await renderOAuthHook();
    let loginResult: Awaited<ReturnType<typeof result.current.login>> | undefined;
    await act(async () => {
      const pending = result.current.login(server);
      await vi.advanceTimersByTimeAsync(2_000);
      loginResult = await pending;
    });

    expect(loginResult).toEqual({ success: true });
    expect(popup.location.replace).toHaveBeenCalledWith('https://auth.example.test/authorize?state=server-issued');
    expect(mcpService.checkOAuthStatus.invoke).toHaveBeenCalledTimes(2);
    expect(popup.close).toHaveBeenCalledOnce();
    expect(result.current.oauthStatus[server.id]).toMatchObject({
      isAuthenticated: true,
      needsLogin: false,
      isChecking: false,
    });
  });

  it('does not claim authentication when the browser blocks the authorization window', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null);
    vi.mocked(mcpService.loginMcpOAuth.invoke).mockResolvedValue({
      success: true,
      pending: true,
      auth_url: 'https://auth.example.test/authorize',
    });
    const { result } = await renderOAuthHook();

    let loginResult: Awaited<ReturnType<typeof result.current.login>> | undefined;
    await act(async () => {
      loginResult = await result.current.login(server);
    });

    expect(loginResult).toEqual({
      success: false,
      error: 'The browser blocked the OAuth authorization window',
    });
    expect(mcpService.checkOAuthStatus.invoke).not.toHaveBeenCalled();
    expect(result.current.oauthStatus[server.id]?.isAuthenticated).not.toBe(true);
  });

  it('returns a localized error for unsupported transports', async () => {
    const { result } = await renderOAuthHook('zh-CN');
    const stdioServer = { ...server, transport: { type: 'stdio', command: 'server' } } as IMcpServer;

    let loginResult: Awaited<ReturnType<typeof result.current.login>> | undefined;
    await act(async () => {
      loginResult = await result.current.login(stdioServer);
    });

    expect(loginResult).toEqual({ success: false, error: 'OAuth 需要使用基于 HTTP 的 MCP 传输方式' });
  });
});
