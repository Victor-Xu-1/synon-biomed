import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const css = readFileSync(
  'packages/desktop/src/renderer/pages/conversation/Messages/components/MessageToolGroupSummary.css',
  'utf8'
);
const prose = readFileSync('packages/desktop/src/renderer/pages/conversation/Messages/messages.css', 'utf8');
const shell = readFileSync('packages/desktop/src/renderer/styles/workspace-theme.css', 'utf8');
const rule = (selector: string) =>
  css.match(new RegExp(`${selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} \\{([^}]+)\\}`))?.[1] ?? '';

describe('transcript typography authority', () => {
  it('keeps prose typography in the message layer rather than competing shell overrides', () => {
    expect(shell).not.toContain('.assistant-transcript-text');
    expect(prose).toContain('--chat-heading-2-font-size');
  });
  it('has one value rule and a readable shared sans serif hierarchy', () => {
    expect(css.match(/^\.tool-public-detail__value \{/gm)).toHaveLength(1);
    expect(rule('.tool-public-detail__value')).toContain('font-family: inherit');
    expect(rule('.tool-public-detail__identity-value')).toContain('font-size: 12px');
    expect(rule('.tool-public-detail__block-header')).toContain('font-size: 12px');
    expect(rule('.tool-public-detail__narrative')).toContain('font-family: inherit');
  });
  it('keeps code readable without shrinking deeper disclosures', () => {
    expect(rule('.tool-public-detail__block-content')).toContain('font-size: 12.5px');
    expect(rule('.tool-public-detail__block-content')).toContain('var(--tool-stream-font-mono)');
    expect(rule('.tool-detail-tree__count')).toContain('font-size: 12px');
  });
  it('derives colors from the current theme and retains explicit keyboard focus', () => {
    expect(css).toContain('--tool-stream-text-muted: var(--workspace-text-secondary)');
    expect(css).toContain('--tool-detail-inset: var(--workspace-subtle)');
    expect(css).toContain('.tool-public-detail__block-trigger:focus-visible');
    expect(css).toContain('@media (prefers-reduced-motion: reduce)');
  });
});
