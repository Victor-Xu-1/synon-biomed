/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/** Stable persisted light id for the warm palette. */
export const LIGHT_THEME_ID = 'light';
/** Stable persisted dark id for the cool palette. */
export const DARK_THEME_ID = 'dark';

/** Persisted IDs are an existing storage contract, not visual-family names.
 * Keep their bytes stable so saved selections survive upgrades. All display
 * labels and source symbols use the warm/cool family names.
 */
export const WARM_LIGHT_THEME_ID = LIGHT_THEME_ID;
export const WARM_DARK_THEME_ID = 'claude-dark';
export const COOL_LIGHT_THEME_ID = 'codex-light';
export const COOL_DARK_THEME_ID = DARK_THEME_ID;
/** New pure-white and night presets use dedicated ids so they do not alter legacy ids. */
export const WHITE_LIGHT_THEME_ID = 'pure-white';
export const NIGHT_DARK_THEME_ID = 'night';

/** Warm palette system sentinel stored in theme.activeId. */
export const SYSTEM_THEME_ID = 'system';
/** Cool palette system sentinel stored in theme.activeId. */
export const WARM_SYSTEM_THEME_ID = SYSTEM_THEME_ID;
export const COOL_SYSTEM_THEME_ID = 'system-codex';

export const THEME_FAMILIES = ['warm', 'cool', 'white', 'night'] as const;
export const THEME_MODES = ['system', 'light', 'dark'] as const;

export type ThemeFamily = (typeof THEME_FAMILIES)[number];
export type ThemeMode = (typeof THEME_MODES)[number];

export const isSystemThemeId = (themeId: string): boolean =>
  themeId === SYSTEM_THEME_ID || themeId === COOL_SYSTEM_THEME_ID;

export const getThemeFamilyFromId = (themeId: string): ThemeFamily =>
  themeId === COOL_LIGHT_THEME_ID || themeId === COOL_DARK_THEME_ID || themeId === COOL_SYSTEM_THEME_ID
    ? 'cool'
    : themeId === WHITE_LIGHT_THEME_ID
      ? 'white'
      : themeId === NIGHT_DARK_THEME_ID
        ? 'night'
        : 'warm';

export const getThemeModeFromId = (themeId: string): ThemeMode => {
  if (isSystemThemeId(themeId)) return 'system';
  return themeId === WARM_DARK_THEME_ID || themeId === COOL_DARK_THEME_ID || themeId === NIGHT_DARK_THEME_ID
    ? 'dark'
    : 'light';
};

export const getThemeId = (family: ThemeFamily, mode: ThemeMode): string => {
  if (family === 'white') return WHITE_LIGHT_THEME_ID;
  if (family === 'night') return NIGHT_DARK_THEME_ID;
  if (family === 'cool') {
    if (mode === 'system') return COOL_SYSTEM_THEME_ID;
    return mode === 'dark' ? COOL_DARK_THEME_ID : COOL_LIGHT_THEME_ID;
  }
  if (mode === 'system') return SYSTEM_THEME_ID;
  return mode === 'dark' ? WARM_DARK_THEME_ID : WARM_LIGHT_THEME_ID;
};
