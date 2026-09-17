import fs from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { BUILTIN_THEMES, BUILTIN_THEME_IDS } from '@/renderer/theme/builtinThemes';

const presetDir = path.resolve(
  process.cwd(),
  'packages/desktop/src/renderer/pages/settings/AppearanceSettings/presets'
);

describe('builtin theme catalog', () => {
  it('exposes the warm, cool, pure-white, and night palettes', () => {
    expect(BUILTIN_THEMES.map(({ id, name, appearance }) => ({ id, name, appearance }))).toEqual([
      { id: 'light', name: 'Warm', appearance: 'light' },
      { id: 'claude-dark', name: 'Warm Dark', appearance: 'dark' },
      { id: 'codex-light', name: 'Cool', appearance: 'light' },
      { id: 'dark', name: 'Cool Dark', appearance: 'dark' },
      { id: 'pure-white', name: 'Pure White', appearance: 'light' },
      { id: 'night', name: 'Night', appearance: 'dark' },
    ]);
    expect([...BUILTIN_THEME_IDS]).toEqual(['light', 'claude-dark', 'codex-light', 'dark', 'pure-white', 'night']);
  });

  it('keeps both palettes on the shared conversation and composer contract', () => {
    for (const filename of ['warm.css', 'cool.css', 'white.css']) {
      const css = fs.readFileSync(path.join(presetDir, filename), 'utf8');
      expect(css).toContain('--conversation-user-surface');
      expect(css).toContain('--conversation-stream-surface');
      expect(css).toContain('--composer-surface');
      expect(css).toContain('--workspace-overlay-surface');
      expect(css).toContain('--workspace-dialog-shadow');
    }
  });

  it('uses a warm neutral palette instead of the retired blue and orange accents', () => {
    const css = fs.readFileSync(path.join(presetDir, 'warm.css'), 'utf8');

    expect(css).toContain('--workspace-accent: #3b3a37');
    expect(css).toContain('--conversation-stream-accent: #6f6b65');
    expect(css).not.toMatch(/#(?:a9d7e5|173b48|5f9caf|cc785c|b9684d|e39a79|f2e5df|4a342b)\b/i);
    expect(css).not.toMatch(/medical blue|terracotta|orange/i);

    for (const match of css.matchAll(/#[0-9a-f]{6}\b/gi)) {
      const channels = [1, 3, 5].map((offset) => Number.parseInt(match[0].slice(offset, offset + 2), 16));
      expect(Math.max(...channels) - Math.min(...channels)).toBeLessThanOrEqual(60);
    }
  });
});
