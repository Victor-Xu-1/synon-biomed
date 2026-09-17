/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { PreviewContentType } from '@/common/types/office/preview';

/**
 * 文件扩展名到内容类型的映射配置
 * Mapping configuration from file extensions to content types
 */
export const FILE_EXTENSION_MAP: Record<PreviewContentType, readonly string[]> = {
  markdown: ['md', 'markdown', 'mdown', 'mkd'],
  html: ['html', 'htm'],
  pdf: ['pdf'],
  word: ['doc', 'docx', 'docm', 'odt'],
  ppt: ['ppt', 'pptx', 'pptm', 'odp'],
  excel: ['xls', 'xlsx', 'xlsm', 'ods'],
  image: ['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp', 'bmp', 'ico', 'avif'],
  audio: ['mp3', 'wav', 'ogg', 'oga', 'm4a', 'aac', 'flac', 'opus', 'wma', 'aiff', 'aif'],
  video: ['mp4', 'webm', 'ogv', 'mov', 'm4v', 'mkv', 'avi', 'wmv', 'mpeg', 'mpg'],
  molecule: ['smi', 'smiles', 'cxsmiles', 'ket', 'rxn'],
  structure: ['pdb', 'ent', 'cif', 'mmcif', 'bcif', 'pdbqt', 'pqr', 'sdf', 'mol', 'mol2', 'xyz', 'gro', 'cube', 'cub'],
  table: ['csv', 'tsv', 'sam'],
  msa: ['aln', 'clustal', 'clustalw', 'sto', 'stockholm', 'stk', 'afa', 'mfa'],
  genome: ['vcf', 'bed', 'wig', 'gff', 'gff3', 'gtf', 'seg', 'bigwig', 'bw'],
  sequence: ['gb', 'gbk', 'genbank', 'fasta', 'fa', 'fna', 'fas', 'faa', 'fastq', 'fq', 'dna', 'sbol', 'jbei'],
  notebook: ['ipynb'],
  hdf5: ['h5ad', 'h5', 'hdf5', 'hdf'],
  latex: ['tex'],
  archive: [
    'zip',
    'tar',
    'tgz',
    'tbz',
    'tbz2',
    'txz',
    'gz',
    'br',
    'bz2',
    'lz4',
    'lz',
    'mz',
    'sz',
    's2',
    'xz',
    'zz',
    'zst',
    '7z',
    'rar',
  ],
  unsupported: [
    'bam',
    'bai',
    'cram',
    'csi',
    'tbi',
    'dcm',
    'dicom',
    'nii',
    'nifti',
    'mha',
    'mhd',
    'nrrd',
    'mrc',
    'raw',
    'tif',
    'tiff',
    'dcd',
    'xtc',
    'trr',
    'netcdf',
    'parquet',
    'feather',
    'arrow',
    'loom',
    'h5mu',
    'zarr',
    'pse',
    'mae',
    'maegz',
    'cxs',
  ],
  code: [], // code 作为默认类型，不需要显式映射 / code is the default type, no explicit mapping needed
  diff: ['diff', 'patch'],
  url: [], // url 类型用于网页预览，无扩展名映射 / url type for web preview, no extension mapping
};

/**
 * 从文件路径中提取文件扩展名
 * Extract file extension from file path
 *
 * @param file_path - 文件路径 / File path
 * @returns 文件扩展名（小写），如果没有扩展名则返回空字符串 / File extension in lowercase, or empty string if no extension
 *
 * @example
 * ```ts
 * getFileExtension('document.pdf') // => 'pdf'
 * getFileExtension('archive.tar.gz') // => 'gz'
 * getFileExtension('noextension') // => ''
 * getFileExtension('image.PNG') // => 'png'
 * ```
 */
export const getFileExtension = (file_path: string): string => {
  if (!file_path) return '';

  const lastDotIndex = file_path.lastIndexOf('.');
  // 没有点号，或点号在最后（如 "file."），返回空字符串
  // No dot, or dot at the end (e.g., "file."), return empty string
  if (lastDotIndex === -1 || lastDotIndex === file_path.length - 1) {
    return '';
  }

  return file_path.substring(lastDotIndex + 1).toLowerCase();
};

/**
 * 根据文件扩展名确定预览内容类型
 * Determine preview content type based on file extension
 *
 * @param file_path - 文件路径 / File path
 * @returns 预览内容类型 / Preview content type
 *
 * @example
 * ```ts
 * getContentTypeByExtension('README.md') // => 'markdown'
 * getContentTypeByExtension('index.html') // => 'html'
 * getContentTypeByExtension('report.pdf') // => 'pdf'
 * getContentTypeByExtension('script.ts') // => 'code'
 * getContentTypeByExtension('image.png') // => 'image'
 * ```
 */
export const getContentTypeByExtension = (file_path: string): PreviewContentType => {
  if (isNamedAlignedFasta(file_path)) return 'msa';
  if (isCompressedMsa(file_path)) return 'msa';
  if (isCompressedSequence(file_path)) return 'sequence';
  if (isCompressedGenomeTrack(file_path)) return 'genome';
  if (isCompressedTable(file_path)) return 'table';
  if (isUnsupportedCompoundBinary(file_path)) return 'unsupported';
  if (isArchive(file_path)) return 'archive';
  const ext = getFileExtension(file_path);
  if (!ext) return 'code'; // 没有扩展名，默认为 code / No extension, default to code

  // 遍历映射表查找匹配的内容类型 / Iterate through mapping to find matching content type
  for (const [contentType, extensions] of Object.entries(FILE_EXTENSION_MAP)) {
    if (extensions.includes(ext)) {
      return contentType as PreviewContentType;
    }
  }

  // 未找到匹配的扩展名，默认为 code / No matching extension found, default to code
  return 'code';
};

