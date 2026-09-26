/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { readFile as readFileRaw } from 'node:fs/promises';
import { resolveDesignTokens } from '../_helpers/designTokens';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const stylesPath = fileURLToPath(
  new URL('../../../packages/desktop/src/renderer/pages/settings/MessageChannelsSettings.css', import.meta.url)
);

const generalSkin = "html body .settings-page-wrapper[data-settings-route='general']";

function ruleBody(styles: string, selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return styles.match(new RegExp(`${escaped}\\s*\\{([^}]*)\\}`, 's'))?.[1] ?? '';
}

/**
 * The General sheet stacks the two card halves instead of laying them out side
 * by side: the header is an absolutely positioned overlay over the whole left
 * half, the body is an absolutely positioned layer underneath it, and the
 * action footer is positioned inside that same left half. Because the header
 * sits above the body it used to absorb every pointer event aimed at the
 * footer, which made scan / cancel / unpair visible but unclickable for mouse
 * users. `pointer-events: none` is what keeps those buttons reachable; these
 * assertions exist so the guard cannot be dropped silently again.
 */
describe('message channel card hit targets', () => {
  it('keeps the absolutely positioned card header out of hit testing', async () => {
    const styles = resolveDesignTokens(await readFileRaw(stylesPath, 'utf8'));
    const header = ruleBody(styles, `${generalSkin} .message-channel-card__header`);

    expect(header).toContain('position: absolute;');
    expect(header).toContain('pointer-events: none;');
  });

  it('still overlays the footer with that header, so the guard stays required', async () => {
    const styles = resolveDesignTokens(await readFileRaw(stylesPath, 'utf8'));
    const header = ruleBody(styles, `${generalSkin} .message-channel-card__header`);
    const body = ruleBody(styles, `${generalSkin} .message-channel-card__body`);
    const footer = ruleBody(styles, `${generalSkin} .message-channel-card__footer`);

    // The overlay spans the full height of the left half…
    expect(header).toContain('top: 0;');
    expect(header).toContain('bottom: 0;');
    expect(header).toContain('left: 0;');
    expect(header).toContain('width: 50%;');
    // …and the footer is positioned inside that half.
    expect(footer).toContain('position: absolute;');
    expect(footer).toContain('left: 24px;');
    expect(footer).toContain('bottom: 20px;');
    // The header keeps painting above the body; the fix must not invert them.
    expect(header).toContain('z-index: 2;');
    expect(body).toContain('z-index: 1;');
  });
});
