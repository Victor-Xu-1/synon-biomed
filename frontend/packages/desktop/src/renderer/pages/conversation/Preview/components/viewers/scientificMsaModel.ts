export type ScientificAlignmentFormat = 'fasta' | 'clustal' | 'stockholm';

export type ScientificAlignmentSequence = {
  name: string;
  sequence: string;
};

export type ScientificAlignmentModel = {
  format: ScientificAlignmentFormat;
  sequences: ScientificAlignmentSequence[];
  positionCount: number;
  isNucleotide: boolean;
};

export function resolveScientificAlignmentFormat(filename: string): ScientificAlignmentFormat {
  const normalized = stripCompressionSuffix(filename.toLowerCase());
  if (hasExtension(normalized, ['.aln', '.clustal', '.clustalw'])) return 'clustal';
  if (hasExtension(normalized, ['.sto', '.stockholm', '.stk'])) return 'stockholm';
  return 'fasta';
}

export function parseScientificAlignment(
  content: string,
  filename: string,
  fallbackSequenceName: (index: number) => string = (index) => `Sequence ${index}`
): ScientificAlignmentModel {
  const format = resolveScientificAlignmentFormat(filename);
  const parsed =
    format === 'clustal'
      ? parseInterleavedAlignment(content, { skipHeader: true })
      : format === 'stockholm'
        ? parseInterleavedAlignment(content, { skipHeader: false })
        : parseFasta(content, fallbackSequenceName);
  const sequences = parsed.map((record) => ({
    name: record.name,
    sequence: normalizeSequence(record.sequence),
  }));

  if (sequences.length < 2) {
    throw new ScientificPreviewError('parse-failed');
  }
  const positionCount = sequences[0]?.sequence.length ?? 0;
  if (positionCount === 0) {
    throw new ScientificPreviewError('empty-content');
  }
  if (sequences.some((record) => record.sequence.length !== positionCount)) {
    throw new ScientificPreviewError('parse-failed');
  }

  return {
    format,
    sequences,
    positionCount,
    isNucleotide: inferNucleotide(sequences),
  };
}

function parseFasta(content: string, fallbackSequenceName: (index: number) => string): ScientificAlignmentSequence[] {
  const sequences: ScientificAlignmentSequence[] = [];
  let name = '';
  let fragments: string[] = [];

  const flush = () => {
    if (!name) return;
    sequences.push({ name, sequence: fragments.join('') });
  };

  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line.startsWith(';')) continue;
    if (line.startsWith('>')) {
      flush();
      name = line.slice(1).trim() || fallbackSequenceName(sequences.length + 1);
      fragments = [];
      continue;
    }
    if (!name) throw new ScientificPreviewError('parse-failed');
    fragments.push(line);
  }
  flush();
  return sequences;
}

function parseInterleavedAlignment(content: string, options: { skipHeader: boolean }): ScientificAlignmentSequence[] {
  const sequences = new Map<string, string>();
  const order: string[] = [];

  for (const rawLine of content.split(/\r?\n/)) {
    const line = rawLine.trim();
    if (!line || line === '//' || line.startsWith('#')) continue;
    if (options.skipHeader && /^(?:CLUSTAL|MUSCLE|PROBCONS)\b/i.test(line)) continue;
    if (/^[*:.\s]+$/.test(rawLine)) continue;
    const match = line.match(/^(\S+)\s+([A-Za-z*?.-]+)(?:\s+\d+)?$/);
    if (!match) continue;
    const [, name, fragment] = match;
    if (!sequences.has(name)) order.push(name);
    sequences.set(name, `${sequences.get(name) ?? ''}${fragment}`);
  }

  return order.map((name) => ({ name, sequence: sequences.get(name) ?? '' }));
}

function normalizeSequence(sequence: string): string {
  const normalized = sequence.replace(/\s+/g, '').replaceAll('.', '-').toUpperCase();
  if (!/^[A-Z*?-]+$/.test(normalized)) {
    throw new ScientificPreviewError('parse-failed');
  }
  return normalized;
}

function inferNucleotide(sequences: ScientificAlignmentSequence[]): boolean {
  const symbols = sequences
    .slice(0, 20)
    .map((record) => record.sequence.replace(/[-?*]/g, ''))
    .join('');
  if (!symbols) return false;
  const nonNucleotide = symbols.replace(/[ATGCUNRYSWKMBDHV]/g, '').length;
  return nonNucleotide / symbols.length < 0.1;
}

function hasExtension(filename: string, extensions: string[]): boolean {
  return extensions.some((extension) => filename.endsWith(extension));
}

function stripCompressionSuffix(filename: string): string {
  return filename.toLowerCase().endsWith('.gz') ? filename.slice(0, -3) : filename;
}
import { ScientificPreviewError } from './scientificPreviewError';
