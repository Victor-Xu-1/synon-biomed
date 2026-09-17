/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { PreviewContentType } from '@/common/types/office/preview';

export type SynonBiomedArtifactPreviewSource = {
  filename: string;
  contentType?: string | null;
  previewKind?: string | null;
};

export type SynonBiomedArtifactPreviewPlan = {
  type: PreviewContentType;
  fetchText: boolean;
  language?: string;
};

/**
 * Only bounded textual artifact formats may enter the versioned editor.
 * Binary/scientific viewers remain read-only and continue to expose download.
 */
export function isSynonBiomedArtifactPreviewEditable(plan: SynonBiomedArtifactPreviewPlan): boolean {
  return plan.fetchText && ['markdown', 'html', 'code', 'latex', 'table'].includes(plan.type);
}

export const SYNON_BIOMED_TEXT_ACCEPT_HEADER =
  'text/plain, text/markdown, application/json, text/csv, text/tab-separated-values, text/html';

const MAX_INTERNET_SHORTCUT_BYTES = 64 * 1024;

const CODE_EXTENSION_LANGUAGES: Readonly<Record<string, string>> = {
  '.yaml': 'yaml',
  '.yml': 'yaml',
  '.py': 'python',
  '.pyw': 'python',
  '.js': 'javascript',
  '.mjs': 'javascript',
  '.cjs': 'javascript',
  '.jsx': 'javascript',
  '.ts': 'typescript',
  '.mts': 'typescript',
  '.cts': 'typescript',
  '.tsx': 'typescript',
  '.r': 'r',
  '.rb': 'ruby',
  '.go': 'go',
  '.rs': 'rust',
  '.java': 'java',
  '.c': 'c',
  '.h': 'c',
  '.cc': 'cpp',
  '.cpp': 'cpp',
  '.cxx': 'cpp',
  '.hh': 'cpp',
  '.hpp': 'cpp',
  '.sh': 'shell',
  '.bash': 'shell',
  '.zsh': 'shell',
  '.fish': 'shell',
  '.ps1': 'powershell',
  '.sql': 'sql',
  '.css': 'css',
  '.scss': 'scss',
  '.less': 'less',
  '.xml': 'xml',
  '.xsd': 'xml',
  '.toml': 'toml',
  '.ini': 'ini',
  '.properties': 'properties',
  '.txt': 'plaintext',
  '.log': 'plaintext',
  '.cfg': 'ini',
  '.conf': 'ini',
  '.cnf': 'ini',
  '.env': 'plaintext',
  '.rtf': 'plaintext',
  '.fastq': 'plaintext',
  '.fq': 'plaintext',
  '.qual': 'plaintext',
  '.sam': 'plaintext',
  '.mgf': 'plaintext',
  '.dta': 'plaintext',
  '.mztab': 'plaintext',
  '.inp': 'plaintext',
  '.in': 'plaintext',
  '.out': 'plaintext',
  '.com': 'plaintext',
  '.gjf': 'plaintext',
  '.nw': 'plaintext',
  '.res': 'plaintext',
  '.resfile': 'plaintext',
  '.params': 'plaintext',
  '.score': 'plaintext',
  '.silent': 'plaintext',
  '.dlg': 'plaintext',
  '.phy': 'plaintext',
  '.phylip': 'plaintext',
  '.nex': 'plaintext',
  '.nexus': 'plaintext',
  '.tree': 'plaintext',
  '.nwk': 'plaintext',
  '.mtx': 'plaintext',
  '.ped': 'plaintext',
  '.mvsj': 'json',
  '.mzml': 'xml',
  '.mzxml': 'xml',
  '.mzid': 'xml',
  '.mzidentml': 'xml',
  '.xsl': 'xml',
  '.xslt': 'xml',
};

const CODE_CONTENT_TYPE_LANGUAGES: Readonly<Record<string, string>> = {
  'application/yaml': 'yaml',
  'application/x-yaml': 'yaml',
  'text/yaml': 'yaml',
  'application/toml': 'toml',
  'text/toml': 'toml',
  'application/xml': 'xml',
  'text/xml': 'xml',
  'application/javascript': 'javascript',
  'text/javascript': 'javascript',
  'application/typescript': 'typescript',
  'text/typescript': 'typescript',
  'application/x-python': 'python',
  'text/x-python': 'python',
  'application/sql': 'sql',
  'text/x-sql': 'sql',
  'application/x-sh': 'shell',
  'text/x-shellscript': 'shell',
  'text/css': 'css',
};

