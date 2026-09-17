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

describe('image preview canvas styles', () => {
  it('keeps the image canvas solid white without a decorative pattern', async () => {
    const styles = await readFile(previewStylesPath, 'utf8');
    const imageCanvas = styles.match(/^\.preview-image\s*\{([^}]*)\}/ms)?.[1] ?? '';

    expect(imageCanvas).toContain('background: #fff;');
    expect(imageCanvas).not.toContain('linear-gradient');
  });

  it('scales multi-file tile content without changing the single-file reader', async () => {
    const styles = await readFile(previewStylesPath, 'utf8');
    const boardTile = styles.match(/^\.preview-board__tile\s*\{([^}]*)\}/ms)?.[1] ?? '';
    const boardMarkdown =
      styles.match(/\.preview-board__tile-body \.preview-markdown__document\s*\{([^}]*)\}/s)?.[1] ?? '';
    const boardImage =
      styles.match(/\.preview-board__tile-body \.preview-image \.arco-image-img\s*\{([^}]*)\}/s)?.[1] ?? '';

    expect(boardTile).toContain('container-type: inline-size;');
    expect(boardMarkdown).toContain('font-size: clamp(');
    expect(boardImage).toContain('object-fit: contain;');
    expect(styles).not.toMatch(/^\.preview-markdown__document\s*\{[^}]*cqi/ms);
  });
});
