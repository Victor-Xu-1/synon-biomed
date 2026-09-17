import { Button } from '@arco-design/web-react';
import { AddOne, Delete } from '@icon-park/react';
import React, { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  appendDelimitedColumn,
  appendDelimitedRow,
  parseDelimitedTableDocument,
  removeDelimitedColumn,
  removeDelimitedRow,
  serializeDelimitedTableDocument,
  updateDelimitedCell,
  type DelimitedTableDocument,
} from './delimitedTableModel';
import './delimitedTableEditor.css';

type DelimitedTableEditorProps = {
  content: string;
  filename: string;
  onChange: (content: string) => void;
};

type CellSelection = { row: number; column: number } | null;

const DelimitedTableEditor: React.FC<DelimitedTableEditorProps> = ({ content, filename, onChange }) => {
  const { t } = useTranslation();
  const [documentValue, setDocumentValue] = useState<DelimitedTableDocument>(() =>
    parseDelimitedTableDocument(content, filename)
  );
  const [selection, setSelection] = useState<CellSelection>(null);
  const lastEmittedRef = useRef<string | null>(null);
  const lastPropContentRef = useRef(content);
  const serializedValue = useMemo(() => serializeDelimitedTableDocument(documentValue), [documentValue]);

  useEffect(() => {
    const propChanged = content !== lastPropContentRef.current;
    lastPropContentRef.current = content;

    if (lastEmittedRef.current && !propChanged) return;
    if (lastEmittedRef.current) {
      // Parent acknowledgement clears the pending emission. A different prop
      // value is an authoritative external refresh (cancel, tab replay, or a
      // newer artifact version) and must replace local editor state instead of
      // being swallowed while an earlier change is waiting to round-trip.
      lastEmittedRef.current = null;
    }
    if (content === serializedValue) return;
    setDocumentValue(parseDelimitedTableDocument(content, filename));
    setSelection(null);
  }, [content, filename, serializedValue]);

  const commit = (next: DelimitedTableDocument) => {
    const nextContent = serializeDelimitedTableDocument(next);
    lastEmittedRef.current = nextContent;
    setDocumentValue(next);
    onChange(nextContent);
  };

  const removeRow = () => {
    if (!selection) return;
    commit(removeDelimitedRow(documentValue, selection.row));
    setSelection(null);
  };

  const removeColumn = () => {
    if (!selection) return;
    commit(removeDelimitedColumn(documentValue, selection.column));
    setSelection(null);
  };

  return (
    <section className='delimited-table-editor' aria-label={t('preview.tableEditor.namedEditor', { name: filename })}>
      <div className='delimited-table-editor__toolbar' role='toolbar' aria-label={t('preview.tableEditor.toolbar')}>
        <Button
          size='mini'
          type='text'
          icon={<AddOne size={14} />}
          onClick={() => commit(appendDelimitedRow(documentValue))}
        >
          {t('preview.tableEditor.addRow')}
        </Button>
        <Button
          size='mini'
          type='text'
          icon={<AddOne size={14} />}
          onClick={() => commit(appendDelimitedColumn(documentValue))}
        >
          {t('preview.tableEditor.addColumn')}
        </Button>
        <span className='delimited-table-editor__separator' />
        <Button size='mini' type='text' icon={<Delete size={14} />} disabled={!selection} onClick={removeRow}>
          {t('preview.tableEditor.deleteRow')}
        </Button>
        <Button size='mini' type='text' icon={<Delete size={14} />} disabled={!selection} onClick={removeColumn}>
          {t('preview.tableEditor.deleteColumn')}
        </Button>
        <span className='delimited-table-editor__dimensions'>
          {t('preview.tableEditor.dimensions', {
            rows: documentValue.rows.length,
            columns: documentValue.rows[0]?.length || 1,
          })}
        </span>
      </div>
      <div className='delimited-table-editor__viewport'>
        <table className='delimited-table-editor__table'>
          <tbody>
            {documentValue.rows.map((row, rowIndex) => (
              <tr key={rowIndex}>
                <th className='delimited-table-editor__row-number' scope='row'>
                  {rowIndex + 1}
                </th>
                {row.map((cell, columnIndex) => {
                  const selected = selection?.row === rowIndex && selection.column === columnIndex;
                  const Cell = rowIndex === 0 ? 'th' : 'td';
                  return (
                    <Cell key={columnIndex} className={selected ? 'is-selected' : undefined}>
                      <input
                        value={cell}
                        aria-label={t('preview.tableEditor.cell', {
                          row: rowIndex + 1,
                          column: columnIndex + 1,
                        })}
                        onFocus={() => setSelection({ row: rowIndex, column: columnIndex })}
                        onChange={(event) =>
                          commit(updateDelimitedCell(documentValue, rowIndex, columnIndex, event.target.value))
                        }
                      />
                    </Cell>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
};

export default DelimitedTableEditor;
