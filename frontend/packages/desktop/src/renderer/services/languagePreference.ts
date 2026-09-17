import { configService } from '@/common/config/configService';
import { normalizeLanguageCode, type SupportedLanguage } from '@/common/config/i18n';

export const LANGUAGE_STORAGE_KEY = 'i18nextLng';
export const LANGUAGE_PREAUTH_OVERRIDE_KEY = 'synon.language.preAuthOverride';

type LanguagePreferenceListener = (language: SupportedLanguage) => void;

function storeLanguageHint(language: SupportedLanguage): void {
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(LANGUAGE_STORAGE_KEY, language);
  }
}

/**
 * Stage a language selected before authentication without attempting a
 * protected account-settings write. The authenticated login flow persists
 * this value through persistLanguagePreference after the session exists.
 */
export function stageLanguagePreference(language: string): SupportedLanguage {
  const normalized = normalizeLanguageCode(language);
  storeLanguageHint(normalized);
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(LANGUAGE_PREAUTH_OVERRIDE_KEY, '1');
  }
  configService.setLocal('language', normalized);
  return normalized;
}

/**
 * Persist locally before the authenticated write so a login-page selection
 * survives a rejected pre-auth settings request and can be retried after login.
 */
export async function persistLanguagePreference(language: string): Promise<SupportedLanguage> {
  const normalized = stageLanguagePreference(language);
  await configService.set('language', normalized);
  if (typeof localStorage !== 'undefined') {
    localStorage.removeItem(LANGUAGE_PREAUTH_OVERRIDE_KEY);
  }
  return normalized;
}

/**
 * Synchronize browser tabs through the platform storage event. The tab that
 * performs the write has already switched its own i18n instance, while other
 * tabs receive this event from the browser without a desktop IPC dependency.
 */
export function subscribeLanguagePreference(listener: LanguagePreferenceListener): () => void {
  if (typeof window === 'undefined') return () => {};

  const handleStorage = (event: StorageEvent) => {
    if (event.key !== LANGUAGE_STORAGE_KEY || !event.newValue) return;
    listener(normalizeLanguageCode(event.newValue));
  };

  window.addEventListener('storage', handleStorage);
  return () => window.removeEventListener('storage', handleStorage);
}
