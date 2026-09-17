/**
 * @license
 * Copyright 2026 Synon-AI
 * SPDX-License-Identifier: Apache-2.0
 */

import { Attention } from '@icon-park/react';
import React, { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { buildPdfSrc } from '../../previewUrls';

type MediaPreviewProps = {
  mediaType: 'audio' | 'video';
  filename: string;
  content?: string;
  filePath?: string;
};

const MediaPreview: React.FC<MediaPreviewProps> = ({ mediaType, filename, content, filePath }) => {
  const { t } = useTranslation();
  const mediaRef = useRef<HTMLAudioElement | HTMLVideoElement>(null);
  const source = useMemo(() => buildPdfSrc(filePath, content), [content, filePath]);
  const [loading, setLoading] = useState(Boolean(source));
  const [failed, setFailed] = useState(!source);

  useEffect(() => {
    setLoading(Boolean(source));
    setFailed(!source);
  }, [source]);

  useEffect(() => {
    const pauseWhenHidden = () => {
      if (document.visibilityState === 'hidden') mediaRef.current?.pause();
    };
    document.addEventListener('visibilitychange', pauseWhenHidden);
    return () => document.removeEventListener('visibilitychange', pauseWhenHidden);
  }, []);

  const failedMessage =
    mediaType === 'audio' ? t('preview.artifact.audioLoadFailed') : t('preview.artifact.videoLoadFailed');
  const mediaProps = {
    key: source,
    src: source,
    controls: true,
    preload: 'metadata' as const,
    onLoadedMetadata: () => setLoading(false),
    onCanPlay: () => setLoading(false),
    onError: () => {
      setLoading(false);
      setFailed(true);
    },
  };

  return (
    <div className='relative size-full flex-center overflow-hidden bg-fill-2'>
      {loading && !failed ? (
        <div className='absolute inset-0 flex-center' role='status' aria-label={t('common.loading')}>
          <span className='size-32px animate-spin rounded-full border-2 border-solid border-fill-4 border-t-transparent' />
        </div>
      ) : null}
      {failed ? (
        <div className='text-center text-t-secondary' role='alert'>
          <Attention size={52} className='mx-auto mb-12px text-danger-6' />
          <p>{failedMessage}</p>
        </div>
      ) : mediaType === 'audio' ? (
        <audio
          {...mediaProps}
          ref={(node) => {
            mediaRef.current = node;
          }}
          aria-label={filename}
          className='w-full max-w-720px px-24px'
          style={{ display: loading ? 'none' : 'block' }}
        />
      ) : (
        <video
          {...mediaProps}
          ref={(node) => {
            mediaRef.current = node;
          }}
          aria-label={filename}
          playsInline
          className='size-full object-contain'
          style={{ display: loading ? 'none' : 'block' }}
        />
      )}
    </div>
  );
};

export default MediaPreview;
