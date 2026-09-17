/**
 * Build a valid src for the PDF <webview>.
 *
 * On Windows, file paths use backslashes (e.g. `C:\Users\me\a.pdf`). Feeding such a
 * path straight into `file://${encodeURI(path)}` yields `file://C:%5CUsers%5C...`,
 * a malformed URL that fails to load (ERR_FAILED) and renders a blank preview.
 *
 * Normalize backslashes to forward slashes, guarantee the leading slash so the result
 * is a proper `file:///` URL on every platform, and encode segments (spaces / CJK) via
 * encodeURI (which preserves `/` and `:`).
 */
export const buildPdfSrc = (file_path?: string, content?: string): string => {
  // Synon Biomed project files use a virtual workspace identity rather than a
  // filesystem path. The authenticated content endpoint is the only URL a
  // browser iframe can load; coercing the virtual identity to `file://` leaves
  // the preview permanently stuck in its loading state.
  if (file_path?.startsWith('synonbiomed://')) {
    return content || '';
  }

  if (file_path) {
    if (/^(?:https?:|data:|blob:|file:)/i.test(file_path)) {
      return file_path;
    }
    const normalized = file_path.replace(/\\/g, '/');
    const withLeadingSlash = normalized.startsWith('/') ? normalized : `/${normalized}`;
    return `file://${encodeURI(withLeadingSlash)}`;
  }
  return content || '';
};

/**
 * Keep the browser's native PDF renderer while opening the first page at a
 * readable plot scale. Existing document fragments are preserved so
 * callers can still target a page or named destination; the preview-owned
 * viewport settings are the single authority for initial framing.
 */
export const buildNativePdfPreviewSrc = (source: string): string => {
  if (!source) return source;
  const fragmentIndex = source.indexOf('#');
  const base = fragmentIndex >= 0 ? source.slice(0, fragmentIndex) : source;
  const fragment = new URLSearchParams(fragmentIndex >= 0 ? source.slice(fragmentIndex + 1) : '');
  fragment.set('page', fragment.get('page') || '1');
  fragment.set('zoom', '50');
  fragment.set('toolbar', '0');
  fragment.set('navpanes', '0');
  return `${base}#${fragment.toString()}`;
};
