import { serializeComposerReference } from '@/renderer/components/chat/SendBox/composerReferenceModel';
import { previewRegionText } from '@/renderer/services/i18n/previewRegionLocale';
import type { CSSProperties } from 'react';

export const PREVIEW_REGION_MIN_SIZE_PX = 8;
export const PREVIEW_REGION_COMMENT_MAX_LENGTH = 2000;
export const PREVIEW_REGION_EVIDENCE_MAX_LENGTH = 1200;

export type PreviewRegionPoint = {
  x: number;
  y: number;
};

export type PreviewRegionBounds = {
  left: number;
  top: number;
  width: number;
  height: number;
};

export type PreviewRegionRect = {
  leftPercent: number;
  topPercent: number;
  widthPercent: number;
  heightPercent: number;
};

export type PreviewRegionCommentSource = {
  fileName: string;
  contentType: string;
  artifactId?: string;
  versionId?: string;
  filePath?: string;
};

export type PreviewRegionViewport = {
  widthPx: number;
  heightPx: number;
  scrollLeftPercent: number;
  scrollTopPercent: number;
};

export type PreviewRegionComment = {
  source: PreviewRegionCommentSource;
  region: PreviewRegionRect;
  note: string;
  visibleText?: string;
  viewport?: PreviewRegionViewport;
};

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function roundedPercent(value: number): number {
  return Number(clamp(value, 0, 100).toFixed(2));
}

export function normalizePreviewRegion(
  start: PreviewRegionPoint,
  end: PreviewRegionPoint,
  bounds: PreviewRegionBounds,
  minimumSize = PREVIEW_REGION_MIN_SIZE_PX
): PreviewRegionRect | null {
  if (bounds.width <= 0 || bounds.height <= 0) return null;

  const startX = clamp(start.x - bounds.left, 0, bounds.width);
  const startY = clamp(start.y - bounds.top, 0, bounds.height);
  const endX = clamp(end.x - bounds.left, 0, bounds.width);
  const endY = clamp(end.y - bounds.top, 0, bounds.height);
  const left = Math.min(startX, endX);
  const top = Math.min(startY, endY);
  const width = Math.abs(endX - startX);
  const height = Math.abs(endY - startY);

  if (width < minimumSize || height < minimumSize) return null;

  return {
    leftPercent: roundedPercent((left / bounds.width) * 100),
    topPercent: roundedPercent((top / bounds.height) * 100),
    widthPercent: roundedPercent((width / bounds.width) * 100),
    heightPercent: roundedPercent((height / bounds.height) * 100),
  };
}

export function previewRegionToStyle(region: PreviewRegionRect): CSSProperties {
  return {
    left: `${region.leftPercent}%`,
    top: `${region.topPercent}%`,
    width: `${region.widthPercent}%`,
    height: `${region.heightPercent}%`,
  };
}

function boundedText(value: string | null | undefined, limit: number): string {
  const normalized = (value ?? '').replace(/\s+/g, ' ').trim();
  if (normalized.length <= limit) return normalized;
  return `${normalized.slice(0, Math.max(0, limit - 1)).trimEnd()}…`;
}

function quoteForComposer(value: string): string {
  return value
    .split(/\r?\n/)
    .map((line) => `> ${line}`)
    .join('\n');
}

function formatPercent(value: number): string {
  return Number(value.toFixed(2)).toString();
}

function buildArtifactReference(source: PreviewRegionCommentSource): string | null {
  if (!source.artifactId) return null;
  try {
    return serializeComposerReference({
      kind: 'artifact',
      key: `artifact:${source.artifactId}:${source.versionId || 'latest'}`,
      label: source.fileName,
      detail: '',
      projectId: null,
      projectName: null,
      isCurrentProject: false,
      artifactId: source.artifactId,
      versionId: source.versionId,
    });
  } catch {
    return null;
  }
}

function formatViewport(viewport: PreviewRegionViewport): string {
  return [
    `${Math.max(0, Math.round(viewport.widthPx))}x${Math.max(0, Math.round(viewport.heightPx))}px`,
    `scrollX=${formatPercent(viewport.scrollLeftPercent)}%`,
    `scrollY=${formatPercent(viewport.scrollTopPercent)}%`,
  ].join(', ');
}

