import { describe, expect, it } from 'vitest';
import { isSafeInlineMarkdownImageUrl } from '@/renderer/components/Markdown/markdownImageUrlPolicy';

describe('markdownImageUrlPolicy', () => {
  it('accepts bounded raster image data URLs', () => {
    expect(isSafeInlineMarkdownImageUrl('data:image/png;base64,iVBORw0KGgo=')).toBe(true);
    expect(isSafeInlineMarkdownImageUrl('data:image/jpeg;base64,/9j/4AAQSkZJRg==')).toBe(true);
  });

  it('rejects active, malformed, and oversized inline image sources', () => {
    expect(isSafeInlineMarkdownImageUrl('data:image/svg+xml;base64,PHN2Zz48c2NyaXB0Lz48L3N2Zz4=')).toBe(false);
    expect(isSafeInlineMarkdownImageUrl('data:image/png,not-base64')).toBe(false);
    expect(isSafeInlineMarkdownImageUrl(`data:image/png;base64,${'A'.repeat(11_184_813)}`)).toBe(false);
  });
});
