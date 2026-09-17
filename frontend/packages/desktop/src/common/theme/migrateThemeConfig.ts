/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { Theme } from './types';
import { LIGHT_THEME_ID, DARK_THEME_ID } from './constants';

type OldCssTheme = {
  id: string;
  name: string;
  cover?: string;
  css: string;
  is_preset?: boolean;
  created_at: number;
  updated_at: number;
};

export type OldThemeConfig = {
  theme?: string;
  'css.activeThemeId'?: string;
  'css.themes'?: OldCssTheme[];
  customCss?: string;
};

export type NewThemeConfig = {
  'theme.activeId': string;
  'theme.userThemes': Theme[];
};

const OLD_DEFAULT_ID = 'default-theme';

/**
 * Legacy CSS skins are intentionally not migrated into the active catalog.
 * Legacy coupled selections map to their equivalent built-in visuals. Runtime
 * family and color-mode selection are independent after migration.
 */

export function migrateThemeConfig(old: OldThemeConfig): NewThemeConfig {
  const appearance = old.theme === 'dark' ? 'dark' : 'light';

  let activeId: string;
  const oldActive = old['css.activeThemeId'] || '';
  if (oldActive === LIGHT_THEME_ID) {
    activeId = LIGHT_THEME_ID;
  } else if (oldActive === DARK_THEME_ID) {
    activeId = DARK_THEME_ID;
  } else if (oldActive && oldActive !== OLD_DEFAULT_ID) {
    activeId = appearance === 'dark' ? DARK_THEME_ID : LIGHT_THEME_ID;
  } else {
    activeId = appearance === 'dark' ? DARK_THEME_ID : LIGHT_THEME_ID;
  }

  // Do not carry retired CSS skins, cover images, or custom CSS into the new catalog.
  return { 'theme.activeId': activeId, 'theme.userThemes': [] };
}
