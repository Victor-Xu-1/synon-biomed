/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

/**
 * One settings entry for the four merged library routes.
 *
 * Experts, skills, connectors and scientific environments render the one merged
 * catalog page, so the sider (desktop row list and mobile top nav alike) shows a
 * single "科学工具集" row. The four routes themselves are not retired: a deep
 * link to `#/settings/skills` (or tools/environments) must still open the merged
 * page on its own tab, and whichever of the four the hash names must keep the
 * one row lit. This contract pins all three halves of that arrangement.
 */

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import { resolveSiderEntryId } from '@/renderer/pages/settings/settingsNavigation';
import { isSettingsRouteId, type SettingsRouteId } from '@/renderer/pages/settings/settingsRouteLoaders';

const settingsDir = fileURLToPath(new URL('../../../packages/desktop/src/renderer/pages/settings/', import.meta.url));
const read = (relative: string): string => readFileSync(path.join(settingsDir, relative), 'utf8');
const readLocale = (language: string): Record<string, unknown> =>
  JSON.parse(
    readFileSync(
      path.join(settingsDir, `../services/i18n/locales/${language}/settings.json`),
      'utf8'
    )
  ) as Record<string, unknown>;

const siderSource = read('components/SettingsSider.tsx');
const wrapperSource = read('components/SettingsPageWrapper.tsx');
const routeLoadersSource = read('settingsRouteLoaders.ts');
const libraryPageSource = read('LibrarySettingsPage.tsx');
const i18nKeys = readFileSync(path.join(settingsDir, '../services/i18n/i18n-keys.d.ts'), 'utf8');

const LIBRARY_ROUTES = ['experts', 'skills', 'tools', 'environments'] as const;
const UNCHANGED_ROUTES = [
  'models',
  'compute',
  'governance',
  'network',
  'credentials',
  'storage',
  'general',
] as const;

const countOccurrences = (source: string, marker: string): number => source.split(marker).length - 1;

describe('merged library sidebar entry', () => {
  it('resolves all four library routes to the one scientific toolkit entry', () => {
    for (const route of LIBRARY_ROUTES) {
      expect(resolveSiderEntryId(route), route).toBe('experts');
    }
    for (const route of UNCHANGED_ROUTES) {
      expect(resolveSiderEntryId(route as SettingsRouteId), route).toBe(route);
    }
  });

  it('keeps the four legacy routes loadable and pointed at the merged page', () => {
    for (const route of LIBRARY_ROUTES) {
      expect(isSettingsRouteId(route), route).toBe(true);
      expect(routeLoadersSource, route).toMatch(
        new RegExp(`${route}: cachedLoader\\(\\(\\) => import\\('\\./LibrarySettingsPage'\\)\\)`)
      );
    }
    // Every library route still preselects its own tab on the merged page.
    for (const route of LIBRARY_ROUTES) {
      expect(libraryPageSource, route).toMatch(new RegExp(`value === '${route}'`));
    }
  });

  it('exposes exactly one row, labelled by the new i18n key, with the toolkit glyph', () => {
    const builtinIds = /export const BUILTIN_TAB_IDS = \[([\s\S]*?)\] as const;/.exec(siderSource)?.[1] ?? '';
    expect(builtinIds).toContain("'experts'");
    for (const route of UNCHANGED_ROUTES) {
      expect(builtinIds, route).toContain(`'${route}'`);
    }
    for (const folded of ['skills', 'tools', 'environments'] as const) {
      expect(builtinIds, folded).not.toContain(`'${folded}'`);
    }

    const row = /id: 'experts',[\s\S]*?label: t\('settings\.scientificToolkit'\),[\s\S]*?icon: <SettingsGeneratedNavIcon id='tools' \/>[\s\S]*?path: 'experts'/;
    expect(siderSource).toMatch(row);
    expect(wrapperSource).toMatch(row);
    // The mobile top nav reuses the desktop row list, so it collapses too.
    expect(wrapperSource).toContain('BUILTIN_TAB_IDS.map');
    // One label lookup per navigation surface, and the folded routes keep the
    // single row lit through the shared resolver.
    expect(countOccurrences(siderSource, "t('settings.scientificToolkit')")).toBe(1);
    expect(countOccurrences(wrapperSource, "t('settings.scientificToolkit')")).toBe(1);
    expect(siderSource).toMatch(/resolveSiderEntryId\(activeRoute\) === item\.id/);
    expect(wrapperSource).toMatch(/resolveSiderEntryId\(activeRoute\) === item\.id/);
  });

  it('ships the label in both locales and in the generated key declarations', () => {
    expect(readLocale('zh-CN').scientificToolkit).toBe('科学工具集');
    expect(readLocale('en-US').scientificToolkit).toBe('Scientific Toolkit');
    expect(i18nKeys).toContain("'settings.scientificToolkit'");
  });
});
