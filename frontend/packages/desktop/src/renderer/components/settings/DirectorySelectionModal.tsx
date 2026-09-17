/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Button, Modal, Spin } from '@arco-design/web-react';
import { IconFile, IconFolder, IconUp } from '@arco-design/web-react/icon';
import React, { useCallback, useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { getBaseUrl } from '@/common/adapter/httpBridge';
import { stripWindowsVerbatimPrefix } from '@/renderer/utils/file/fileSelection';

interface DirectoryItem {
  name: string;
  path: string;
  isDirectory: boolean;
  isFile?: boolean;
}

interface DirectoryData {
  items: DirectoryItem[];
  canGoUp: boolean;
  parentPath?: string;
}

interface DirectorySelectionModalProps {
  visible: boolean;
  isFileMode?: boolean;
  onConfirm: (paths: string[] | undefined) => void;
  onCancel: () => void;
}

type DirectoryLoadError = 'loadFailed' | 'invalidResponse';

type HostGrant = { hostPath?: string };

const hostPathSeparator = (value: string): '/' | '\\' => (value.includes('\\') && !value.includes('/') ? '\\' : '/');

export const joinHostPath = (base: string, name: string): string => {
  const separator = hostPathSeparator(base);
  return `${base.replace(/[\\/]+$/, '')}${separator}${name}`;
};

export const parentHostPath = (value: string): string => {
  const trimmed = value.replace(/[\\/]+$/, '');
  const separatorIndex = Math.max(trimmed.lastIndexOf('/'), trimmed.lastIndexOf('\\'));
  if (separatorIndex < 0) return trimmed;
  if (separatorIndex === 0) return trimmed.slice(0, 1);
  if (/^[A-Za-z]:$/.test(trimmed.slice(0, separatorIndex))) return `${trimmed.slice(0, separatorIndex)}\\`;
  return trimmed.slice(0, separatorIndex);
};

const DirectorySelectionModal: React.FC<DirectorySelectionModalProps> = ({
  visible,
  isFileMode = false,
  onConfirm,
  onCancel,
}) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(false);
  const [directoryData, setDirectoryData] = useState<DirectoryData>({ items: [], canGoUp: false });
  const [selectedPath, setSelectedPath] = useState<string>('');
  const [currentPath, setCurrentPath] = useState<string>('');
  const [error, setError] = useState<DirectoryLoadError | null>(null);

  const loadDirectory = useCallback(
    async (dirPath = '') => {
      setLoading(true);
      setError(null);
      try {
        if (!dirPath) {
          const [homeResponse, grantsResponse] = await Promise.all([
            fetch(`${getBaseUrl()}/api/preferences/host-home`, { credentials: 'include' }),
            fetch(`${getBaseUrl()}/api/preferences/host-grants`, { credentials: 'include' }),
          ]);
          if (!homeResponse.ok || !grantsResponse.ok) {
            setError('loadFailed');
            return;
          }
          const homePayload = (await homeResponse.json()) as { path?: string };
          const grantsPayload = (await grantsResponse.json()) as { grants?: HostGrant[] };
          if (!homePayload.path || !Array.isArray(grantsPayload.grants)) {
            setError('invalidResponse');
            return;
          }
          const roots = [homePayload.path, ...grantsPayload.grants.map((grant) => grant.hostPath ?? '')]
            .filter((path, index, all) => Boolean(path) && all.indexOf(path) === index)
            .map((path) => ({
              name: path === homePayload.path ? t('fileSelection.homeDirectory') : path,
              path,
              isDirectory: true,
            }));
          setDirectoryData({ items: roots, canGoUp: false });
          setCurrentPath('');
          return;
        }
        const response = await fetch(
          `${getBaseUrl()}/api/preferences/host-browse?path=${encodeURIComponent(dirPath)}`,
          { method: 'GET', credentials: 'include' }
        );
        if (!response.ok) {
          console.error(`Failed to load directory: HTTP ${response.status}`);
          setError('loadFailed');
          return;
        }
        const entries = (await response.json()) as Array<{ name?: string; isDirectory?: boolean }>;
        if (!Array.isArray(entries)) {
          console.error('Failed to load directory: invalid response payload');
          setError('invalidResponse');
          return;
        }
        const normalized: DirectoryData = {
          items: entries
            .filter((item): item is { name: string; isDirectory: boolean } => Boolean(item.name))
            .map((item) => ({
              name: item.name,
              path: stripWindowsVerbatimPrefix(joinHostPath(dirPath, item.name)),
              isDirectory: item.isDirectory,
              isFile: !item.isDirectory,
            })),
          canGoUp: true,
          parentPath: parentHostPath(dirPath),
        };
        setDirectoryData(normalized);
        setCurrentPath(dirPath);
      } catch (err) {
        console.error('Failed to load directory:', err);
        setError('loadFailed');
      } finally {
        setLoading(false);
      }
    },
    [t]
  );

  useEffect(() => {
    if (visible) {
      setSelectedPath('');
      void loadDirectory('');
    }
  }, [visible, loadDirectory]);

  const handleItemClick = (item: DirectoryItem) => {
    if (item.isDirectory) {
      void loadDirectory(item.path);
    }
  };

  const handleSelect = (path: string) => {
    setSelectedPath(path);
  };

  const handleGoUp = () => {
    if (directoryData.parentPath !== undefined) {
      // Handle '__ROOT__' as empty path to show drive list on Windows
      // 处理 '__ROOT__' 为空路径，在 Windows 上显示驱动器列表
      const targetPath = directoryData.parentPath === currentPath ? '' : directoryData.parentPath;
      void loadDirectory(targetPath);
    }
  };

  const handleConfirm = () => {
    if (selectedPath) {
      onConfirm([selectedPath]);
    }
  };

  const canSelect = (item: DirectoryItem) => {
    return isFileMode ? item.isFile : item.isDirectory;
  };

  return (
    // This picker is opened from other modals (for example, the task dialog sits at
    // zIndex 10000, the cron workspace menu at 10020), so it must float above all
    // of them — it's the topmost layer while choosing a folder.
    <Modal
      visible={visible}
      title={isFileMode ? t('fileSelection.selectFile') : t('fileSelection.selectDirectory')}
      onCancel={onCancel}
      onOk={handleConfirm}
      okButtonProps={{ disabled: !selectedPath }}
      className='w-[90vw] md:w-[600px]'
      style={{ width: 'min(600px, 90vw)' }}
      wrapStyle={{ zIndex: 10050 }}
      maskStyle={{ zIndex: 10040 }}
      footer={
        <div className='w-full flex justify-between items-center'>
          <div
            className='text-t-secondary text-14px overflow-hidden text-ellipsis whitespace-nowrap max-w-[70vw]'
            title={selectedPath || currentPath}
          >
            {selectedPath ||
              currentPath ||
              (isFileMode ? t('fileSelection.pleaseSelectFile') : t('fileSelection.pleaseSelectDirectory'))}
          </div>
          <div className='flex gap-10px'>
            <Button onClick={onCancel}>{t('common.cancel')}</Button>
            <Button type='primary' onClick={handleConfirm} disabled={!selectedPath}>
              {t('common.confirm')}
            </Button>
          </div>
        </div>
      }
    >
      <Spin loading={loading} className='w-full'>
        <div className='w-full border border-b-base rd-4px overflow-hidden' style={{ height: 'min(400px, 60vh)' }}>
          <div className='h-full overflow-y-auto'>
            {directoryData.canGoUp && (
              <div
                className='flex items-center p-10px border-b border-b-light cursor-pointer hover:bg-hover transition'
                onClick={handleGoUp}
              >
                <IconUp className='mr-10px text-t-secondary' />
                <span>..</span>
              </div>
            )}
            {error && (
              <div className='p-16px text-center text-danger text-13px'>
                <div>{t(`fileSelection.${error}`)}</div>
                <Button size='mini' className='mt-8px' onClick={() => void loadDirectory(currentPath)}>
                  {t('common.retry')}
                </Button>
              </div>
            )}
            {directoryData.items.map((item) => (
              <div
                key={item.path}
                className='flex items-center justify-between p-10px border-b border-b-light cursor-pointer hover:bg-hover transition'
                style={selectedPath === item.path ? { background: 'var(--brand-light)' } : {}}
                onClick={() => handleItemClick(item)}
              >
                <div className='flex items-center flex-1 min-w-0'>
                  {item.isDirectory ? (
                    <IconFolder className='mr-10px text-warning shrink-0' />
                  ) : (
                    <IconFile className='mr-10px text-primary shrink-0' />
                  )}
                  <span className='overflow-hidden text-ellipsis whitespace-nowrap'>{item.name}</span>
                </div>
                {canSelect(item) && (
                  <Button
                    type='primary'
                    size='mini'
                    onClick={(e) => {
                      e.stopPropagation();
                      handleSelect(item.path);
                    }}
                  >
                    {t('common.select')}
                  </Button>
                )}
              </div>
            ))}
          </div>
        </div>
      </Spin>
    </Modal>
  );
};

export default DirectorySelectionModal;
