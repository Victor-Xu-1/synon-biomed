import {
  getScientificTableColumns,
  MAX_TABLE_PREVIEW_ROWS,
  parseScientificTable,
} from '@/renderer/pages/conversation/Preview/components/viewers/scientificTableModel';
import { gzipSync } from 'node:zlib';
import { describe, expect, it } from 'vitest';

describe('scientific table model', () => {
  it('parses delimited scientific data with SheetJS and preserves rectangular rows', async () => {
    const data = new TextEncoder().encode('gene,,score\nSTAT6,kinase,0.91\nCRBN,,0.83').buffer;
    const sheets = await parseScientificTable(data, { filename: 'scores.csv' });

    expect(sheets).toHaveLength(1);
    expect(sheets[0].rows).toEqual([
      ['gene', '', 'score'],
      ['STAT6', 'kinase', '0.91'],
      ['CRBN', '', '0.83'],
    ]);
    expect(sheets[0].sampled).toBe(false);
    expect(getScientificTableColumns(sheets[0])).toEqual(['gene', 'B', 'score']);
  });

  it('decodes UTF-8 delimited artifacts exactly instead of treating bytes as Windows-1252', async () => {
    const data = new TextEncoder().encode('unit,claim\nÅngström,mechanism — confirmed\n中文,NEK7–NLRP3').buffer;

    const sheets = await parseScientificTable(data, { filename: 'evidence.csv' });

    expect(sheets[0].rows).toEqual([
      ['unit', 'claim'],
      ['Ångström', 'mechanism — confirmed'],
      ['中文', 'NEK7–NLRP3'],
    ]);
  });

  it('bounds very wide delimited previews while keeping the first cells useful', async () => {
    const header = Array.from({ length: 140 }, (_, index) => `sample_${index + 1}`).join(',');
    const row = Array.from({ length: 140 }, (_, index) => String(index + 1)).join(',');
    const data = new TextEncoder().encode(`${header}\n${row}`).buffer;

    const sheets = await parseScientificTable(data, { filename: 'matrix.csv' });

    expect(sheets[0].rows).toHaveLength(2);
    expect(sheets[0].rows[0]).toHaveLength(120);
    expect(sheets[0].rows[0][0]).toBe('sample_1');
    expect(sheets[0].rows[0][119]).toBe('sample_120');
    expect(sheets[0].sampled).toBe(true);
  });

  it('decompresses only a bounded prefix of large gzip tables and keeps complete rows', async () => {
    const longCell = Array.from(
      { length: 2_500 },
      (_, index) => `${index.toString(36)}-${(index * 7919).toString(36)}`
    ).join('|');
    const csv = ['id,value', ...Array.from({ length: 360 }, (_, index) => `${index + 1},${longCell}`)].join('\n');
    const compressed = gzipSync(csv);
    const data = compressed.buffer.slice(compressed.byteOffset, compressed.byteOffset + compressed.byteLength);

    const sheets = await parseScientificTable(data, { filename: 'large.csv.gz' });

    expect(sheets[0].sampled).toBe(true);
    expect(sheets[0].rows.length).toBeLessThanOrEqual(MAX_TABLE_PREVIEW_ROWS + 1);
    expect(sheets[0].rows[0]).toEqual(['id', 'value']);
    expect(sheets[0].rows.at(-1)).toHaveLength(2);
  });

  it('parses SAM headers away from alignment rows and names optional tags', async () => {
    const sam = [
      '@HD\tVN:1.6\tSO:coordinate',
      '@SQ\tSN:chr1\tLN:248956422',
      'read-001\t0\tchr1\t101\t60\t4M\t*\t0\t0\tACGT\tIIII\tNM:i:0\tAS:i:4',
    ].join('\n');
    const data = new TextEncoder().encode(sam).buffer;

    const sheets = await parseScientificTable(data, { filename: 'variants.sam' });

    expect(sheets[0]).toEqual({
      name: 'alignments',
      rows: [
        ['QNAME', 'FLAG', 'RNAME', 'POS', 'MAPQ', 'CIGAR', 'RNEXT', 'PNEXT', 'TLEN', 'SEQ', 'QUAL', 'TAG_1', 'TAG_2'],
        ['read-001', '0', 'chr1', '101', '60', '4M', '*', '0', '0', 'ACGT', 'IIII', 'NM:i:0', 'AS:i:4'],
      ],
      sampled: false,
    });
  });

  it('decompresses SAM gzip input without exposing binary bytes to the table parser', async () => {
    const sam = '@HD\tVN:1.6\nread-001\t0\tchr1\t1\t20\t4M\t*\t0\t0\tACGT\tIIII';
    const compressed = gzipSync(sam);
    const data = compressed.buffer.slice(compressed.byteOffset, compressed.byteOffset + compressed.byteLength);

    const sheets = await parseScientificTable(data, { filename: 'reads.sam.gz' });

    expect(sheets[0].rows[1]).toEqual(['read-001', '0', 'chr1', '1', '20', '4M', '*', '0', '0', 'ACGT', 'IIII']);
  });

  it('keeps a usable preview when a bounded UTF-8 prefix ends inside a multibyte character', async () => {
    const byteLimit = 4 * 1024 * 1024;
    const prefix = 'id,value\n1,ok\n2,';
    const padding = 'x'.repeat(byteLimit - 1 - new TextEncoder().encode(prefix).byteLength);
    const data = new TextEncoder().encode(`${prefix}${padding}中文`).buffer;

    const sheets = await parseScientificTable(data, { filename: 'large.csv' });

    expect(sheets[0].sampled).toBe(true);
    expect(sheets[0].rows[0]).toEqual(['id', 'value']);
    expect(sheets[0].rows.at(-1)).toHaveLength(2);
  });
});
