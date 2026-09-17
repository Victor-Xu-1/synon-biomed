export type NotebookText = string | string[];

export type NotebookOutput = {
  output_type?: string;
  name?: string;
  text?: NotebookText;
  ename?: string;
  evalue?: string;
  traceback?: string[];
  data?: Record<string, NotebookText>;
};

export type NotebookCell = {
  cell_type: 'markdown' | 'code' | 'raw' | string;
  metadata?: Record<string, unknown>;
  source?: NotebookText;
  execution_count?: number | null;
  outputs?: NotebookOutput[];
};

export type NotebookDocument = {
  nbformat: number;
  nbformatMinor?: number;
  language: string;
  kernelLabel: string;
  metadata: Record<string, unknown>;
  cells: NotebookCell[];
};

type NotebookMetadata = {
  language_info?: { name?: string };
  kernelspec?: { display_name?: string; language?: string; name?: string };
};

export function parseNotebookDocument(source: string): NotebookDocument {
  const value = JSON.parse(source) as {
    nbformat?: unknown;
    nbformat_minor?: unknown;
    metadata?: NotebookMetadata;
    cells?: unknown;
  };
  if (!value || typeof value !== 'object') throw new ScientificPreviewError('parse-failed');
  if (!Array.isArray(value.cells)) {
    if (typeof value.nbformat === 'number' && value.nbformat > 0 && value.nbformat < 4) {
      throw new ScientificPreviewError('parse-failed');
    }
    throw new ScientificPreviewError('parse-failed');
  }
  const nbformat = typeof value.nbformat === 'number' ? value.nbformat : 4;
  if (nbformat < 4) {
    throw new ScientificPreviewError('parse-failed');
  }
  const metadata = value.metadata ?? {};
  const language =
    metadata.language_info?.name ?? metadata.kernelspec?.language ?? metadata.kernelspec?.name ?? 'python';
  return {
    nbformat,
    nbformatMinor: typeof value.nbformat_minor === 'number' ? value.nbformat_minor : undefined,
    language,
    kernelLabel: metadata.kernelspec?.display_name ?? language,
    metadata: metadata as Record<string, unknown>,
    cells: value.cells.filter((cell): cell is NotebookCell => Boolean(cell && typeof cell === 'object')),
  };
}

export function normalizeNotebookText(value: NotebookText | undefined): string {
  if (typeof value === 'string') return value;
  return Array.isArray(value) ? value.filter((item) => typeof item === 'string').join('') : '';
}

export function stripNotebookAnsi(value: string): string {
  let result = '';
  let index = 0;
  while (index < value.length) {
    if (value.charCodeAt(index) !== 27) {
      result += value[index];
      index += 1;
      continue;
    }

    index += 1;
    const marker = value[index];
    if (marker === '[') {
      index += 1;
      while (index < value.length) {
        const code = value.charCodeAt(index);
        index += 1;
        if (code >= 64 && code <= 126) break;
      }
    } else if (marker === ']') {
      index += 1;
      while (index < value.length) {
        if (value.charCodeAt(index) === 7) {
          index += 1;
          break;
        }
        if (value.charCodeAt(index) === 27 && value[index + 1] === '\\') {
          index += 2;
          break;
        }
        index += 1;
      }
    } else {
      index += 1;
    }
  }
  return result;
}

export function sliceNotebookCells(cells: NotebookCell[], limit: number) {
  if (cells.length <= limit) return { head: [...cells], tail: [] as NotebookCell[], hidden: 0 };
  const headSize = Math.floor(limit / 2);
  const tailSize = limit - headSize;
  return {
    head: cells.slice(0, headSize),
    tail: cells.slice(cells.length - tailSize),
    hidden: cells.length - limit,
  };
}
import { ScientificPreviewError } from './scientificPreviewError';
