/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

const previewStylesPath = fileURLToPath(
  new URL(
    '../../../packages/desktop/src/renderer/pages/conversation/Preview/components/PreviewPanel/preview.css',
    import.meta.url
  )
);

describe('file board layout styles', () => {
  it('keeps the selected column count as the only grid column authority', async () => {
    const styles = await readFile(previewStylesPath, 'utf8');
    const boardGrid = styles.match(/^\.preview-board__grid\s*\{([^}]*)\}/ms)?.[1] ?? '';
    const boardColumnDeclarations = [...styles.matchAll(/grid-template-columns\s*:/g)];

    expect(boardGrid).toContain('repeat(var(--preview-board-columns, 2), minmax(0, 1fr))');
    expect(boardColumnDeclarations).toHaveLength(1);
  });
});
