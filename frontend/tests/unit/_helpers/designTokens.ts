import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

/**
 * Visual contracts pin literal px values. Module CSS now spells those values
 * through the canonical primitives, so contracts read stylesheets with the
 * primitives inlined back to numbers - the pinned value stays the assertion.
 */
const tokenSource = readFileSync(
  path.join(fileURLToPath(new URL('../../../packages/desktop/src/renderer/', import.meta.url)), 'styles/tokens.css'),
  'utf8'
);

const primitiveValues = new Map<string, string>();
for (const match of tokenSource.matchAll(/--(ui-[a-z0-9-]+):\s*([^;]+);/g)) {
  primitiveValues.set(match[1]!, match[2]!.trim());
}

/** Stylesheets only: binary fixtures (locked reference images) must stay raw. */
export const resolveDesignTokensIn = (target: string | URL, text: string): string =>
  `${target}`.endsWith('.css') ? resolveDesignTokens(text) : text;

export const resolveDesignTokens = (css: string): string =>
  css.replace(/var\(--(ui-[a-z0-9-]+)\)/g, (whole, name: string) => primitiveValues.get(name) ?? whole);
