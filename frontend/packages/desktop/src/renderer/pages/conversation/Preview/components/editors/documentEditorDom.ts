import { ipcBridge } from '@/common';
import { resolveMarkdownArtifactVersionReferences } from '../viewers/markdownArtifactReferences';
import {
  isLocalDocumentResource,
  ORIGINAL_LINK_ATTRIBUTE,
  ORIGINAL_RESOURCE_ATTRIBUTE,
  sanitizeDocumentPasteHtml,
} from './documentWysiwygModel';

export function selectDocumentImage(
  target: EventTarget | null,
  select: (image: HTMLImageElement | null) => void
): void {
  const element = documentElementFromEventTarget(target);
  const image = element?.tagName === 'IMG' ? (element as HTMLImageElement) : null;
  const ownerDocument = element?.ownerDocument ?? document;
  ownerDocument
    .querySelectorAll('[data-document-selected]')
    .forEach((node) => node.removeAttribute('data-document-selected'));
  if (image) image.setAttribute('data-document-selected', 'true');
  select(image);
}

export function handleDocumentEditorClick(
  event: Pick<MouseEvent, 'target' | 'preventDefault'>,
  select: (image: HTMLImageElement | null) => void
): void {
  preventDocumentEditorNavigation(event);
  selectDocumentImage(event.target, select);
}

export function preventDocumentEditorNavigation(event: Pick<MouseEvent, 'target' | 'preventDefault'>): void {
  const element = documentElementFromEventTarget(event.target);
  if (element?.closest('a[href]')) event.preventDefault();
}

export function pasteSanitizedDocumentContent(
  event: ClipboardEvent,
  editorDocument: Document,
  emitChange: () => void
): void {
  const html = event.clipboardData?.getData('text/html') ?? '';
  const text = event.clipboardData?.getData('text/plain') ?? '';
  if (!html && !text) return;
  event.preventDefault();
  editorDocument.execCommand(html ? 'insertHTML' : 'insertText', false, html ? sanitizeDocumentPasteHtml(html) : text);
  emitChange();
}

export function directoryOfDocument(filePath?: string): string | undefined {
  if (!filePath) return undefined;
  const normalized = filePath.replaceAll('\\', '/');
  const slash = normalized.lastIndexOf('/');
  return slash >= 0 ? normalized.slice(0, slash) : undefined;
}

export async function prepareHtmlDocumentForEditing(
  content: string,
  filePath?: string,
  workspace?: string
): Promise<string> {
  if (!filePath && !content.includes('{{artifact:')) return content;
  const documentValue = new DOMParser().parseFromString(content, 'text/html');
  const images = [...documentValue.querySelectorAll<HTMLImageElement>('img[src]')];
  documentValue.querySelectorAll<HTMLAnchorElement>('a[href]').forEach((link) => {
    const source = link.getAttribute('href') || '';
    const resolved = resolveMarkdownArtifactVersionReferences(source);
    if (resolved === source) return;
    link.setAttribute(ORIGINAL_LINK_ATTRIBUTE, source);
    link.href = resolved;
  });
  await Promise.all(
    images.map(async (image) => {
      const source = image.getAttribute('src') || '';
      const artifactSource = resolveMarkdownArtifactVersionReferences(source);
      if (artifactSource !== source) {
        image.setAttribute(ORIGINAL_RESOURCE_ATTRIBUTE, source);
        image.src = artifactSource;
        return;
      }
      if (!filePath || !isLocalDocumentResource(source)) return;
      try {
        const dataUrl = await ipcBridge.fs.getImageBase64.invoke({
          path: resolveDocumentPath(filePath, source),
          workspace,
        });
        if (!dataUrl) return;
        image.setAttribute(ORIGINAL_RESOURCE_ATTRIBUTE, source);
        image.src = dataUrl;
      } catch {
        // Preserve the authored source. Saving never rewrites it to a fallback.
      }
    })
  );
  return `${documentValue.doctype ? '<!DOCTYPE html>\n' : ''}${documentValue.documentElement.outerHTML}`;
}

export async function prepareInsertedImageSource(
  source: string,
  filePath?: string,
  workspace?: string
): Promise<string> {
  const artifactSource = resolveMarkdownArtifactVersionReferences(source);
  if (artifactSource !== source) return artifactSource;
  if (!filePath || !isLocalDocumentResource(source)) return source;
  try {
    return (
      (await ipcBridge.fs.getImageBase64.invoke({
        path: resolveDocumentPath(filePath, source),
        workspace,
      })) || source
    );
  } catch {
    return source;
  }
}

function resolveDocumentPath(filePath: string, resource: string): string {
  if (/^(?:[a-zA-Z]:[\\/]|\\\\|\/)/.test(resource)) return resource;
  const normalized = filePath.replaceAll('\\', '/');
  const baseParts = normalized.slice(0, normalized.lastIndexOf('/') + 1).split('/');
  for (const part of resource.replaceAll('\\', '/').split('/')) {
    if (!part || part === '.') continue;
    if (part === '..') baseParts.pop();
    else baseParts.push(part);
  }
  return baseParts.join('/');
}

function documentElementFromEventTarget(target: EventTarget | null): Element | null {
  if (!target || typeof target !== 'object') return null;
  const candidate = target as EventTarget & {
    nodeType?: number;
    tagName?: unknown;
    ownerDocument?: Document;
    closest?: unknown;
  };
  const ElementConstructor = candidate.ownerDocument?.defaultView?.Element;
  if (typeof ElementConstructor === 'function' && target instanceof ElementConstructor) return target as Element;

  // Browser extension isolation can expose a real DOM element while hiding
  // constructors on document.defaultView. Use the standard element shape as
  // a bounded fallback so image selection still works in that environment.
  return candidate.nodeType === 1 && typeof candidate.tagName === 'string' && typeof candidate.closest === 'function'
    ? (target as Element)
    : null;
}
