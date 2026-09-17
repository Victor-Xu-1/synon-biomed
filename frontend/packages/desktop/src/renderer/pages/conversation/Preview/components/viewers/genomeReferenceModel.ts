export const GENOME_REFERENCE_OPTIONS = [
  { value: 'hg38', labelKey: 'preview.scientific.genome.references.hg38' },
  { value: 'hg19', labelKey: 'preview.scientific.genome.references.hg19' },
  { value: 'mm39', labelKey: 'preview.scientific.genome.references.mm39' },
  { value: 'mm10', labelKey: 'preview.scientific.genome.references.mm10' },
] as const;

export type GenomeReferenceId = (typeof GENOME_REFERENCE_OPTIONS)[number]['value'];

export type ExplicitGenomeReference = {
  id: GenomeReferenceId;
  name: string;
  format: 'chromsizes';
  fastaURL: string;
};

export function resolveGenomeReference(genome: GenomeReferenceId, name: string): ExplicitGenomeReference {
  return {
    id: genome,
    name,
    format: 'chromsizes',
    fastaURL: `/genomes/ucsc/${genome}.chrom.sizes`,
  };
}
