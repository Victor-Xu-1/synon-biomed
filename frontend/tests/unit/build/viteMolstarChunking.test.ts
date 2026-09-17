import { rendererManualChunk, rendererOnlyExplicitManualChunks } from '../../../vite.config';
import { describe, expect, it } from 'vitest';

describe('renderer Mol* chunking', () => {
  it('keeps Mol* lazy while assigning only approved shared runtime vendors', () => {
    expect(rendererManualChunk('/workspace/frontend/node_modules/molstar/lib/mol-plugin/context.js')).toBe('molstar');
    expect(rendererManualChunk(String.raw`C:\workspace\frontend\node_modules\molstar\lib\mol-model\structure.js`)).toBe(
      'molstar'
    );
    expect(rendererManualChunk('/workspace/frontend/node_modules/react/index.js')).toBe('react-vendor');
    expect(rendererManualChunk('/workspace/frontend/node_modules/react-dom/client.js')).toBe('react-vendor');
    expect(rendererManualChunk('/workspace/frontend/node_modules/scheduler/index.js')).toBe('react-vendor');
    expect(rendererManualChunk('/workspace/frontend/node_modules/mdast-util-to-markdown/lib/index.js')).toBe(
      'markdown-vendor'
    );
    expect(rendererManualChunk('/workspace/frontend/node_modules/tslib/tslib.es6.mjs')).toBe('runtime-vendor');
    expect(rendererManualChunk('/workspace/frontend/node_modules/@arco-design/web-react/es/index.js')).toBeUndefined();
  });

  it('keeps Mol* private dependencies with the manual chunk to prevent an import cycle', () => {
    expect(rendererOnlyExplicitManualChunks).toBe(false);
  });
});
