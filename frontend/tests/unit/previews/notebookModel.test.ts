import {
  normalizeNotebookText,
  parseNotebookDocument,
  sliceNotebookCells,
  stripNotebookAnsi,
} from '@/renderer/pages/conversation/Preview/components/viewers/notebookModel';
import { describe, expect, it } from 'vitest';

describe('notebookModel', () => {
  it('parses nbformat 4 metadata, markdown, code, and outputs', () => {
    const notebook = parseNotebookDocument(
      JSON.stringify({
        nbformat: 4,
        nbformat_minor: 5,
        metadata: {
          kernelspec: { display_name: 'Python 3 (ipykernel)', language: 'python', name: 'python3' },
        },
        cells: [
          { cell_type: 'markdown', metadata: {}, source: ['# Synon Biomed\n', 'Notebook preview'] },
          {
            cell_type: 'code',
            execution_count: 7,
            metadata: {},
            source: ['print("STAT6")'],
            outputs: [{ output_type: 'stream', name: 'stdout', text: ['STAT6\n'] }],
          },
        ],
      })
    );

    expect(notebook.language).toBe('python');
    expect(notebook.kernelLabel).toBe('Python 3 (ipykernel)');
    expect(notebook.cells).toHaveLength(2);
    expect(normalizeNotebookText(notebook.cells[0]?.source)).toBe('# Synon Biomed\nNotebook preview');
  });

  it('rejects legacy notebooks with a stable typed parse error', () => {
    let thrown: unknown;
    try {
      parseNotebookDocument(JSON.stringify({ nbformat: 3, worksheets: [] }));
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toMatchObject({ name: 'ScientificPreviewError', code: 'parse-failed' });
  });

  it('keeps the first and last 100 cells for oversized notebooks', () => {
    const cells = Array.from({ length: 205 }, (_, index) => ({
      cell_type: 'raw' as const,
      metadata: {},
      source: [`cell ${index}`],
    }));
    const result = sliceNotebookCells(cells, 200);

    expect(result.head).toHaveLength(100);
    expect(result.tail).toHaveLength(100);
    expect(result.hidden).toBe(5);
    expect(normalizeNotebookText(result.tail[0]?.source)).toBe('cell 105');
  });

  it('normalizes source arrays and strips terminal ANSI sequences', () => {
    expect(normalizeNotebookText(['alpha\n', 'beta'])).toBe('alpha\nbeta');
    expect(stripNotebookAnsi('\u001b[31mfailed\u001b[0m')).toBe('failed');
  });
});
