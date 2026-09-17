/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { configService } from '@/common/config/configService';
import { getThemeFamilyFromId, isSystemThemeId } from '@/common/theme/constants';
import { resolveActiveTheme } from '@/common/theme/resolveTheme';
import type { Theme } from '@/common/theme/types';
import { BUILTIN_THEMES } from '@renderer/theme/builtinThemes';
import { processCustomCss } from './customCssProcessor';
import { getSystemPrefersDark } from './systemAppearance';

const TOKENS_STYLE_ID = 'theme-tokens';
const DECORATION_STYLE_ID = 'theme-decoration';

function upsertStyle(id: string, css: string | null, root: Document = document): void {
  const existing = root.getElementById(id);
  if (!css) {
    existing?.remove();
    return;
  }
  const element = (existing as HTMLStyleElement | null) ?? root.createElement('style');
  element.id = id;
  element.textContent = css;
  root.head.appendChild(element);
}

function tokensToCss(tokens?: Record<string, string>): string | null {
  if (!tokens || Object.keys(tokens).length === 0) return null;
  const body = Object.entries(tokens)
    .map(([key, value]) => `  ${key}: ${value};`)
    .join('\n');
  return `:root {\n${body}\n}`;
}

/** Apply a resolved theme to every app-chrome surface. */
export function applyTheme(theme: Theme, root: Document = document): void {
  root.documentElement.setAttribute('data-theme', theme.appearance);
  root.documentElement.setAttribute('data-theme-family', getThemeFamilyFromId(theme.id));
  root.body?.setAttribute('arco-theme', theme.appearance);
  upsertStyle(TOKENS_STYLE_ID, tokensToCss(theme.tokens), root);
  upsertStyle(DECORATION_STYLE_ID, theme.css ? processCustomCss(theme.css) : null, root);
}

/** Resolve, apply, persist, and publish a concrete or family-specific system selection. */
export async function setActiveTheme(activeId: string): Promise<void> {
  const resolved = resolveActiveTheme(activeId, BUILTIN_THEMES, getSystemPrefersDark());
  const canonicalId = isSystemThemeId(activeId) ? activeId : resolved.id;
  applyTheme(resolved);
  await configService.set('theme.activeId', canonicalId);
  await ipcBridge.theme.setActive.invoke(resolved);
}
