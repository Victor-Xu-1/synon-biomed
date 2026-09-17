import { unifiedLatexToHast } from '@unified-latex/unified-latex-to-hast';
import { unifiedLatexFromString } from '@unified-latex/unified-latex-util-parse';
import rehypeStringify from 'rehype-stringify';
import { unified } from 'unified10';

export interface LatexDocumentConversion {
  html: string;
  warnings: string[];
}

export type LatexArtifactResourceUrls = Readonly<Record<string, string>>;

export function resolveLatexArtifactResources(html: string, resourceUrls: LatexArtifactResourceUrls): string {
  if (!html || typeof DOMParser === 'undefined') return html;
  const document = new DOMParser().parseFromString(html, 'text/html');

  for (const image of document.querySelectorAll<HTMLImageElement>('img')) {
    const source = image.getAttribute('src')?.trim() ?? '';
    if (!source) continue;

    if (isAllowedEmbeddedImage(source) || isArtifactContentUrl(source)) continue;

    const resourceUrl = resolveResourceUrl(source, resourceUrls);
    if (resourceUrl) {
      image.setAttribute('src', resourceUrl);
      image.setAttribute('loading', 'lazy');
      continue;
    }

    image.removeAttribute('src');
    image.setAttribute('data-unresolved-src', source.slice(0, 500));
    image.setAttribute('alt', image.getAttribute('alt') || source);
    image.classList.add('latex-unresolved-resource');
  }

  return document.body.innerHTML;
}

function resolveResourceUrl(source: string, resourceUrls: LatexArtifactResourceUrls): string | null {
  const normalized = normalizeLatexResourceKey(source);
  if (!normalized) return null;
  const basename = normalized.split('/').at(-1) ?? normalized;
  const stem = basename.replace(/\.[a-z0-9]+$/i, '');
  return resourceUrls[normalized] ?? resourceUrls[basename] ?? resourceUrls[stem] ?? null;
}

export function normalizeLatexResourceKey(value: string): string | null {
  let decoded: string;
  try {
    decoded = decodeURIComponent(value);
  } catch {
    return null;
  }
  const normalized = decoded.trim().replaceAll('\\', '/').replace(/^\.\//, '').toLowerCase();
  if (
    !normalized ||
    normalized.startsWith('/') ||
    normalized.startsWith('//') ||
    normalized.split('/').includes('..') ||
    /^[a-z][a-z0-9+.-]*:/i.test(normalized)
  ) {
    return null;
  }
  return normalized.replace(/\/+/g, '/');
}

function isAllowedEmbeddedImage(source: string): boolean {
  return /^data:image\/(?:avif|gif|jpeg|png|svg\+xml|webp);/i.test(source);
}

function isArtifactContentUrl(source: string): boolean {
  return /^\/api\/artifacts\/[^/?#]+(?:[?#].*)?$/i.test(source);
}

export function convertLatexDocumentToHtml(source: string): LatexDocumentConversion {
  if (!source.trim()) return { html: '', warnings: [] };
  const result = unified().use(unifiedLatexFromString).use(unifiedLatexToHast).use(rehypeStringify).processSync(source);
  return {
    html: String(result.value),
    warnings: result.messages.map((message) => message.message),
  };
}
