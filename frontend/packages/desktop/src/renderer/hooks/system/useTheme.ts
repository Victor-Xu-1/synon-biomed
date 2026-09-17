/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { configService } from '@/common/config/configService';
import { getThemeFamilyFromId, isSystemThemeId, WARM_LIGHT_THEME_ID } from '@/common/theme/constants';
import { resolveActiveTheme } from '@/common/theme/resolveTheme';
import type { Theme } from '@/common/theme/types';
import { applyTheme, setActiveTheme } from '@/renderer/utils/theme/applyTheme';
import { getSystemPrefersDark } from '@/renderer/utils/theme/systemAppearance';
import { startSystemThemeWatcher } from '@/renderer/utils/theme/systemThemeWatcher';
import { BUILTIN_THEMES } from '@renderer/theme/builtinThemes';
import { useCallback, useEffect, useState } from 'react';

const APPEARANCE_CACHE_KEY = '__synon-ai_theme';
const FAMILY_CACHE_KEY = '__synon-ai_theme_family';

function persistThemeBootCache(theme: Theme): void {
  try {
    localStorage.setItem(APPEARANCE_CACHE_KEY, theme.appearance);
    localStorage.setItem(FAMILY_CACHE_KEY, getThemeFamilyFromId(theme.id));
  } catch {
    /* Storage can be unavailable in restricted webviews. */
  }
}

function getPersistedActiveId(): string {
  return (configService.get('theme.activeId') as string) || WARM_LIGHT_THEME_ID;
}

async function initActiveTheme(): Promise<Theme> {
  try {
    await configService.whenReady();
    const activeId = getPersistedActiveId();
    const resolved = resolveActiveTheme(activeId, BUILTIN_THEMES, getSystemPrefersDark());
    const canonicalId = isSystemThemeId(activeId) ? activeId : resolved.id;
    if (canonicalId !== activeId) await configService.set('theme.activeId', canonicalId);
    applyTheme(resolved);
    persistThemeBootCache(resolved);
    void ipcBridge.theme.setActive.invoke(resolved).catch(() => {});
    return resolved;
  } catch (error) {
    console.error('init theme failed', error);
    const fallback = resolveActiveTheme(WARM_LIGHT_THEME_ID, BUILTIN_THEMES);
    applyTheme(fallback);
    persistThemeBootCache(fallback);
    return fallback;
  }
}

let initialPromise: Promise<Theme> | null = null;
if (typeof window !== 'undefined') initialPromise = initActiveTheme();

/** Returns the resolved theme, selection callback, and persisted selection id. */
const useTheme = (): [Theme | null, (activeId: string) => Promise<void>, string | null] => {
  const [active, setActive] = useState<Theme | null>(null);
  const [activeId, setActiveId] = useState<string | null>(null);

  useEffect(() => {
    let mounted = true;
    initialPromise
      ?.then((theme) => {
        if (!mounted) return;
        setActive(theme);
        setActiveId(getPersistedActiveId());
      })
      .catch((error) => console.error('init theme failed', error));
    const off = ipcBridge.theme.changed.on((theme: Theme) => {
      applyTheme(theme);
      if (mounted) {
        setActive((previous) => (previous?.id === theme.id ? previous : theme));
        setActiveId((configService.get('theme.activeId') as string) || theme.id);
      }
      persistThemeBootCache(theme);
    });
    const offSystemWatch = startSystemThemeWatcher();
    return () => {
      mounted = false;
      off?.();
      offSystemWatch();
    };
  }, []);

  const select = useCallback(async (themeId: string) => {
    const resolved = resolveActiveTheme(themeId, BUILTIN_THEMES, getSystemPrefersDark());
    const canonicalId = isSystemThemeId(themeId) ? themeId : resolved.id;
    setActive(resolved);
    setActiveId(canonicalId);
    persistThemeBootCache(resolved);
    await setActiveTheme(canonicalId);
  }, []);

  return [active, select, activeId];
};

export default useTheme;
