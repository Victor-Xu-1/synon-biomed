export type GenomeTrackDefinition = {
  type: 'annotation' | 'variant' | 'wig' | 'seg';
  format: 'bed' | 'gff3' | 'gtf' | 'vcf' | 'wig' | 'bigwig' | 'seg';
  indexed?: false;
};

const UNINDEXED_TRACKS: Array<{
  extensions: string[];
  definition: GenomeTrackDefinition;
}> = [
  { extensions: ['.vcf', '.vcf.gz'], definition: { type: 'variant', format: 'vcf', indexed: false } },
  { extensions: ['.bed', '.bed.gz'], definition: { type: 'annotation', format: 'bed', indexed: false } },
  {
    extensions: ['.gff', '.gff3', '.gff.gz', '.gff3.gz'],
    definition: { type: 'annotation', format: 'gff3', indexed: false },
  },
  { extensions: ['.gtf', '.gtf.gz'], definition: { type: 'annotation', format: 'gtf', indexed: false } },
  { extensions: ['.wig', '.wig.gz'], definition: { type: 'wig', format: 'wig', indexed: false } },
  { extensions: ['.seg', '.seg.gz'], definition: { type: 'seg', format: 'seg', indexed: false } },
];

export function resolveGenomeTrackDefinition(filename: string): GenomeTrackDefinition {
  const normalized = filename.toLowerCase();
  const unindexed = UNINDEXED_TRACKS.find((candidate) =>
    candidate.extensions.some((extension) => normalized.endsWith(extension))
  );
  if (unindexed) return { ...unindexed.definition };
  if (normalized.endsWith('.bigwig') || normalized.endsWith('.bw')) {
    return { type: 'wig', format: 'bigwig' };
  }
  throw new ScientificPreviewError('unsupported-format', { filename });
}
import { ScientificPreviewError } from './scientificPreviewError';
