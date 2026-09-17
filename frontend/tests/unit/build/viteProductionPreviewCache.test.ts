import { describe, expect, it } from 'vitest';
import { productionPreviewCacheControl } from '../../../vite.config';

describe('production preview cache policy', () => {
  it('keeps navigation fresh and content-hashed bundles immutable', () => {
    expect(productionPreviewCacheControl('/')).toBe('no-cache');
    expect(productionPreviewCacheControl('/conversation/frame-1')).toBe('no-cache');
    expect(productionPreviewCacheControl('/index.html')).toBe('no-cache');
    expect(productionPreviewCacheControl('/assets/index-Bd83ksPq.js')).toBe('public, max-age=31536000, immutable');
    expect(productionPreviewCacheControl('/assets/vendor.css')).toBe('public, max-age=3600');
  });

  it('does not alter proxied API, login or realtime response caching', () => {
    expect(productionPreviewCacheControl('/api/assistants')).toBeUndefined();
    expect(productionPreviewCacheControl('/login')).toBeUndefined();
    expect(productionPreviewCacheControl('/ws/session')).toBeUndefined();
    expect(productionPreviewCacheControl('/rdkit/RDKit_minimal.wasm')).toBe('no-cache, must-revalidate');
  });
});
