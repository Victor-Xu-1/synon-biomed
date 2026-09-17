/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ipcBridge } from '@/common';
import DirectorySelectionModal from '@renderer/components/settings/DirectorySelectionModal';

interface DirectorySelectionRequest {
  isFileMode?: boolean;
  properties?: string[];
}

export const useDirectorySelection = () => {
  const [visible, setVisible] = useState(false);
  const [requestData, setRequestData] = useState<DirectorySelectionRequest | null>(null);
  const resolveRequestRef = useRef<((paths: string[] | undefined) => void) | null>(null);

  const settleRequest = useCallback((paths: string[] | undefined) => {
    const resolve = resolveRequestRef.current;
    resolveRequestRef.current = null;
    resolve?.(paths);
    setVisible(false);
    setRequestData(null);
  }, []);

  const handleConfirm = useCallback(
    (paths: string[] | undefined) => {
      settleRequest(paths);
    },
    [settleRequest]
  );

  const handleCancel = useCallback(() => {
    settleRequest(undefined);
  }, [settleRequest]);

  useEffect(() => {
    ipcBridge.dialog.showOpen.provider(
      (data) =>
        new Promise<string[] | undefined>((resolve) => {
          // A second request supersedes the first instead of leaving an
          // unresolved bridge invocation behind.
          resolveRequestRef.current?.(undefined);
          resolveRequestRef.current = resolve;
          const properties = data?.properties ?? [];
          const isFileMode = properties.includes('openFile') && !properties.includes('openDirectory');
          setRequestData({ properties, isFileMode });
          setVisible(true);
        })
    );

    return () => {
      resolveRequestRef.current?.(undefined);
      resolveRequestRef.current = null;
      ipcBridge.dialog.showOpen.provider(async () => undefined);
    };
  }, []);

  const contextHolder = (
    <DirectorySelectionModal
      visible={visible}
      isFileMode={requestData?.isFileMode}
      onConfirm={handleConfirm}
      onCancel={handleCancel}
    />
  );

  return { contextHolder };
};
