export interface SynonBiomedArtifactTextSelection {
  type: 'text_selection';
  text: string;
  x: number;
  y: number;
  startLine: number | null;
  startColumn: number | null;
  endLine: number | null;
  endColumn: number | null;
  selectionPrefix: string | null;
  pageNumber: number | null;
}

export function locateRenderedTextSelection(
  content: string,
  selectedText: string,
  x: number,
  y: number
): SynonBiomedArtifactTextSelection {
  const startOffset = content.indexOf(selectedText);
  if (startOffset < 0) {
    return {
      type: 'text_selection',
      text: selectedText,
      x,
      y,
      startLine: null,
      startColumn: null,
      endLine: null,
      endColumn: null,
      selectionPrefix: null,
      pageNumber: null,
    };
  }
  const endOffset = startOffset + selectedText.length;
  const start = offsetToLineColumn(content, startOffset);
  const end = offsetToLineColumn(content, endOffset);
  return {
    type: 'text_selection',
    text: selectedText,
    x,
    y,
    startLine: start.line,
    startColumn: start.column,
    endLine: end.line,
    endColumn: end.column,
    selectionPrefix: content.slice(Math.max(0, startOffset - 80), startOffset) || null,
    pageNumber: null,
  };
}

function offsetToLineColumn(content: string, offset: number): { line: number; column: number } {
  const before = content.slice(0, offset);
  const lines = before.split('\n');
  return { line: lines.length, column: (lines.at(-1)?.length ?? 0) + 1 };
}
