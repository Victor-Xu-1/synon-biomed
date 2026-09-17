import type { SynonBiomedTranscriptAnnotation } from '@/renderer/services/synonBiomedAnnotations';

export type TranscriptAnchorResolution =
  | { status: 'legacy'; startOffset: null; endOffset: null }
  | { status: 'resolved'; startOffset: number; endOffset: number; relocated: boolean }
  | { status: 'orphaned'; reason: 'empty' | 'missing' | 'ambiguous' | 'invalid-range' };

export interface TranscriptSelectionAnchor {
  anchorText: string;
  startOffset: number;
  endOffset: number;
}

/** Resolve persisted UTF-16 offsets without guessing when the same text occurs more than once. */
export function resolveTranscriptAnchor(
  renderedText: string,
  annotation: Pick<SynonBiomedTranscriptAnnotation, 'anchorText' | 'startOffset' | 'endOffset'>
): TranscriptAnchorResolution {
  const { anchorText, startOffset, endOffset } = annotation;
  if (startOffset === null && endOffset === null) {
    return { status: 'legacy', startOffset: null, endOffset: null };
  }
  if (!anchorText) return { status: 'orphaned', reason: 'empty' };

  const hasValidRange =
    Number.isInteger(startOffset) &&
    Number.isInteger(endOffset) &&
    startOffset !== null &&
    endOffset !== null &&
    startOffset >= 0 &&
    endOffset > startOffset &&
    endOffset <= renderedText.length;
  if (hasValidRange && renderedText.slice(startOffset, endOffset) === anchorText) {
    return { status: 'resolved', startOffset, endOffset, relocated: false };
  }

  const occurrences = findOccurrences(renderedText, anchorText);
  if (occurrences.length === 1) {
    return {
      status: 'resolved',
      startOffset: occurrences[0],
      endOffset: occurrences[0] + anchorText.length,
      relocated: true,
    };
  }
  if (occurrences.length > 1) return { status: 'orphaned', reason: 'ambiguous' };
  return { status: 'orphaned', reason: hasValidRange ? 'missing' : 'invalid-range' };
}

export function selectionAnchorFromRange(root: HTMLElement, range: Range): TranscriptSelectionAnchor | null {
  if (range.collapsed || !root.contains(range.commonAncestorContainer)) return null;
  const prefix = range.cloneRange();
  prefix.selectNodeContents(root);
  prefix.setEnd(range.startContainer, range.startOffset);
  const anchorText = range.toString();
  if (!anchorText.trim()) return null;
  const startOffset = prefix.toString().length;
  return { anchorText, startOffset, endOffset: startOffset + anchorText.length };
}

export function domRangeForOffsets(root: HTMLElement, startOffset: number, endOffset: number): Range | null {
  if (startOffset < 0 || endOffset <= startOffset) return null;
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  let consumed = 0;
  let startNode: Text | null = null;
  let endNode: Text | null = null;
  let startInNode = 0;
  let endInNode = 0;

  for (let node = walker.nextNode() as Text | null; node; node = walker.nextNode() as Text | null) {
    const next = consumed + node.data.length;
    if (!startNode && startOffset >= consumed && startOffset <= next) {
      startNode = node;
      startInNode = startOffset - consumed;
    }
    if (endOffset >= consumed && endOffset <= next) {
      endNode = node;
      endInNode = endOffset - consumed;
      break;
    }
    consumed = next;
  }
  if (!startNode || !endNode) return null;
  const range = document.createRange();
  range.setStart(startNode, startInNode);
  range.setEnd(endNode, endInNode);
  return range;
}

function findOccurrences(text: string, needle: string): number[] {
  const offsets: number[] = [];
  for (let offset = text.indexOf(needle); offset >= 0; offset = text.indexOf(needle, offset + 1)) {
    offsets.push(offset);
    if (offsets.length > 1) break;
  }
  return offsets;
}
