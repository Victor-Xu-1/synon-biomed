import { Attention } from '@icon-park/react';
import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

type VideoPreviewProps = {
  url: string;
  filename: string;
};

const VideoPreviewSession: React.FC<VideoPreviewProps> = ({ url, filename }) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const videoRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    const pauseWhenHidden = () => {
      if (document.visibilityState === 'hidden') videoRef.current?.pause();
    };
    document.addEventListener('visibilitychange', pauseWhenHidden);
    return () => document.removeEventListener('visibilitychange', pauseWhenHidden);
  }, []);

  return (
    <div className='relative size-full flex-center overflow-hidden bg-fill-2'>
      {loading && !failed && (
        <div className='absolute inset-0 flex-center' role='status' aria-label={t('common.loading')}>
          <span className='size-32px animate-spin rounded-full border-2 border-solid border-fill-4 border-t-transparent' />
        </div>
      )}
      {failed ? (
        <div className='text-center text-t-secondary' role='alert'>
          <Attention size={64} className='mx-auto mb-16px text-danger-6' />
          <p>{t('preview.artifact.videoLoadFailed')}</p>
        </div>
      ) : (
        <video
          ref={videoRef}
          src={url}
          controls
          playsInline
          preload='metadata'
          aria-label={filename}
          className='size-full object-contain'
          style={{ display: loading ? 'none' : 'block' }}
          onLoadedMetadata={() => setLoading(false)}
          onCanPlay={() => setLoading(false)}
          onError={() => {
            setLoading(false);
            setFailed(true);
          }}
        />
      )}
    </div>
  );
};

const VideoPreview: React.FC<VideoPreviewProps> = (props) => <VideoPreviewSession key={props.url} {...props} />;

export default VideoPreview;
