/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { Theme } from './types';
import { getThemeFamilyFromId, getThemeId, isSystemThemeId, WARM_LIGHT_THEME_ID } from './constants';

/** Resolve a persisted selection without coupling visual family to OS appearance. */
export function resolveActiveTheme(activeId: string, themes: Theme[], prefersDark?: boolean): Theme {
  const targetId = isSystemThemeId(activeId)
    ? getThemeId(getThemeFamilyFromId(activeId), prefersDark ? 'dark' : 'light')
    : activeId;
  const resolved =
    themes.find((theme) => theme.id === targetId) ??
    themes.find((theme) => theme.id === WARM_LIGHT_THEME_ID) ??
    themes[0];
  if (!resolved) throw new Error('Unable to resolve theme: no themes are registered');
  return resolved;
}
