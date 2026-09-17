/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import type { PreviewContentType } from '@/common/types/office/preview';

interface FileTypeInfo {
  contentType: PreviewContentType;
  editable: boolean;
  language: string;
}

// 扩展名到类型的直接映射 / Direct extension to type mapping
const EXTENSION_MAP: Record<string, FileTypeInfo> = {
  // Markdown
  md: { contentType: 'markdown', editable: true, language: 'markdown' },
  markdown: { contentType: 'markdown', editable: true, language: 'markdown' },
  // HTML
  html: { contentType: 'html', editable: true, language: 'html' },
  htm: { contentType: 'html', editable: true, language: 'html' },
  // Diff
  diff: { contentType: 'diff', editable: false, language: 'diff' },
  patch: { contentType: 'diff', editable: false, language: 'diff' },
  // PDF
  pdf: { contentType: 'pdf', editable: false, language: 'pdf' },
  // PPT
  ppt: { contentType: 'ppt', editable: false, language: 'ppt' },
  pptx: { contentType: 'ppt', editable: false, language: 'ppt' },
  pptm: { contentType: 'ppt', editable: false, language: 'ppt' },
  odp: { contentType: 'ppt', editable: false, language: 'ppt' },
  // Word
  doc: { contentType: 'word', editable: false, language: 'word' },
  docx: { contentType: 'word', editable: false, language: 'word' },
  docm: { contentType: 'word', editable: false, language: 'word' },
  odt: { contentType: 'word', editable: false, language: 'word' },
  // Excel
  xls: { contentType: 'excel', editable: false, language: 'excel' },
  xlsx: { contentType: 'excel', editable: false, language: 'excel' },
  xlsm: { contentType: 'excel', editable: false, language: 'excel' },
  ods: { contentType: 'excel', editable: false, language: 'excel' },
  csv: { contentType: 'table', editable: true, language: 'csv' },
  tsv: { contentType: 'table', editable: true, language: 'tsv' },
  sam: { contentType: 'table', editable: false, language: 'sam' },
  // Image
  png: { contentType: 'image', editable: false, language: 'image' },
  jpg: { contentType: 'image', editable: false, language: 'image' },
  jpeg: { contentType: 'image', editable: false, language: 'image' },
  gif: { contentType: 'image', editable: false, language: 'image' },
  bmp: { contentType: 'image', editable: false, language: 'image' },
  webp: { contentType: 'image', editable: false, language: 'image' },
  svg: { contentType: 'image', editable: false, language: 'image' },
  ico: { contentType: 'image', editable: false, language: 'image' },
  tif: { contentType: 'unsupported', editable: false, language: 'binary' },
  tiff: { contentType: 'unsupported', editable: false, language: 'binary' },
  avif: { contentType: 'image', editable: false, language: 'image' },
};

/**
 * 根据文件名推断内容类型及是否可编辑
 * Determine preview content type and editability from file name
 */
export const getFileTypeInfo = (file_name: string): FileTypeInfo => {
  const ext = file_name.toLowerCase().split('.').pop() || '';
  return EXTENSION_MAP[ext] || { contentType: 'code', editable: true, language: ext || 'text' };
};
