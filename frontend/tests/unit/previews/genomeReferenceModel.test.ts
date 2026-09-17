import { createHash } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';
import {
  GENOME_REFERENCE_OPTIONS,
  resolveGenomeReference,
} from '@/renderer/pages/conversation/Preview/components/viewers/genomeReferenceModel';

type ProvenanceFile = {
  file: string;
  bytes: number;
  md5: string;
  sha256: string;
};

type Provenance = {
  schemaVersion: number;
  files: ProvenanceFile[];
};

const assetDirectory = fileURLToPath(new URL('../../../public/genomes/ucsc/', import.meta.url));
const provenance = JSON.parse(readFileSync(`${assetDirectory}/PROVENANCE.json`, 'utf8')) as Provenance;

const fixtures = [
  {
    id: 'hg38',
    chr1: 248956422,
    bytes: 11672,
    md5: 'c42e9f75fff906c6a7143cb2fac86602',
    sha256: 'e1d9152418038457a959e949d99c9caf7ae3e4f87cfbb4ffe7d8b9a54ba1202b',
  },
  {
    id: 'hg19',
    chr1: 249250621,
    bytes: 1971,
    md5: 'b3b0fcf79b5477ab0b3af02e81eac8dc',
    sha256: 'b404927655a4aada254ea94ad4da0c8901ed0737e67a0dcabedf673354b1f505',
  },
  {
    id: 'mm39',
    chr1: 195154279,
    bytes: 1350,
    md5: '967c609a714df35c86a4f52ac85db377',
    sha256: 'acd0edeab2409fe5e112444846dd89c4f8873df1f8ee4ca616aed6cf75be2a05',
  },
  {
    id: 'mm10',
    chr1: 195471971,
    bytes: 1405,
    md5: '5a103c9a15dd660c295a089ef5035672',
    sha256: '55463d59f400b2c8260d100f4b521800ee773fa9711fe3f6dad9c7887db2574f',
  },
] as const;

describe('genomeReferenceModel', () => {
  it('maps every selectable assembly to an offline same-origin chromosome-size reference', () => {
    expect(GENOME_REFERENCE_OPTIONS.map((option) => option.value)).toEqual(fixtures.map((fixture) => fixture.id));
    for (const fixture of fixtures) {
      const reference = resolveGenomeReference(fixture.id, fixture.id);
      expect(reference).toEqual({
        id: fixture.id,
        name: fixture.id,
        format: 'chromsizes',
        fastaURL: `/genomes/ucsc/${fixture.id}.chrom.sizes`,
      });
      expect(JSON.stringify(reference)).not.toMatch(/https?:|twoBitURL|chromSizesURL/u);
    }
  });

  it.each(fixtures)('ships a checksum-bound valid $id chromosome-size table', (fixture) => {
    const filename = `${fixture.id}.chrom.sizes`;
    const payload = readFileSync(`${assetDirectory}/${filename}`);
    const lines = payload.toString('utf8').trimEnd().split('\n');
    const chromosomes = new Map<string, number>();
    for (const line of lines) {
      const match = /^([^\t\s]+)\t([1-9][0-9]*)$/u.exec(line);
      expect(match, `invalid ${fixture.id} row: ${line}`).not.toBeNull();
      if (!match) continue;
      expect(chromosomes.has(match[1]), `duplicate ${fixture.id} chromosome: ${match[1]}`).toBe(false);
      chromosomes.set(match[1], Number(match[2]));
    }

    expect(chromosomes.get('chr1')).toBe(fixture.chr1);
    expect(payload.byteLength).toBe(fixture.bytes);
    expect(createHash('md5').update(payload).digest('hex')).toBe(fixture.md5);
    expect(createHash('sha256').update(payload).digest('hex')).toBe(fixture.sha256);
    expect(provenance.files.find((entry) => entry.file === filename)).toMatchObject({
      bytes: fixture.bytes,
      md5: fixture.md5,
      sha256: fixture.sha256,
    });
  });

  it('uses a closed provenance schema for exactly the selectable assemblies', () => {
    expect(provenance.schemaVersion).toBe(1);
    expect(provenance.files.map((entry) => entry.file)).toEqual(fixtures.map((fixture) => `${fixture.id}.chrom.sizes`));
    expect(provenance.files).toHaveLength(GENOME_REFERENCE_OPTIONS.length);
  });
});
