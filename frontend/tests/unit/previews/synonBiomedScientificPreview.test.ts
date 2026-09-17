import {
  isSynonBiomedArtifactPreviewEditable,
  normalizeSynonBiomedJsonLines,
  resolveSynonBiomedInternetShortcut,
  resolveSynonBiomedArtifactPreviewPlan,
} from '@/renderer/services/synonBiomedArtifactPreview';
import { resolveStructureFormat } from '@/renderer/pages/conversation/Preview/components/viewers/SynonBiomedStructureViewer';
import {
  extractTableThumbnail,
  resolveArtifactThumbnailVisualKind,
} from '@/renderer/components/synonBiomed/files/ArtifactThumbnailPreview';
import { parseScientificTable } from '@/renderer/pages/conversation/Preview/components/viewers/scientificTableModel';
import { gzipSync } from 'node:zlib';
import { describe, expect, it } from 'vitest';

describe('Synon Biomed scientific preview planning', () => {
  it('extracts CSV cells for compact artifact cards', () => {
    expect(extractTableThumbnail('rank,name,score\n1,alpha,0.91\n2,beta,0.85')).toEqual({
      headers: ['rank', 'name', 'score'],
      rows: [
        ['1', 'alpha', '0.91'],
        ['2', 'beta', '0.85'],
      ],
      columnTypes: ['number', 'text', 'number'],
      totalRows: 2,
      totalColumns: 3,
    });
  });

  it('uses v1.1 semantic thumbnail families for non-image artifacts', () => {
    expect(resolveArtifactThumbnailVisualKind('code', 'summary.json', 'application/json')).toBe('code');
    expect(resolveArtifactThumbnailVisualKind('table', 'scores.csv', 'text/csv')).toBe('spreadsheet');
    expect(resolveArtifactThumbnailVisualKind('msa', 'alignment.fasta', 'text/plain')).toBe('heatmap');
    expect(resolveArtifactThumbnailVisualKind('hdf5', 'matrix.h5ad', 'application/octet-stream')).toBe('scatter');
    expect(resolveArtifactThumbnailVisualKind('code', 'network.graphml', 'application/graphml+xml')).toBe('network');
    expect(resolveArtifactThumbnailVisualKind('markdown', 'report.md', 'text/markdown')).toBe('document');
  });

  it.each([
    ['events.jsonl', 'application/octet-stream', { type: 'code', fetchText: true, language: 'jsonl' }],
    ['events.ndjson', 'application/x-ndjson', { type: 'code', fetchText: true, language: 'jsonl' }],
    ['gri30.yaml', 'application/octet-stream', { type: 'code', fetchText: true, language: 'yaml' }],
    ['settings.yml', 'application/x-yaml', { type: 'code', fetchText: true, language: 'yaml' }],
    ['run_equilibrium.py', 'application/octet-stream', { type: 'code', fetchText: true, language: 'python' }],
    ['workflow.sh', 'application/octet-stream', { type: 'code', fetchText: true, language: 'shell' }],
    ['reads.fastq', 'text/plain', { type: 'sequence', fetchText: false }],
    ['variants.sam', 'text/plain', { type: 'table', fetchText: false }],
    ['spectrum.mzML', 'application/octet-stream', { type: 'code', fetchText: true, language: 'xml' }],
    ['job.inp', 'application/octet-stream', { type: 'code', fetchText: true, language: 'plaintext' }],
    ['notes.txt', 'application/octet-stream', { type: 'code', fetchText: true, language: 'plaintext' }],
    [
      'report.docx',
      'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
      { type: 'word', fetchText: false },
    ],
    [
      'slides.pptx',
      'application/vnd.openxmlformats-officedocument.presentationml.presentation',
      { type: 'ppt', fetchText: false },
    ],
    ['slides.pptm', 'application/octet-stream', { type: 'ppt', fetchText: false }],
    ['workbook.xlsm', 'application/octet-stream', { type: 'table', fetchText: false }],
    ['recording.mp3', 'application/octet-stream', { type: 'audio', fetchText: false }],
    ['movie.mp4', 'application/octet-stream', { type: 'video', fetchText: false }],
    ['reference.url', 'application/x-mswinurl', { type: 'url', fetchText: true }],
  ])('routes %s through its dedicated preview', (filename, contentType, expected) => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename, contentType })).toEqual(expected);
  });

  it('does not let a code/config artifact fall through to the internet shortcut viewer', () => {
    expect(
      resolveSynonBiomedArtifactPreviewPlan({
        filename: 'gri30.yaml',
        contentType: 'application/octet-stream',
        previewKind: 'url',
      })
    ).toEqual({ type: 'code', fetchText: true, language: 'yaml' });
  });

  it('normalizes bounded JSON Lines into one structured JSON array', () => {
    expect(normalizeSynonBiomedJsonLines('{"id":1}\n\n{"id":2}')).toBe(
      '[\n  {\n    "id": 1\n  },\n  {\n    "id": 2\n  }\n]'
    );
    expect(() => normalizeSynonBiomedJsonLines('{"id":1}\nnot-json')).toThrow('invalid_json_line:2');
  });

  it('resolves only safe HTTP internet shortcuts', () => {
    expect(resolveSynonBiomedInternetShortcut('[InternetShortcut]\nURL=https://example.org/paper?id=1')).toBe(
      'https://example.org/paper?id=1'
    );
    expect(resolveSynonBiomedInternetShortcut('https://example.org/direct')).toBe('https://example.org/direct');
    expect(resolveSynonBiomedInternetShortcut('[InternetShortcut]\nURL=file:///etc/passwd')).toBeNull();
    expect(resolveSynonBiomedInternetShortcut('[InternetShortcut]\nURL=https://user:secret@example.org')).toBeNull();
  });

  it.each([
    ['complex.pdb', 'chemical/x-pdb', 'structure'],
    ['complex.cif', 'chemical/x-cif', 'structure'],
    ['ligand.sdf', 'chemical/x-mdl-sdfile', 'structure'],
    ['molecule.mol2', 'chemical/x-mol2', 'structure'],
    ['docked.pdbqt', 'chemical/x-pdb', undefined],
    ['charges.pqr', 'chemical/x-pqr', undefined],
    ['electrostatic.cub', 'application/octet-stream', undefined],
  ])('routes %s through the native SynonAI Mol* structure viewer', (filename, contentType, previewKind) => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename, contentType, previewKind })).toEqual({
      type: 'structure',
      fetchText: false,
    });
  });

  it.each([
    ['library.smi', 'chemical/x-daylight-smiles', 'molecule'],
    ['hits.smiles', 'text/plain', 'molecule'],
    ['enumeration.cxsmiles', 'chemical/smiles', 'molecule'],
  ])('routes %s through the RDKit molecule viewer', (filename, contentType, previewKind) => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename, contentType, previewKind })).toEqual({
      type: 'molecule',
      fetchText: false,
    });
  });

  it('does not send unsupported KET and RXN payloads to the line-based SMILES renderer', () => {
    expect(
      resolveSynonBiomedArtifactPreviewPlan({ filename: 'editable.ket', contentType: 'application/json' })
    ).toEqual({ type: 'molecule', fetchText: false });
    expect(
      resolveSynonBiomedArtifactPreviewPlan({ filename: 'reaction.rxn', contentType: 'chemical/x-mdl-rxnfile' })
    ).toEqual({ type: 'molecule', fetchText: false });
  });

  it('uses an explicit safe download-only state for unknown and unsupported binaries', () => {
    expect(
      resolveSynonBiomedArtifactPreviewPlan({
        filename: 'unknown.proprietary',
        contentType: 'application/octet-stream',
      })
    ).toEqual({ type: 'unsupported', fetchText: false });
    expect(
      resolveSynonBiomedArtifactPreviewPlan({ filename: 'sample.bam', contentType: 'application/octet-stream' })
    ).toEqual({
      type: 'unsupported',
      fetchText: false,
    });
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename: 'scan.tiff', contentType: 'image/tiff' })).toEqual({
      type: 'unsupported',
      fetchText: false,
    });
  });

  it.each([
    ['bundle.zip', 'application/zip'],
    ['bundle.tar.gz', 'application/gzip'],
    ['bundle.7z', 'application/x-7z-compressed'],
    ['bundle.rar', 'application/vnd.rar'],
  ])('routes %s through the archive browser', (filename, contentType) => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename, contentType })).toEqual({
      type: 'archive',
      fetchText: false,
    });
  });

  it('upgrades legacy unsupported archive metadata to the archive browser', () => {
    expect(
      resolveSynonBiomedArtifactPreviewPlan({
        filename: 'legacy.zip',
        contentType: 'application/octet-stream',
        previewKind: 'unsupported',
      })
    ).toEqual({ type: 'archive', fetchText: false });
  });

  it.each([
    ['results.csv', 'text/csv', true],
    ['results.tsv', 'text/tab-separated-values', true],
    ['results.csv.gz', 'application/gzip', false],
    ['results.tsv.gz', 'application/gzip', false],
    ['results.xlsx', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet', false],
    ['results.ods', 'application/vnd.oasis.opendocument.spreadsheet', false],
  ])('routes %s through the native SynonAI table viewer', (filename, contentType, fetchText) => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename, contentType })).toEqual({
      type: 'table',
      fetchText,
    });
  });

  it('decompresses a gzip-compressed CSV before building the native table preview', async () => {
    const compressed = gzipSync('gene,score\nSTAT6,0.91\nCRBN,0.83');
    const data = compressed.buffer.slice(compressed.byteOffset, compressed.byteOffset + compressed.byteLength);

    await expect(parseScientificTable(data, { filename: 'scores.csv.gz' })).resolves.toEqual([
      {
        name: 'Sheet1',
        rows: [
          ['gene', 'score'],
          ['STAT6', '0.91'],
          ['CRBN', '0.83'],
        ],
        sampled: false,
      },
    ]);
  });

  it.each([
    ['alignment.aln', 'text/plain'],
    ['alignment.clustal', 'text/plain'],
    ['alignment.sto', 'text/plain'],
    ['nif3_aligned.fasta', 'text/plain'],
    ['nif3_trimmed.fasta', 'text/plain'],
    ['alignment.aln.gz', 'application/gzip'],
    ['nif3_aligned.fasta.gz', 'application/gzip'],
  ])('routes %s through the native SynonAI multiple-sequence alignment viewer', (filename, contentType) => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename, contentType })).toEqual({
      type: 'msa',
      fetchText: false,
    });
  });

  it.each([
    ['variants.vcf', 'text/vcf'],
    ['variants.vcf.gz', 'application/gzip'],
    ['regions.bed', 'text/plain'],
    ['features.gff3', 'text/plain'],
    ['expression.wig', 'text/plain'],
    ['segments.seg', 'text/plain'],
    ['signal.bigwig', 'application/octet-stream'],
  ])('routes %s through the native SynonAI IGV genome viewer', (filename, contentType) => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename, contentType })).toEqual({
      type: 'genome',
      fetchText: false,
    });
  });

  it.each([
    ['plasmid.gb', 'chemical/seq-na-genbank'],
    ['plasmid.gbk', 'text/plain'],
    ['plasmid.genbank', 'text/plain'],
    ['construct.dna', 'application/octet-stream'],
    ['design.sbol', 'application/xml'],
    ['design.jbei', 'application/json'],
    ['sequence.fasta', 'text/x-fasta'],
    ['protein.faa.gz', 'application/gzip'],
    ['reads.fastq', 'text/plain'],
    ['reads.fq.gz', 'application/gzip'],
  ])('routes %s through the native SynonAI sequence viewer', (filename, contentType) => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename, contentType })).toEqual({
      type: 'sequence',
      fetchText: false,
    });
  });

  it('routes Jupyter notebooks before the generic JSON preview', () => {
    expect(
      resolveSynonBiomedArtifactPreviewPlan({
        filename: 'analysis.ipynb',
        contentType: 'application/x-ipynb+json',
      })
    ).toEqual({ type: 'notebook', fetchText: false });
  });

  it.each([
    ['simulated_raw.h5ad', 'application/octet-stream', undefined],
    ['matrix.h5', 'application/x-hdf5', undefined],
    ['archive.bin', 'application/octet-stream', 'anndata'],
  ])('routes %s through the browser HDF5 viewer', (filename, contentType, previewKind) => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename, contentType, previewKind })).toEqual({
      type: 'hdf5',
      fetchText: false,
    });
  });

  it('keeps markdown and ordinary text on the existing editable-source path', () => {
    const markdown = resolveSynonBiomedArtifactPreviewPlan({ filename: 'report.md', contentType: 'text/markdown' });
    const text = resolveSynonBiomedArtifactPreviewPlan({ filename: 'notes.txt', contentType: 'text/plain' });
    expect(markdown).toEqual({
      type: 'markdown',
      fetchText: true,
    });
    expect(text).toEqual({
      type: 'code',
      fetchText: true,
      language: 'plaintext',
    });
    expect(isSynonBiomedArtifactPreviewEditable(markdown)).toBe(true);
    expect(isSynonBiomedArtifactPreviewEditable(text)).toBe(true);
  });

  it('keeps binary scientific previews read-only', () => {
    const image = resolveSynonBiomedArtifactPreviewPlan({ filename: 'figure.png', contentType: 'image/png' });
    const structure = resolveSynonBiomedArtifactPreviewPlan({ filename: 'complex.pdb', contentType: 'chemical/x-pdb' });
    expect(isSynonBiomedArtifactPreviewEditable(image)).toBe(false);
    expect(isSynonBiomedArtifactPreviewEditable(structure)).toBe(false);
  });

  it('recognizes image extensions when artifact metadata does not include an image MIME type', () => {
    expect(
      resolveSynonBiomedArtifactPreviewPlan({
        filename: 'fig_temperature.png',
        contentType: 'application/octet-stream',
        previewKind: null,
      })
    ).toEqual({ type: 'image', fetchText: false });
  });

  it('maps TeX artifacts to the dedicated LaTeX document renderer', () => {
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename: 'paper.tex', contentType: 'application/x-tex' })).toEqual({
      type: 'latex',
      fetchText: true,
      language: 'latex',
    });
    expect(resolveSynonBiomedArtifactPreviewPlan({ filename: 'appendix.TEX', contentType: 'text/plain' })).toEqual({
      type: 'latex',
      fetchText: true,
      language: 'latex',
    });
  });

  it('maps every supported molecular extension to a Mol* parser format', () => {
    expect(resolveStructureFormat('complex.pdb')).toBe('pdb');
    expect(resolveStructureFormat('complex.ent')).toBe('pdb');
    expect(resolveStructureFormat('complex.cif')).toBe('mmcif');
    expect(resolveStructureFormat('complex.mmcif')).toBe('mmcif');
    expect(resolveStructureFormat('complex.bcif')).toBe('mmcif');
    expect(resolveStructureFormat('docked.pdbqt')).toBe('pdbqt');
    expect(resolveStructureFormat('charges.pqr')).toBe('pqr');
    expect(resolveStructureFormat('trajectory.gro')).toBe('gro');
    expect(resolveStructureFormat('coordinates.xyz')).toBe('xyz');
    expect(resolveStructureFormat('ligand.mol')).toBe('mol');
    expect(resolveStructureFormat('ligand.sdf')).toBe('sdf');
    expect(resolveStructureFormat('ligand.mol2')).toBe('mol2');
    expect(resolveStructureFormat('density.cube')).toBe('cube');
    expect(resolveStructureFormat('electrostatic.cub')).toBe('cube');
  });

  it('routes every supported coordinate format through the PDB-style structure viewer despite generic metadata', () => {
    for (const filename of [
      'protein.pdb',
      'protein.cif',
      'protein.mmcif',
      'docking.pdbqt',
      'charges.pqr',
      'ligand.sdf',
      'ligand.mol',
      'ligand.mol2',
      'coordinates.xyz',
      'trajectory.gro',
    ]) {
      expect(
        resolveSynonBiomedArtifactPreviewPlan({
          filename,
          previewKind: 'molecule',
        })
      ).toEqual({ type: 'structure', fetchText: false });
    }

    expect(
      resolveSynonBiomedArtifactPreviewPlan({
        filename: 'ligand.sdf',
        contentType: 'chemical/x-mdl-sdfile',
        previewKind: 'unsupported',
      })
    ).toEqual({ type: 'structure', fetchText: false });
    expect(
      resolveSynonBiomedArtifactPreviewPlan({
        filename: 'download',
        contentType: 'chemical/x-cif; charset=utf-8',
      })
    ).toEqual({ type: 'structure', fetchText: false });
  });
});
