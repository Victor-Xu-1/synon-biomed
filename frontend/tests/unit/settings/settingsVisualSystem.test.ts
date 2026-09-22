import { readFileSync, readdirSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { fileURLToPath } from 'node:url';

/**
 * Settings used to carry a route-level density file that shrank three pages
 * into a second visual language (24px titles, 32px controls, 9–11px copy) and
 * four colour families. This contract keeps one ramp, one accent and one
 * container rhythm across every settings module.
 */
const settingsDir = fileURLToPath(new URL('../../../packages/desktop/src/renderer/pages/settings/', import.meta.url));
const coreCss = readFileSync(path.join(settingsDir, 'components/settings-core.css'), 'utf8');
const densityCss = readFileSync(path.join(settingsDir, 'components/settings-card-density.css'), 'utf8');

const collectCss = (dir: string): string[] =>
  readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const target = path.join(dir, entry.name);
    if (entry.isDirectory()) return collectCss(target);
    return entry.name.endsWith('.css') ? [target] : [];
  });

// The account activity chart keeps its own SVG-scale labels.
const EXEMPT = /AccountActivityChart\.css$/;
const moduleCss = collectCss(settingsDir)
  .filter((file) => !EXEMPT.test(file))
  .map((file) => [path.relative(settingsDir, file), readFileSync(file, 'utf8')] as const);

describe('settings visual system', () => {
  it('covers every settings stylesheet', () => {
    expect(moduleCss.length).toBeGreaterThan(20);
  });

  it('owns one type ramp, one control height and one container rhythm', () => {
    for (const declaration of [
      '--settings-control-height: var(--ui-control-lg)',
      '--settings-card-radius: var(--ui-radius-xl)',
      '--settings-card-gap: var(--ui-space-6)',
      '--settings-section-gap: var(--ui-space-7)',
      '--settings-entity-card-height: 236px',
      '--settings-page-title-size: 26px',
      '--settings-page-title-line: 34px',
      '--settings-page-description-size: var(--ui-font-subtitle)',
      '--settings-section-title-size: var(--ui-font-section)',
      '--settings-card-title-size: var(--ui-font-title)',
      '--settings-meta-size: var(--ui-font-meta)',
      '--settings-module-accent: var(--workspace-accent)',
      '--settings-section-padding: 20px',
      '--settings-content-inline: clamp(24px, 2.4vw, 38px)',
    ]) {
      expect(coreCss, declaration).toContain(declaration);
    }
  });

  it('routes the page header and section titles through those tokens', () => {
    expect(coreCss).toMatch(
      /\.settings-page-header__title\s*\{[^}]*font-size:\s*var\(--settings-page-title-size\)\s*!important/
    );
    expect(coreCss).toMatch(
      /\.settings-page-header__description\s*\{[^}]*font-size:\s*var\(--settings-page-description-size\)\s*!important/
    );
    expect(coreCss).toMatch(/settings-page-body h2\s*\{[^}]*font-size:\s*var\(--settings-section-title-size\)/);
  });

  it('no longer shrinks three routes into a second hierarchy', () => {
    expect(densityCss).not.toMatch(/font-size:\s*(?:24|18)px/);
    expect(densityCss).not.toMatch(/font-size:\s*var\(--ui-font-(?:micro|hero)\)\s*!important/);
    expect(densityCss).not.toMatch(/--settings-control-height:\s*\d+px/);
    expect(densityCss).not.toMatch(/--settings-card-gap:\s*\d+px/);
    expect(densityCss).not.toMatch(/--settings-section-gap:\s*var\(--ui-space-[1-4]\)/);
    expect(densityCss).toMatch(/font-size:\s*var\(--settings-page-title-size\)/);
    expect(densityCss).toMatch(/font-size:\s*var\(--settings-page-description-size\)/);
  });

  it('keeps one accent and neutral status colours', () => {
    const all = moduleCss.map(([, css]) => css).join('\n');
    for (const hex of [
      '#22b85a',
      '#ff9800',
      '#39a4da',
      '#147cf2',
      '#159cc8',
      '#07969b',
      '#28a455',
      '#d44d82',
      '#7651c9',
    ]) {
      expect(all).not.toContain(hex);
    }
    expect(all).toContain('--mcp-accent: var(--settings-module-accent)');
    expect(all).toContain('--storage-category: var(--settings-module-accent)');
  });

  it('keeps one card grid and one metadata ramp', () => {
    const all = moduleCss.map(([, css]) => css).join('\n');
    expect(all).not.toMatch(/grid-template-columns:\s*repeat\((?:4|5),\s*minmax\(0,\s*1fr\)\)/);
    expect(all).not.toMatch(/font-size:\s*(?:8|9|10|11|18)px/);
  });
});

describe.each(moduleCss)('%s', (_name, css) => {
  it('declares type through the settings ramp', () => {
    for (const match of css.matchAll(/font-size:\s*([0-9]+px)/g)) {
      throw new Error(`raw font-size ${match[1]} - use a --settings-* or --ui-* type token`);
    }
  });
});
