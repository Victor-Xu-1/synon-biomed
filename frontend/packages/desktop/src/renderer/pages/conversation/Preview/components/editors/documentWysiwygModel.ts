import DOMPurify from 'dompurify';
import TurndownService from 'turndown';
import { gfm } from 'turndown-plugin-gfm';
import { ORIGINAL_LINK_ATTRIBUTE, ORIGINAL_RESOURCE_ATTRIBUTE } from './documentResourceModel';

export { ORIGINAL_LINK_ATTRIBUTE, ORIGINAL_RESOURCE_ATTRIBUTE } from './documentResourceModel';

const ARTIFACT_VERSION_URL = /^\/api\/artifacts\/versions\/([0-9a-f-]{36})$/i;

export function restoreMarkdownResourceSource(source: string): string {
  const match = source.match(ARTIFACT_VERSION_URL);
  return match ? `{{artifact:${match[1]}}}` : source;
}

export function markdownFromEditableHtml(html: string): string {
  const container = document.createElement('div');
  container.innerHTML = html;
  container
    .querySelectorAll('[data-document-selected]')
    .forEach((node) => node.removeAttribute('data-document-selected'));

  const turndown = new TurndownService({
    headingStyle: 'atx',
    bulletListMarker: '-',
    codeBlockStyle: 'fenced',
    emDelimiter: '*',
    strongDelimiter: '**',
  });
  turndown.use(gfm);
  turndown.addRule('synon-original-image-source', {
    filter: 'img',
    replacement: (_content, node) => {
      const image = node as HTMLImageElement;
      const source = restoreMarkdownResourceSource(
        image.getAttribute(ORIGINAL_RESOURCE_ATTRIBUTE) || image.getAttribute('src') || ''
      );
      const alt = image.getAttribute('alt') || '';
      const title = image.getAttribute('title');
      const width = image.style.width || image.getAttribute('width') || '';
      if (source && width && width !== '100%') {
        const encodedSource = escapeHtmlAttribute(source);
        const encodedAlt = escapeHtmlAttribute(alt);
        const encodedWidth = escapeHtmlAttribute(width);
        return `<img src="${encodedSource}" alt="${encodedAlt}" style="width:${encodedWidth};height:auto">`;
      }
      return source ? `![${alt}](${source}${title ? ` "${title.replaceAll('"', '\\"')}"` : ''})` : '';
    },
  });
  turndown.addRule('synon-artifact-link-source', {
    filter: (node) => node.nodeName === 'A' && Boolean((node as HTMLAnchorElement).getAttribute('href')),
    replacement: (content, node) => {
      const link = node as HTMLAnchorElement;
      const source = restoreMarkdownResourceSource(
        link.getAttribute(ORIGINAL_LINK_ATTRIBUTE) || link.getAttribute('href') || ''
      );
      const title = link.getAttribute('title');
      return `[${content}](${source}${title ? ` "${title.replaceAll('"', '\\"')}"` : ''})`;
    },
  });
  turndown.addRule('synon-katex-source', {
    filter: (node) => {
      if (!(node instanceof HTMLElement) || !node.classList.contains('katex')) return false;
      return !node.parentElement?.closest('.katex');
    },
    replacement: (_content, node) => {
      const source = node.querySelector('annotation[encoding="application/x-tex"]')?.textContent?.trim() ?? '';
      const display = node.parentElement?.classList.contains('katex-display');
      return source ? (display ? `\n\n$$${source}$$\n\n` : `$${source}$`) : '';
    },
  });

  return turndown
    .turndown(container)
    .replace(/\n{3,}/g, '\n\n')
    .trimEnd();
}

export function replaceHtmlDocumentBody(originalHtml: string, editedBodyOrDocument: string): string {
  const parser = new DOMParser();
  const original = parser.parseFromString(originalHtml, 'text/html');
  const edited = parser.parseFromString(editedBodyOrDocument, 'text/html');
  const body = original.importNode(edited.body, true);

  restoreOriginalResourceSources(body);
  body.removeAttribute('contenteditable');
  body.removeAttribute('aria-label');
  original.documentElement.replaceChild(body, original.body);

  return `${serializeDoctype(original.doctype)}${original.documentElement.outerHTML}`;
}

export function restoreOriginalResourceSources(root: ParentNode): void {
  root.querySelectorAll<HTMLElement>(`[${ORIGINAL_RESOURCE_ATTRIBUTE}]`).forEach((element) => {
    const source = element.getAttribute(ORIGINAL_RESOURCE_ATTRIBUTE);
    if (source) element.setAttribute('src', source);
    element.removeAttribute(ORIGINAL_RESOURCE_ATTRIBUTE);
    element.removeAttribute('data-document-selected');
  });
  root.querySelectorAll<HTMLElement>(`[${ORIGINAL_LINK_ATTRIBUTE}]`).forEach((element) => {
    const source = element.getAttribute(ORIGINAL_LINK_ATTRIBUTE);
    if (source) element.setAttribute('href', source);
    element.removeAttribute(ORIGINAL_LINK_ATTRIBUTE);
  });
}

export function isLocalDocumentResource(source: string): boolean {
  return (
    Boolean(source) && !/^\{\{artifact:[^{}]+\}\}$/i.test(source) && !/^(?:https?:|data:|blob:|\/\/|#)/i.test(source)
  );
}

export function sanitizeDocumentPasteHtml(html: string): string {
  return DOMPurify.sanitize(html, {
    USE_PROFILES: { html: true },
    FORBID_TAGS: ['script', 'style', 'iframe', 'object', 'embed'],
    FORBID_ATTR: ['srcdoc'],
  });
}

function serializeDoctype(doctype: DocumentType | null): string {
  if (!doctype) return '';
  const publicId = doctype.publicId ? ` PUBLIC "${doctype.publicId}"` : '';
  const systemId = doctype.systemId ? `${publicId ? '' : ' SYSTEM'} "${doctype.systemId}"` : '';
  return `<!DOCTYPE ${doctype.name}${publicId}${systemId}>\n`;
}

function escapeHtmlAttribute(value: string): string {
  return value.replaceAll('&', '&amp;').replaceAll('"', '&quot;').replaceAll('<', '&lt;').replaceAll('>', '&gt;');
}