const ARCHIVE_SUFFIXES = [
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

function isArchive(filePath: string): boolean {
  const normalized = filePath.toLowerCase();
  return ARCHIVE_SUFFIXES.some((suffix) => normalized.endsWith(suffix));
}

function isUnsupportedCompoundBinary(filePath: string): boolean {
  const normalized = filePath.toLowerCase();
  return ['.nii.gz', '.mae.gz'].some((suffix) => normalized.endsWith(suffix));
}

const isNamedAlignedFasta = (filePath: string): boolean => {
  const normalized = stripCompressionSuffix(filePath);
  const ext = getFileExtension(normalized);
  if (!['fasta', 'fa', 'faa', 'fna'].includes(ext)) return false;
  const basename = normalized.split(/[\\/]/).at(-1) ?? normalized;
  return /(?:^|[_.-])(?:aligned|alignment|trimmed)(?:[_.-]|$)/.test(basename);
};

const stripCompressionSuffix = (filePath: string): string => {
  const normalized = filePath.toLowerCase();
  return normalized.endsWith('.gz') ? normalized.slice(0, -3) : normalized;
};

const isCompressedMsa = (filePath: string): boolean =>
  ['.aln.gz', '.clustal.gz', '.clustalw.gz', '.sto.gz', '.stockholm.gz', '.stk.gz', '.afa.gz', '.mfa.gz'].some(
    (suffix) => filePath.toLowerCase().endsWith(suffix)
  );

const isCompressedSequence = (filePath: string): boolean =>
  [
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
  ].some((suffix) => filePath.toLowerCase().endsWith(suffix));

const isCompressedGenomeTrack = (filePath: string): boolean => {
  const normalized = filePath.toLowerCase();
  return ['.vcf.gz', '.bed.gz', '.gff.gz', '.gff3.gz', '.gtf.gz', '.wig.gz', '.seg.gz'].some((suffix) =>
    normalized.endsWith(suffix)
  );
};

const isCompressedTable = (filePath: string): boolean => {
  const normalized = filePath.toLowerCase();
  return normalized.endsWith('.csv.gz') || normalized.endsWith('.tsv.gz') || normalized.endsWith('.sam.gz');
};

/**
 * 检查文件是否为图片类型
 * Check if file is an image type
 *
 * @param file_path - 文件路径 / File path
 * @returns 是否为图片 / Whether it's an image
 */
export const isImageFile = (file_path: string): boolean => {
  return getContentTypeByExtension(file_path) === 'image';
};

/**
 * 检查文件是否为文本类型（可编辑）
 * Check if file is a text type (editable)
 *
 * @param file_path - 文件路径 / File path
 * @returns 是否为文本类型 / Whether it's a text type
 */
export const isTextFile = (file_path: string): boolean => {
  const contentType = getContentTypeByExtension(file_path);
  return ['markdown', 'html', 'code'].includes(contentType);
};

/**
 * 检查文件是否为 Office 文档类型
 * Check if file is an Office document type
 *
 * @param file_path - 文件路径 / File path
 * @returns 是否为 Office 文档 / Whether it's an Office document
 */
export const isOfficeFile = (file_path: string): boolean => {
  const contentType = getContentTypeByExtension(file_path);
  return ['word', 'excel', 'ppt'].includes(contentType);
};

/**
 * Return the MIME type used when a local binary preview is transported through
 * the existing authenticated read-buffer IPC endpoint.
 */
export const getLocalBinaryPreviewMimeType = (
  file_path: string,
  contentType: PreviewContentType
): string | undefined => {
  const normalized = file_path.toLowerCase();
  if (contentType === 'hdf5') return 'application/octet-stream';
  if (normalized.endsWith('.bcif')) return 'application/octet-stream';
  if (normalized.endsWith('.csv.gz') || normalized.endsWith('.tsv.gz') || normalized.endsWith('.sam.gz'))
    return 'application/gzip';
  if (isCompressedMsa(file_path) || isCompressedSequence(file_path) || isNamedAlignedFasta(file_path))
    return 'application/gzip';
  if (
    ['.vcf.gz', '.bed.gz', '.gff.gz', '.gff3.gz', '.gtf.gz', '.wig.gz', '.seg.gz'].some((suffix) =>
      normalized.endsWith(suffix)
    )
  )
    return 'application/gzip';
  return undefined;
};

/**
 * Convert the base64 payload returned by /api/fs/read-buffer into a fetchable
 * data URL. Reject malformed payloads instead of rendering arbitrary text as a
 * scientific binary document.
 */
export const buildBase64PreviewDataUrl = (base64: string, mimeType: string): string => {
  const normalized = base64.replace(/\s/g, '');
  if (!normalized || !/^[A-Za-z0-9+/]*={0,2}$/.test(normalized) || normalized.length % 4 === 1) {
    throw new Error('INVALID_BINARY_PREVIEW_PAYLOAD');
  }
  return `data:${mimeType};base64,${normalized}`;
};
