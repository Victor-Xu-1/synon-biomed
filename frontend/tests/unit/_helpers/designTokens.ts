import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

/**
 * Visual contracts pin literal px values. Module CSS now spells those values
 * through the canonical primitives, so contracts read stylesheets with the
 * primitives inlined back to numbers - the pinned value stays the assertion.
 */
const rendererDir = fileURLToPath(new URL('../../../packages/desktop/src/renderer/', import.meta.url));

const primitiveValues = new Map<string, string>();
for (const source of ['styles/tokens.css', 'pages/settings/components/settings-core.css']) {
  for (const match of readFileSync(path.join(rendererDir, source), 'utf8').matchAll(
    /--(ui-[a-z0-9-]+|settings-[a-z0-9-]+):\s*([^;]+);/g
  )) {
    primitiveValues.set(match[1]!, match[2]!.trim());
  }
}

const resolveOnce = (css: string): string =>
  css.replace(
    /var\(--(ui-[a-z0-9-]+|settings-[a-z0-9-]+)\)/g,
    (whole, name: string) => primitiveValues.get(name) ?? whole
  );

export const resolveDesignTokens = (css: string): string => {
  // Settings tokens are themselves expressed through the ui primitives.
  let resolved = resolveOnce(css);
  for (let pass = 0; pass < 3 && /var\(--(?:ui|settings)-/.test(resolved); pass += 1) resolved = resolveOnce(resolved);
  return resolved;
};
