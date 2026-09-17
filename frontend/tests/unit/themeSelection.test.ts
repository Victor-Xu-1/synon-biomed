import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
  WARM_DARK_THEME_ID,
  WARM_LIGHT_THEME_ID,
  COOL_DARK_THEME_ID,
  COOL_LIGHT_THEME_ID,
  COOL_SYSTEM_THEME_ID,
  SYSTEM_THEME_ID,
  THEME_FAMILIES,
  THEME_MODES,
  getThemeFamilyFromId,
  getThemeId,
  getThemeModeFromId,
} from '@/common/theme/constants';
import { resolveActiveTheme } from '@/common/theme/resolveTheme';
import type { Theme } from '@/common/theme/types';
import { BUILTIN_THEMES } from '@renderer/theme/builtinThemes';

const theme = (id: string, appearance: Theme['appearance']): Theme => ({
  id,
  name: id,
  appearance,
  builtin: true,
  created_at: 0,
  updated_at: 0,
});

const themes: Theme[] = [
  theme(WARM_LIGHT_THEME_ID, 'light'),
  theme(WARM_DARK_THEME_ID, 'dark'),
  theme(COOL_LIGHT_THEME_ID, 'light'),
  theme(COOL_DARK_THEME_ID, 'dark'),
];

const generalSettingsSource = readFileSync(
  new URL('../../packages/desktop/src/renderer/pages/settings/GeneralSettings.tsx', import.meta.url),
  'utf8'
);

describe('visual theme selection', () => {
  it('keeps legacy theme ids while exposing four visual choices', () => {
    expect(THEME_FAMILIES).toEqual(['warm', 'cool', 'white', 'night']);
    expect(THEME_MODES).toEqual(['system', 'light', 'dark']);
    expect(generalSettingsSource).toMatch(/themeWarm/);
    expect(generalSettingsSource).toMatch(/themeCool/);
    expect(generalSettingsSource).toMatch(/themeWhite/);
    expect(generalSettingsSource).toMatch(/themeNight/);
    expect(generalSettingsSource).not.toMatch(/themeFamilyDark/);
    expect(generalSettingsSource).not.toMatch(/theme-mode-select/);
  });

  it('routes settings changes through the shared theme provider state', () => {
    expect(generalSettingsSource).toMatch(
      /import\s*\{\s*useThemeContext\s*\}\s*from\s*['"]@\/renderer\/hooks\/context\/ThemeContext['"]/
    );
    expect(generalSettingsSource).toMatch(
      /const\s*\{\s*selectTheme,\s*activeId:\s*activeThemeId,\s*activeTheme\s*\}\s*=\s*useThemeContext\(\)/
    );
    expect(generalSettingsSource).not.toMatch(/import useTheme from ['"]@\/renderer\/hooks\/system\/useTheme['"]/);
  });

  it('registers every family and appearance combination exactly once', () => {
    expect(BUILTIN_THEMES.map(({ id, appearance }) => [id, appearance])).toEqual([
      [WARM_LIGHT_THEME_ID, 'light'],
      [WARM_DARK_THEME_ID, 'dark'],
      [COOL_LIGHT_THEME_ID, 'light'],
      [COOL_DARK_THEME_ID, 'dark'],
      ['pure-white', 'light'],
      ['night', 'dark'],
    ]);
    expect(new Set(BUILTIN_THEMES.map(({ id }) => id)).size).toBe(6);
    expect(BUILTIN_THEMES[0].css).toBe(BUILTIN_THEMES[1].css);
    expect(BUILTIN_THEMES[2].css).toBe(BUILTIN_THEMES[3].css);
    expect(BUILTIN_THEMES[4].css).toBe(BUILTIN_THEMES[5].css);
  });

  it.each([
    [WARM_LIGHT_THEME_ID, false, WARM_LIGHT_THEME_ID],
    [WARM_DARK_THEME_ID, false, WARM_DARK_THEME_ID],
    [COOL_LIGHT_THEME_ID, false, COOL_LIGHT_THEME_ID],
    [COOL_DARK_THEME_ID, false, COOL_DARK_THEME_ID],
    [SYSTEM_THEME_ID, false, WARM_LIGHT_THEME_ID],
    [SYSTEM_THEME_ID, true, WARM_DARK_THEME_ID],
    [COOL_SYSTEM_THEME_ID, false, COOL_LIGHT_THEME_ID],
    [COOL_SYSTEM_THEME_ID, true, COOL_DARK_THEME_ID],
  ] as const)('resolves %s with prefersDark=%s to %s', (activeId, prefersDark, expectedId) => {
    expect(resolveActiveTheme(activeId, themes, prefersDark).id).toBe(expectedId);
  });

  it.each(['warm', 'cool'] as const)('round-trips every %s mode', (family) => {
    for (const mode of ['light', 'dark', 'system'] as const) {
      const id = getThemeId(family, mode);
      expect(getThemeFamilyFromId(id)).toBe(family);
      expect(getThemeModeFromId(id)).toBe(mode);
    }
  });

  it('keeps pure white and night as fixed-appearance families', () => {
    expect(getThemeId('white', 'light')).toBe('pure-white');
    expect(getThemeId('white', 'dark')).toBe('pure-white');
    expect(getThemeId('white', 'system')).toBe('pure-white');
    expect(getThemeModeFromId('pure-white')).toBe('light');
    expect(getThemeId('night', 'light')).toBe('night');
    expect(getThemeId('night', 'dark')).toBe('night');
    expect(getThemeId('night', 'system')).toBe('night');
    expect(getThemeModeFromId('night')).toBe('dark');
  });

  it.each([
    [WARM_LIGHT_THEME_ID, 'cool', COOL_LIGHT_THEME_ID],
    [WARM_DARK_THEME_ID, 'cool', COOL_DARK_THEME_ID],
    [SYSTEM_THEME_ID, 'cool', COOL_SYSTEM_THEME_ID],
    [COOL_LIGHT_THEME_ID, 'warm', WARM_LIGHT_THEME_ID],
    [COOL_DARK_THEME_ID, 'warm', WARM_DARK_THEME_ID],
    [COOL_SYSTEM_THEME_ID, 'warm', SYSTEM_THEME_ID],
  ] as const)('switches %s to %s without changing its mode', (activeId, family, expectedId) => {
    expect(getThemeId(family, getThemeModeFromId(activeId))).toBe(expectedId);
  });

  it('canonicalizes an unknown legacy id to warm light', () => {
    expect(resolveActiveTheme('removed-custom-theme', themes).id).toBe(WARM_LIGHT_THEME_ID);
  });

  it('fails clearly when the theme registry is empty', () => {
    expect(() => resolveActiveTheme(WARM_LIGHT_THEME_ID, [])).toThrow('no themes are registered');
  });
});
