import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';

import { configService } from '@/common/config/configService';
import { ipcBridge } from '@/common';
import i18nConfig from '@/common/config/i18n-config.json';
import {
  DEFAULT_LANGUAGE,
  normalizeLanguageCode,
  mergeWithFallback,
  ensureAndSwitch,
  type LocaleData,
  type SupportedLanguage,
} from '@/common/config/i18n';
import {
  LANGUAGE_PREAUTH_OVERRIDE_KEY,
  persistLanguagePreference,
  stageLanguagePreference,
  subscribeLanguagePreference,
} from '../languagePreference';

// The login/auth/onboarding shell must render synchronously and offline. Large
// route-specific dictionaries are split into local build chunks and loaded
// before the authenticated workspace mounts instead of blocking first paint.
import enCommon from './locales/en-US/common.json';
import enGuid from './locales/en-US/guid.json';
import enLogin from './locales/en-US/login.json';
import zhCommon from './locales/zh-CN/common.json';
import zhGuid from './locales/zh-CN/guid.json';
import zhLogin from './locales/zh-CN/login.json';
export type { I18nKey, I18nModule } from './i18n-keys';

// Re-exports
export { normalizeLanguageCode } from '@/common/config/i18n';
export type { SupportedLanguage } from '@/common/config/i18n';

export const supportedLanguages = i18nConfig.supportedLanguages;

const bootstrapLocaleData: LocaleData = {
  'en-US': { common: enCommon, guid: enGuid, login: enLogin },
  'zh-CN': { common: zhCommon, guid: zhGuid, login: zhLogin },
};

const fallbackBootstrapLocale = bootstrapLocaleData[DEFAULT_LANGUAGE] ?? {};

// Full dictionaries are cached separately from the bootstrap resources. A
// partial locale must never satisfy a later authenticated-workspace load.
const loadedTranslations = new Map<string, Record<string, unknown>>();
const loadingTranslations = new Map<string, Promise<Record<string, unknown>>>();

function syncDesktopLanguage(language: SupportedLanguage): void {
  // The browser-hosted login shell must remain local until authentication;
  // the desktop preload bridge is only available in the Electron renderer.
  if (typeof window !== 'undefined' && !(window as Window & { __backendPort?: number }).__backendPort) return;
  ipcBridge.systemSettings.changeLanguage.invoke({ language }).catch(() => {});
}

function getBootstrapLocaleModules(locale: string): Record<string, unknown> {
  const normalized = normalizeLanguageCode(locale);
  const modules = bootstrapLocaleData[normalized] ?? fallbackBootstrapLocale;
  if (normalized === DEFAULT_LANGUAGE) return modules;
  return mergeWithFallback(fallbackBootstrapLocale, modules);
}

function getLocalStorageLanguageHint(): string | null {
  if (typeof localStorage === 'undefined') return null;
  return localStorage.getItem('i18nextLng');
}

function getInjectedLanguageHint(): string | null {
  if (typeof window === 'undefined') return null;
  const language = window.__initialLanguage;
  return typeof language === 'string' && language.trim() !== '' ? language : null;
}

function getPreAuthLanguageOverride(): SupportedLanguage | null {
  if (typeof localStorage === 'undefined' || localStorage.getItem(LANGUAGE_PREAUTH_OVERRIDE_KEY) !== '1') {
    return null;
  }
  const hint = getLocalStorageLanguageHint();
  return hint ? normalizeLanguageCode(hint) : null;
}

function getInitialLanguage(): SupportedLanguage {
  const localStorageLanguage = getLocalStorageLanguageHint();
  const injectedLanguage = getInjectedLanguageHint();
  const hint = localStorageLanguage || injectedLanguage;
  return normalizeLanguageCode(hint || DEFAULT_LANGUAGE);
}

