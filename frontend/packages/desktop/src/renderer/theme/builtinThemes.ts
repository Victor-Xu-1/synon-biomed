/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { Theme } from '@/common/theme/types';
import {
  COOL_DARK_THEME_ID,
  COOL_LIGHT_THEME_ID,
  NIGHT_DARK_THEME_ID,
  WHITE_LIGHT_THEME_ID,
  WARM_DARK_THEME_ID,
  WARM_LIGHT_THEME_ID,
} from '@/common/theme/constants';
import warmCss from '@renderer/pages/settings/AppearanceSettings/presets/warm.css?raw';
import coolCss from '@renderer/pages/settings/AppearanceSettings/presets/cool.css?raw';
import whiteCss from '@renderer/pages/settings/AppearanceSettings/presets/white.css?raw';

const T0 = 0;

const builtin = (id: string, name: string, appearance: Theme['appearance'], css: string): Theme => ({
  id,
  name,
  appearance,
  css,
  builtin: true,
  created_at: T0,
  updated_at: T0,
});

export const BUILTIN_THEMES: Theme[] = [
  builtin(WARM_LIGHT_THEME_ID, 'Warm', 'light', warmCss),
  builtin(WARM_DARK_THEME_ID, 'Warm Dark', 'dark', warmCss),
  builtin(COOL_LIGHT_THEME_ID, 'Cool', 'light', coolCss),
  builtin(COOL_DARK_THEME_ID, 'Cool Dark', 'dark', coolCss),
  builtin(WHITE_LIGHT_THEME_ID, 'Pure White', 'light', whiteCss),
  builtin(NIGHT_DARK_THEME_ID, 'Night', 'dark', whiteCss),
];

export const BUILTIN_THEME_IDS = new Set(BUILTIN_THEMES.map((theme) => theme.id));
