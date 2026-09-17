export const ORIGINAL_RESOURCE_ATTRIBUTE = 'data-synon-original-src';
export const ORIGINAL_LINK_ATTRIBUTE = 'data-synon-original-href';

export type DocumentResourceKind = 'link' | 'image';
export type DocumentResourceValidation =
  | { valid: true; source: string }
  | { valid: false; reason: 'empty' | 'unsafe-protocol' | 'invalid-image-data' };

const ARTIFACT_REFERENCE = /^\{\{artifact:[0-9a-f-]{36}\}\}$/i;
const SAFE_IMAGE_DATA = /^data:image\/(?:png|jpe?g|gif|webp|avif|bmp);base64,[a-z0-9+/=\s]+$/i;

export function validateDocumentResourceSource(
  rawSource: string,
  kind: DocumentResourceKind
): DocumentResourceValidation {
  const source = rawSource.trim();
  if (!source) return { valid: false, reason: 'empty' };
  if (/\p{C}/u.test(source)) return { valid: false, reason: 'unsafe-protocol' };
  if (ARTIFACT_REFERENCE.test(source)) return { valid: true, source };
  if (/^[a-zA-Z]:[\\/]/.test(source) || source.startsWith('\\\\')) return { valid: true, source };
  if (source.startsWith('//')) return { valid: false, reason: 'unsafe-protocol' };

  const scheme = /^([a-z][a-z0-9+.-]*):/i.exec(source)?.[1]?.toLowerCase();
  if (!scheme) return { valid: true, source };
  if (scheme === 'http' || scheme === 'https') return { valid: true, source };
  if (kind === 'link' && (scheme === 'mailto' || scheme === 'tel')) return { valid: true, source };
  if (kind === 'image' && scheme === 'data') {
    return SAFE_IMAGE_DATA.test(source) ? { valid: true, source } : { valid: false, reason: 'invalid-image-data' };
  }
  return { valid: false, reason: 'unsafe-protocol' };
}

export function buildDocumentLinkHtml(source: string, label: string, displaySource = source): string {
  const visibleLabel = label.trim() || source;
  return `<a href="${escapeHtmlAttribute(displaySource)}" ${ORIGINAL_LINK_ATTRIBUTE}="${escapeHtmlAttribute(source)}">${escapeHtmlText(visibleLabel)}</a>`;
}

export function buildDocumentImageHtml(source: string, alt: string, displaySource = source): string {
  return `<img src="${escapeHtmlAttribute(displaySource)}" ${ORIGINAL_RESOURCE_ATTRIBUTE}="${escapeHtmlAttribute(source)}" alt="${escapeHtmlAttribute(alt.trim())}" style="max-width:100%;height:auto">`;
}

export function buildDocumentTableHtml(columnLabels: readonly string[]): string {
  const labels = columnLabels.length > 0 ? columnLabels : ['', '', ''];
  const header = labels.map((label) => `<th>${escapeHtmlText(label)}</th>`).join('');
  const blankRow = labels.map(() => '<td><br></td>').join('');
  return `<table><thead><tr>${header}</tr></thead><tbody><tr>${blankRow}</tr><tr>${blankRow}</tr></tbody></table><p><br></p>`;
}

function escapeHtmlText(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
}

function escapeHtmlAttribute(value: string): string {
  return escapeHtmlText(value).replaceAll('"', '&quot;');
}
