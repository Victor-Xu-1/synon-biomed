/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, it, expect } from 'vitest';
import { migrateThemeConfig } from '@/common/theme/migrateThemeConfig';
import { LIGHT_THEME_ID, DARK_THEME_ID } from '@/common/theme/constants';

describe('migrateThemeConfig', () => {
  it('maps old css.activeThemeId default-theme to Light', () => {
    const out = migrateThemeConfig({
      theme: 'light',
      'css.activeThemeId': 'default-theme',
      'css.themes': [],
      customCss: '',
    });
    expect(out['theme.activeId']).toBe(LIGHT_THEME_ID);
  });
  it('maps a retired preset id to the Claude builtin', () => {
    const out = migrateThemeConfig({
      theme: 'light',
      'css.activeThemeId': 'retired-template-id',
      'css.themes': [],
      customCss: '',
    });
    expect(out['theme.activeId']).toBe(LIGHT_THEME_ID);
  });
  it('uses dark toggle when no active css theme', () => {
    const out = migrateThemeConfig({ theme: 'dark', 'css.activeThemeId': '', 'css.themes': [], customCss: '' });
    expect(out['theme.activeId']).toBe(DARK_THEME_ID);
  });
  it('does not migrate retired user CSS themes into the two-template catalog', () => {
    const out = migrateThemeConfig({
      theme: 'dark',
      'css.activeThemeId': '',
      customCss: '',
      'css.themes': [{ id: 'u1', name: 'Mine', css: 'body{color:red}', created_at: 5, updated_at: 6 }],
    });
    expect(out['theme.activeId']).toBe(DARK_THEME_ID);
    expect(out['theme.userThemes']).toEqual([]);
  });
});
