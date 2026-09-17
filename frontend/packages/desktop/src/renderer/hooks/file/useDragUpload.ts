/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { useCallback, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Message } from '@arco-design/web-react';
import type { FileMetadata } from '@renderer/services/FileService';
import { isSupportedFile, FileService } from '@renderer/services/FileService';

export interface UseDragUploadOptions {
  supportedExts?: string[];
  onFilesAdded?: (files: FileMetadata[]) => void;
  /** Browser files that should stay in the draft until the message is sent. */
  onBrowserFilesAdded?: (files: File[]) => void | Promise<void>;
  /** Conversation ID for WebUI file uploads */
  conversation_id?: string;
}

export const useDragUpload = ({
  supportedExts = [],
  onFilesAdded,
  onBrowserFilesAdded,
  conversation_id,
}: UseDragUploadOptions) => {
  const { t } = useTranslation();
  const [isFileDragging, setIsFileDragging] = useState(false);

  // 拖拽计数器，防止状态闪烁
  const dragCounter = useRef(0);

  const handleDragOver = useCallback(
    (e: React.DragEvent) => {
      e.preventDefault();
      e.stopPropagation();

      if (!isFileDragging) {
        setIsFileDragging(true);
        dragCounter.current += 1;
      }
    },
    [isFileDragging]
  );

  const handleDragEnter = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();

    dragCounter.current += 1;
    setIsFileDragging(true);
  }, []);

  const handleDragLeave = useCallback((e: React.DragEvent) => {
    e.preventDefault();
    e.stopPropagation();

    dragCounter.current -= 1;

    if (dragCounter.current <= 0) {
      dragCounter.current = 0;
      setIsFileDragging(false);
    }
  }, []);

  const handleDrop = useCallback(
    async (e: React.DragEvent) => {
      e.preventDefault();
      e.stopPropagation();

      // 重置状态
      dragCounter.current = 0;
      setIsFileDragging(false);

      if (!onFilesAdded && !onBrowserFilesAdded) return;

      try {
        const droppedFiles = e.nativeEvent.dataTransfer!.files;

        // 第一步：先校验文件类型，筛选出支持的文件
        const validFiles: File[] = [];

        for (let i = 0; i < droppedFiles.length; i++) {
          const file = droppedFiles[i];
          if (supportedExts.length === 0 || isSupportedFile(file.name, supportedExts)) {
            validFiles.push(file);
          }
          // 注意：不支持的文件会被静默过滤，与原逻辑保持一致
        }

        // 第二步：只处理校验通过的文件
        if (validFiles.length > 0) {
          const browserFiles = validFiles.filter((file) => !(file as File & { path?: string }).path);
          const hostFiles = validFiles.filter((file) => Boolean((file as File & { path?: string }).path));

          if (browserFiles.length > 0 && onBrowserFilesAdded) {
            await onBrowserFilesAdded(browserFiles);
          }

          if (hostFiles.length > 0 && onFilesAdded) {
            // 创建 FileList 对象给 processDroppedFiles。Native files already
            // carry their host path, so they do not need a browser upload.
            const hostFileList = Object.assign(hostFiles, {
              length: hostFiles.length,
              item: (index: number) => hostFiles[index] || null,
            }) as unknown as FileList;
            const processedFiles = await FileService.processDroppedFiles(hostFileList, conversation_id);

            if (processedFiles.length > 0) {
              onFilesAdded(processedFiles);
            }
          }

          // Preserve the legacy path when no dedicated browser-file handler is
          // supplied by a caller that still expects every dropped file to be
          // processed by FileService.
          if (browserFiles.length > 0 && !onBrowserFilesAdded && onFilesAdded) {
            const browserFileList = Object.assign(browserFiles, {
              length: browserFiles.length,
              item: (index: number) => browserFiles[index] || null,
            }) as unknown as FileList;
            const processedFiles = await FileService.processDroppedFiles(browserFileList, conversation_id);
            if (processedFiles.length > 0) onFilesAdded(processedFiles);
          }
        }
      } catch (err) {
        console.error('Failed to process dropped files:', err);
        Message.error(t('conversation.workspace.dragFailed', 'Failed to process dropped files'));
      }
    },
    [conversation_id, onBrowserFilesAdded, onFilesAdded, supportedExts, t]
  );

  const dragHandlers = {
    onDragOver: handleDragOver,
    onDragEnter: handleDragEnter,
    onDragLeave: handleDragLeave,
    onDrop: handleDrop,
  };

  return {
    isFileDragging,
    dragHandlers,
  };
};
