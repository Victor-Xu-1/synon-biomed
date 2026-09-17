import {
  parseSequenceDocument,
  resolveSequenceFormat,
} from '@/renderer/pages/conversation/Preview/components/viewers/sequenceModel';
import { describe, expect, it } from 'vitest';

const genBankFixture = `LOCUS       SYNON001                 120 bp    DNA     circular SYN 11-JUL-2026
DEFINITION  Synthetic Synon Biomed test plasmid.
ACCESSION   SYNON001
VERSION     SYNON001.1
FEATURES             Location/Qualifiers
     source          1..120
                     /organism="synthetic construct"
     promoter        1..20
                     /label="synon_promoter"
     CDS             21..90
                     /gene="synA"
                     /label="synA CDS"
ORIGIN
        1 atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc
       61 atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc atgcatgcat gcatgcatgc
//`;

const sbolFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns:sbol="http://sbols.org/v2#">
  <sbol:Sequence rdf:about="urn:synon:sequence:safe-xml">
    <sbol:displayId>synon_safe_xml</sbol:displayId>
    <sbol:elements>ATGCGTACGTTAGC</sbol:elements>
  </sbol:Sequence>
</rdf:RDF>`;

describe('sequenceModel', () => {
  it.each([
    ['plasmid.gb', 'genbank'],
    ['plasmid.gbk', 'genbank'],
    ['sequence.fasta', 'fasta'],
    ['sequence.fasta.gz', 'fasta'],
    ['reads.fastq', 'fastq'],
    ['reads.fq.gz', 'fastq'],
    ['construct.dna', 'snapgene'],
    ['design.sbol', 'sbol'],
    ['design.jbei', 'jbei'],
  ] as const)('resolves %s as %s', (filename, expected) => {
    expect(resolveSequenceFormat(filename)).toBe(expected);
  });

  it('parses a real GenBank record into the SeqViz document contract', async () => {
    const parsed = await parseSequenceDocument(genBankFixture, 'synon-test.gbk');

    expect(parsed.name).toContain('SYNON001');
    expect(parsed.seq).toHaveLength(120);
    expect(parsed.annotations.length).toBeGreaterThanOrEqual(2);
    const annotationNames = parsed.annotations.map((annotation) => annotation.name);
    expect(annotationNames).toContain('synon_promoter');
    expect(annotationNames).toContain('synA');
  });

  it('parses a real SBOL XML record through the patched transitive parser', async () => {
    const parsed = await parseSequenceDocument(sbolFixture, 'synon-safe.sbol');

    expect(parsed.name).toBe('synon_safe_xml');
    expect(parsed.seq).toBe('ATGCGTACGTTAGC');
    expect(parsed.annotations).toEqual([]);
  });

  it('parses wrapped FASTQ records with bounded quality statistics', async () => {
    const parsed = await parseSequenceDocument(
      '@read-001 instrument=synthetic\nACGT\n+\nIIII\n@read-002\nAA\n+\n!!',
      'reads.fastq'
    );

    expect(parsed).toMatchObject({
      name: 'read-001',
      seq: 'ACGT',
      type: 'dna',
      format: 'fastq',
      recordCount: 2,
      qualityStats: { min: 0, max: 40, mean: 26.67 },
    });
  });

  it('rejects FASTQ records whose quality length does not match the sequence', async () => {
    await expect(parseSequenceDocument('@read-001\nACGT\n+\nIII', 'reads.fastq')).rejects.toMatchObject({
      code: 'parse-failed',
    });
  });
});