export function normalizeSynonBiomedJsonLines(content: string): string {
  const values: unknown[] = [];
  for (const [index, rawLine] of content.split(/\r?\n/).entries()) {
    const line = rawLine.trim();
    if (!line) continue;
    try {
      values.push(JSON.parse(line) as unknown);
    } catch {
      throw new Error(`invalid_json_line:${index + 1}`);
    }
  }
  return JSON.stringify(values, null, 2);
}

export function resolveSynonBiomedInternetShortcut(content: string): string | null {
  if (new TextEncoder().encode(content).byteLength > MAX_INTERNET_SHORTCUT_BYTES) return null;
  const trimmed = content.trim();
  const shortcut = trimmed.match(/^URL\s*=\s*(.+)$/im)?.[1]?.trim() ?? trimmed;
  try {
    const url = new URL(shortcut);
    if ((url.protocol !== 'https:' && url.protocol !== 'http:') || url.username || url.password) return null;
    return url.href;
  } catch {
    return null;
  }
}

export function resolveSynonBiomedArtifactPreviewPlan(
  source: SynonBiomedArtifactPreviewSource
): SynonBiomedArtifactPreviewPlan {
  const filename = source.filename.toLowerCase();
  const contentType = source.contentType?.toLowerCase() ?? '';
  const previewKind = source.previewKind?.toLowerCase() ?? '';

  // Supported molecular coordinate formats always use the same Mol*/PDB-style
  // structure viewer. Upstream metadata sometimes labels SDF/MOL2 as a generic
  // molecule (or even unsupported); the concrete file/MIME contract is more
  // authoritative and prevents a second, reduced preview path.
  if (hasExtension(filename, STRUCTURE_EXTENSIONS) || isStructureContentType(contentType)) {
    return { type: 'structure', fetchText: false };
  }

  if (
    previewKind === 'unsupported' &&
    !hasExtension(filename, ARCHIVE_EXTENSIONS) &&
    !isArchiveContentType(contentType)
  ) {
    return { type: 'unsupported', fetchText: false };
  }
  if (previewKind === 'audio' || contentType.startsWith('audio/') || hasExtension(filename, AUDIO_EXTENSIONS)) {
    return { type: 'audio', fetchText: false };
  }
  if (previewKind === 'video' || contentType.startsWith('video/') || hasExtension(filename, VIDEO_EXTENSIONS)) {
    return { type: 'video', fetchText: false };
  }
  if (contentType === 'image/tiff' || hasExtension(filename, TIFF_EXTENSIONS)) {
    return { type: 'unsupported', fetchText: false };
  }
  if (previewKind === 'image' || contentType.startsWith('image/') || hasExtension(filename, IMAGE_EXTENSIONS)) {
    return { type: 'image', fetchText: false };
  }
  if (previewKind === 'pdf' || contentType === 'application/pdf' || filename.endsWith('.pdf')) {
    return { type: 'pdf', fetchText: false };
  }
  if (previewKind === 'markdown' || contentType.includes('markdown') || filename.endsWith('.md')) {
    return { type: 'markdown', fetchText: true };
  }
  if (
    previewKind === 'latex' ||
    filename.endsWith('.tex') ||
    contentType === 'application/x-tex' ||
    contentType === 'text/x-tex'
  ) {
    return { type: 'latex', fetchText: true, language: 'latex' };
  }
  if (filename.endsWith('.html') || filename.endsWith('.htm') || contentType.includes('html')) {
    return { type: 'html', fetchText: true, language: 'html' };
  }
  if (previewKind === 'notebook' || filename.endsWith('.ipynb') || contentType.includes('ipynb')) {
    return { type: 'notebook', fetchText: false };
  }
  if (
    hasExtension(filename, ['.doc', '.docx', '.docm', '.odt']) ||
    contentType.includes('wordprocessingml') ||
    contentType.includes('msword')
  ) {
    return { type: 'word', fetchText: false };
  }
  if (
    hasExtension(filename, ['.ppt', '.pptx', '.pptm', '.odp']) ||
    contentType.includes('presentationml') ||
    contentType.includes('powerpoint')
  ) {
    return { type: 'ppt', fetchText: false };
  }
  if (hasExtension(filename, ['.jsonl', '.ndjson']) || contentType.includes('ndjson')) {
    return { type: 'code', fetchText: true, language: 'jsonl' };
  }
  if (filename.endsWith('.url') || contentType === 'application/x-mswinurl') {
    return { type: 'url', fetchText: true };
  }
  if (
    previewKind === 'hdf5' ||
    previewKind === 'anndata' ||
    hasExtension(filename, HDF5_EXTENSIONS) ||
    contentType.includes('x-hdf5') ||
    contentType.includes('x-hdf')
  ) {
    return { type: 'hdf5', fetchText: false };
  }
  if (
    previewKind === 'molecule' ||
    hasExtension(filename, MOLECULE_EXTENSIONS) ||
    contentType.includes('chemical/x-daylight-smiles') ||
    contentType.includes('chemical/smiles')
  ) {
    return { type: 'molecule', fetchText: false };
  }
  if (previewKind === 'msa' || hasExtension(filename, MSA_EXTENSIONS) || isNamedAlignedFasta(filename)) {
    return { type: 'msa', fetchText: false };
  }
  if (previewKind === 'sequence' || hasExtension(filename, SEQUENCE_EXTENSIONS)) {
    return { type: 'sequence', fetchText: false };
  }
  if (previewKind === 'json' || contentType.includes('json') || filename.endsWith('.json')) {
    return { type: 'code', fetchText: true, language: 'json' };
  }
  if (previewKind === 'structure') {
    return { type: 'structure', fetchText: false };
  }
  if (previewKind === 'genome' || hasExtension(filename, GENOME_TRACK_EXTENSIONS)) {
    return { type: 'genome', fetchText: false };
  }
  if (
    previewKind === 'csv' ||
    hasExtension(filename, ['.csv', '.tsv']) ||
    contentType.includes('csv') ||
    contentType.includes('tab-separated')
  ) {
    return { type: 'table', fetchText: true };
  }
  if (
    previewKind === 'spreadsheet' ||
    hasExtension(filename, TABLE_EXTENSIONS) ||
    contentType.includes('csv') ||
    contentType.includes('tab-separated') ||
    contentType.includes('spreadsheet') ||
    contentType.includes('ms-excel') ||
    contentType.includes('opendocument.spreadsheet') ||
    contentType.includes('excel')
  ) {
    return { type: 'table', fetchText: false };
  }
  if (hasExtension(filename, UNSUPPORTED_EXTENSIONS)) {
    return { type: 'unsupported', fetchText: false };
  }
  if (previewKind === 'archive' || hasExtension(filename, ARCHIVE_EXTENSIONS) || isArchiveContentType(contentType)) {
    return { type: 'archive', fetchText: false };
  }
  if (previewKind === 'code' || previewKind === 'config' || isCodeLikeContentType(contentType)) {
    return {
      type: 'code',
      fetchText: true,
      language: resolveCodeLanguage(filename, contentType) ?? 'plaintext',
    };
  }
  const codeLanguage = resolveCodeLanguage(filename, contentType);
  if (codeLanguage) {
    return { type: 'code', fetchText: true, language: codeLanguage };
  }
  if (isUnsupportedContentType(contentType)) {
    return { type: 'unsupported', fetchText: false };
  }
  if (previewKind === 'text' || contentType.startsWith('text/')) {
    return { type: 'code', fetchText: true, language: 'plaintext' };
  }
  return { type: 'unsupported', fetchText: false };
}

