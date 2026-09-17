/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, it, expect } from 'vitest';
import { normalizeLanguageCode, DEFAULT_LANGUAGE, SUPPORTED_LANGUAGES } from '@/common/config/i18n';
import i18nConfig from '@/common/config/i18n-config.json';
import enResources from '@/renderer/services/i18n/locales/en-US';
import zhResources from '@/renderer/services/i18n/locales/zh-CN';

const supportedLanguages = ['zh-CN', 'en-US'] as const;

const localeResources = {
  'zh-CN': zhResources,
  'en-US': enResources,
};

describe('i18n', () => {
  it('defaults new users to Simplified Chinese', () => {
    expect(DEFAULT_LANGUAGE).toBe('zh-CN');
  });

  it('exposes every bundled product language', () => {
    expect(i18nConfig.supportedLanguages).toEqual(supportedLanguages);
    expect(SUPPORTED_LANGUAGES).toEqual(supportedLanguages);
  });

  it('keeps the configured modules aligned with every product language bundle', () => {
    const expectedModules = i18nConfig.modules.toSorted();
    for (const resources of Object.values(localeResources)) {
      expect(Object.keys(resources).toSorted()).toEqual(expectedModules);
    }
  });

  it('keeps every Chinese and English resource in key parity', () => {
    expect(leafKeys(enResources)).toEqual(leafKeys(zhResources));
  });

  describe('normalizeLanguageCode', () => {
    it('passes through exact supported tags', () => {
      for (const language of supportedLanguages) {
        expect(normalizeLanguageCode(language)).toBe(language);
      }
    });

    it('normalizes underscores to hyphens', () => {
      expect(normalizeLanguageCode('en_US')).toBe('en-US');
      expect(normalizeLanguageCode('zh_CN')).toBe('zh-CN');
    });

    it('resolves base language codes to their supported region', () => {
      expect(normalizeLanguageCode('en')).toBe('en-US');
      expect(normalizeLanguageCode('zh')).toBe('zh-CN');
    });

    it('routes Chinese variants to Chinese and other unsupported locales to English', () => {
      expect(normalizeLanguageCode('zh-TW')).toBe('zh-CN');
      expect(normalizeLanguageCode('ja-JP')).toBe('en-US');
      expect(normalizeLanguageCode('de-DE')).toBe('en-US');
      expect(normalizeLanguageCode('fr')).toBe('en-US');
      expect(normalizeLanguageCode('')).toBe(DEFAULT_LANGUAGE);
    });
  });
});

function leafKeys(value: unknown, prefix = ''): string[] {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return prefix ? [prefix] : [];
  return Object.entries(value as Record<string, unknown>)
    .flatMap(([key, child]) => leafKeys(child, prefix ? `${prefix}.${key}` : key))
    .toSorted();
}
