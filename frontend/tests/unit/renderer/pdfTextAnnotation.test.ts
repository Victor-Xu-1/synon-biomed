// @vitest-environment jsdom

import { describe, expect, it } from 'vitest';
import {
  findPdfTextAnnotationRange,
  normalizePdfTextAnnotationRects,
} from '@/renderer/pages/artifact/pdfTextAnnotation';

describe('PDF text annotation anchors', () => {
  it('uses the saved prefix to disambiguate repeated selected text across text-layer spans', () => {
    const layer = document.createElement('div');
    layer.innerHTML = '<span>Introduction assay result</span><span> Conclusion assay result</span>';

    const range = findPdfTextAnnotationRange(layer, 'assay result', 'Conclusion ');

    expect(range?.toString()).toBe('assay result');
    expect(range?.startContainer.parentElement?.textContent).toContain('Conclusion');
  });

  it('falls back to whitespace-flexible matching and normalizes rendered rectangles', () => {
    const layer = document.createElement('div');
    layer.innerHTML = '<span>alpha</span><span>   beta</span>';
    const range = findPdfTextAnnotationRange(layer, 'alpha beta');
    expect(range?.toString()).toBe('alpha   beta');

    const rects = normalizePdfTextAnnotationRects(
      [
        { left: 120, top: 80, width: 200, height: 30, right: 320, bottom: 110 } as DOMRect,
        { left: 130, top: 85, width: 20, height: 10, right: 150, bottom: 95 } as DOMRect,
      ],
      { left: 100, top: 50 } as DOMRect,
      2
    );
    expect(rects).toEqual([{ left: 10, top: 15, width: 100, height: 15 }]);
  });
});
