import { readFileSync as readFileRaw } from 'node:fs';
import { resolveDesignTokens } from '../../_helpers/designTokens';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

/** Stylesheets are read with primitives inlined; contracts keep pinning numbers. */
const readFileSync = (target: string | URL, encoding?: BufferEncoding): string =>
  `${target}`.endsWith('.css')
    ? resolveDesignTokens(readFileRaw(target, 'utf8'))
    : (readFileRaw(target, encoding) as unknown as string);

const source = (relative: string): string =>
  readFileSync(fileURLToPath(new URL(`../../../../packages/desktop/src/${relative}`, import.meta.url)), 'utf8');

describe('Synon tool group visual contract', () => {
  it('keeps all tool-group geometry and motion in the component stylesheet', () => {
    const css = source('renderer/pages/conversation/Messages/components/MessageToolGroupSummary.css');
    const planCss = source('renderer/components/synonBiomed/runtime/SynonBiomedPlanReviewContent.css');
    expect(css).toContain('font-family: var(--conversation-font-sans, system-ui);');
    expect(css).not.toContain('.tool-stream-phase__narrative');
    expect(css).not.toContain('.tool-group-summary--expanded > .tool-group-summary__header');
    expect(css).toContain('box-sizing: border-box;');
    expect(css).toContain('.tool-group-summary--single');
    expect(css).toMatch(
      /\.tool-group-summary--single\s*\{[^}]*border-radius:\s*12px;[^}]*background:\s*var\(--tool-stream-bg\);[^}]*padding:\s*4px 6px;/s
    );
    expect(css).toContain('.tool-research-sources__query');
    expect(css).toContain('.tool-research-source__snippet');
    expect(css).toContain('max-height: 430px;');
    expect(css).toContain('.tool-public-detail__rows');
    expect(css).toContain('.tool-public-detail__row');
    expect(css).toContain('grid-template-columns: minmax(88px, 0.28fr) minmax(0, 1fr);');
    expect(css).toContain('.tool-public-detail__result-trigger');
    expect(css).toContain('.tool-public-detail__surface--input');
    expect(css).toContain('.tool-public-detail__surface--output');
    expect(css).toContain('.tool-public-detail__result-summary');
    expect(css).toContain('.tool-public-detail__collection-trigger');
    expect(css).toContain('.tool-public-detail__collection-item');
    expect(css).toContain('overflow-wrap: anywhere;');
    expect(css).toContain('max-height: 256px;');
    expect(css).toContain('--tool-detail-inset: var(--workspace-subtle);');
    expect(css).toContain('background: var(--tool-detail-inset);');
    expect(css).toMatch(/--tool-stream-font-mono:\s*[^;]+, monospace;/);
    expect(css).toContain('font-family: var(--tool-stream-font-mono);');
    expect(css).toContain('font-size: 12.5px;');
    expect(css).toMatch(
      /\.tool-detail-panel\s*\{[^}]*padding:\s*14px 16px;[^}]*border:\s*1px solid var\(--tool-stream-border\);[^}]*background:\s*var\(--tool-detail-surface\);/s
    );
    expect(css).toContain('0 1px 2px rgb(0 0 0 / 6%)');
    expect(css).toContain('box-shadow: 0 1px 3px rgb(0 0 0 / 4%);');
    expect(css).toContain('min-height: 31px;');
    expect(css).toContain('padding: 5px 10px 5px 6px;');
    expect(css).toMatch(
      /\.tool-group-summary\s*\{[^}]*border-radius:\s*12px;[^}]*background:\s*var\(--tool-stream-bg\);[^}]*padding:\s*4px 6px;/s
    );
    expect(css).toMatch(
      /\.tool-group-summary__header\s*\{[^}]*padding:\s*5px 10px 5px 6px;[^}]*border-radius:\s*8px;[^}]*background:\s*transparent;/s
    );
    expect(css).toMatch(
      /\.tool-step\s*\{[^}]*margin:\s*0;[^}]*border-radius:\s*8px;[^}]*background:\s*transparent;[^}]*padding:\s*0;/s
    );
    expect(css).toMatch(/\.tool-step-row__primary\s*\{[^}]*font-size:\s*13px;[^}]*line-height:\s*22px;/s);
    expect(css).not.toContain('.tool-activity-section');
    expect(css).not.toContain('.tool-step--expanded > .tool-step-row');
    expect(css).not.toContain('.tool-public-detail__plan-step-description');
    expect(planCss).toContain('.synon-biomed-plan-review__step-description');
    expect(planCss).toContain('.synon-biomed-plan-review__step-status--in_progress');
    expect(css).not.toContain('.tool-public-detail__plan-step-chevron');
    expect(css).toContain('background-color 150ms cubic-bezier(0.4, 0, 0.2, 1)');
    expect(css).toContain('grid-template-rows 200ms cubic-bezier(0.165, 0.84, 0.44, 1)');
    expect(css).toContain('.tool-group-summary__disclosure-icon--expanded');
    expect(css).toContain('transform: rotate(90deg);');
    expect(css).toContain('rgb(205 226 251) 0 0 6px 1px');
    expect(css).not.toContain('transition:\n    grid-template-rows 200ms ease-out,\n    opacity');
  });

  it('does not let the global theme compete with component geometry', () => {
    const globalTheme = source('renderer/styles/workspace-theme.css');
    for (const selector of ['tool-group-summary', 'tool-step-row', 'tool-detail-content']) {
      expect(globalTheme).not.toContain(selector);
    }
  });

  it('keeps thinking expansion free of opacity flicker', () => {
    const css = source('renderer/pages/conversation/Messages/components/MessageThinking.module.css');
    expect(css).toContain('grid-template-rows 300ms cubic-bezier(0.165, 0.84, 0.44, 1)');
    expect(css).toContain('background: rgb(101 84 233 / 5%);');
    expect(css).not.toContain('opacity 220ms');
  });
});
