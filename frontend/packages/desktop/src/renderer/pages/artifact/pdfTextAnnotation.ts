export interface PdfTextAnnotationRect {
  top: number;
  left: number;
  width: number;
  height: number;
}

const PREFIX_MATCH_LENGTH = 30;

export function findPdfTextAnnotationRange(
  textLayer: Element,
  selectedText: string,
  selectionPrefix?: string | null
): Range | null {
  if (!selectedText) return null;
  const walker = document.createTreeWalker(textLayer, NodeFilter.SHOW_TEXT);
  const nodes: Array<{ node: Text; start: number }> = [];
  let fullText = '';
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const textNode = node as Text;
    nodes.push({ node: textNode, start: fullText.length });
    fullText += textNode.textContent ?? '';
  }

  const normalizedPrefix = normalizeWhitespace(selectionPrefix ?? '').slice(-PREFIX_MATCH_LENGTH);
  const prefixMatches = (index: number): boolean => {
    if (!normalizedPrefix) return true;
    const candidate = normalizeWhitespace(fullText.slice(Math.max(0, index - 50), index)).slice(-PREFIX_MATCH_LENGTH);
    return (
      candidate.endsWith(normalizedPrefix) ||
      (candidate.length > 0 && normalizedPrefix.length < PREFIX_MATCH_LENGTH && normalizedPrefix.endsWith(candidate))
    );
  };

  let match = findExactMatch(fullText, selectedText, prefixMatches);
  if (!match) {
    const flexiblePattern = selectedText.split(/\s+/).filter(Boolean).map(escapeRegExp).join('\\s*');
    if (flexiblePattern) {
      const expression = new RegExp(flexiblePattern, 'g');
      let firstMatch: { start: number; end: number } | null = null;
      for (let result = expression.exec(fullText); result; result = expression.exec(fullText)) {
        const candidate = { start: result.index, end: result.index + result[0].length };
        firstMatch ??= candidate;
        if (prefixMatches(candidate.start)) {
          match = candidate;
          break;
        }
        if (result[0].length === 0) expression.lastIndex += 1;
      }
      match ??= firstMatch;
    }
  }
  if (!match) return null;

  const start = locateTextOffset(nodes, match.start, false);
  const end = locateTextOffset(nodes, match.end, true);
  if (!start || !end) return null;
  const range = document.createRange();
  range.setStart(start.node, start.offset);
  range.setEnd(end.node, end.offset);
  return range;
}

export function normalizePdfTextAnnotationRects(
  rects: Iterable<DOMRect>,
  pageRect: DOMRect,
  renderScale: number
): PdfTextAnnotationRect[] {
  const scale = renderScale > 0 ? renderScale : 1;
  const candidates = Array.from(rects)
    .filter((rect) => rect.width > 0 && rect.height > 0)
    .map((rect) => ({
      top: (rect.top - pageRect.top) / scale,
      left: (rect.left - pageRect.left) / scale,
      width: rect.width / scale,
      height: rect.height / scale,
    }));
  return candidates.filter(
    (candidate, index) =>
      !candidates.some(
        (other, otherIndex) =>
          otherIndex !== index &&
          other.left <= candidate.left + 1 &&
          other.top <= candidate.top + 1 &&
          other.left + other.width >= candidate.left + candidate.width - 1 &&
          other.top + other.height >= candidate.top + candidate.height - 1 &&
          (other.width > candidate.width + 1 || other.height > candidate.height + 1 || otherIndex < index)
      )
  );
}

function findExactMatch(
  fullText: string,
  selectedText: string,
  prefixMatches: (index: number) => boolean
): { start: number; end: number } | null {
  let firstMatch: { start: number; end: number } | null = null;
  for (let offset = 0; offset < fullText.length; ) {
    const index = fullText.indexOf(selectedText, offset);
    if (index < 0) break;
    const match = { start: index, end: index + selectedText.length };
    firstMatch ??= match;
    if (prefixMatches(index)) return match;
    offset = index + 1;
  }
  return firstMatch;
}

function locateTextOffset(
  nodes: Array<{ node: Text; start: number }>,
  absoluteOffset: number,
  includeBoundaryInPreviousNode: boolean
): { node: Text; offset: number } | null {
  for (const entry of nodes) {
    const length = entry.node.textContent?.length ?? 0;
    const end = entry.start + length;
    if (absoluteOffset < end || (includeBoundaryInPreviousNode && absoluteOffset === end)) {
      return { node: entry.node, offset: Math.max(0, absoluteOffset - entry.start) };
    }
  }
  const last = nodes.at(-1);
  return last ? { node: last.node, offset: last.node.textContent?.length ?? 0 } : null;
}

function normalizeWhitespace(value: string): string {
  return value.replace(/\s+/g, ' ').trim();
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
