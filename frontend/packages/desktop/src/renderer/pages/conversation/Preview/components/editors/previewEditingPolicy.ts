import type { PreviewTab } from '../../context/PreviewContext';
import { parseDelimitedTableDocument } from './delimitedTableModel';

export type PreviewEditingMode = 'document' | 'source' | 'delimited-table';

export const MAX_DELIMITED_EDITOR_CHARACTERS = 1_000_000;
export const MAX_DELIMITED_EDITOR_ROWS = 250;
export const MAX_DELIMITED_EDITOR_COLUMNS = 64;

/**
 * One authority for preview editability. Both the single preview and preview
 * board consume this policy so a format cannot silently become editable in
 * one presentation while remaining read-only in the other.
 */
export function getPreviewEditingMode(tab: PreviewTab): PreviewEditingMode | null {
  const metadata = tab.metadata;
  if (metadata?.editable === false || metadata?.truncated || metadata?.missingFile) return null;
  if (!hasDurableSaveTarget(tab)) return null;

  if (tab.content_type === 'markdown' || tab.content_type === 'html') return 'document';
  if (tab.content_type === 'code' || tab.content_type === 'latex') return 'source';
  if (tab.content_type !== 'table') return null;

  const filename = (metadata?.file_name || tab.title).trim().toLowerCase();
  const isEditableDelimitedText = filename.endsWith('.csv') || filename.endsWith('.tsv');
  if (!isEditableDelimitedText || tab.content.length > MAX_DELIMITED_EDITOR_CHARACTERS) return null;
  const documentValue = parseDelimitedTableDocument(tab.content, filename);
  if (
    documentValue.rows.length > MAX_DELIMITED_EDITOR_ROWS ||
    documentValue.rows.some((row) => row.length > MAX_DELIMITED_EDITOR_COLUMNS)
  ) {
    return null;
  }
  return 'delimited-table';
}

export function isPreviewTabEditable(tab: PreviewTab): boolean {
  return getPreviewEditingMode(tab) !== null;
}

function hasDurableSaveTarget(tab: PreviewTab): boolean {
  return Boolean(tab.metadata?.artifactId || (tab.metadata?.file_path && tab.metadata?.workspace));
}
