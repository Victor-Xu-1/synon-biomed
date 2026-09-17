import { describe, expect, it } from 'vitest';
import { compactSettingsDescription } from '@/renderer/pages/settings/components/settingsPresentation';

describe('settings presentation', () => {
  it('normalizes whitespace and keeps only the first concise sentence', () => {
    expect(compactSettingsDescription('  First   sentence. Second sentence.  ')).toBe('First sentence.');
  });

  it('removes a repeated item name without losing the actual description', () => {
    expect(
      compactSettingsDescription('OPERON: General research agent with tools.', {
        stripPrefixes: ['OPERON'],
      })
    ).toBe('General research agent with tools.');
  });

  it('truncates by Unicode characters and exposes a clear continuation marker', () => {
    expect(compactSettingsDescription('生物医学研究与药物开发工作流', { maxLength: 6 })).toBe('生物医学研究…');
  });
});
