/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { Theme } from '@/common/theme/types';
import {
  WARM_LIGHT_THEME_ID,
  getThemeFamilyFromId,
  getThemeId,
  getThemeModeFromId,
  THEME_FAMILIES,
  type ThemeFamily,
} from '@/common/theme/constants';
import { useThemeContext } from '@renderer/hooks/context/ThemeContext.tsx';
import { Message } from '@arco-design/web-react';
import { CheckOne } from '@icon-park/react';
import React, { useCallback, useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import { BUILTIN_THEMES } from '@renderer/theme/builtinThemes';

interface ThemePreviewPalette {
  appBg: string;
  headerBg: string;
  sideBg: string;
  mainBg: string;
  border: string;
  accent: string;
  textMuted: string;
  userBubble: string;
  aiBubble: string;
}

type ThemeMode = 'light' | 'dark';

const fallbackThemePreviewPalettes: Record<ThemeFamily, Record<ThemeMode, ThemePreviewPalette>> = {
  warm: {
    light: {
      appBg: '#faf9f7',
      headerBg: '#f2f1ee',
      sideBg: '#f2f1ee',
      mainBg: '#faf9f7',
      border: '#e1dfda',
      accent: '#3b3a37',
      textMuted: '#75716b',
      userBubble: '#f0efec',
      aiBubble: '#f4f2ee',
    },
    dark: {
      appBg: '#1f1e1c',
      headerBg: '#191816',
      sideBg: '#191816',
      mainBg: '#1f1e1c',
      border: '#393835',
      accent: '#f1efeb',
      textMuted: '#a39f98',
      userBubble: '#282724',
      aiBubble: '#242320',
    },
  },
  cool: {
    light: {
      appBg: '#f7f7f6',
      headerBg: '#eeeeec',
      sideBg: '#eeeeec',
      mainBg: '#f7f7f6',
      border: '#d8d8d4',
      accent: '#20201e',
      textMuted: '#74706a',
      userBubble: '#e9e9e7',
      aiBubble: '#fafaf9',
    },
    dark: {
      appBg: '#181818',
      headerBg: '#111111',
      sideBg: '#111111',
      mainBg: '#181818',
      border: '#303030',
      accent: '#f2f2f2',
      textMuted: '#9c9c9c',
      userBubble: '#292927',
      aiBubble: '#1e1e1e',
    },
  },
  white: {
    light: {
      appBg: '#ffffff',
      headerBg: '#fbfbfb',
      sideBg: '#fbfbfb',
      mainBg: '#ffffff',
      border: '#e5e5e5',
      accent: '#111111',
      textMuted: '#6d6d6d',
      userBubble: '#f5f5f5',
      aiBubble: '#ffffff',
    },
    dark: {
      appBg: '#121212',
      headerBg: '#0d0d0d',
      sideBg: '#0d0d0d',
      mainBg: '#121212',
      border: '#303030',
      accent: '#f5f5f5',
      textMuted: '#999999',
      userBubble: '#202020',
      aiBubble: '#181818',
    },
  },
  night: {
    light: {
      appBg: '#121212',
      headerBg: '#0d0d0d',
      sideBg: '#0d0d0d',
      mainBg: '#121212',
      border: '#303030',
      accent: '#f5f5f5',
      textMuted: '#999999',
      userBubble: '#202020',
      aiBubble: '#181818',
    },
    dark: {
      appBg: '#121212',
      headerBg: '#0d0d0d',
      sideBg: '#0d0d0d',
      mainBg: '#121212',
      border: '#303030',
      accent: '#f5f5f5',
      textMuted: '#999999',
      userBubble: '#202020',
      aiBubble: '#181818',
    },
  },
};

const getThemeFamilyLabel = (family: ThemeFamily, t: (key: string) => string): string => {
  if (family === 'warm') return t('settings.generalSettings.themeWarm');
  if (family === 'cool') return t('settings.generalSettings.themeCool');
  if (family === 'white') return t('settings.generalSettings.themeWhite');
  return t('settings.generalSettings.themeNight');
};

const readThemeFamily = (theme: Theme): ThemeFamily => getThemeFamilyFromId(theme.id);

const stripImportant = (value: string) => value.replace(/\s*!important\s*/gi, '').trim();
const escapeRegExp = (value: string) => value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

const normalizeColorLike = (value: string, fallback: string) => {
  const cleaned = stripImportant(value);
  if (!cleaned || cleaned.includes('{{') || cleaned.includes('}}') || /var\(/i.test(cleaned)) return fallback;
  if (/^\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}$/.test(cleaned)) return `rgb(${cleaned})`;
  if (/^\d{1,3}\s*,\s*\d{1,3}\s*,\s*\d{1,3}\s*,\s*(0|0?\.\d+|1)$/.test(cleaned)) {
    return `rgba(${cleaned})`;
  }
  return cleaned;
};

const parseCssVarsFromBlocks = (css: string, selector: string) => {
  if (!css) return {};
  const regex = new RegExp(`${escapeRegExp(selector)}\\s*\\{([\\s\\S]*?)\\}`, 'gi');
  const map: Record<string, string> = {};
  let blockMatch: RegExpExecArray | null;
  while ((blockMatch = regex.exec(css)) !== null) {
    const block = blockMatch[1] || '';
    const varRegex = /--([a-zA-Z0-9-_]+)\s*:\s*([^;]+);/g;
    let varMatch: RegExpExecArray | null;
    while ((varMatch = varRegex.exec(block)) !== null) map[varMatch[1]] = varMatch[2].trim();
  }
  return map;
};

const resolveCssVarValue = (value: string, vars: Record<string, string>, depth = 0): string => {
  if (!value || depth > 6) return value;
  const cleaned = stripImportant(value);
  const match = cleaned.match(/^var\(\s*--([a-zA-Z0-9-_]+)\s*(?:,\s*(.+))?\)$/);
  if (!match) return cleaned;
  const fallback = match[2]?.trim();
  return vars[match[1]]
    ? resolveCssVarValue(vars[match[1]], vars, depth + 1)
    : fallback
      ? resolveCssVarValue(fallback, vars, depth + 1)
      : cleaned;
};

const readFromVarMap = (vars: Record<string, string>, keys: string[]) => {
  for (const key of keys) {
    const value = vars[key];
    if (value) return resolveCssVarValue(value, vars);
  }
  return '';
};

const extractThemePreviewPalette = (
  css: string,
  mode: ThemeMode,
  modeFallback: ThemePreviewPalette
): ThemePreviewPalette => {
  const rootVars = {
    ...parseCssVarsFromBlocks(css, ':root'),
    ...parseCssVarsFromBlocks(css, "[data-color-scheme='default']"),
  };
  const darkVars = {
    ...parseCssVarsFromBlocks(css, "[data-theme='dark']"),
    ...parseCssVarsFromBlocks(css, '[data-theme="dark"]'),
    ...parseCssVarsFromBlocks(css, '[data-theme=dark]'),
  };
  const activeVars = mode === 'dark' ? { ...rootVars, ...darkVars } : rootVars;
  const appBgRaw = readFromVarMap(activeVars, ['codex-canvas', 'bg-1', 'color-bg-1']);
  const panelBgRaw = readFromVarMap(activeVars, ['codex-sidebar', 'bg-2', 'color-bg-2', 'fill-1', 'color-fill-1']);
  const borderRaw = readFromVarMap(activeVars, ['codex-border', 'bg-3', 'color-border-2', 'border-base']);
  const accentRaw = readFromVarMap(activeVars, [
    'codex-accent',
    'conversation-stream-accent',
    'color-primary',
    'color-primary-base',
    'primary-6',
  ]);
  const textMutedRaw = readFromVarMap(activeVars, [
    'codex-text-tertiary',
    'color-text-3',
    'text-secondary',
    'color-text-2',
  ]);
  const aiBubbleRaw = readFromVarMap(activeVars, [
    'conversation-stream-surface',
    'codex-subtle',
    'color-fill-2',
    'fill-2',
    'bg-2',
    'color-bg-2',
  ]);
  const userBubbleRaw = readFromVarMap(activeVars, [
    'conversation-user-surface',
    'codex-selected',
    'color-primary-light-3',
    'color-primary-light-2',
    'color-primary',
  ]);

  return {
    appBg: normalizeColorLike(appBgRaw, modeFallback.appBg),
    headerBg: normalizeColorLike(panelBgRaw, modeFallback.headerBg),
    sideBg: normalizeColorLike(panelBgRaw, modeFallback.sideBg),
    mainBg: normalizeColorLike(appBgRaw, modeFallback.mainBg),
    border: normalizeColorLike(borderRaw, modeFallback.border),
    accent: normalizeColorLike(accentRaw, modeFallback.accent),
    textMuted: normalizeColorLike(textMutedRaw, modeFallback.textMuted),
    userBubble: normalizeColorLike(userBubbleRaw, modeFallback.userBubble),
    aiBubble: normalizeColorLike(aiBubbleRaw, modeFallback.aiBubble),
  };
};

const ThemeLayoutPreview: React.FC<{ palette: ThemePreviewPalette }> = ({ palette }) => (
  <div className='absolute inset-0 pointer-events-none' aria-hidden='true'>
    <div className='absolute inset-0' style={{ background: palette.appBg }} />
    <div
      className='absolute left-8px right-8px top-8px bottom-8px rounded-8px overflow-hidden border border-solid'
      style={{ borderColor: palette.border, background: palette.mainBg }}
    >
      <div
        className='h-14px border-b border-solid flex items-center px-6px gap-4px'
        style={{ borderColor: palette.border, background: palette.headerBg }}
      >
        <span className='block w-5px h-5px rounded-full' style={{ background: palette.accent, opacity: 0.9 }} />
        <span className='block w-18px h-4px rounded-full' style={{ background: palette.border, opacity: 0.45 }} />
        <span
          className='block w-12px h-4px rounded-full ml-auto'
          style={{ background: palette.border, opacity: 0.45 }}
        />
      </div>
      <div style={{ height: 'calc(100% - 14px)', display: 'flex' }}>
        <div
          className='border-r border-solid px-3px py-3px flex flex-col gap-3px'
          style={{ width: '23%', borderColor: palette.border, background: palette.sideBg }}
        >
          <span className='block h-3px rounded-full' style={{ background: palette.textMuted, opacity: 0.4 }} />
          <span className='block h-3px rounded-full w-4/5' style={{ background: palette.textMuted, opacity: 0.33 }} />
          <span className='block h-3px rounded-full w-3/5' style={{ background: palette.textMuted, opacity: 0.28 }} />
        </div>
        <div
          className='border-r border-solid px-4px py-4px flex flex-col gap-4px'
          style={{ width: '54%', borderColor: palette.border, background: palette.mainBg }}
        >
          <span className='block h-6px rounded-[6px] w-4/5' style={{ background: palette.aiBubble, opacity: 0.9 }} />
          <span
            className='block h-6px rounded-[6px] w-3/5 self-end'
            style={{ background: palette.userBubble, opacity: 0.95 }}
          />
          <span className='block h-6px rounded-[6px] w-2/3' style={{ background: palette.aiBubble, opacity: 0.82 }} />
        </div>
        <div className='px-3px py-3px flex flex-col gap-3px' style={{ width: '23%', background: palette.sideBg }}>
          <span className='block h-3px rounded-full' style={{ background: palette.textMuted, opacity: 0.36 }} />
          <span className='block h-3px rounded-full w-5/6' style={{ background: palette.textMuted, opacity: 0.3 }} />
        </div>
      </div>
    </div>
  </div>
);

const CssThemeSettings: React.FC = () => {
  const { t } = useTranslation();
  const { activeTheme, activeId, selectTheme } = useThemeContext();
  const themes = BUILTIN_THEMES;
  const selectedThemeId = activeId ?? activeTheme?.id ?? WARM_LIGHT_THEME_ID;
  const activeThemeFamily = getThemeFamilyFromId(selectedThemeId);
  const activeThemeMode = getThemeModeFromId(selectedThemeId);
  const resolvedThemeMode: ThemeMode = activeTheme?.appearance === 'dark' ? 'dark' : 'light';

  const themePreviewPalettes = useMemo(() => {
    const map = new Map<string, ThemePreviewPalette>();
    themes.forEach((cssTheme) => {
      const mode: ThemeMode = cssTheme.appearance === 'dark' ? 'dark' : 'light';
      const family = readThemeFamily(cssTheme);
      map.set(
        cssTheme.id,
        extractThemePreviewPalette(cssTheme.css || '', mode, fallbackThemePreviewPalettes[family][mode])
      );
    });
    return map;
  }, [themes]);

  const themeCards = useMemo(
    () =>
      THEME_FAMILIES.flatMap((family) => {
        const theme = themes.find((candidate) => candidate.id === getThemeId(family, resolvedThemeMode));
        return theme ? [{ family, theme }] : [];
      }),
    [resolvedThemeMode, themes]
  );

  const handleSelectTheme = useCallback(
    async (family: ThemeFamily) => {
      const familyName = getThemeFamilyLabel(family, t);
      try {
        await selectTheme(getThemeId(family, activeThemeMode));
        Message.success(t('settings.cssTheme.applied', { name: familyName }));
      } catch (error) {
        console.error('Failed to apply theme:', error);
        Message.error(t('settings.cssTheme.applyFailed'));
      }
    },
    [activeThemeMode, selectTheme, t]
  );

  return (
    <div className='space-y-12px' data-testid='css-theme-settings'>
      <div className='flex items-start md:items-center justify-between gap-8px flex-wrap'>
        <span className='text-14px text-t-secondary leading-22px'>{t('settings.cssTheme.selectOrCustomize')}</span>
      </div>

      <div
        className='grid w-full gap-12px'
        style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(min(180px, 100%), 1fr))' }}
        data-testid='css-theme-gallery'
      >
        {themeCards.map(({ family, theme }) => {
          const themeMode: ThemeMode = theme.appearance === 'dark' ? 'dark' : 'light';
          const previewPalette = themePreviewPalettes.get(theme.id) || fallbackThemePreviewPalettes[family][themeMode];
          const isActive = activeThemeFamily === family;
          const familyName = getThemeFamilyLabel(family, t);
          return (
            <button
              type='button'
              key={family}
              aria-pressed={isActive}
              aria-label={familyName}
              data-testid={`css-theme-card-${family}`}
              className='css-theme-card relative cursor-pointer appearance-none p-0 text-left rounded-12px overflow-hidden border h-112px w-full border-transparent'
              style={{
                backgroundColor: previewPalette.appBg,
                boxShadow: isActive ? '0 7px 20px rgb(0 0 0 / 11%)' : '0 3px 12px rgb(0 0 0 / 6%)',
              }}
              onClick={() => void handleSelectTheme(family)}
            >
              <ThemeLayoutPreview palette={previewPalette} />
              <div className='absolute bottom-0 left-0 right-0 h-1/3 bg-gradient-to-t from-black/60 to-transparent flex items-end justify-between p-8px'>
                <span className='text-13px text-white truncate flex-1'>{familyName}</span>
              </div>
              {isActive && (
                <div
                  className='absolute top-8px right-8px'
                  aria-label={t('settings.cssTheme.applied', { name: familyName })}
                >
                  <CheckOne theme='filled' size='20' fill='var(--color-primary)' />
                </div>
              )}
            </button>
          );
        })}
      </div>
    </div>
  );
};

export default CssThemeSettings;
