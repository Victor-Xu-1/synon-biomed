import { Message } from '@arco-design/web-react';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { SUPPORTED_LANGUAGES, SUPPORTED_LANGUAGE_LABELS } from '@/common/config/i18n';
import { changeLanguageLocally } from '@/renderer/services/i18n';
import { persistLanguagePreference } from '@/renderer/services/languagePreference';
import {
  beginExternalLogin,
  consumeExternalAuthError,
  loadAuthCapabilities,
  type AuthCapabilities,
} from '@/renderer/services/authProviders';
import AppLoader from '@renderer/components/layout/AppLoader';
import { hasExpiredAuthSession } from '@renderer/services/authSession';
import { useAuth } from '../../hooks/context/AuthContext';
import './LoginPage.css';

type MessageState = { type: 'error' | 'success'; text: string };
type AuthMode = 'login' | 'register';

const REMEMBER_ME_KEY = 'rememberMe';
const REMEMBERED_USERNAME_KEY = 'rememberedUsername';
const REMEMBERED_PASSWORD_KEY = 'rememberedPassword';
const DEFAULT_USERNAME = 'victor';
const DEFAULT_AUTH_CAPABILITIES: AuthCapabilities = { localPassword: true, providers: [] };
const PRODUCT_LANGUAGES = SUPPORTED_LANGUAGES.map(
  (language) => [language, SUPPORTED_LANGUAGE_LABELS[language]] as const
);

function containsControlCharacter(value: string): boolean {
  return [...value].some((character) => {
    const code = character.charCodeAt(0);
    return code <= 0x1f || (code >= 0x7f && code <= 0x9f);
  });
}

function decodeLegacyUsername(value: string): string {
  try {
    const reversed = value.split('').toReversed().join('');
    const decoded = decodeURIComponent(atob(reversed));
    const roundTrip = btoa(encodeURIComponent(decoded)).split('').toReversed().join('');
    return decoded && roundTrip === value ? decoded : value;
  } catch {
    return value;
  }
}

function normalizedRememberedUsername(value: string | null): string {
  const candidate = decodeLegacyUsername(value ?? '').trim();
  return candidate && !containsControlCharacter(candidate) ? candidate : DEFAULT_USERNAME;
}

