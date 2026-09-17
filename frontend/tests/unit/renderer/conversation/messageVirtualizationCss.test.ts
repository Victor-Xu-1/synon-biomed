import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

import { describe, expect, it } from 'vitest';

const messagesCssPath = fileURLToPath(
  new URL('../../../../packages/desktop/src/renderer/pages/conversation/Messages/messages.css', import.meta.url)
);

describe('message list virtualization CSS', () => {
  it('leaves row measurement to react-virtuoso', () => {
    const css = readFileSync(messagesCssPath, 'utf8');

    expect(css).not.toContain('content-visibility: auto');
    expect(css).not.toContain('contain-intrinsic-size:');
  });
});
