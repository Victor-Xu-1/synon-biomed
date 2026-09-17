import {
  parseScientificAlignment,
  resolveScientificAlignmentFormat,
} from '@/renderer/pages/conversation/Preview/components/viewers/scientificMsaModel';
import { describe, expect, it } from 'vitest';

describe('scientific MSA model', () => {
  it('parses aligned FASTA records and normalizes dot gaps', () => {
    const model = parseScientificAlignment('>alpha\nAC.E\n>beta description\nACFE\n', 'aligned.fasta');

    expect(model).toEqual({
      format: 'fasta',
      sequences: [
        { name: 'alpha', sequence: 'AC-E' },
        { name: 'beta description', sequence: 'ACFE' },
      ],
      positionCount: 4,
      isNucleotide: false,
    });
  });

  it('combines repeated sequence blocks in CLUSTAL alignments', () => {
    const model = parseScientificAlignment(
      'CLUSTAL W\n\nalpha AC-D\nbeta  ACTD\n       ** *\n\nalpha EF\nbeta  EF\n',
      'alignment.aln'
    );

    expect(model.sequences).toEqual([
      { name: 'alpha', sequence: 'AC-DEF' },
      { name: 'beta', sequence: 'ACTDEF' },
    ]);
    expect(model.positionCount).toBe(6);
  });

  it('parses Stockholm records and detects nucleotide alignments', () => {
    const model = parseScientificAlignment('# STOCKHOLM 1.0\nseq1 ACG-U\nseq2 ACGUU\n//\n', 'alignment.sto');

    expect(model.format).toBe('stockholm');
    expect(model.isNucleotide).toBe(true);
    expect(model.sequences).toHaveLength(2);
  });

  it('rejects malformed or non-aligned content with a user-facing error', () => {
    expect(() => parseScientificAlignment('>only\nACGT\n', 'alignment.fasta')).toThrow('parse-failed');
    expect(() => parseScientificAlignment('>one\nACGT\n>two\nACGTA\n', 'alignment.fasta')).toThrow('parse-failed');
  });

  it('resolves the v1.1 alignment extension matrix', () => {
    expect(resolveScientificAlignmentFormat('result.clustalw')).toBe('clustal');
    expect(resolveScientificAlignmentFormat('result.stockholm')).toBe('stockholm');
    expect(resolveScientificAlignmentFormat('result.mfa')).toBe('fasta');
    expect(resolveScientificAlignmentFormat('alignment.aln.gz')).toBe('clustal');
    expect(resolveScientificAlignmentFormat('aligned.fasta.gz')).toBe('fasta');
  });
});
