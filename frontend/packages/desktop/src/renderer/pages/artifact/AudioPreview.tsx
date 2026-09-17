import { Attention } from '@icon-park/react';
import React, { useEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

type AudioPreviewProps = {
  url: string;
  filename: string;
};

const AudioPreviewSession: React.FC<AudioPreviewProps> = ({ url, filename }) => {
  const { t } = useTranslation();
  const [loading, setLoading] = useState(true);
  const [failed, setFailed] = useState(false);
  const audioRef = useRef<HTMLAudioElement>(null);

  useEffect(() => {
    const pauseWhenHidden = () => {
      if (document.visibilityState === 'hidden') audioRef.current?.pause();
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
          <p>{t('preview.artifact.audioLoadFailed')}</p>
        </div>
      ) : (
        <audio
          ref={audioRef}
          src={url}
          controls
          preload='metadata'
          aria-label={filename}
          className='w-full max-w-720px px-24px'
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

const AudioPreview: React.FC<AudioPreviewProps> = (props) => <AudioPreviewSession key={props.url} {...props} />;

export default AudioPreview;
