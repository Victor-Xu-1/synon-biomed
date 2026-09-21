/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const titlebarPath = fileURLToPath(
  new URL('../../../packages/desktop/src/renderer/components/layout/Titlebar/index.tsx', import.meta.url)
);
const layoutPath = fileURLToPath(
  new URL('../../../packages/desktop/src/renderer/components/layout/Layout.tsx', import.meta.url)
);
const zhCommonPath = fileURLToPath(
  new URL('../../../packages/desktop/src/renderer/services/i18n/locales/zh-CN/common.json', import.meta.url)
);
const enCommonPath = fileURLToPath(
  new URL('../../../packages/desktop/src/renderer/services/i18n/locales/en-US/common.json', import.meta.url)
);

/**
 * The titlebar renders a star shortcut right after the history-forward arrow
 * so users can find (and star) the open-source repository. These assertions
 * keep the wiring honest: the tooltip/aria label comes from i18n (both
 * locales), the click opens the canonical repo URL through the shared
 * external-link helper (never a raw window.open scattered in the chrome), and
 * the button sits next to the navigation arrows — not back in the sider
 * header it was moved away from.
 */
describe('github star badge next to the history-forward arrow', () => {
  it('renders the star button with tooltip label and external navigation', async () => {
    const titlebar = await readFile(titlebarPath, 'utf8');

    expect(titlebar).toContain("data-testid='github-star-button'");
    expect(titlebar).toContain('GITHUB_REPO_URL');
    expect(titlebar).toContain("'https://github.com/Victor-Xu-1/synon-biomed'");
    expect(titlebar).toContain('void openExternalUrl(GITHUB_REPO_URL)');
    expect(titlebar).toContain("aria-label={t('common.starOnGitHub')}");
    expect(titlebar).toContain("<Tooltip content={t('common.starOnGitHub')} position='bottom'>");
  });

  it('shares the icon spec with the navigation arrows for a uniform row', async () => {
    const titlebar = await readFile(titlebarPath, 'utf8');

    // Same @icon-park set, same size/stroke props, same optical-alignment class.
    expect(titlebar).toContain("<Star theme='outline' size={iconSize} fill='currentColor' strokeWidth={iconStroke} />");
    expect(titlebar).toContain("'app-titlebar__button app-titlebar__button--nav synon-biomed-github-star'");
    expect(titlebar).toContain('<GitHubStarButton iconSize={iconSize} iconStroke={desktopIconStroke} />');
  });

  it('sits directly after the history-forward button', async () => {
    const titlebar = await readFile(titlebarPath, 'utf8');

    const forwardIndex = titlebar.indexOf('navigationHistory?.forward()');
    const starIndex = titlebar.indexOf('<GitHubStarButton iconSize={iconSize}');
    expect(forwardIndex).toBeGreaterThan(-1);
    expect(starIndex).toBeGreaterThan(forwardIndex);
  });

  it('lives in the titlebar only — the sider header must stay star-free', async () => {
    const layout = await readFile(layoutPath, 'utf8');

    expect(layout).not.toContain('github-star-button');
    expect(layout).not.toContain('GITHUB_REPO_URL');
  });

  it('declares the starOnGitHub label in every supported locale', async () => {
    const zh = JSON.parse(await readFile(zhCommonPath, 'utf8')) as Record<string, string>;
    const en = JSON.parse(await readFile(enCommonPath, 'utf8')) as Record<string, string>;

    expect(zh.starOnGitHub).toBe('去 GitHub 收藏');
    expect(en.starOnGitHub).toBe('Star on GitHub');
  });
});
