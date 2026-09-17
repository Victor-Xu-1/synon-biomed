import { resolveGenomeTrackDefinition } from '@/renderer/pages/conversation/Preview/components/viewers/genomeTrackModel';
import { ScientificPreviewError } from '@/renderer/pages/conversation/Preview/components/viewers/scientificPreviewError';
import { describe, expect, it } from 'vitest';

describe('genome track model', () => {
  it.each([
    ['variants.vcf', { type: 'variant', format: 'vcf', indexed: false }],
    ['variants.vcf.gz', { type: 'variant', format: 'vcf', indexed: false }],
    ['regions.bed', { type: 'annotation', format: 'bed', indexed: false }],
    ['regions.bed.gz', { type: 'annotation', format: 'bed', indexed: false }],
    ['features.gff3', { type: 'annotation', format: 'gff3', indexed: false }],
    ['features.gtf', { type: 'annotation', format: 'gtf', indexed: false }],
    ['expression.wig', { type: 'wig', format: 'wig', indexed: false }],
    ['segments.seg', { type: 'seg', format: 'seg', indexed: false }],
    ['signal.bigwig', { type: 'wig', format: 'bigwig' }],
    ['signal.bw', { type: 'wig', format: 'bigwig' }],
  ])('maps %s to the IGV track contract', (filename, expected) => {
    expect(resolveGenomeTrackDefinition(filename)).toEqual(expected);
  });

  it('rejects unsupported files instead of guessing an IGV parser', () => {
    expect(() => resolveGenomeTrackDefinition('notes.txt')).toThrowError(ScientificPreviewError);
    try {
      resolveGenomeTrackDefinition('notes.txt');
    } catch (error) {
      expect(error).toMatchObject({ code: 'unsupported-format', details: { filename: 'notes.txt' } });
    }
  });
});
