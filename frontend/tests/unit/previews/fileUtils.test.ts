/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { describe, it, expect } from 'vitest';
import {
  getFileExtension,
  getContentTypeByExtension,
  getLocalBinaryPreviewMimeType,
  buildBase64PreviewDataUrl,
  isImageFile,
  isTextFile,
  isOfficeFile,
  FILE_EXTENSION_MAP,
} from '@/renderer/pages/conversation/Preview/fileUtils';
import { buildPdfSrc } from '@/renderer/pages/conversation/Preview/previewUrls';

describe('fileUtils', () => {
  describe('getFileExtension', () => {
    it('extracts extension in lowercase', () => {
      expect(getFileExtension('document.PDF')).toBe('pdf');
      expect(getFileExtension('script.TS')).toBe('ts');
    });

    it('returns empty string for no extension', () => {
      expect(getFileExtension('noextension')).toBe('');
      expect(getFileExtension('')).toBe('');
    });

    it('returns empty string for dot at end', () => {
      expect(getFileExtension('file.')).toBe('');
    });

    it('extracts last extension for multi-dot names', () => {
      expect(getFileExtension('archive.tar.gz')).toBe('gz');
    });

    it('handles null-ish input gracefully', () => {
      expect(getFileExtension('')).toBe('');
    });
  });

  describe('getContentTypeByExtension', () => {
    it('returns markdown for .md', () => {
      expect(getContentTypeByExtension('README.md')).toBe('markdown');
    });

    it('returns html for .html', () => {
      expect(getContentTypeByExtension('index.html')).toBe('html');
    });

    it('returns pdf for .pdf', () => {
      expect(getContentTypeByExtension('report.pdf')).toBe('pdf');
    });

    it('returns word for .docx', () => {
      expect(getContentTypeByExtension('document.docx')).toBe('word');
      expect(getContentTypeByExtension('document.docm')).toBe('word');
    });

    it('returns ppt for .pptx', () => {
      expect(getContentTypeByExtension('slides.pptx')).toBe('ppt');
      expect(getContentTypeByExtension('slides.pptm')).toBe('ppt');
    });

    it('returns excel for .xlsx', () => {
      expect(getContentTypeByExtension('spreadsheet.xlsx')).toBe('excel');
      expect(getContentTypeByExtension('spreadsheet.xlsm')).toBe('excel');
    });

    it('returns structure for molecular structure files', () => {
      expect(getContentTypeByExtension('complex.pdb')).toBe('structure');
      expect(getContentTypeByExtension('docked.pdbqt')).toBe('structure');
      expect(getContentTypeByExtension('charges.pqr')).toBe('structure');
      expect(getContentTypeByExtension('ligand.sdf')).toBe('structure');
      expect(getContentTypeByExtension('density.cube')).toBe('structure');
      expect(getContentTypeByExtension('electrostatic.cub')).toBe('structure');
    });

    it('returns molecule for SMILES document formats', () => {
      expect(getContentTypeByExtension('hits.smi')).toBe('molecule');
      expect(getContentTypeByExtension('library.smiles')).toBe('molecule');
      expect(getContentTypeByExtension('screen.CXSMILES')).toBe('molecule');
    });

    it('returns table for delimited data files', () => {
      expect(getContentTypeByExtension('results.csv')).toBe('table');
      expect(getContentTypeByExtension('results.tsv')).toBe('table');
      expect(getContentTypeByExtension('results.csv.gz')).toBe('table');
      expect(getContentTypeByExtension('results.tsv.gz')).toBe('table');
      expect(getContentTypeByExtension('variants.sam')).toBe('table');
      expect(getContentTypeByExtension('variants.sam.gz')).toBe('table');
    });

    it('returns media types for common audio and video files', () => {
      expect(getContentTypeByExtension('recording.flac')).toBe('audio');
      expect(getContentTypeByExtension('screen-recording.mp4')).toBe('video');
    });

    it('returns msa for alignment formats and aligned FASTA names', () => {
      expect(getContentTypeByExtension('alignment.aln')).toBe('msa');
      expect(getContentTypeByExtension('alignment.stockholm')).toBe('msa');
      expect(getContentTypeByExtension('alignment.aln.gz')).toBe('msa');
      expect(getContentTypeByExtension('nif3_aligned.fasta.gz')).toBe('msa');
      expect(getContentTypeByExtension('nif3_aligned.fasta')).toBe('msa');
      expect(getContentTypeByExtension('nif3_trimmed.fasta')).toBe('msa');
      expect(getContentTypeByExtension('unaligned_sequences.fasta')).toBe('sequence');
    });

    it('returns sequence for sequence map formats and ordinary FASTA', () => {
      expect(getContentTypeByExtension('plasmid.gbk')).toBe('sequence');
      expect(getContentTypeByExtension('construct.dna')).toBe('sequence');
      expect(getContentTypeByExtension('design.sbol')).toBe('sequence');
      expect(getContentTypeByExtension('protein.faa')).toBe('sequence');
      expect(getContentTypeByExtension('protein.faa.gz')).toBe('sequence');
      expect(getContentTypeByExtension('reads.fastq')).toBe('sequence');
      expect(getContentTypeByExtension('reads.fq.gz')).toBe('sequence');
    });

    it('returns notebook for .ipynb', () => {
      expect(getContentTypeByExtension('analysis.ipynb')).toBe('notebook');
    });

    it('returns genome for IGV-supported genomic tracks', () => {
      expect(getContentTypeByExtension('variants.vcf')).toBe('genome');
      expect(getContentTypeByExtension('regions.bed')).toBe('genome');
      expect(getContentTypeByExtension('features.gff3')).toBe('genome');
      expect(getContentTypeByExtension('signal.bigwig')).toBe('genome');
    });

    it('returns image for .png', () => {
      expect(getContentTypeByExtension('photo.png')).toBe('image');
    });

    it('returns diff for .diff', () => {
      expect(getContentTypeByExtension('changes.diff')).toBe('diff');
    });

    it('returns code as default for unknown extension', () => {
      expect(getContentTypeByExtension('script.ts')).toBe('code');
      expect(getContentTypeByExtension('app.jsx')).toBe('code');
    });

    it('returns code for files without extension', () => {
      expect(getContentTypeByExtension('Makefile')).toBe('code');
    });

    it('marks known binary formats as download-only instead of reading them as UTF-8', () => {
      expect(getContentTypeByExtension('sample.bam')).toBe('unsupported');
      expect(getContentTypeByExtension('scan.nii.gz')).toBe('unsupported');
      expect(getContentTypeByExtension('microscopy.tiff')).toBe('unsupported');
      expect(getContentTypeByExtension('scan.tif')).toBe('unsupported');
    });

    it('routes supported archive containers to the archive browser', () => {
      expect(getContentTypeByExtension('archive.zip')).toBe('archive');
      expect(getContentTypeByExtension('bundle.tar.gz')).toBe('archive');
      expect(getContentTypeByExtension('nested.7z')).toBe('archive');
      expect(getContentTypeByExtension('legacy.rar')).toBe('archive');
    });
  });

  describe('local binary preview transport', () => {
    it('routes existing binary scientific sources through read-buffer data URLs', () => {
      expect(getLocalBinaryPreviewMimeType('matrix.h5ad', 'hdf5')).toBe('application/octet-stream');
      expect(getLocalBinaryPreviewMimeType('structure.bcif', 'structure')).toBe('application/octet-stream');
      expect(getLocalBinaryPreviewMimeType('scores.csv.gz', 'table')).toBe('application/gzip');
      expect(getLocalBinaryPreviewMimeType('variants.vcf.gz', 'genome')).toBe('application/gzip');
      expect(getLocalBinaryPreviewMimeType('alignment.aln.gz', 'msa')).toBe('application/gzip');
      expect(getLocalBinaryPreviewMimeType('protein.faa.gz', 'sequence')).toBe('application/gzip');
      expect(getLocalBinaryPreviewMimeType('reads.fastq.gz', 'sequence')).toBe('application/gzip');
      expect(getLocalBinaryPreviewMimeType('variants.sam.gz', 'table')).toBe('application/gzip');
    });

    it('rejects malformed buffer payloads before creating a data URL', () => {
      expect(() => buildBase64PreviewDataUrl('a', 'application/octet-stream')).toThrow(
        'INVALID_BINARY_PREVIEW_PAYLOAD'
      );
      expect(buildBase64PreviewDataUrl('aGVsbG8=', 'application/octet-stream')).toBe(
        'data:application/octet-stream;base64,aGVsbG8='
      );
    });
  });

  describe('isImageFile', () => {
    it('returns true for image extensions', () => {
      expect(isImageFile('photo.png')).toBe(true);
      expect(isImageFile('icon.svg')).toBe(true);
      expect(isImageFile('image.JPEG')).toBe(true);
    });

    it('returns false for non-image files', () => {
      expect(isImageFile('document.pdf')).toBe(false);
      expect(isImageFile('script.ts')).toBe(false);
    });
  });

  describe('isTextFile', () => {
    it('returns true for text types', () => {
      expect(isTextFile('README.md')).toBe(true);
      expect(isTextFile('index.html')).toBe(true);
      expect(isTextFile('script.ts')).toBe(true);
    });

    it('returns false for binary types', () => {
      expect(isTextFile('document.docx')).toBe(false);
      expect(isTextFile('photo.png')).toBe(false);
      expect(isTextFile('report.pdf')).toBe(false);
      expect(isTextFile('sample.bam')).toBe(false);
    });
  });

  describe('isOfficeFile', () => {
    it('returns true for Office types', () => {
      expect(isOfficeFile('document.docx')).toBe(true);
      expect(isOfficeFile('slides.pptx')).toBe(true);
      expect(isOfficeFile('data.xlsx')).toBe(true);
    });

    it('returns false for non-Office types', () => {
      expect(isOfficeFile('photo.png')).toBe(false);
      expect(isOfficeFile('script.ts')).toBe(false);
    });
  });

  describe('FILE_EXTENSION_MAP', () => {
    it('contains markdown extensions', () => {
      expect(FILE_EXTENSION_MAP.markdown).toContain('md');
      expect(FILE_EXTENSION_MAP.markdown).toContain('markdown');
    });

    it('contains image extensions', () => {
      expect(FILE_EXTENSION_MAP.image).toContain('png');
      expect(FILE_EXTENSION_MAP.image).toContain('svg');
    });
  });
});

