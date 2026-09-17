const TEXT_PREVIEW_TYPES = new Set(['markdown', 'html', 'code', 'diff', 'latex']);

/**
 * One persistence authority for preview content. Presentation components and
 * history loading must agree on which payloads are textual and which MIME type
 * an immutable artifact version receives.
 */
export function isPreviewContentStoredAsText(contentType: string, filename?: string): boolean {
  return TEXT_PREVIEW_TYPES.has(contentType) || (contentType === 'table' && isDelimitedTextFile(filename));
}

export function getPreviewVersionContentType(contentType: string, filename?: string): string {
  if (contentType === 'markdown') return 'text/markdown';
  if (contentType === 'html') return 'text/html';
  if (['code', 'diff', 'latex'].includes(contentType)) return 'text/plain';
  if (contentType === 'table' && filename?.trim().toLowerCase().endsWith('.tsv')) {
    return 'text/tab-separated-values';
  }
  if (contentType === 'table' && filename?.trim().toLowerCase().endsWith('.csv')) return 'text/csv';
  return 'application/octet-stream';
}

function isDelimitedTextFile(filename?: string): boolean {
  const normalized = filename?.trim().toLowerCase() ?? '';
  return normalized.endsWith('.csv') || normalized.endsWith('.tsv');
}
