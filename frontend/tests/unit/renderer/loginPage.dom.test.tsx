import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import React from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import LoginPage from '@/renderer/pages/login';

const translations: Record<string, string> = {
  'login.languageToggle': '切换语言',
  'login.pageTitle': 'Synon Biomed - 登录',
  'login.loginTitle': '登录',
  'login.registerTitle': '创建账户',
  'login.subtitle': '欢迎回到 Synon Biomed 工作台',
  'login.registerSubtitle': '创建 Synon Biomed 本地工作台账户',
  'login.username': '用户名',
  'login.name': '名称',
  'login.email': '邮箱（可选但推荐）',
  'login.password': '密码',
  'login.usernamePlaceholder': '请输入用户名',
  'login.namePlaceholder': '请输入名称',
  'login.emailPlaceholder': 'name@example.com',
  'login.passwordPlaceholder': '请输入密码',
  'login.newPasswordPlaceholder': '至少 8 个字符',
  'login.showPassword': '显示密码',
  'login.hidePassword': '隐藏密码',
  'login.rememberMe': '记住我',
  'login.submit': '登录',
  'login.submitting': '登录中...',
  'login.createAccount': '创建新账户',
  'login.existingAccount': '已有账户？返回登录',
  'login.createAndLogin': '创建并登录',
  'login.success': '登录成功',
  'login.registerSuccess': '账户创建成功',
  'login.footerPrimary': '生物医药 AI 工作台',
  'login.footerSecondary': '项目、专家、Skill 与 MCP 一体化',
  'login.oauth.methodsLabel': '安全登录方式',
  'login.oauth.alternativeMethods': '使用其他方式登录',
  'login.oauth.continueWith': '使用{{provider}}继续',
  'login.oauth.provider.google': 'Google',
  'login.oauth.provider.apple': 'Apple',
  'login.oauth.provider.wechat': '微信',
  'login.oauth.orPassword': '或使用本地密码',
  'login.errors.empty': '请输入用户名和密码',
  'login.errors.invalidName': '名称应为 2 至 64 个字符',
  'login.errors.invalidEmail': '请输入有效的邮箱地址',
  'login.errors.invalidPassword': '密码至少需要 8 个字符',
  'login.errors.csrfError': '浏览器安全令牌无法刷新，请清除本站数据后重试。',
  'login.errors.unknown': '登录失败',
  'login.errors.sessionExpired': '登录状态已过期',
};

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, values?: Record<string, string>) =>
      (translations[key] ?? key).replace('{{provider}}', values?.provider ?? ''),
    i18n: { language: 'zh-CN' },
  }),
}));

const login = vi.fn();
const register = vi.fn();
const i18nMocks = vi.hoisted(() => ({
  changeLanguageLocally: vi.fn().mockResolvedValue(undefined),
}));
const authProviderMocks = vi.hoisted(() => ({
  load: vi.fn(),
  begin: vi.fn(),
  consumeError: vi.fn(),
}));

vi.mock('@/renderer/hooks/context/AuthContext', () => ({
  useAuth: () => ({ status: 'unauthenticated', login, register }),
}));
vi.mock('@/renderer/services/authSession', () => ({ hasExpiredAuthSession: () => false }));
vi.mock('@/renderer/services/languagePreference', () => ({
  persistLanguagePreference: vi.fn().mockResolvedValue(undefined),
}));
vi.mock('@/renderer/services/i18n', () => i18nMocks);
vi.mock('@/renderer/services/authProviders', () => ({
  loadAuthCapabilities: authProviderMocks.load,
  beginExternalLogin: authProviderMocks.begin,
  consumeExternalAuthError: authProviderMocks.consumeError,
}));