describe('previewUrls', () => {
  describe('buildPdfSrc', () => {
    it('builds file:// URI from file_path', () => {
      const result = buildPdfSrc('/path/to/doc.pdf');
      expect(result).toBe('file:///path/to/doc.pdf');
    });

    it('returns content when file_path is absent', () => {
      const result = buildPdfSrc(undefined, 'base64data');
      expect(result).toBe('base64data');
    });

    it('uses the authenticated content URL for a Synon Biomed virtual project file', () => {
      const result = buildPdfSrc(
        'synonbiomed://project/proj_123/project-files/report.pdf',
        '/api/projects/proj_123/artifacts/artifact_123/content'
      );
      expect(result).toBe('/api/projects/proj_123/artifacts/artifact_123/content');
    });

    it('does not coerce a Synon Biomed virtual project file into a file URL without content', () => {
      const result = buildPdfSrc('synonbiomed://project/proj_123/project-files/report.pdf');
      expect(result).toBe('');
    });

    it('preserves an already valid browser URL', () => {
      const result = buildPdfSrc('https://example.test/report.pdf');
      expect(result).toBe('https://example.test/report.pdf');
    });

    it('returns empty string when both absent', () => {
      const result = buildPdfSrc(undefined, undefined);
      expect(result).toBe('');
    });

    it('encodes URI properly', () => {
      const result = buildPdfSrc('/path/with spaces/doc.pdf');
      expect(result).toContain('file:///path/with%20spaces/doc.pdf');
    });

    it('builds a valid file:/// URI from a Windows backslash path', () => {
      // Regression: raw Windows paths previously produced `file://C:%5C...` (ERR_FAILED → blank preview).
      const result = buildPdfSrc('C:\\Users\\me\\doc.pdf');
      expect(result).toBe('file:///C:/Users/me/doc.pdf');
    });

    it('encodes non-ASCII segments in a Windows path', () => {
      const result = buildPdfSrc('C:\\临时空间\\文章.pdf');
      expect(result).toBe(`file:///C:/${encodeURIComponent('临时空间')}/${encodeURIComponent('文章')}.pdf`);
    });
  });
});
