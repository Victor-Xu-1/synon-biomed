export type ScientificTableSheet = {
  name: string;
  rows: string[][];
  sampled: boolean;
};

export type ScientificTableParseOptions = {
  filename?: string;
};

export const MAX_TABLE_PREVIEW_ROWS = 250;
export const MAX_TABLE_PREVIEW_COLUMNS = 120;
const MAX_DELIMITED_PREVIEW_BYTES = 4 * 1024 * 1024;

type PreparedTableData = {
  data: ArrayBuffer;
  truncated: boolean;
};

type DelimitedFormat = 'csv' | 'tsv' | 'sam';

export async function parseScientificTable(
  data: ArrayBuffer,
  options: ScientificTableParseOptions = {}
): Promise<ScientificTableSheet[]> {
  const prepared = await prepareTableData(data, options.filename);
  const delimited = delimitedFormat(options.filename);
  if (delimited === 'sam') {
    const source = decodeDelimitedText(prepared.data, prepared.truncated);
    return [parseSamTable(source, prepared.truncated)];
  }

  const XLSX = await import('xlsx-republish');
  const workbook = delimited
    ? XLSX.read(decodeDelimitedText(prepared.data, prepared.truncated), {
        type: 'string',
        cellDates: true,
        FS: delimited === 'tsv' ? '\t' : ',',
      })
    : XLSX.read(prepared.data, { type: 'array', cellDates: true });

  return workbook.SheetNames.map((name) => {
    const worksheet = workbook.Sheets[name];
    const rawRows = worksheet
      ? XLSX.utils.sheet_to_json<unknown[]>(worksheet, {
          header: 1,
          raw: false,
          defval: '',
          blankrows: false,
        })
      : [];
    const sourceWidth = rawRows.reduce((largest, row) => Math.max(largest, row.length), 0);
    const width = Math.min(sourceWidth, MAX_TABLE_PREVIEW_COLUMNS);
    const visibleRows = rawRows.slice(0, MAX_TABLE_PREVIEW_ROWS + 1);
    return {
      name,
      rows: visibleRows.map((row) => Array.from({ length: width }, (_, index) => formatTableCell(row[index]))),
      sampled:
        prepared.truncated || rawRows.length > MAX_TABLE_PREVIEW_ROWS + 1 || sourceWidth > MAX_TABLE_PREVIEW_COLUMNS,
    };
  }).filter((sheet) => sheet.rows.length > 0);
}

function delimitedFormat(filename?: string): DelimitedFormat | null {
  const normalized = (filename?.trim().toLowerCase() ?? '').replace(/\.gz$/, '');
  if (normalized.endsWith('.csv')) return 'csv';
  if (normalized.endsWith('.tsv')) return 'tsv';
  if (normalized.endsWith('.sam')) return 'sam';
  return null;
}

async function prepareTableData(data: ArrayBuffer, filename?: string): Promise<PreparedTableData> {
  if (!delimitedFormat(filename)) return { data, truncated: false };
  if (!filename?.trim().toLowerCase().endsWith('.gz')) {
    return data.byteLength <= MAX_DELIMITED_PREVIEW_BYTES
      ? { data, truncated: false }
      : { data: data.slice(0, MAX_DELIMITED_PREVIEW_BYTES), truncated: true };
  }

  const decompressor = new DecompressionStream('gzip');
  const stream = new Blob([new Uint8Array(data)]).stream().pipeThrough(decompressor);
  return readStreamPrefix(stream, MAX_DELIMITED_PREVIEW_BYTES);
}

async function readStreamPrefix(stream: ReadableStream<Uint8Array>, limit: number): Promise<PreparedTableData> {
  const reader = stream.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  let truncated = false;

  try {
    while (total < limit) {
      // Stream reads are intentionally sequential so memory stays bounded.
      // oxlint-disable-next-line eslint/no-await-in-loop
      const { done, value } = await reader.read();
      if (done) break;
      const remaining = limit - total;
      if (value.byteLength > remaining) {
        chunks.push(value.subarray(0, remaining));
        total += remaining;
        truncated = true;
        break;
      }
      chunks.push(value);
      total += value.byteLength;
    }

    if (total === limit && !truncated) {
      const { done } = await reader.read();
      truncated = !done;
    }
  } finally {
    if (truncated) await reader.cancel();
    reader.releaseLock();
  }

  const bytes = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return { data: bytes.buffer, truncated };
}

function decodeDelimitedText(data: ArrayBuffer, truncated: boolean): string {
  const bytes = new Uint8Array(data);
  let text: string;
  if (bytes[0] === 0xff && bytes[1] === 0xfe) {
    text = new TextDecoder('utf-16le', { fatal: !truncated }).decode(bytes.subarray(2));
  } else if (bytes[0] === 0xfe && bytes[1] === 0xff) {
    text = new TextDecoder('utf-16be', { fatal: !truncated }).decode(bytes.subarray(2));
  } else {
    text = new TextDecoder('utf-8', { fatal: !truncated }).decode(bytes);
  }

  if (!truncated) return text;
  const lastLineBreak = Math.max(text.lastIndexOf('\n'), text.lastIndexOf('\r'));
  return lastLineBreak > 0 ? text.slice(0, lastLineBreak + 1) : text;
}

const SAM_COLUMNS = ['QNAME', 'FLAG', 'RNAME', 'POS', 'MAPQ', 'CIGAR', 'RNEXT', 'PNEXT', 'TLEN', 'SEQ', 'QUAL'];

function parseSamTable(source: string, truncated: boolean): ScientificTableSheet {
  const records = source
    .split(/\r?\n/)
    .filter((line) => line.length > 0 && !/^(?:@HD|@SQ|@RG|@PG|@CO)(?:\t|$)/.test(line))
    .map((line) => line.split('\t'));
  const sourceWidth = records.reduce((largest, row) => Math.max(largest, row.length), SAM_COLUMNS.length);
  const width = Math.min(sourceWidth, MAX_TABLE_PREVIEW_COLUMNS);
  const visibleRecords = records.slice(0, MAX_TABLE_PREVIEW_ROWS);
  const headers = Array.from(
    { length: width },
    (_, index) => SAM_COLUMNS[index] ?? `TAG_${index - SAM_COLUMNS.length + 1}`
  );

  return {
    name: 'alignments',
    rows: [headers, ...visibleRecords.map((row) => Array.from({ length: width }, (_, index) => row[index] ?? ''))],
    sampled: truncated || records.length > MAX_TABLE_PREVIEW_ROWS || sourceWidth > MAX_TABLE_PREVIEW_COLUMNS,
  };
}

export function getScientificTableColumns(sheet: ScientificTableSheet): string[] {
  const width = sheet.rows.reduce((largest, row) => Math.max(largest, row.length), 0);
  const header = sheet.rows[0] ?? [];
  return Array.from({ length: width }, (_, index) => {
    const value = header[index]?.trim();
    return value || columnName(index);
  });
}

function formatTableCell(value: unknown): string {
  if (value == null) return '';
  if (value instanceof Date) return value.toISOString();
  if (typeof value === 'object') {
    try {
      return JSON.stringify(value);
    } catch {
      return String(value);
    }
  }
  return String(value);
}

function columnName(index: number): string {
  let value = index + 1;
  let result = '';
  while (value > 0) {
    value -= 1;
    result = String.fromCharCode(65 + (value % 26)) + result;
    value = Math.floor(value / 26);
  }
  return result;
}
