import { Empty, Spin } from '@arco-design/web-react';
import React, { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { TableVirtuoso } from 'react-virtuoso';
import { getScientificTableColumns, parseScientificTable, type ScientificTableSheet } from './scientificTableModel';
import {
  logScientificPreviewError,
  resolveScientificPreviewError,
  ScientificPreviewError,
  scientificPreviewErrorKey,
} from './scientificPreviewError';

type SynonBiomedTableViewerProps = {
  contentUrl?: string;
  content?: string;
  filename: string;
};

const MAX_REMOTE_TABLE_BYTES = 64 * 1024 * 1024;

const SynonBiomedTableViewer: React.FC<SynonBiomedTableViewerProps> = ({ contentUrl, content, filename }) => {
  const { i18n, t } = useTranslation();
  const [sheets, setSheets] = useState<ScientificTableSheet[]>([]);
  const [activeSheetIndex, setActiveSheetIndex] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<ScientificPreviewError | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    setLoading(true);
    setError(null);
    setSheets([]);
    setActiveSheetIndex(0);

    void loadTableData({ contentUrl, content, signal: controller.signal })
      .then((data) => parseScientificTable(data, { filename }))
      .then((nextSheets) => {
        if (!active) return;
        setSheets(nextSheets);
      })
      .catch((reason) => {
        if (!active || controller.signal.aborted) return;
        logScientificPreviewError('[SynonBiomedTableViewer] Failed to load table', reason, 'parse-failed');
        setError(resolveScientificPreviewError(reason, 'parse-failed'));
      })
      .finally(() => {
        if (active) setLoading(false);
      });

    return () => {
      active = false;
      controller.abort();
    };
  }, [content, contentUrl, filename]);

  const activeSheet = sheets[activeSheetIndex] ?? null;
  const columns = useMemo(() => (activeSheet ? getScientificTableColumns(activeSheet) : []), [activeSheet]);
  const dataRows = activeSheet?.rows.slice(1) ?? [];

  if (loading) {
    return (
      <div
        className='size-full min-h-360px flex-center'
        aria-label={t('preview.scientific.loadingNamed', { name: filename })}
      >
        <Spin />
      </div>
    );
  }

  if (error) {
    return (
      <Empty
        className='size-full min-h-360px flex-center'
        description={t(scientificPreviewErrorKey(error), {
          kind: t('preview.scientific.table.kind'),
          ...error.details,
        })}
      />
    );
  }

  if (!activeSheet || columns.length === 0) {
    return <Empty className='size-full min-h-360px flex-center' description={t('preview.scientific.table.empty')} />;
  }

  return (
    <section
      className='preview-table size-full min-h-360px flex flex-col bg-1'
      aria-label={t('preview.scientific.table.preview')}
    >
      <header className='preview-table__toolbar min-h-46px flex items-center gap-6px px-16px border-b border-solid border-[var(--color-border-2)]'>
        {sheets.length > 1 &&
          sheets.map((sheet, index) => (
            <button
              key={`${sheet.name}-${index}`}
              type='button'
              aria-pressed={index === activeSheetIndex}
              className={`preview-table__sheet h-30px px-10px border-0 text-12px cursor-pointer ${index === activeSheetIndex ? 'bg-fill-2 text-t-primary font-[600]' : 'bg-transparent text-t-secondary hover:bg-fill-1'}`}
              onClick={() => setActiveSheetIndex(index)}
            >
              {sheet.name}
            </button>
          ))}
        <span className='preview-table__dimensions ml-auto text-12px text-t-tertiary'>
          {activeSheet.sampled
            ? t('preview.scientific.table.sampledDimensions', {
                rows: dataRows.length.toLocaleString(i18n.resolvedLanguage),
                columns: columns.length.toLocaleString(i18n.resolvedLanguage),
              })
            : t('preview.scientific.table.dimensions', {
                rows: dataRows.length.toLocaleString(i18n.resolvedLanguage),
                columns: columns.length.toLocaleString(i18n.resolvedLanguage),
              })}
        </span>
      </header>
      <div className='preview-table__viewport min-h-0 flex-1 overflow-hidden'>
        <TableVirtuoso
          data={dataRows}
          fixedHeaderContent={() => (
            <tr>
              {columns.map((column, index) => (
                <th
                  key={`${column}-${index}`}
                  className='preview-table__heading min-w-140px max-w-420px text-left font-[600] text-t-primary whitespace-nowrap'
                >
                  {column}
                </th>
              ))}
            </tr>
          )}
          itemContent={(rowIndex, row) =>
            columns.map((_, columnIndex) => (
              <td
                key={`${rowIndex}-${columnIndex}`}
                className='preview-table__cell max-w-420px text-t-secondary whitespace-nowrap overflow-hidden text-ellipsis'
                title={row[columnIndex]}
              >
                {row[columnIndex]}
              </td>
            ))
          }
          components={{
            Table: (props) => <table {...props} className='preview-table__table w-full border-collapse text-left' />,
            TableRow: (props) => <tr {...props} className='preview-table__row' />,
          }}
        />
      </div>
    </section>
  );
};

async function loadTableData({
  contentUrl,
  content,
  signal,
}: {
  contentUrl?: string;
  content?: string;
  signal: AbortSignal;
}): Promise<ArrayBuffer> {
  if (contentUrl) {
    const response = await fetch(contentUrl, { signal });
    if (!response.ok) throw new ScientificPreviewError('request-failed', { status: response.status });
    const declaredSize = Number(response.headers.get('content-length') ?? 0);
    if (declaredSize > MAX_REMOTE_TABLE_BYTES) {
      throw new ScientificPreviewError('too-large', { limit: '64 MB' });
    }
    return readBoundedResponse(response, MAX_REMOTE_TABLE_BYTES, signal);
  }
  if (content != null) return new TextEncoder().encode(content).buffer;
  throw new ScientificPreviewError('missing-content');
}

async function readBoundedResponse(response: Response, limit: number, signal: AbortSignal): Promise<ArrayBuffer> {
  if (!response.body) {
    const data = await response.arrayBuffer();
    if (data.byteLength > limit) throw new ScientificPreviewError('too-large', { limit: '64 MB' });
    return data;
  }

  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;

  try {
    while (true) {
      if (signal.aborted) throw new DOMException('Aborted', 'AbortError');
      // Stream reads are intentionally sequential so the input limit is enforceable.
      // oxlint-disable-next-line eslint/no-await-in-loop
      const { done, value } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > limit) {
        // oxlint-disable-next-line eslint/no-await-in-loop
        await reader.cancel();
        throw new ScientificPreviewError('too-large', { limit: '64 MB' });
      }
      chunks.push(value);
    }
  } finally {
    reader.releaseLock();
  }

  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return bytes.buffer;
}

export default SynonBiomedTableViewer;