export function formatPreviewRegionCommentForComposer(
  input: PreviewRegionComment,
  language: string | undefined
): string {
  const note = boundedText(input.note, PREVIEW_REGION_COMMENT_MAX_LENGTH);
  if (!note) throw new Error('Preview region comment note is required');

  const source = {
    ...input.source,
    fileName: boundedText(input.source.fileName, 240) || 'preview',
    contentType: boundedText(input.source.contentType, 80) || 'unknown',
    filePath: boundedText(input.source.filePath, 600),
  };
  const reference = buildArtifactReference(source);
  const visibleText = boundedText(input.visibleText, PREVIEW_REGION_EVIDENCE_MAX_LENGTH);
  const coordinates = [
    `left=${formatPercent(input.region.leftPercent)}%`,
    `top=${formatPercent(input.region.topPercent)}%`,
    `width=${formatPercent(input.region.widthPercent)}%`,
    `height=${formatPercent(input.region.heightPercent)}%`,
  ].join(', ');
  const sourceLine =
    reference ??
    previewRegionText(language, source.filePath ? 'sourceFileWithPath' : 'sourceFile', {
      file: source.fileName,
      path: source.filePath,
    });
  const viewportLine = input.viewport
    ? `\n${previewRegionText(language, 'viewport', { viewport: formatViewport(input.viewport) })}`
    : '';
  const evidence = visibleText ? `\n${previewRegionText(language, 'evidence')}\n${quoteForComposer(visibleText)}` : '';
  const description = previewRegionText(language, 'description', {
    type: source.contentType,
    coordinates,
  });
  const comment = previewRegionText(language, 'comment', { note });
  return `${sourceLine}\n${description}${viewportLine}\n${comment}${evidence}`;
}

function scrollPercent(position: number, scrollSize: number, clientSize: number): number {
  const maximum = Math.max(0, scrollSize - clientSize);
  return maximum > 0 ? roundedPercent((position / maximum) * 100) : 0;
}

export function capturePreviewRegionViewport(
  root: HTMLElement,
  bounds: Pick<DOMRect, 'width' | 'height'>
): PreviewRegionViewport {
  let scrollContainer = root;
  let largestScrollableArea = 0;
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT);
  let current = walker.nextNode();
  let inspected = 0;

  while (current && inspected < 1000) {
    inspected += 1;
    const element = current instanceof HTMLElement ? current : null;
    if (element && !element.closest('[data-preview-region-comment-ui]')) {
      const isScrollable =
        element.scrollHeight > element.clientHeight + 1 || element.scrollWidth > element.clientWidth + 1;
      const area = element.clientWidth * element.clientHeight;
      if (isScrollable && area > largestScrollableArea) {
        scrollContainer = element;
        largestScrollableArea = area;
      }
    }
    current = walker.nextNode();
  }

  return {
    widthPx: Math.max(0, Math.round(bounds.width)),
    heightPx: Math.max(0, Math.round(bounds.height)),
    scrollLeftPercent: scrollPercent(
      scrollContainer.scrollLeft,
      scrollContainer.scrollWidth,
      scrollContainer.clientWidth
    ),
    scrollTopPercent: scrollPercent(
      scrollContainer.scrollTop,
      scrollContainer.scrollHeight,
      scrollContainer.clientHeight
    ),
  };
}

type RegionEdges = Pick<DOMRect, 'left' | 'right' | 'top' | 'bottom'>;

export function regionsIntersect(left: RegionEdges, right: RegionEdges): boolean {
  return left.left < right.right && left.right > right.left && left.top < right.bottom && left.bottom > right.top;
}

export function collectVisibleTextInPreviewRegion(root: HTMLElement, region: DOMRect): string {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const fragments: string[] = [];
  let totalLength = 0;
  let current = walker.nextNode();

  while (current && totalLength < PREVIEW_REGION_EVIDENCE_MAX_LENGTH) {
    const parent = current.parentElement;
    const text = current.textContent?.replace(/\s+/g, ' ').trim() ?? '';
    if (
      text &&
      parent &&
      !parent.closest('[data-preview-region-comment-ui]') &&
      parent.getAttribute('aria-hidden') !== 'true'
    ) {
      const range = document.createRange();
      range.selectNodeContents(current);
      const rects = typeof range.getClientRects === 'function' ? Array.from(range.getClientRects()) : [];
      const intersects = rects.some((rect) => regionsIntersect(rect, region));
      range.detach();
      if (intersects) {
        fragments.push(text);
        totalLength += text.length + 1;
      }
    }
    current = walker.nextNode();
  }

  return boundedText(fragments.join(' '), PREVIEW_REGION_EVIDENCE_MAX_LENGTH);
}
