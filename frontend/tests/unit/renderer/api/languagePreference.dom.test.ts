import { beforeEach, describe, expect, it, vi } from 'vitest';

const configSet = vi.hoisted(() => vi.fn());
const configSetLocal = vi.hoisted(() => vi.fn());

vi.mock('@/common/config/configService', () => ({
  configService: {
    set: configSet,
    setLocal: configSetLocal,
  },
}));

import {
  LANGUAGE_PREAUTH_OVERRIDE_KEY,
  LANGUAGE_STORAGE_KEY,
  persistLanguagePreference,
  stageLanguagePreference,
  subscribeLanguagePreference,
} from '@/renderer/services/languagePreference';

describe('languagePreference', () => {
  beforeEach(() => {
    configSet.mockReset();
    configSetLocal.mockReset();
    localStorage.clear();
  });

  it('stores the normalized language before a rejected authenticated write', async () => {
    let hintAtRemoteWrite: string | null = null;
    configSet.mockImplementation(async () => {
      hintAtRemoteWrite = localStorage.getItem('i18nextLng');
      throw new Error('unauthenticated');
    });

    await expect(persistLanguagePreference('zh')).rejects.toThrow('unauthenticated');

    expect(hintAtRemoteWrite).toBe('zh-CN');
    expect(localStorage.getItem('i18nextLng')).toBe('zh-CN');
    expect(configSet).toHaveBeenCalledWith('language', 'zh-CN');
  });

  it('returns and persists the normalized language after authentication', async () => {
    configSet.mockResolvedValue(undefined);

    await expect(persistLanguagePreference('en')).resolves.toBe('en-US');

    expect(localStorage.getItem('i18nextLng')).toBe('en-US');
    expect(configSet).toHaveBeenCalledWith('language', 'en-US');
  });

  it('stages a pre-auth language selection without a remote write', () => {
    const staged = stageLanguagePreference('zh');

    expect(staged).toBe('zh-CN');
    expect(localStorage.getItem('i18nextLng')).toBe('zh-CN');
    expect(localStorage.getItem(LANGUAGE_PREAUTH_OVERRIDE_KEY)).toBe('1');
    expect(configSet).not.toHaveBeenCalled();
  });

  it('clears the pre-auth override only after the authenticated write succeeds', async () => {
    localStorage.setItem(LANGUAGE_PREAUTH_OVERRIDE_KEY, '1');
    configSet.mockResolvedValueOnce(undefined);

    await persistLanguagePreference('en-US');

    expect(configSet).toHaveBeenCalledWith('language', 'en-US');
    expect(localStorage.getItem(LANGUAGE_PREAUTH_OVERRIDE_KEY)).toBeNull();
  });

  it('synchronizes normalized language changes from other browser tabs and cleans up', () => {
    const listener = vi.fn();
    const unsubscribe = subscribeLanguagePreference(listener);

    window.dispatchEvent(new StorageEvent('storage', { key: 'unrelated', newValue: 'en' }));
    window.dispatchEvent(new StorageEvent('storage', { key: LANGUAGE_STORAGE_KEY, newValue: null }));
    expect(listener).not.toHaveBeenCalled();

    window.dispatchEvent(new StorageEvent('storage', { key: LANGUAGE_STORAGE_KEY, newValue: 'en' }));
    expect(listener).toHaveBeenCalledOnce();
    expect(listener).toHaveBeenCalledWith('en-US');

    unsubscribe();
    window.dispatchEvent(new StorageEvent('storage', { key: LANGUAGE_STORAGE_KEY, newValue: 'zh' }));
    expect(listener).toHaveBeenCalledOnce();
  });
});