describe('Synon Biomed login and registration page', () => {
  beforeEach(() => {
    localStorage.clear();
    login.mockReset();
    register.mockReset();
    i18nMocks.changeLanguageLocally.mockReset();
    i18nMocks.changeLanguageLocally.mockResolvedValue(undefined);
    login.mockResolvedValue({ success: true });
    register.mockResolvedValue({ success: true, user: { id: 'new-user', username: 'Researcher' } });
    authProviderMocks.load.mockReset();
    authProviderMocks.begin.mockReset();
    authProviderMocks.consumeError.mockReset();
    authProviderMocks.load.mockResolvedValue({ localPassword: true, providers: [] });
    authProviderMocks.consumeError.mockReturnValue(null);
  });

  it('defaults the local login username to victor when no identity was remembered', async () => {
    render(<LoginPage />);
    await waitFor(() => expect(screen.getByLabelText('用户名')).toHaveValue('victor'));
    expect(screen.getByLabelText('记住我')).not.toBeChecked();
  });

  it('changes the login language through the local-only pre-auth path', async () => {
    render(<LoginPage />);

    fireEvent.change(screen.getByLabelText('切换语言'), { target: { value: 'en-US' } });

    await waitFor(() => expect(i18nMocks.changeLanguageLocally).toHaveBeenCalledWith('en-US'));
  });

  it('keeps an already migrated plain remembered username unchanged', async () => {
    localStorage.setItem('rememberMe', 'true');
    localStorage.setItem('rememberedUsername', 'victor');
    render(<LoginPage />);
    await waitFor(() => expect(screen.getByLabelText('用户名')).toHaveValue('victor'));
    expect(localStorage.getItem('rememberedUsername')).toBe('victor');
  });

  it('removes the legacy stored password and migrates only the remembered username', async () => {
    const legacy = btoa(encodeURIComponent('victor')).split('').toReversed().join('');
    localStorage.setItem('rememberMe', 'true');
    localStorage.setItem('rememberedUsername', legacy);
    localStorage.setItem('rememberedPassword', 'reversible-secret');
    render(<LoginPage />);
    await waitFor(() => expect(screen.getByLabelText('用户名')).toHaveValue('victor'));
    expect(screen.getByLabelText('密码')).toHaveValue('');
    expect(localStorage.getItem('rememberedUsername')).toBe('victor');
    expect(localStorage.getItem('rememberedPassword')).toBeNull();
  });

  it('switches accessibly to registration and validates name, email, and password before the request', async () => {
    render(<LoginPage />);
    expect(screen.queryByText('登录', { selector: '.login-page__title' })).not.toBeInTheDocument();
    expect(screen.queryByText('欢迎回到 Synon Biomed 工作台')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: '创建新账户' }));
    expect(screen.getByRole('heading', { name: 'Synon Biomed' })).toBeVisible();
    expect(screen.queryByText('创建账户')).not.toBeInTheDocument();
    expect(screen.queryByText('创建 Synon Biomed 本地工作台账户')).not.toBeInTheDocument();
    expect(screen.getByLabelText('邮箱（可选但推荐）')).toHaveAttribute('type', 'email');
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'R' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'short' } });
    fireEvent.click(screen.getByRole('button', { name: '创建并登录' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('名称应为 2 至 64 个字符');
    expect(register).not.toHaveBeenCalled();
  });

  it('creates and signs in a real account request while never persisting its password', async () => {
    render(<LoginPage />);
    fireEvent.click(screen.getByRole('button', { name: '创建新账户' }));
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: 'Researcher' } });
    fireEvent.change(screen.getByLabelText('邮箱（可选但推荐）'), { target: { value: 'researcher@example.org' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: 'strong-pass-1' } });
    fireEvent.click(screen.getByLabelText('记住我'));
    fireEvent.click(screen.getByRole('button', { name: '创建并登录' }));
    await waitFor(() =>
      expect(register).toHaveBeenCalledWith({
        name: 'Researcher',
        email: 'researcher@example.org',
        password: 'strong-pass-1',
        remember: true,
      })
    );
    expect(localStorage.getItem('rememberMe')).toBe('true');
    expect(localStorage.getItem('rememberedUsername')).toBe('Researcher');
    expect(localStorage.getItem('rememberedPassword')).toBeNull();
  });

  it('returns to existing-account login and submits the bootstrap credential contract', async () => {
    render(<LoginPage />);
    fireEvent.click(screen.getByRole('button', { name: '创建新账户' }));
    fireEvent.click(screen.getByRole('button', { name: '已有账户？返回登录' }));
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'victor' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: '12345678' } });
    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() =>
      expect(login).toHaveBeenCalledWith({ username: 'victor', password: '12345678', remember: false })
    );
  });

  it('shows a localized recovery message for browser security-token failures', async () => {
    login.mockResolvedValue({ success: false, code: 'csrfError', shouldClearCache: true });
    render(<LoginPage />);
    fireEvent.change(screen.getByLabelText('用户名'), { target: { value: 'victor' } });
    fireEvent.change(screen.getByLabelText('密码'), { target: { value: '12345678' } });
    fireEvent.click(screen.getByRole('button', { name: '登录' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('浏览器安全令牌无法刷新，请清除本站数据后重试。');
  });

  it('offers configured Google, Apple, and WeChat login while keeping the local password fallback', async () => {
    authProviderMocks.load.mockResolvedValue({
      localPassword: true,
      providers: [
        { id: 'google', displayName: 'Google', enabled: true, startPath: '/api/auth/oidc/google/start' },
        { id: 'apple', displayName: 'Apple', enabled: true, startPath: '/api/auth/oidc/apple/start' },
        { id: 'wechat', displayName: 'WeChat', enabled: true, startPath: '/api/auth/oauth/wechat/start' },
      ],
    });
    render(<LoginPage />);
    const alternativeMethods = await screen.findByTestId('login-alternative-methods');
    expect(alternativeMethods).not.toHaveAttribute('open');
    fireEvent.click(screen.getByText('使用其他方式登录'));
    const google = await screen.findByRole('button', { name: '使用Google继续' });
    expect(screen.getByRole('button', { name: '使用Apple继续' })).toBeVisible();
    const wechat = screen.getByRole('button', { name: '使用微信继续' });
    expect(wechat).toBeVisible();
    expect(screen.getByLabelText('用户名')).toBeVisible();
    fireEvent.click(screen.getByLabelText('记住我'));
    fireEvent.click(google);
    expect(authProviderMocks.begin).toHaveBeenCalledWith(
      { id: 'google', displayName: 'Google', enabled: true, startPath: '/api/auth/oidc/google/start' },
      true
    );
  });

  it('starts the configured WeChat login flow', async () => {
    authProviderMocks.load.mockResolvedValue({
      localPassword: true,
      providers: [
        { id: 'google', displayName: 'Google', enabled: true, startPath: '/api/auth/oidc/google/start' },
        { id: 'apple', displayName: 'Apple', enabled: true, startPath: '/api/auth/oidc/apple/start' },
        { id: 'wechat', displayName: 'WeChat', enabled: true, startPath: '/api/auth/oauth/wechat/start' },
      ],
    });
    render(<LoginPage />);
    const wechat = await screen.findByRole('button', { name: '使用微信继续' });
    fireEvent.click(screen.getByLabelText('记住我'));
    fireEvent.click(wechat);
    expect(authProviderMocks.begin).toHaveBeenCalledWith(
      { id: 'wechat', displayName: 'WeChat', enabled: true, startPath: '/api/auth/oauth/wechat/start' },
      true
    );
  });
});
