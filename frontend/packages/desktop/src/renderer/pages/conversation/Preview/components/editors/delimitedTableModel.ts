export type DelimitedTableDocument = {
  delimiter: ',' | '\t';
  newline: '\n' | '\r\n';
  terminalNewline: boolean;
  rows: string[][];
};

export function parseDelimitedTableDocument(source: string, filename: string): DelimitedTableDocument {
  const delimiter = filename.trim().toLowerCase().endsWith('.tsv') ? '\t' : ',';
  const newline = source.includes('\r\n') ? '\r\n' : '\n';
  const terminalNewline = source.endsWith('\n');
  const rows: string[][] = [];
  let row: string[] = [];
  let field = '';
  let quoted = false;

  for (let index = 0; index < source.length; index += 1) {
    const character = source[index];
    if (quoted) {
      if (character === '"' && source[index + 1] === '"') {
        field += '"';
        index += 1;
      } else if (character === '"') {
        quoted = false;
      } else {
        field += character;
      }
      continue;
    }

    if (character === '"' && field.length === 0) {
      quoted = true;
    } else if (character === delimiter) {
      row.push(field);
      field = '';
    } else if (character === '\n') {
      row.push(field);
      rows.push(row);
      row = [];
      field = '';
    } else if (character !== '\r') {
      field += character;
    }
  }

  if (field || row.length > 0 || (!terminalNewline && source.length > 0)) {
    row.push(field);
    rows.push(row);
  }

  return {
    delimiter,
    newline,
    terminalNewline,
    rows: normalizeRows(rows.length > 0 ? rows : [['']]),
  };
}

export function serializeDelimitedTableDocument(documentValue: DelimitedTableDocument): string {
  const body = normalizeRows(documentValue.rows)
    .map((row) => row.map((cell) => serializeCell(cell, documentValue.delimiter)).join(documentValue.delimiter))
    .join(documentValue.newline);
  return documentValue.terminalNewline ? `${body}${documentValue.newline}` : body;
}

export function updateDelimitedCell(
  documentValue: DelimitedTableDocument,
  rowIndex: number,
  columnIndex: number,
  value: string
): DelimitedTableDocument {
  const rows = normalizeRows(documentValue.rows);
  rows[rowIndex][columnIndex] = value;
  return { ...documentValue, rows };
}

export function appendDelimitedRow(documentValue: DelimitedTableDocument): DelimitedTableDocument {
  const rows = normalizeRows(documentValue.rows);
  const width = rows[0]?.length || 1;
  return {
    ...documentValue,
    rows: [...rows, Array.from({ length: width }, () => '')],
  };
}

export function appendDelimitedColumn(documentValue: DelimitedTableDocument): DelimitedTableDocument {
  return { ...documentValue, rows: normalizeRows(documentValue.rows).map((row) => row.concat('')) };
}

export function removeDelimitedRow(documentValue: DelimitedTableDocument, rowIndex: number): DelimitedTableDocument {
  const rows = normalizeRows(documentValue.rows).filter((_, index) => index !== rowIndex);
  return { ...documentValue, rows: rows.length > 0 ? rows : [['']] };
}

export function removeDelimitedColumn(
  documentValue: DelimitedTableDocument,
  columnIndex: number
): DelimitedTableDocument {
  const rows = normalizeRows(documentValue.rows);
  if ((rows[0]?.length || 1) <= 1) return documentValue;
  return {
    ...documentValue,
    rows: rows.map((row) => row.filter((_, index) => index !== columnIndex)),
  };
}

function normalizeRows(rows: string[][]): string[][] {
  const width = Math.max(1, ...rows.map((row) => row.length));
  return rows.map((row) => Array.from({ length: width }, (_, index) => row[index] ?? ''));
}

function serializeCell(value: string, delimiter: string): string {
  if (!value.includes(delimiter) && !/["\r\n]/.test(value) && value.trim() === value) return value;
  return `"${value.replaceAll('"', '""')}"`;
}
