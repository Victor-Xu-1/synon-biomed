import type { Annotation, Seq } from 'seqparse';
import { ScientificPreviewError } from './scientificPreviewError';

export type SequenceFormat = 'genbank' | 'fasta' | 'fastq' | 'snapgene' | 'sbol' | 'jbei' | 'unknown';

export type FastqQualityStats = {
  min: number;
  max: number;
  mean: number;
};

export type SequenceDocument = {
  name: string;
  seq: string;
  type: Seq['type'];
  annotations: Annotation[];
  format: SequenceFormat;
  recordCount?: number;
  qualityStats?: FastqQualityStats;
};

export function resolveSequenceFormat(filename: string): SequenceFormat {
  const normalized = stripCompressionSuffix(filename.toLowerCase());
  if (hasExtension(normalized, ['.gb', '.gbk', '.genbank'])) return 'genbank';
  if (hasExtension(normalized, ['.fasta', '.fa', '.fna', '.fas', '.faa'])) return 'fasta';
  if (hasExtension(normalized, ['.fastq', '.fq'])) return 'fastq';
  if (normalized.endsWith('.dna')) return 'snapgene';
  if (normalized.endsWith('.sbol')) return 'sbol';
  if (normalized.endsWith('.jbei')) return 'jbei';
  return 'unknown';
}

export async function parseSequenceDocument(input: string | ArrayBuffer, filename: string): Promise<SequenceDocument> {
  const format = resolveSequenceFormat(filename);
  const parserFilename = stripCompressionSuffix(filename);
  if (format === 'fastq') return parseFastqDocument(input, parserFilename);

  const { default: seqparse } = await import('seqparse');
  const parsed =
    input instanceof ArrayBuffer
      ? await seqparse('', { fileName: parserFilename, source: input })
      : await seqparse(input, { fileName: parserFilename });

  if (!parsed?.seq) throw new ScientificPreviewError('empty-content');
  return {
    name: parsed.name || parserFilename.replace(/\.[^/.]+$/, ''),
    seq: parsed.seq,
    type: parsed.type,
    annotations: parsed.annotations ?? [],
    format,
  };
}

async function parseFastqDocument(input: string | ArrayBuffer, filename: string): Promise<SequenceDocument> {
  const source =
    typeof input === 'string' ? input : new TextDecoder('utf-8', { fatal: true }).decode(new Uint8Array(input));
  const records = parseFastqRecords(source);
  const first = records.first;
  if (!first) throw new ScientificPreviewError('empty-content');

  const parsedType = inferSequenceType(first.sequence);
  return {
    name: first.name || filename.replace(/\.[^/.]+$/, ''),
    seq: first.sequence,
    type: parsedType,
    annotations: [],
    format: 'fastq',
    recordCount: records.count,
    qualityStats: records.qualityStats,
  };
}

type FastqRecord = { name: string; sequence: string };

function parseFastqRecords(source: string): {
  count: number;
  first: FastqRecord | null;
  qualityStats: FastqQualityStats;
} {
  const lines = source.replace(/^\uFEFF/, '').split(/\r?\n/);
  let index = 0;
  let count = 0;
  let qualityCount = 0;
  let qualitySum = 0;
  let qualityMin = Number.POSITIVE_INFINITY;
  let qualityMax = Number.NEGATIVE_INFINITY;
  let first: FastqRecord | null = null;

  while (index < lines.length) {
    if (lines[index] === '') {
      index += 1;
      continue;
    }
    const header = lines[index];
    if (!header.startsWith('@')) throw new ScientificPreviewError('parse-failed');
    index += 1;

    const sequenceLines: string[] = [];
    while (index < lines.length && !lines[index].startsWith('+')) {
      if (!lines[index]) throw new ScientificPreviewError('parse-failed');
      sequenceLines.push(lines[index].replace(/\s+/g, ''));
      index += 1;
    }
    if (index >= lines.length || sequenceLines.length === 0) throw new ScientificPreviewError('parse-failed');
    const sequence = sequenceLines.join('').toUpperCase();
    index += 1;

    const qualityLines: string[] = [];
    let qualityLength = 0;
    while (index < lines.length && qualityLength < sequence.length) {
      const qualityLine = lines[index];
      qualityLines.push(qualityLine);
      qualityLength += qualityLine.length;
      index += 1;
    }
    if (qualityLength !== sequence.length) throw new ScientificPreviewError('parse-failed');

    const quality = qualityLines.join('');
    for (const character of quality) {
      const score = Math.max(0, character.charCodeAt(0) - 33);
      qualityCount += 1;
      qualitySum += score;
      qualityMin = Math.min(qualityMin, score);
      qualityMax = Math.max(qualityMax, score);
    }

    count += 1;
    first ??= { name: header.slice(1).trim().split(/\s+/, 1)[0] ?? '', sequence };
  }

  if (count === 0 || qualityCount === 0) throw new ScientificPreviewError('empty-content');
  return {
    count,
    first,
    qualityStats: {
      min: qualityMin,
      max: qualityMax,
      mean: Number((qualitySum / qualityCount).toFixed(2)),
    },
  };
}

function inferSequenceType(sequence: string): Seq['type'] {
  if (/^[ACGTNURYKMSWBDHV.-]+$/i.test(sequence)) return sequence.includes('U') ? 'rna' : 'dna';
  if (/^[A-Z*.-]+$/i.test(sequence)) return 'aa';
  return 'unknown';
}

function hasExtension(filename: string, extensions: string[]): boolean {
  return extensions.some((extension) => filename.endsWith(extension));
}

function stripCompressionSuffix(filename: string): string {
  return filename.toLowerCase().endsWith('.gz') ? filename.slice(0, -3) : filename;
}
