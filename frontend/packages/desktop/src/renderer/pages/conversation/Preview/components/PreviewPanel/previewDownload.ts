/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { downloadFileFromPath, downloadFileFromUrl, downloadTextContent } from '@/renderer/utils/file/download';
import type { PreviewTab } from '../../context/PreviewContext';

const resolveTextDownload = (tab: PreviewTab): { extension: string; mimeType: string } => {
  const { content_type: type, metadata } = tab;
  const nameExtension = metadata?.file_name?.split('.').pop();
  let extension = 'txt';
  let mimeType = 'text/plain;charset=utf-8';

  if (type === 'markdown') {
    extension = 'md';
    mimeType = 'text/markdown;charset=utf-8';
  } else if (type === 'html') {
    extension = 'html';
    mimeType = 'text/html;charset=utf-8';
  } else if (type === 'diff') {
    extension = 'diff';
  } else if (type === 'code' || type === 'latex') {
    const extensions: Record<string, string> = {
      javascript: 'js',
      js: 'js',
      typescript: 'ts',
      ts: 'ts',
      python: 'py',
      py: 'py',
      java: 'java',
      cpp: 'cpp',
      'c++': 'cpp',
      c: 'c',
      html: 'html',
      css: 'css',
      json: 'json',
      latex: 'tex',
    };
    extension = extensions[metadata?.language ?? ''] ?? (type === 'latex' ? 'tex' : 'txt');
  }

  return { extension: nameExtension || extension, mimeType };
};

export const downloadPreviewTab = async (tab: PreviewTab): Promise<void> => {
  const { content, content_type: type, metadata } = tab;
  const rawFileName = metadata?.file_name || `${type}-${Date.now()}`;

  if (metadata?.contentUrl) {
    await downloadFileFromUrl(metadata.contentUrl, rawFileName);
    return;
  }
  if (metadata?.file_path) {
    await downloadFileFromPath(metadata.file_path, rawFileName, metadata.workspace);
    return;
  }
  if (type === 'image') {
    if (!content) throw new Error('IMAGE_CONTENT_MISSING');
    const response = await fetch(content);
    if (!response.ok) throw new Error('IMAGE_DOWNLOAD_FAILED');
    const blob = await response.blob();
    const nameExtension = metadata?.file_name?.split('.').pop();
    const mimeExtension = blob.type?.includes('/') ? blob.type.split('/').pop() : undefined;
    const extension = nameExtension || mimeExtension || 'png';
    const fileName = rawFileName.toLowerCase().endsWith(`.${extension.toLowerCase()}`)
      ? rawFileName
      : `${rawFileName}.${extension}`;
    const url = URL.createObjectURL(blob);
    const link = document.createElement('a');
    link.href = url;
    link.download = fileName;
    document.body.appendChild(link);
    link.click();
    document.body.removeChild(link);
    URL.revokeObjectURL(url);
    return;
  }

  const { extension, mimeType } = resolveTextDownload(tab);
  const fileName = rawFileName.toLowerCase().endsWith(`.${extension.toLowerCase()}`)
    ? rawFileName
    : `${rawFileName}.${extension}`;
  downloadTextContent(content, fileName, mimeType);
};
