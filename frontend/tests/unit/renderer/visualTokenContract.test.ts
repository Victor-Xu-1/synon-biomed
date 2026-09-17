import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';

const rendererRoot = join(process.cwd(), 'packages/desktop/src/renderer');

function sourceFiles(root: string): string[] {
  return readdirSync(root, { withFileTypes: true }).flatMap((entry) => {
    const path = join(root, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(?:tsx?|css)$/.test(entry.name) ? [path] : [];
  });
}

describe('visual token contract', () => {
  it('does not ship drifted background or border utility names', () => {
    const invalidPatterns = [
      /\bbg-bg-\d+\b/,
      /\bborder-border-\d+\b/,
      /\bdivide-border-\d+\b/,
      /\bbg-border-\d+\b/,
      /\bbg-(?=\s|,|\/|\))/,
      /\bborder-arco-(?=\s|,|\/|\))/,
      /divide-\[var\(--color-border-\)\]/,
    ];
    const violations = sourceFiles(rendererRoot).flatMap((path) => {
      const source = readFileSync(path, 'utf8');
      return invalidPatterns
        .filter((pattern) => pattern.test(source))
        .map((pattern) => relative(rendererRoot, path) + ': ' + pattern.source);
    });

    expect(violations).toEqual([]);
  });

  it('keeps preview modules on the shared surface contract', () => {
    const previewCss = readFileSync(
      join(rendererRoot, 'pages/conversation/Preview/components/PreviewPanel/preview.css'),
      'utf8'
    );
    const moleculeViewer = readFileSync(
      join(rendererRoot, 'pages/conversation/Preview/components/viewers/SynonBiomedMoleculeViewer.tsx'),
      'utf8'
    );

    expect(previewCss).toContain('--preview-canvas: var(--synon-surface-canvas');
    expect(previewCss).toContain('--preview-border: var(--synon-divider');
    expect(moleculeViewer).toContain('border-arco-2');
    expect(moleculeViewer).toContain('bg-1');
    expect(moleculeViewer).not.toContain('bg-bg-');
  });
});
