/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { ipcBridge } from '@/common';
import { Image } from '@arco-design/web-react';
import React, { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';

interface ImagePreviewProps {
  file_path?: string;
  content?: string;
  file_name?: string;
  workspace?: string;
}

const ImagePreview: React.FC<ImagePreviewProps> = ({ file_path, content, file_name, workspace }) => {
  const { t } = useTranslation();
  const [imageSrc, setImageSrc] = useState<string>(content || '');
  const [loading, setLoading] = useState<boolean>(!!file_path && !content);
  const [loadFailed, setLoadFailed] = useState(!content && !file_path);

  useEffect(() => {
    let isMounted = true;

    const loadImage = async () => {
      if (content) {
        setImageSrc(content);
        setLoading(false);
        setLoadFailed(false);
        return;
      }

      if (!file_path) {
        setImageSrc('');
        setLoading(false);
        setLoadFailed(true);
        return;
      }

      try {
        setLoading(true);
        setLoadFailed(false);
        const base64 = await ipcBridge.fs.getImageBase64.invoke({ path: file_path, workspace });
        if (!base64) {
          throw new Error('IMAGE_NOT_FOUND');
        }
        if (!isMounted) return;
        setImageSrc(base64);
      } catch {
        if (!isMounted) return;
        console.warn('[ImagePreview] Failed to load image');
        setLoadFailed(true);
      } finally {
        if (isMounted) {
          setLoading(false);
        }
      }
    };

    void loadImage();

    return () => {
      isMounted = false;
    };
  }, [content, file_path, workspace]);

  const renderStatus = () => {
    if (loading) {
      return <div className='text-14px text-t-secondary'>{t('common.loading')}</div>;
    }

    if (loadFailed) {
      return (
        <div className='text-center text-14px text-t-secondary'>
          <div>{t('messages.imageLoadFailed')}</div>
          {file_path && <div className='text-12px'>{file_path}</div>}
        </div>
      );
    }

    return (
      <Image
        src={imageSrc}
        alt={file_name ? t('preview.image.altNamed', { name: file_name }) : t('preview.image.alt')}
        className='w-full h-full flex items-center justify-center [&_.arco-image-img]:w-full [&_.arco-image-img]:h-full [&_.arco-image-img]:object-contain'
        preview={!!imageSrc}
      />
    );
  };

  return (
    <div className='preview-image preview-content-scroll flex-1 flex items-center justify-center bg-1 p-32px overflow-auto'>
      {renderStatus()}
    </div>
  );
};

export default ImagePreview;