const IMAGE_EXTENSIONS = ['.png', '.jpg', '.jpeg', '.gif', '.webp', '.avif', '.svg', '.bmp', '.ico'];

const TIFF_EXTENSIONS = ['.tif', '.tiff'];

const AUDIO_EXTENSIONS = ['.mp3', '.wav', '.ogg', '.oga', '.m4a', '.aac', '.flac', '.opus', '.wma', '.aiff', '.aif'];

const VIDEO_EXTENSIONS = ['.mp4', '.webm', '.ogv', '.mov', '.m4v', '.mkv', '.avi', '.wmv', '.mpeg', '.mpg'];

const STRUCTURE_EXTENSIONS = [
  '.pdb',
  '.ent',
  '.cif',
  '.mmcif',
  '.bcif',
  '.pdbqt',
  '.pqr',
  '.sdf',
  '.mol',
  '.mol2',
  '.xyz',
  '.gro',
  '.cube',
  '.cub',
];

const MOLECULE_EXTENSIONS = ['.smi', '.smiles', '.cxsmiles', '.ket', '.rxn'];

const HDF5_EXTENSIONS = ['.h5ad', '.h5', '.hdf5', '.hdf'];

const TABLE_EXTENSIONS = ['.csv', '.tsv', '.sam', '.csv.gz', '.tsv.gz', '.sam.gz', '.xlsx', '.xls', '.xlsm', '.ods'];

const ARCHIVE_EXTENSIONS = [
  '.zip',
  '.tar',
  '.tar.gz',
  '.tgz',
  '.tar.bz2',
  '.tbz',
  '.tbz2',
  '.tar.xz',
  '.txz',
  '.tar.zst',
  '.gz',
  '.br',
  '.bz2',
  '.lz4',
  '.lz',
  '.mz',
  '.sz',
  '.s2',
  '.xz',
  '.zz',
  '.zst',
  '.7z',
  '.rar',
];