async function loadLocaleModules(locale: string): Promise<Record<string, unknown>> {
  const normalized = normalizeLanguageCode(locale);
  const cached = loadedTranslations.get(normalized);
  if (cached) return cached;

  const inFlight = loadingTranslations.get(normalized);
  if (inFlight) return inFlight;

  const request = (async () => {
    const fallback = (await import('./locales/zh-CN/index')).default as Record<string, unknown>;
    const modules =
      normalized === DEFAULT_LANGUAGE
        ? fallback
        : mergeWithFallback(fallback, (await import('./locales/en-US/index')).default as Record<string, unknown>);
    loadedTranslations.set(normalized, modules);
    return modules;
  })().finally(() => loadingTranslations.delete(normalized));
  loadingTranslations.set(normalized, request);
  return request;
}

export async function ensureFullLocale(locale = i18n.language): Promise<SupportedLanguage> {
  const normalized = normalizeLanguageCode(locale);
  const translation = await loadLocaleModules(normalized);
  i18n.addResourceBundle(normalized, 'translation', translation, true, true);
  return normalized;
}

const initialLanguage = getInitialLanguage();
const initialResources: Record<string, { translation: Record<string, unknown> }> = {
  [DEFAULT_LANGUAGE]: {
    translation: fallbackBootstrapLocale,
  },
};
if (initialLanguage !== DEFAULT_LANGUAGE) {
  initialResources[initialLanguage] = {
    translation: getBootstrapLocaleModules(initialLanguage),
  };
}

// Initialize i18n with fallback and initial locale loaded synchronously to avoid FOUC.
// NOTE: We intentionally do NOT use i18next-browser-languagedetector here.
// In WebUI mode the browser's localStorage is on a different origin than the
// Electron renderer, so the detector would read the wrong (or missing) value
// and fall back to navigator.language, causing a language mismatch (Issue #1176).
// Instead, we use localStorage and Electron's injected local config language
// only as hints for the initial render, then let configService be the source of truth.
i18n
  .use(initReactI18next)
  .init({
    resources: initialResources,
    lng: initialLanguage,
    fallbackLng: DEFAULT_LANGUAGE,
    debug: false,
    interpolation: { escapeValue: false },
  })
  .catch((error: Error) => {
    console.error('Failed to initialize i18n:', error);
  });

// Load initial language from configService (single source of truth).
// Wait until configService.whenReady() so we observe the authoritative value
// fetched from the backend rather than the empty cache that exists during
// module load.
async function initLanguage(): Promise<void> {
  try {
    await configService.whenReady();
    const savedLanguage = configService.get('language');
    const language = getPreAuthLanguageOverride() ?? normalizeLanguageCode(savedLanguage || DEFAULT_LANGUAGE);
    if (!i18n.hasResourceBundle(language, 'translation')) {
      i18n.addResourceBundle(language, 'translation', getBootstrapLocaleModules(language), true, true);
    }
    await i18n.changeLanguage(language);
    // Sync to localStorage so next page load can use it as a fast hint
    if (typeof localStorage !== 'undefined') {
      localStorage.setItem('i18nextLng', normalizeLanguageCode(language));
    }
  } catch (error) {
    console.error('Failed to initialize language:', error);
  }
}

// Initialize on module load
void initLanguage();

// The current workbench is browser-hosted. Browser storage events provide the
// real cross-tab language channel; the writing tab switches itself directly.
subscribeLanguagePreference(async (normalized) => {
  if (i18n.language === normalized) return;
  await ensureAndSwitch(i18n, normalized, loadLocaleModules);
});

/**
 * Change language with lazy loading.
 */
export async function changeLanguage(lang: string): Promise<void> {
  await ensureAndSwitch(i18n, lang, loadLocaleModules);
  const normalized = await persistLanguagePreference(lang);
  // Notify the desktop process to sync i18n (for tray menu, etc.).
  syncDesktopLanguage(normalized);
}

/**
 * Change the login shell language before an account session exists. The
 * selection is local-only until LoginPage persists it after authentication.
 */
export async function changeLanguageLocally(lang: string): Promise<void> {
  await ensureAndSwitch(i18n, lang, loadLocaleModules);
  const normalized = stageLanguagePreference(lang);
  syncDesktopLanguage(normalized);
}

// Clear translation cache (useful for development/testing)
export function clearTranslationCache(): void {
  loadedTranslations.clear();
  loadingTranslations.clear();
}

// Get loaded languages
export function getLoadedLanguages(): string[] {
  return Array.from(loadedTranslations.keys());
}

export default i18n;