const LoginPage: React.FC = () => {
  const { t, i18n } = useTranslation();
  const { status, login, register } = useAuth();
  const [mode, setMode] = useState<AuthMode>('login');
  const [username, setUsername] = useState(DEFAULT_USERNAME);
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [rememberMe, setRememberMe] = useState(false);
  const [passwordVisible, setPasswordVisible] = useState(false);
  const [message, setMessage] = useState<MessageState | null>(null);
  const [loading, setLoading] = useState(false);
  const [authCapabilities, setAuthCapabilities] = useState<AuthCapabilities>(DEFAULT_AUTH_CAPABILITIES);
  const [sessionExpired] = useState(hasExpiredAuthSession);
  const usernameRef = useRef<HTMLInputElement | null>(null);
  const messageTimer = useRef<number | undefined>(undefined);
  const providerNames: Record<string, string> = {
    google: t('login.oauth.provider.google'),
    apple: t('login.oauth.provider.apple'),
    wechat: t('login.oauth.provider.wechat'),
  };

  useEffect(() => {
    document.body.classList.add('login-page-active');
    return () => {
      document.body.classList.remove('login-page-active');
      if (messageTimer.current) window.clearTimeout(messageTimer.current);
    };
  }, []);

  useEffect(() => {
    if (sessionExpired) Message.clear();
  }, [sessionExpired]);

  useEffect(() => {
    document.title = t('login.pageTitle');
    document.documentElement.lang = i18n.language;
  }, [i18n.language, t]);

  useEffect(() => {
    localStorage.removeItem(REMEMBERED_PASSWORD_KEY);
    const remembered = localStorage.getItem(REMEMBER_ME_KEY) === 'true';
    const storedUsername = localStorage.getItem(REMEMBERED_USERNAME_KEY);
    if (remembered) {
      const migratedUsername = normalizedRememberedUsername(storedUsername);
      setUsername(migratedUsername);
      localStorage.setItem(REMEMBERED_USERNAME_KEY, migratedUsername);
      setRememberMe(true);
    }
    window.setTimeout(() => usernameRef.current?.focus(), 0);
  }, []);

  const showMessage = useCallback((next: MessageState) => {
    setMessage(next);
    if (messageTimer.current) window.clearTimeout(messageTimer.current);
    if (next.type === 'error') {
      messageTimer.current = window.setTimeout(() => setMessage(null), 5000);
    }
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    void loadAuthCapabilities(controller.signal).then((capabilities) => {
      if (!controller.signal.aborted) setAuthCapabilities(capabilities);
    });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const code = consumeExternalAuthError();
    if (!code) return;
    const keyByCode: Record<string, string> = {
      provider_cancelled: 'login.oauth.errors.cancelled',
      external_email_link_required: 'login.oauth.errors.emailLinkRequired',
      external_verified_email_required: 'login.oauth.errors.verifiedEmailRequired',
      wechat_unionid_required: 'login.oauth.errors.wechatUnionIDRequired',
      provider_unavailable: 'login.oauth.errors.providerUnavailable',
    };
    showMessage({ type: 'error', text: t(keyByCode[code] ?? 'login.oauth.errors.failed') });
  }, [showMessage, t]);

  const persistRememberedIdentity = useCallback(
    (identity: string) => {
      localStorage.removeItem(REMEMBERED_PASSWORD_KEY);
      if (rememberMe) {
        localStorage.setItem(REMEMBER_ME_KEY, 'true');
        localStorage.setItem(REMEMBERED_USERNAME_KEY, identity);
      } else {
        localStorage.removeItem(REMEMBER_ME_KEY);
        localStorage.removeItem(REMEMBERED_USERNAME_KEY);
      }
    },
    [rememberMe]
  );

  const handleSubmit = useCallback(
    async (event: React.FormEvent) => {
      event.preventDefault();
      const name = username.trim();
      const trimmedEmail = email.trim();
      if (!name || !password) {
        showMessage({ type: 'error', text: t('login.errors.empty') });
        return;
      }
      if (mode === 'register' && name.length < 2) {
        showMessage({ type: 'error', text: t('login.errors.invalidName') });
        return;
      }
      if (mode === 'register' && trimmedEmail && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/u.test(trimmedEmail)) {
        showMessage({ type: 'error', text: t('login.errors.invalidEmail') });
        return;
      }
      if (mode === 'register' && password.length < 8) {
        showMessage({ type: 'error', text: t('login.errors.invalidPassword') });
        return;
      }

      setLoading(true);
      setMessage(null);
      const result =
        mode === 'login'
          ? await login({ username: name, password, remember: rememberMe })
          : await register({ name, email: trimmedEmail || undefined, password, remember: rememberMe });

      if (result.success) {
        persistRememberedIdentity(name);
        setPassword('');
        try {
          await persistLanguagePreference(i18n.language);
        } catch (error) {
          console.error('Failed to persist language after authentication:', error);
        }
        showMessage({ type: 'success', text: t(mode === 'login' ? 'login.success' : 'login.registerSuccess') });
      } else {
        const code = 'code' in result ? result.code : 'unknown';
        const keyByCode: Record<string, string> = {
          invalidCredentials: 'login.errors.invalidCredentials',
          tooManyAttempts: 'login.errors.tooManyAttempts',
          networkError: 'login.errors.networkError',
          serverError: 'login.errors.serverError',
          csrfError: 'login.errors.csrfError',
          INVALID_NAME: 'login.errors.invalidName',
          INVALID_EMAIL: 'login.errors.invalidEmail',
          INVALID_PASSWORD: 'login.errors.invalidPassword',
          USERNAME_EXISTS: 'login.errors.usernameExists',
          EMAIL_EXISTS: 'login.errors.emailExists',
          TOO_MANY_ATTEMPTS: 'login.errors.tooManyAttempts',
          NETWORK_ERROR: 'login.errors.networkError',
          SERVER_ERROR: 'login.errors.serverError',
        };
        showMessage({ type: 'error', text: t(keyByCode[code] ?? 'login.errors.unknown') });
      }
      setLoading(false);
    },
    [
      email,
      i18n.language,
      login,
      mode,
      password,
      persistRememberedIdentity,
      register,
      rememberMe,
      showMessage,
      t,
      username,
    ]
  );

  const switchMode = useCallback(() => {
    setMode((current) => (current === 'login' ? 'register' : 'login'));
    setPassword('');
    setEmail('');
    setMessage(null);
    setPasswordVisible(false);
    window.setTimeout(() => usernameRef.current?.focus(), 0);
  }, []);

  if (status === 'checking') return <AppLoader />;

  return (
    <main className='login-page'>
      <section className='login-page__card' aria-labelledby='auth-title'>
        <label className='login-page__lang-select-wrapper' htmlFor='lang-select'>
          <span className='sr-only'>{t('login.languageToggle')}</span>
          <select
            id='lang-select'
            className='login-page__lang-select'
            value={i18n.language}
            onChange={(event) => void changeLanguageLocally(event.target.value)}
          >
            {PRODUCT_LANGUAGES.map(([code, label]) => (
              <option key={code} value={code}>
                {label}
              </option>
            ))}
          </select>
        </label>

        <header className='login-page__header'>
          <div className='login-page__logo'>
            <img src='./pwa/icon-192.png?v=9b986028' alt='' aria-hidden='true' />
          </div>
          <h1 id='auth-title'>Synon Biomed</h1>
        </header>

        <form className='login-page__form' onSubmit={handleSubmit} noValidate>
          {authCapabilities.providers.length > 0 && (
            <details
              className='login-page__alternative-methods'
              open={!authCapabilities.localPassword}
              data-testid='login-alternative-methods'
            >
              <summary>{t('login.oauth.alternativeMethods')}</summary>
              <div className='login-page__providers' aria-label={t('login.oauth.methodsLabel')}>
                {authCapabilities.providers.map((provider) => (
                  <button
                    key={provider.id}
                    type='button'
                    className='login-page__provider-button'
                    disabled={loading}
                    onClick={() => {
                      setLoading(true);
                      setMessage(null);
                      beginExternalLogin(provider, rememberMe);
                    }}
                  >
                    {provider.id === 'google' ? (
                      <svg className='login-page__provider-icon' viewBox='0 0 24 24' aria-hidden='true'>
                        <path
                          fill='#4285F4'
                          d='M21.6 12.23c0-.71-.06-1.4-.18-2.07H12v3.91h5.38a4.6 4.6 0 0 1-2 3.02v2.54h3.23c1.89-1.74 2.99-4.3 2.99-7.4Z'
                        />
                        <path
                          fill='#34A853'
                          d='M12 22c2.7 0 4.96-.9 6.61-2.37l-3.23-2.54c-.9.6-2.04.96-3.38.96-2.6 0-4.81-1.76-5.6-4.13H3.06v2.62A10 10 0 0 0 12 22Z'
                        />
                        <path
                          fill='#FBBC05'
                          d='M6.4 13.92A6.02 6.02 0 0 1 6.08 12c0-.67.12-1.32.32-1.92V7.46H3.06A10 10 0 0 0 2 12c0 1.61.38 3.14 1.06 4.54l3.34-2.62Z'
                        />
                        <path
                          fill='#EA4335'
                          d='M12 5.95c1.47 0 2.78.5 3.82 1.5l2.86-2.86A9.6 9.6 0 0 0 12 2a10 10 0 0 0-8.94 5.46l3.34 2.62C7.19 7.71 9.4 5.95 12 5.95Z'
                        />
                      </svg>
                    ) : provider.id === 'apple' ? (
                      <svg className='login-page__provider-icon' viewBox='0 0 24 24' aria-hidden='true'>
                        <path
                          fill='currentColor'
                          d='M17.05 20.28c-.98.95-2.05.8-3.08.35-1.09-.46-2.09-.48-3.24 0-1.44.62-2.2.44-3.06-.35C2.79 15.25 3.51 7.59 9.05 7.31c1.35.07 2.29.74 3.1.8 1.21-.24 2.37-.93 3.65-.84 1.54.13 2.7.74 3.46 1.88-3.18 1.9-2.43 6.09.49 7.26-.58 1.52-1.33 3.04-2.7 3.87zM12.03 7.25C11.88 4.99 13.71 3.13 15.87 3c.3 2.61-2.36 4.55-3.84 4.25z'
                        />
                      </svg>
                    ) : provider.id === 'wechat' ? (
                      <svg className='login-page__provider-icon' viewBox='0 0 24 24' aria-hidden='true'>
                        <path
                          fill='#07C160'
                          d='M9.65 4.2C4.87 4.2 1 7.36 1 11.25c0 2.2 1.25 4.17 3.2 5.46l-.8 2.39 2.8-1.4c1.05.39 2.2.61 3.45.61.38 0 .76-.02 1.12-.06a6.2 6.2 0 0 1-.35-2.03c0-3.72 3.34-6.74 7.46-6.74.1 0 .21 0 .31.01C17.21 6.46 13.83 4.2 9.65 4.2Z'
                        />
                        <path
                          fill='#07C160'
                          d='M23 16.18c0-3.08-3.1-5.58-6.92-5.58s-6.92 2.5-6.92 5.58 3.1 5.58 6.92 5.58c1.01 0 1.96-.18 2.82-.49l2.27 1.13-.65-1.93C22.05 19.45 23 17.93 23 16.18Z'
                        />
                        <circle cx='6.7' cy='10.1' r='1' fill='#fff' />
                        <circle cx='12.35' cy='10.1' r='1' fill='#fff' />
                        <circle cx='13.75' cy='15.55' r='.85' fill='#fff' />
                        <circle cx='18.4' cy='15.55' r='.85' fill='#fff' />
                      </svg>
                    ) : null}
                    <span>
                      {t('login.oauth.continueWith', {
                        provider: providerNames[provider.id] ?? provider.displayName,
                      })}
                    </span>
                  </button>
                ))}
              </div>
            </details>
          )}

          {authCapabilities.providers.length > 0 && authCapabilities.localPassword && (
            <div className='login-page__divider' role='separator'>
              <span>{t('login.oauth.orPassword')}</span>
            </div>
          )}

          {authCapabilities.localPassword && (
            <>
              <div className='login-page__form-item'>
                <label className='login-page__label' htmlFor='username'>
                  {t(mode === 'login' ? 'login.username' : 'login.name')}
                </label>
                <div className='login-page__input-wrapper'>
                  <svg
                    className='login-page__input-icon'
                    viewBox='0 0 24 24'
                    fill='none'
                    stroke='currentColor'
                    strokeWidth='2'
                    aria-hidden='true'
                  >
                    <path d='M20 21v-2a4 4 0 0 0-4-4H8a4 4 0 0 0-4 4v2' />
                    <circle cx='12' cy='7' r='4' />
                  </svg>
                  <input
                    ref={usernameRef}
                    id='username'
                    name='username'
                    className='login-page__input'
                    placeholder={t(mode === 'login' ? 'login.usernamePlaceholder' : 'login.namePlaceholder')}
                    autoComplete='username'
                    value={username}
                    onChange={(event) => setUsername(event.target.value)}
                    maxLength={64}
                    required
                  />
                </div>
              </div>

              {mode === 'register' && (
                <div className='login-page__form-item'>
                  <label className='login-page__label' htmlFor='email'>
                    {t('login.email')}
                  </label>
                  <div className='login-page__input-wrapper'>
                    <svg
                      className='login-page__input-icon'
                      viewBox='0 0 24 24'
                      fill='none'
                      stroke='currentColor'
                      strokeWidth='2'
                      aria-hidden='true'
                    >
                      <path d='M4 4h16v16H4z' />
                      <path d='m4 6 8 6 8-6' />
                    </svg>
                    <input
                      id='email'
                      name='email'
                      type='email'
                      className='login-page__input'
                      placeholder={t('login.emailPlaceholder')}
                      autoComplete='email'
                      value={email}
                      onChange={(event) => setEmail(event.target.value)}
                      maxLength={254}
                    />
                  </div>
                </div>
              )}

              <div className='login-page__form-item'>
                <label className='login-page__label' htmlFor='password'>
                  {t('login.password')}
                </label>
                <div className='login-page__input-wrapper'>
                  <svg
                    className='login-page__input-icon'
                    viewBox='0 0 24 24'
                    fill='none'
                    stroke='currentColor'
                    strokeWidth='2'
                    aria-hidden='true'
                  >
                    <rect x='3' y='11' width='18' height='11' rx='2' />
                    <path d='M7 11V7a5 5 0 0 1 10 0v4' />
                  </svg>
                  <input
                    id='password'
                    name='password'
                    type={passwordVisible ? 'text' : 'password'}
                    className='login-page__input'
                    placeholder={t(mode === 'register' ? 'login.newPasswordPlaceholder' : 'login.passwordPlaceholder')}
                    autoComplete={mode === 'register' ? 'new-password' : 'current-password'}
                    value={password}
                    onChange={(event) => setPassword(event.target.value)}
                    maxLength={128}
                    required
                  />
                  <button
                    type='button'
                    className='login-page__toggle-password'
                    onClick={() => setPasswordVisible((value) => !value)}
                    aria-label={passwordVisible ? t('login.hidePassword') : t('login.showPassword')}
                  >
                    <svg viewBox='0 0 24 24' fill='none' stroke='currentColor' strokeWidth='2' aria-hidden='true'>
                      {passwordVisible ? (
                        <>
                          <path d='M3 3l18 18M10.6 10.6a2 2 0 0 0 2.8 2.8M9.9 4.2A9 9 0 0 1 12 4c7 0 11 8 11 8a17 17 0 0 1-2 3M6.1 6.1C2.8 8.3 1 12 1 12s4 8 11 8a10 10 0 0 0 5.9-2' />
                        </>
                      ) : (
                        <>
                          <path d='M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z' />
                          <circle cx='12' cy='12' r='3' />
                        </>
                      )}
                    </svg>
                  </button>
                </div>
              </div>

              <div className='login-page__checkbox'>
                <input
                  type='checkbox'
                  id='remember-me'
                  checked={rememberMe}
                  onChange={(event) => setRememberMe(event.target.checked)}
                />
                <label htmlFor='remember-me'>{t('login.rememberMe')}</label>
              </div>
              <button type='submit' className='login-page__submit' disabled={loading}>
                {loading && <span className='login-page__spinner' aria-hidden='true' />}
                <span>
                  {t(loading ? 'login.submitting' : mode === 'login' ? 'login.submit' : 'login.createAndLogin')}
                </span>
              </button>
              <button type='button' className='login-page__mode-switch' onClick={switchMode} disabled={loading}>
                {t(mode === 'login' ? 'login.createAccount' : 'login.existingAccount')}
              </button>
            </>
          )}
          <div
            role='alert'
            aria-live='polite'
            className={`login-page__message ${message || sessionExpired ? 'login-page__message--visible' : ''} ${message?.type === 'success' ? 'login-page__message--success' : 'login-page__message--error'}`}
            hidden={!message && !sessionExpired}
          >
            {message?.text ?? (sessionExpired ? t('login.errors.sessionExpired') : null)}
          </div>
        </form>

        <footer className='login-page__footer'>
          <div className='login-page__footer-content'>
            <span>{t('login.footerPrimary')}</span>
            <span className='login-page__footer-divider'>•</span>
            <span>{t('login.footerSecondary')}</span>
          </div>
        </footer>
      </section>
    </main>
  );
};

export default LoginPage;