const UNSUPPORTED_EXTENSIONS = [
  '.bam',
  '.bai',
  '.cram',
  '.csi',
  '.tbi',
  '.dcm',
  '.dicom',
  '.nii',
  '.nii.gz',
  '.nifti',
  '.mha',
  '.mhd',
  '.nrrd',
  '.mrc',
  '.raw',
  '.dcd',
  '.xtc',
  '.trr',
  '.netcdf',
  '.parquet',
  '.feather',
  '.arrow',
  '.loom',
  '.h5mu',
  '.zarr',
  '.pse',
  '.mae',
  '.maegz',
  '.cxs',
];

const MSA_EXTENSIONS = [
  '.aln',
  '.clustal',
  '.clustalw',
  '.sto',
  '.stockholm',
  '.stk',
  '.afa',
  '.mfa',
  '.aln.gz',
  '.clustal.gz',
  '.clustalw.gz',
  '.sto.gz',
  '.stockholm.gz',
  '.stk.gz',
  '.afa.gz',
  '.mfa.gz',
];

const SEQUENCE_EXTENSIONS = [
  '.gb',
  '.gbk',
  '.genbank',
  '.fasta',
  '.fa',
  '.fna',
  '.fas',
  '.faa',
  '.fastq',
  '.fq',
  '.dna',
  '.sbol',
  '.jbei',
  '.gb.gz',
  '.gbk.gz',
  '.genbank.gz',
  '.fasta.gz',
  '.fa.gz',
  '.fna.gz',
  '.fas.gz',
  '.faa.gz',
  '.fastq.gz',
  '.fq.gz',
  '.dna.gz',
  '.sbol.gz',
  '.jbei.gz',
];

const GENOME_TRACK_EXTENSIONS = [
  '.vcf',
  '.vcf.gz',
  '.bed',
  '.bed.gz',
  '.gff.gz',
  '.gff3.gz',
  '.wig',
  '.wig.gz',
  '.gff',
  '.gff3',
  '.gtf',
  '.gtf.gz',
  '.seg',
  '.seg.gz',
  '.bigwig',
  '.bw',
];

function isNamedAlignedFasta(filename: string): boolean {
  const normalized = filename.endsWith('.gz') ? filename.slice(0, -3) : filename;
  if (!hasExtension(normalized, ['.fasta', '.fa', '.faa', '.fna'])) return false;
  const basename = normalized.split('/').at(-1) ?? normalized;
  return /(?:^|[_.-])(?:aligned|alignment|trimmed)(?:[_.-]|$)/.test(basename);
}

function hasExtension(filename: string, extensions: string[]): boolean {
  return extensions.some((extension) => filename.endsWith(extension));
}

function resolveCodeLanguage(filename: string, contentType: string): string | null {
  const extensionLanguage = Object.entries(CODE_EXTENSION_LANGUAGES).find(([extension]) =>
    filename.endsWith(extension)
  );
  if (extensionLanguage) return extensionLanguage[1];

  const normalizedContentType = contentType.split(';', 1)[0]?.trim() ?? contentType;
  return CODE_CONTENT_TYPE_LANGUAGES[normalizedContentType] ?? null;
}

function isCodeLikeContentType(contentType: string): boolean {
  const normalizedContentType = contentType.split(';', 1)[0]?.trim() ?? contentType;
  return normalizedContentType in CODE_CONTENT_TYPE_LANGUAGES;
}

function isUnsupportedContentType(contentType: string): boolean {
  const normalizedContentType = contentType.split(';', 1)[0]?.trim() ?? contentType;
  return [
    'application/x-bzip2',
    'application/x-xz',
    'application/dicom',
    'application/x-bam',
    'application/cram',
  ].includes(normalizedContentType);
}

function isStructureContentType(contentType: string): boolean {
  const normalizedContentType = contentType.split(';', 1)[0]?.trim() ?? contentType;
  return [
    'chemical/x-pdb',
    'chemical/x-cif',
    'chemical/x-mmcif',
    'chemical/x-pdbqt',
    'chemical/x-pqr',
    'chemical/x-mdl-molfile',
    'chemical/x-mdl-sdfile',
    'chemical/x-mol2',
    'chemical/x-xyz',
    'chemical/x-gromacs-gro',
  ].includes(normalizedContentType);
}

function isArchiveContentType(contentType: string): boolean {
  const normalizedContentType = contentType.split(';', 1)[0]?.trim() ?? contentType;
  return [
    'application/zip',
    'application/x-zip-compressed',
    'application/x-tar',
    'application/x-gtar',
    'application/gzip',
    'application/x-gzip',
    'application/x-bzip2',
    'application/x-xz',
    'application/zstd',
    'application/x-7z-compressed',
    'application/vnd.rar',
    'application/x-rar-compressed',
  ].includes(normalizedContentType);
}
