import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

const titlebarSourcePath = fileURLToPath(
  new URL('../../../../packages/desktop/src/renderer/components/layout/Titlebar/index.tsx', import.meta.url)
);
const titlebarCssPath = fileURLToPath(
  new URL('../../../../packages/desktop/src/renderer/components/layout/Titlebar/titlebar.css', import.meta.url)
);

describe('titlebar visual contract', () => {
  it('keeps the workspace toggle on the shared titlebar action surface', () => {
    const source = readFileSync(titlebarSourcePath, 'utf8');

    expect(source).toContain("'app-titlebar__button app-titlebar__button--workspace'");
    expect(source).toContain("data-testid='workspace-toggle'");
  });

  it('centers the workspace icon wrapper and its SVG instead of using text baseline alignment', () => {
    const css = readFileSync(titlebarCssPath, 'utf8');

    expect(css).toMatch(
      /\.app-titlebar__button--workspace > \.i-icon\s*\{[^}]*display:\s*inline-flex[^}]*width:\s*18px[^}]*height:\s*18px[^}]*align-items:\s*center[^}]*justify-content:\s*center[^}]*line-height:\s*0[^}]*vertical-align:\s*middle/s
    );
    expect(css).toMatch(
      /\.app-titlebar__button--workspace > \.i-icon svg\s*\{[^}]*display:\s*block[^}]*width:\s*18px[^}]*height:\s*18px[^}]*vertical-align:\s*middle/s
    );
  });
});
