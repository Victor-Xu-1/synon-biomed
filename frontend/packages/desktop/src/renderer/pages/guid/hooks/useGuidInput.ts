/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { useDragUpload } from '@/renderer/hooks/file/useDragUpload';
import { usePasteService } from '@/renderer/hooks/file/usePasteService';
import { allSupportedExts, type FileMetadata } from '@/renderer/services/FileService';
import type { ComposerContextItem } from '@/renderer/components/chat/SendBox/composerCompositionModel';
import { measureCaretTop, scrollCaretToLastLine } from '../utils/caretUtils';
import { useCallback, useEffect, useRef, useState } from 'react';

export type GuidLocalFile = {
  id: string;
  file: File;
};

export type GuidInputResult = {
  input: string;
  setInput: React.Dispatch<React.SetStateAction<string>>;
  files: string[];
  setFiles: React.Dispatch<React.SetStateAction<string[]>>;
  localFiles: GuidLocalFile[];
  setLocalFiles: React.Dispatch<React.SetStateAction<GuidLocalFile[]>>;
  contextItems: ComposerContextItem[];
  setContextItems: React.Dispatch<React.SetStateAction<ComposerContextItem[]>>;
  dir: string;
  setDir: React.Dispatch<React.SetStateAction<string>>;
  isInputFocused: boolean;
  loading: boolean;
  setLoading: React.Dispatch<React.SetStateAction<boolean>>;
  handleFilesPasted: (pastedFiles: FileMetadata[]) => void;
  handleFilesUploaded: (uploadedPaths: string[]) => void;
  handleLocalFilesSelected: (selectedFiles: File[]) => void;
  handleRemoveFile: (targetPath: string) => void;
  handleRemoveLocalFile: (targetId: string) => void;
  handleTextareaFocus: () => void;
  handleTextareaBlur: () => void;
  onPaste: ReturnType<typeof usePasteService>['onPaste'];
  isFileDragging: boolean;
  dragHandlers: ReturnType<typeof useDragUpload>['dragHandlers'];
};

type UseGuidInputOptions = {
  locationState: { workspace?: string } | null;
};

/**
 * Hook that manages input state, file handling, and drag/paste for the Guid page.
 */
export const useGuidInput = ({ locationState }: UseGuidInputOptions): GuidInputResult => {
  const [input, setInput] = useState('');
  const [files, setFiles] = useState<string[]>([]);
  const [localFiles, setLocalFiles] = useState<GuidLocalFile[]>([]);
  const [contextItems, setContextItems] = useState<ComposerContextItem[]>([]);
  const [dir, setDir] = useState<string>('');
  const [isInputFocused, setIsInputFocused] = useState(false);
  const [loading, setLoading] = useState(false);
  const localFileSequence = useRef(0);

  // Read workspace from location.state (passed from tabs add button)
  useEffect(() => {
    if (locationState?.workspace) {
      setDir(locationState.workspace);
    }
  }, [locationState]);

  // Handle pasted files (append mode to support multiple pastes)
  // Do NOT clear dir here: paste/drag should coexist with a selected workspace,
  // matching the dialog-upload path (handleFilesUploaded).
  const handleFilesPasted = useCallback((pastedFiles: FileMetadata[]) => {
    const file_paths = pastedFiles.map((file) => file.path);
    setFiles((prevFiles) => [...prevFiles, ...file_paths]);
  }, []);

  // Handle files uploaded via dialog (append mode)
  const handleFilesUploaded = useCallback((uploadedPaths: string[]) => {
    setFiles((prevFiles) => [...prevFiles, ...uploadedPaths]);
  }, []);

  const handleLocalFilesSelected = useCallback((selectedFiles: File[]) => {
    const additions = selectedFiles
      .filter((file) => file instanceof File && file.name.trim())
      .map((file) => ({
        id: `guid-local-file-${file.name}-${file.size}-${file.lastModified}-${localFileSequence.current++}`,
        file,
      }));
    if (additions.length === 0) return;
    setLocalFiles((previousFiles) => {
      const existingKeys = new Set(
        previousFiles.map(({ file }) => `${file.name}\u0000${file.size}\u0000${file.lastModified}\u0000${file.type}`)
      );
      const uniqueAdditions = additions.filter(({ file }) => {
        const key = `${file.name}\u0000${file.size}\u0000${file.lastModified}\u0000${file.type}`;
        if (existingKeys.has(key)) return false;
        existingKeys.add(key);
        return true;
      });
      return uniqueAdditions.length > 0 ? [...previousFiles, ...uniqueAdditions] : previousFiles;
    });
  }, []);

  const handleRemoveFile = useCallback((targetPath: string) => {
    setFiles((prevFiles) => prevFiles.filter((file) => file !== targetPath));
  }, []);

  const handleRemoveLocalFile = useCallback((targetId: string) => {
    setLocalFiles((previousFiles) => previousFiles.filter(({ id }) => id !== targetId));
  }, []);

  // Use drag upload hook (drag treated like paste, appends to existing files)
  const { isFileDragging, dragHandlers } = useDragUpload({
    supportedExts: allSupportedExts,
    onFilesAdded: handleFilesPasted,
    onBrowserFilesAdded: handleLocalFilesSelected,
  });

  // Use shared PasteService integration (paste appends to existing files)
  const { onPaste, onFocus } = usePasteService({
    supportedExts: allSupportedExts,
    onFilesAdded: handleFilesPasted,
    onBrowserFilesAdded: handleLocalFilesSelected,
    onTextPaste: (text: string) => {
      const textarea = document.activeElement as HTMLTextAreaElement | null;
      if (textarea && textarea.tagName === 'TEXTAREA') {
        const start = textarea.selectionStart ?? textarea.value.length;
        const end = textarea.selectionEnd ?? start;
        const current_value = textarea.value;
        const newValue = current_value.slice(0, start) + text + current_value.slice(end);
        setInput(newValue);

        setTimeout(() => {
          const newPos = start + text.length;
          textarea.setSelectionRange(newPos, newPos);
          const caretTop = measureCaretTop(textarea, newPos);
          scrollCaretToLastLine(textarea, caretTop);
        }, 0);
      } else {
        setInput((prev) => prev + text);
      }
    },
  });

  const handleTextareaFocus = useCallback(() => {
    onFocus();
    setIsInputFocused(true);
  }, [onFocus]);

  const handleTextareaBlur = useCallback(() => {
    setIsInputFocused(false);
  }, []);

  return {
    input,
    setInput,
    files,
    setFiles,
    localFiles,
    setLocalFiles,
    contextItems,
    setContextItems,
    dir,
    setDir,
    isInputFocused,
    loading,
    setLoading,
    handleFilesPasted,
    handleFilesUploaded,
    handleLocalFilesSelected,
    handleRemoveFile,
    handleRemoveLocalFile,
    handleTextareaFocus,
    handleTextareaBlur,
    onPaste,
    isFileDragging,
    dragHandlers,
  };
};
