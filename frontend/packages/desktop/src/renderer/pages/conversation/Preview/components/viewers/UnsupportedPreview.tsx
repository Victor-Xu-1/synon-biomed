/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { Button, Message } from '@arco-design/web-react';
import { FileText, FolderClose } from '@icon-park/react';
import React, { useCallback } from 'react';
import { useTranslation } from 'react-i18next';

type UnsupportedPreviewProps = {
  filename: string;
  contentType?: string | null;
  downloadUrl?: string;
  filePath?: string;
};

const getFormatLabel = (filename: string, contentType?: string | null): string => {
  const normalizedType = contentType?.trim();
  if (normalizedType) return normalizedType;
  const extension = filename.match(/(\.[^./\\]+)$/)?.[1];
  return extension || 'unknown';
};

const UnsupportedPreview: React.FC<UnsupportedPreviewProps> = ({ filename, contentType, downloadUrl, filePath }) => {
  const { t } = useTranslation();
  const [messageApi, messageContextHolder] = Message.useMessage();
  const format = getFormatLabel(filename, contentType);
  const handleOpenInSystem = useCallback(async () => {
    if (!filePath) return;
    try {
      await ipcBridge.shell.openFile.invoke(filePath);
      messageApi.success(t('preview.openInSystemSuccess'));
    } catch (error) {
      console.warn('[UnsupportedPreview] Failed to open file in system app', error);
      messageApi.error(t('preview.openInSystemFailed'));
    }
  }, [filePath, messageApi, t]);

  return (
    <div className='size-full min-h-240px flex-center px-24px py-32px'>
      <div className='max-w-520px text-center text-t-secondary'>
        {messageContextHolder}
        <FileText theme='outline' size={36} className='mx-auto mb-12px text-t-tertiary' />
        <strong className='block text-15px text-t-primary'>{t('preview.unsupported.title')}</strong>
        <p className='mt-8px text-13px leading-20px'>{t('preview.unsupported.description')}</p>
        <p className='mt-8px text-12px text-t-tertiary'>{t('preview.unsupported.format', { filename, format })}</p>
        <div className='mt-16px flex-center gap-8px'>
          {filePath ? (
            <Button
              size='small'
              type='primary'
              icon={<FolderClose theme='outline' size={14} />}
              onClick={handleOpenInSystem}
            >
              {t('preview.openInSystemApp')}
            </Button>
          ) : null}
          {downloadUrl ? (
            <a className='text-13px text-[rgb(var(--primary-6))]' href={downloadUrl} target='_blank' rel='noreferrer'>
              {t('preview.unsupported.download')}
            </a>
          ) : null}
        </div>
      </div>
    </div>
  );
};

export default UnsupportedPreview;
